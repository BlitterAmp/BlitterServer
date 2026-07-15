package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type splitAlbumPlan struct {
	fragmentIDs []string
	artistIDs   []string
	tracks      []TrackRow
	canonical   map[string]CanonicalTrack
	artID       string
}

// MusicBrainzAlbumUnionCandidate returns a read-only aggregate used to score
// editions that cannot fit the anchor fragment alone. ApplyMusicBrainzMatch
// independently rebuilds and validates the union in its write transaction.
func (s *Store) MusicBrainzAlbumUnionCandidate(ctx context.Context, anchor MusicBrainzAlbum) (MusicBrainzAlbum, []MusicBrainzAlbum, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return anchor, nil, err
	}
	defer tx.Rollback()
	current, err := musicBrainzSnapshotTx(ctx, tx, anchor.AlbumID)
	if err != nil {
		return anchor, nil, err
	}
	if !sameMusicBrainzSnapshot(anchor, current) {
		return anchor, nil, nil
	}
	anchor.PathTitle, anchor.PathDiscs = current.PathTitle, current.PathDiscs
	rows, err := tx.QueryContext(ctx, `SELECT al.album_id,COALESCE(al.musicbrainz_release_group_id,'') FROM albums al
		WHERE al.album_id<>? AND al.missing=0 AND COALESCE(al.year,0)=?
		AND al.musicbrainz_release_id IS NULL
		ORDER BY al.album_id`, anchor.AlbumID, anchor.Year)
	if err != nil {
		return anchor, nil, err
	}
	var candidateIDs []string
	candidateReleaseGroups := map[string]string{}
	for rows.Next() {
		var id, releaseGroupID string
		if err := rows.Scan(&id, &releaseGroupID); err != nil {
			rows.Close()
			return anchor, nil, err
		}
		candidateIDs = append(candidateIDs, id)
		candidateReleaseGroups[id] = releaseGroupID
	}
	if err := rows.Close(); err != nil {
		return anchor, nil, err
	}
	aggregate := albumWithPathDiscs(anchor)
	var fragments []MusicBrainzAlbum
	releaseGroups := map[string]bool{}
	if anchor.ReleaseGroupID != "" {
		releaseGroups[anchor.ReleaseGroupID] = true
	}
	for _, id := range candidateIDs {
		fragment, err := musicBrainzSnapshotTx(ctx, tx, id)
		if err != nil {
			return anchor, nil, err
		}
		if !musicBrainzAlbumTitlesCompatible(anchor, fragment) {
			continue
		}
		if fragment.ReleaseGroupID == "" {
			fragment.ReleaseGroupID = candidateReleaseGroups[id]
		}
		if fragment.ReleaseGroupID != "" {
			releaseGroups[fragment.ReleaseGroupID] = true
			if len(releaseGroups) > 1 {
				return anchor, nil, nil
			}
		}
		var total, present int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(missing=0),0) FROM tracks WHERE album_id=?`, id).Scan(&total, &present); err != nil {
			return anchor, nil, err
		}
		if total == 0 || total != present {
			return anchor, nil, nil
		}
		fragments = append(fragments, fragment)
		aggregate.Tracks = append(aggregate.Tracks, albumWithPathDiscs(fragment).Tracks...)
	}
	aggregate.TrackCount = len(aggregate.Tracks)
	if err := tx.Commit(); err != nil {
		return anchor, nil, err
	}
	return aggregate, fragments, nil
}

func musicBrainzAlbumTitlesCompatible(a, b MusicBrainzAlbum) bool {
	if strings.EqualFold(strings.TrimSpace(a.Title), strings.TrimSpace(b.Title)) {
		return true
	}
	return a.PathTitle != "" && b.PathTitle != "" && normalized(a.PathTitle) == normalized(b.PathTitle)
}

// planSplitAlbumReconciliation derives a possible split release solely from
// album identity and complementary track structure. A nil plan means there
// are no compatible fragments; safe=false means there were fragments, but the
// union was not strong enough to mutate.
func planSplitAlbumReconciliation(ctx context.Context, tx *sql.Tx, anchor MusicBrainzAlbum, release CanonicalRelease, selected MusicBrainzCandidate) (*splitAlbumPlan, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT album_id,artist_id,title,COALESCE(year,0),COALESCE(musicbrainz_release_id,''),COALESCE(musicbrainz_release_group_id,''),COALESCE(art_id,'')
		FROM albums WHERE missing=0 AND COALESCE(year,0)=? ORDER BY album_id`, anchor.Year)
	if err != nil {
		return nil, false, err
	}
	type albumRow struct {
		id, artistID, title, releaseID, releaseGroupID, artID string
		year                                                  int
		pathTitle                                             string
		pathDiscs                                             map[string]int
	}
	var candidates []albumRow
	for rows.Next() {
		var album albumRow
		if err := rows.Scan(&album.id, &album.artistID, &album.title, &album.year, &album.releaseID, &album.releaseGroupID, &album.artID); err != nil {
			rows.Close()
			return nil, false, err
		}
		candidates = append(candidates, album)
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	var compatible []albumRow
	for _, album := range candidates {
		album.pathTitle, album.pathDiscs, err = musicBrainzPathEvidence(ctx, tx, album.id)
		if err != nil {
			return nil, false, err
		}
		candidate := MusicBrainzAlbum{AlbumID: album.id, Title: album.title, PathTitle: album.pathTitle}
		if !musicBrainzAlbumTitlesCompatible(anchor, candidate) {
			continue
		}
		if album.id != anchor.AlbumID && album.releaseGroupID != "" && album.releaseGroupID != release.ReleaseGroupID {
			continue
		}
		compatible = append(compatible, album)
	}
	if len(compatible) <= 1 {
		return nil, true, nil
	}

	plan := &splitAlbumPlan{}
	artistSet := map[string]bool{}
	fragmentSet := map[string]bool{}
	var anchorArt string
	fragmentArts := map[string]bool{}
	pathDiscs := map[string]int{}
	for _, album := range compatible {
		for trackID, disc := range album.pathDiscs {
			pathDiscs[trackID] = disc
		}
		artistSet[album.artistID] = true
		if album.id == anchor.AlbumID {
			anchorArt = album.artID
			continue
		}
		var hasEvidence bool
		if err := tx.QueryRowContext(ctx, `SELECT ?<>'' OR ?<>'' OR EXISTS(
			SELECT 1 FROM album_musicbrainz_matches WHERE album_id=? AND (release_id IS NOT NULL OR release_group_id IS NOT NULL)
		)`, album.releaseID, album.releaseGroupID, album.id).Scan(&hasEvidence); err != nil {
			return nil, false, err
		}
		compatibleReleaseGroup := album.releaseID == "" && album.releaseGroupID != "" && album.releaseGroupID == release.ReleaseGroupID
		if hasEvidence && !compatibleReleaseGroup {
			return plan, false, nil
		}
		var total, present int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(missing=0),0) FROM tracks WHERE album_id=?`, album.id).Scan(&total, &present); err != nil {
			return nil, false, err
		}
		if total == 0 || total != present {
			return plan, false, nil
		}
		plan.fragmentIDs = append(plan.fragmentIDs, album.id)
		fragmentSet[album.id] = true
		if album.artID != "" {
			fragmentArts[album.artID] = true
		}
	}
	if anchorArt == "" {
		if len(fragmentArts) > 1 {
			return plan, false, nil
		}
		for artID := range fragmentArts {
			plan.artID = artID
		}
	}

	if stale, err := staleFragmentEvidence(ctx, tx, selected, fragmentSet); err != nil {
		return nil, false, err
	} else if stale {
		return plan, false, nil
	}
	if ambiguous, err := divergentCompleteEditionsTx(ctx, tx, anchor.AlbumID); err != nil {
		return nil, false, err
	} else if ambiguous {
		return plan, false, nil
	}

	trackRows, err := tx.QueryContext(ctx, `SELECT track_id,COALESCE(idx,0),COALESCE(disc,0),title,duration_ms,artist_id,artist_name,COALESCE(musicbrainz_recording_id,'')
		FROM tracks WHERE missing=0 AND (album_id=? OR album_id IN (`+placeholders(len(plan.fragmentIDs))+`)) ORDER BY track_id`, append([]any{anchor.AlbumID}, stringsToAny(plan.fragmentIDs)...)...)
	if err != nil {
		return nil, false, err
	}
	for trackRows.Next() {
		var track TrackRow
		if err := trackRows.Scan(&track.TrackID, &track.Index, &track.Disc, &track.Title, &track.DurationMs, &track.ArtistID, &track.ArtistName, &track.MusicBrainzRecordingID); err != nil {
			trackRows.Close()
			return nil, false, err
		}
		if track.Disc <= 0 && pathDiscs[track.TrackID] > 0 {
			track.Disc = pathDiscs[track.TrackID]
		}
		plan.tracks = append(plan.tracks, track)
	}
	if err := trackRows.Close(); err != nil {
		return nil, false, err
	}
	canonical, safe := matchReleaseUnion(plan.tracks, release)
	if !safe {
		return plan, false, nil
	}
	plan.canonical = canonical
	if conflict, err := splitProfileConflictTx(ctx, tx, anchor.AlbumID, plan.fragmentIDs); err != nil {
		return nil, false, err
	} else if conflict {
		return plan, false, nil
	}
	var releaseCollision bool
	if release.ReleaseID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM albums WHERE musicbrainz_release_id=? AND album_id<>?)`, release.ReleaseID, anchor.AlbumID).Scan(&releaseCollision); err != nil {
			return nil, false, err
		}
	}
	if releaseCollision {
		return plan, false, nil
	}
	for artistID := range artistSet {
		plan.artistIDs = append(plan.artistIDs, artistID)
	}
	return plan, true, nil
}

func matchReleaseUnion(local []TrackRow, release CanonicalRelease) (map[string]CanonicalTrack, bool) {
	if len(local) == 0 || len(local) != len(release.Tracks) {
		return nil, false
	}
	canonical := make(map[[2]int]CanonicalTrack, len(release.Tracks))
	perDisc := map[int]map[int]bool{}
	for _, track := range release.Tracks {
		key := [2]int{discNumber(track.Disc), track.Index}
		if track.Index <= 0 || canonical[key].Index != 0 {
			return nil, false
		}
		canonical[key] = track
		if perDisc[key[0]] == nil {
			perDisc[key[0]] = map[int]bool{}
		}
		perDisc[key[0]][key[1]] = true
	}
	for _, positions := range perDisc {
		for position := 1; position <= len(positions); position++ {
			if !positions[position] {
				return nil, false
			}
		}
	}
	seen := make(map[[2]int]bool, len(local))
	matched := make(map[string]CanonicalTrack, len(local))
	usedCanonical := make(map[[2]int]bool, len(local))
	var unmatched []TrackRow
	for _, track := range local {
		key := [2]int{discNumber(track.Disc), track.Index}
		candidate, ok := canonical[key]
		if track.Index <= 0 || !ok || seen[key] {
			return nil, false
		}
		seen[key] = true
		if release.Authoritative || normalized(track.Title) == normalized(candidate.Title) || durationClose(track.DurationMs, candidate.DurationMs) {
			matched[track.TrackID] = candidate
			usedCanonical[key] = true
		} else {
			unmatched = append(unmatched, track)
		}
	}
	// Correct isolated bad indices when a track uniquely matches another
	// canonical position by both title and duration.
	var unresolved []TrackRow
	for _, track := range unmatched {
		var found CanonicalTrack
		foundKey := [2]int{}
		matches := 0
		for key, candidate := range canonical {
			if usedCanonical[key] || normalized(track.Title) != normalized(candidate.Title) || !durationClose(track.DurationMs, candidate.DurationMs) {
				continue
			}
			found, foundKey, matches = candidate, key, matches+1
		}
		if matches == 1 {
			matched[track.TrackID] = found
			usedCanonical[foundKey] = true
		} else {
			unresolved = append(unresolved, track)
		}
	}
	if len(matched)*5 < len(local)*4 {
		return nil, false
	}
	// A strong release-wide fit makes the remaining complete positions safe;
	// this covers harmless edition suffix and duration drift without parsing
	// artist strings or guessing between incomplete releases.
	for _, track := range unresolved {
		key := [2]int{discNumber(track.Disc), track.Index}
		candidate := canonical[key]
		if usedCanonical[key] {
			return nil, false
		}
		matched[track.TrackID] = candidate
		usedCanonical[key] = true
	}
	return matched, len(matched) == len(local)
}

func staleFragmentEvidence(ctx context.Context, tx *sql.Tx, selected MusicBrainzCandidate, fragments map[string]bool) (bool, error) {
	raw, ok := selected.Evidence["fragmentSnapshots"]
	if !ok {
		return false, nil
	}
	var snapshots []MusicBrainzAlbum
	switch value := raw.(type) {
	case []MusicBrainzAlbum:
		snapshots = value
	default:
		encoded, err := json.Marshal(value)
		if err != nil || json.Unmarshal(encoded, &snapshots) != nil {
			return true, nil
		}
	}
	if len(snapshots) != len(fragments) {
		return true, nil
	}
	for _, expected := range snapshots {
		if !fragments[expected.AlbumID] {
			return true, nil
		}
		current, err := musicBrainzSnapshotTx(ctx, tx, expected.AlbumID)
		if err != nil {
			return false, err
		}
		if !sameMusicBrainzSnapshot(expected, current) {
			return true, nil
		}
	}
	return false, nil
}

func divergentCompleteEditionsTx(ctx context.Context, tx *sql.Tx, albumID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT evidence_json FROM album_musicbrainz_candidates WHERE album_id=? ORDER BY position`, albumID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var reference string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var evidence map[string]any
		if json.Unmarshal(raw, &evidence) != nil {
			continue
		}
		trackCount, _ := evidence["trackCount"].(string)
		recordings, ok := evidence["recordings"].([]any)
		if trackCount != "complete" || !ok {
			continue
		}
		signature, _ := json.Marshal(recordings)
		if reference == "" {
			reference = string(signature)
		} else if reference != string(signature) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func splitProfileConflictTx(ctx context.Context, tx *sql.Tx, anchorID string, fragmentIDs []string) (bool, error) {
	args := append([]any{anchorID}, stringsToAny(fragmentIDs)...)
	for _, query := range []string{
		`SELECT EXISTS(SELECT 1 FROM loves WHERE ref IN (` + placeholders(len(args)) + `) GROUP BY profile_id HAVING count(DISTINCT state)>1)`,
		`SELECT EXISTS(SELECT 1 FROM ratings WHERE item_id IN (` + placeholders(len(args)) + `) GROUP BY profile_id HAVING count(DISTINCT rating10)>1)`,
	} {
		var conflict bool
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&conflict); err != nil {
			return false, err
		}
		if conflict {
			return true, nil
		}
	}
	return false, nil
}

func applySplitAlbumPlanTx(ctx context.Context, tx *sql.Tx, anchorID string, plan *splitAlbumPlan, seq int64) error {
	fragmentArgs := stringsToAny(plan.fragmentIDs)
	if _, err := tx.ExecContext(ctx, `UPDATE tracks SET album_id=?,change_seq=? WHERE album_id IN (`+placeholders(len(plan.fragmentIDs))+`)`, append([]any{anchorID, seq}, fragmentArgs...)...); err != nil {
		return fmt.Errorf("reparent fragment tracks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tracks SET change_seq=? WHERE album_id=?`, seq, anchorID); err != nil {
		return fmt.Errorf("stamp reconciled tracks: %w", err)
	}
	if plan.artID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET art_id=? WHERE album_id=? AND art_id IS NULL`, plan.artID, anchorID); err != nil {
			return fmt.Errorf("move fragment artwork: %w", err)
		}
	}
	allAlbumArgs := append([]any{anchorID}, fragmentArgs...)
	if _, err := tx.ExecContext(ctx, `DELETE FROM loves WHERE ref IN (`+placeholders(len(allAlbumArgs))+`) AND love_id NOT IN (
		SELECT min(love_id) FROM loves WHERE ref IN (`+placeholders(len(allAlbumArgs))+`) GROUP BY profile_id
	)`, append(allAlbumArgs, allAlbumArgs...)...); err != nil {
		return fmt.Errorf("collapse album loves: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE loves SET ref=? WHERE ref IN (`+placeholders(len(plan.fragmentIDs))+`)`, append([]any{anchorID}, fragmentArgs...)...); err != nil {
		return fmt.Errorf("move album loves: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ratings WHERE item_id IN (`+placeholders(len(allAlbumArgs))+`) AND rowid NOT IN (
		SELECT min(rowid) FROM ratings WHERE item_id IN (`+placeholders(len(allAlbumArgs))+`) GROUP BY profile_id
	)`, append(allAlbumArgs, allAlbumArgs...)...); err != nil {
		return fmt.Errorf("collapse album ratings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ratings SET item_id=? WHERE item_id IN (`+placeholders(len(plan.fragmentIDs))+`)`, append([]any{anchorID}, fragmentArgs...)...); err != nil {
		return fmt.Errorf("move album ratings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE recommendations SET ref=? WHERE ref IN (`+placeholders(len(plan.fragmentIDs))+`)`, append([]any{anchorID}, fragmentArgs...)...); err != nil {
		return fmt.Errorf("move album recommendations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recommendations WHERE ref=? AND rowid NOT IN (
		SELECT min(rowid) FROM recommendations WHERE ref=? GROUP BY from_profile_id,to_profile_id,ref,kind,COALESCE(note,''),seen
	)`, anchorID, anchorID); err != nil {
		return fmt.Errorf("collapse album recommendations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM album_musicbrainz_candidates WHERE album_id IN (`+placeholders(len(plan.fragmentIDs))+`)`, fragmentArgs...); err != nil {
		return fmt.Errorf("retire fragment candidates: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM album_musicbrainz_matches WHERE album_id IN (`+placeholders(len(plan.fragmentIDs))+`)`, fragmentArgs...); err != nil {
		return fmt.Errorf("retire fragment matches: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE albums SET missing=1,change_seq=? WHERE album_id IN (`+placeholders(len(plan.fragmentIDs))+`)`, append([]any{seq}, fragmentArgs...)...); err != nil {
		return fmt.Errorf("retire fragment albums: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE albums SET change_seq=? WHERE album_id=?`, seq, anchorID); err != nil {
		return fmt.Errorf("stamp survivor album: %w", err)
	}
	for _, artistID := range plan.artistIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE artists SET missing=NOT EXISTS(
			SELECT 1 FROM albums WHERE albums.artist_id=artists.artist_id AND albums.missing=0
		),change_seq=? WHERE artist_id=?`, seq, artistID); err != nil {
			return fmt.Errorf("retire drained album owner %s: %w", artistID, err)
		}
	}
	return nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}
