package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

type MusicBrainzAlbum struct {
	AlbumID, Title, ReleaseID, ReleaseGroupID string
	Year, TrackCount                          int
	Version                                   int64
	PrimaryArtist                             ArtistCreditRow
	ArtistCredits                             []ArtistCreditRow
	Tracks                                    []TrackRow
	DueAt                                     int64
}

type MusicBrainzCandidate struct {
	ReleaseID, ReleaseGroupID, Title, ArtistCredit string
	Score                                          float64
	Evidence                                       map[string]any
}

func (s *Store) DueMusicBrainzAlbums(ctx context.Context, now time.Time, limit int) ([]MusicBrainzAlbum, error) {
	return s.DueMusicBrainzAlbumsPage(ctx, now, -1, "", limit)
}

// DueMusicBrainzAlbumsPage returns one keyset page in retry-time and album-id order.
func (s *Store) DueMusicBrainzAlbumsPage(ctx context.Context, now time.Time, afterDueAt int64, afterID string, limit int) ([]MusicBrainzAlbum, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT al.album_id,COALESCE(m.next_attempt_at,0) FROM albums al LEFT JOIN album_musicbrainz_matches m ON m.album_id=al.album_id
		WHERE al.missing=0 AND COALESCE(m.next_attempt_at,0)<=? AND (COALESCE(m.next_attempt_at,0)>? OR (COALESCE(m.next_attempt_at,0)=? AND al.album_id>?))
		ORDER BY COALESCE(m.next_attempt_at,0),al.album_id LIMIT ?`, now.Unix(), afterDueAt, afterDueAt, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type dueAlbum struct {
		id    string
		dueAt int64
	}
	var ids []dueAlbum
	for rows.Next() {
		var due dueAlbum
		if err := rows.Scan(&due.id, &due.dueAt); err != nil {
			return nil, err
		}
		ids = append(ids, due)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]MusicBrainzAlbum, 0, len(ids))
	for _, due := range ids {
		a, found, err := s.GetAlbum(ctx, due.id)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		tracks, err := s.ListAlbumTracks(ctx, due.id)
		if err != nil {
			return nil, err
		}
		var version int64
		if err := s.db.QueryRowContext(ctx, `SELECT change_seq FROM albums WHERE album_id=?`, due.id).Scan(&version); err != nil {
			return nil, err
		}
		out = append(out, MusicBrainzAlbum{AlbumID: a.AlbumID, Title: a.Title, ReleaseID: a.MusicBrainzReleaseID, ReleaseGroupID: a.MusicBrainzReleaseGroupID, Year: a.Year, TrackCount: a.TrackCount, Version: version, PrimaryArtist: a.PrimaryArtist, ArtistCredits: a.ArtistCredits, Tracks: tracks, DueAt: due.dueAt})
	}
	return out, nil
}

func (s *Store) RecordMusicBrainzResult(ctx context.Context, albumID, state string, selected MusicBrainzCandidate, candidates []MusicBrainzCandidate, next time.Time, lastError string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = recordMusicBrainzResultTx(ctx, tx, albumID, state, selected, candidates, next, lastError); err != nil {
		return err
	}
	return tx.Commit()
}

func recordMusicBrainzResultTx(ctx context.Context, tx *sql.Tx, albumID, state string, selected MusicBrainzCandidate, candidates []MusicBrainzCandidate, next time.Time, lastError string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO album_musicbrainz_matches(album_id,state,release_id,release_group_id,confidence,attempt_count,next_attempt_at,last_error,updated_at) VALUES(?,?,?,?,?,1,?,?,?)
		ON CONFLICT(album_id) DO UPDATE SET state=excluded.state,release_id=excluded.release_id,release_group_id=excluded.release_group_id,confidence=excluded.confidence,attempt_count=attempt_count+1,next_attempt_at=excluded.next_attempt_at,last_error=excluded.last_error,updated_at=excluded.updated_at`,
		albumID, state, nullStr(selected.ReleaseID), nullStr(selected.ReleaseGroupID), selected.Score, next.Unix(), lastError, time.Now().Unix())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM album_musicbrainz_candidates WHERE album_id=?`, albumID); err != nil {
		return err
	}
	for i, c := range candidates {
		evidence, _ := json.Marshal(c.Evidence)
		if _, err = tx.ExecContext(ctx, `INSERT INTO album_musicbrainz_candidates(album_id,position,release_id,release_group_id,title,artist_credit,score,evidence_json) VALUES(?,?,?,?,?,?,?,?)`, albumID, i, c.ReleaseID, nullStr(c.ReleaseGroupID), c.Title, c.ArtistCredit, c.Score, evidence); err != nil {
			return err
		}
	}
	return nil
}

type CanonicalTrack struct {
	Disc, Index, DurationMs int
	Title, RecordingID      string
	Credits                 []source.ArtistCredit
}

type CanonicalRelease struct {
	ReleaseID, ReleaseGroupID string
	AlbumCredits              []source.ArtistCredit
	Tracks                    []CanonicalTrack
	Authoritative             bool
}

func (s *Store) ApplyMusicBrainzRelease(ctx context.Context, album MusicBrainzAlbum, release CanonicalRelease, seq int64) (bool, error) {
	return s.applyMusicBrainzRelease(ctx, album, release, seq, nil)
}

type musicBrainzResult struct {
	selected   MusicBrainzCandidate
	candidates []MusicBrainzCandidate
	next       time.Time
}

// ApplyMusicBrainzMatch atomically applies canonical identity and records resolver state.
func (s *Store) ApplyMusicBrainzMatch(ctx context.Context, album MusicBrainzAlbum, release CanonicalRelease, seq int64, selected MusicBrainzCandidate, candidates []MusicBrainzCandidate, next time.Time) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	return s.applyMusicBrainzRelease(ctx, album, release, seq, &musicBrainzResult{selected: selected, candidates: candidates, next: next})
}

func (s *Store) applyMusicBrainzRelease(ctx context.Context, album MusicBrainzAlbum, release CanonicalRelease, seq int64, result *musicBrainzResult) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := musicBrainzSnapshotTx(ctx, tx, album.AlbumID)
	if err != nil {
		return false, err
	}
	if !sameMusicBrainzSnapshot(album, current) {
		return false, nil
	}
	beforeCounts, err := albumArtistCountsTx(ctx, tx, album.AlbumID)
	if err != nil {
		return false, err
	}
	changed := false
	res, err := tx.ExecContext(ctx, `UPDATE albums SET musicbrainz_release_id=?,musicbrainz_release_group_id=?,change_seq=CASE WHEN musicbrainz_release_id IS NOT ? OR musicbrainz_release_group_id IS NOT ? THEN ? ELSE change_seq END WHERE album_id=?`, release.ReleaseID, release.ReleaseGroupID, release.ReleaseID, release.ReleaseGroupID, seq, album.AlbumID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 && (album.ReleaseID != release.ReleaseID || album.ReleaseGroupID != release.ReleaseGroupID) {
		changed = true
		// New identity means new artwork sources (CAA is release-group-keyed):
		// an artless album's pre-identity miss schedule must not delay them.
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET art_next_attempt_at=0,art_miss_count=0 WHERE album_id=? AND art_id IS NULL`, album.AlbumID); err != nil {
			return false, err
		}
	}
	if len(release.AlbumCredits) > 0 {
		c, err := replaceCreditsTx(ctx, tx, "album_artist_credits", "album_id", album.AlbumID, album.PrimaryArtist.ArtistID, release.AlbumCredits, seq)
		if err != nil {
			return false, err
		}
		changed = changed || c
		var primaryID string
		if err := tx.QueryRowContext(ctx, `SELECT artist_id FROM album_artist_credits WHERE album_id=? AND position=0`, album.AlbumID).Scan(&primaryID); err != nil {
			return false, err
		}
		if primaryID != album.PrimaryArtist.ArtistID {
			changed = true
		}
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET artist_id=?,change_seq=CASE WHEN artist_id IS NOT ? THEN ? ELSE change_seq END WHERE album_id=?`, primaryID, primaryID, seq, album.AlbumID); err != nil {
			return false, err
		}
	}
	for _, local := range album.Tracks {
		var match *CanonicalTrack
		matches := 0
		for i := range release.Tracks {
			r := &release.Tracks[i]
			position := discNumber(r.Disc) == discNumber(local.Disc) && r.Index == local.Index
			if position && (release.Authoritative || normalized(r.Title) == normalized(local.Title) && durationClose(r.DurationMs, local.DurationMs)) {
				match = r
				matches++
			}
		}
		if match == nil || matches != 1 {
			continue
		}
		if local.MusicBrainzRecordingID != match.RecordingID {
			changed = true
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tracks SET musicbrainz_recording_id=?,change_seq=CASE WHEN musicbrainz_recording_id IS NOT ? THEN ? ELSE change_seq END WHERE track_id=?`, match.RecordingID, match.RecordingID, seq, local.TrackID); err != nil {
			return false, err
		}
		if len(match.Credits) > 0 {
			c, err := replaceCreditsTx(ctx, tx, "track_artist_credits", "track_id", local.TrackID, local.ArtistID, match.Credits, seq)
			if err != nil {
				return false, err
			}
			changed = changed || c
			var primaryID string
			if err := tx.QueryRowContext(ctx, `SELECT artist_id FROM track_artist_credits WHERE track_id=? AND position=0`, local.TrackID).Scan(&primaryID); err != nil {
				return false, err
			}
			if local.ArtistID != primaryID || local.ArtistName != match.Credits[0].Name {
				changed = true
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tracks SET artist_id=?,artist_name=?,change_seq=CASE WHEN artist_id IS NOT ? OR artist_name IS NOT ? THEN ? ELSE change_seq END WHERE track_id=?`, primaryID, match.Credits[0].Name, primaryID, match.Credits[0].Name, seq, local.TrackID); err != nil {
				return false, err
			}
		}
	}
	if changed {
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET change_seq=? WHERE album_id=?`, seq, album.AlbumID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artists SET
			change_seq = CASE WHEN missing != (NOT EXISTS (SELECT 1 FROM tracks JOIN track_artist_credits c ON c.track_id=tracks.track_id WHERE c.artist_id=artists.artist_id AND tracks.missing=0) AND NOT EXISTS (SELECT 1 FROM tracks WHERE tracks.artist_id=artists.artist_id AND tracks.missing=0) AND NOT EXISTS (SELECT 1 FROM albums JOIN album_artist_credits c ON c.album_id=albums.album_id WHERE c.artist_id=artists.artist_id AND albums.missing=0) AND NOT EXISTS (SELECT 1 FROM albums WHERE albums.artist_id=artists.artist_id AND albums.missing=0)) THEN ? ELSE change_seq END,
			missing = NOT EXISTS (SELECT 1 FROM tracks JOIN track_artist_credits c ON c.track_id=tracks.track_id WHERE c.artist_id=artists.artist_id AND tracks.missing=0) AND NOT EXISTS (SELECT 1 FROM tracks WHERE tracks.artist_id=artists.artist_id AND tracks.missing=0) AND NOT EXISTS (SELECT 1 FROM albums JOIN album_artist_credits c ON c.album_id=albums.album_id WHERE c.artist_id=artists.artist_id AND albums.missing=0) AND NOT EXISTS (SELECT 1 FROM albums WHERE albums.artist_id=artists.artist_id AND albums.missing=0)`, seq); err != nil {
			return false, err
		}
		afterCounts, err := albumArtistCountsTx(ctx, tx, album.AlbumID)
		if err != nil {
			return false, err
		}
		for artistID, before := range beforeCounts {
			if afterCounts[artistID] != before {
				if _, err := tx.ExecContext(ctx, `UPDATE artists SET change_seq=? WHERE artist_id=?`, seq, artistID); err != nil {
					return false, err
				}
			}
		}
		for artistID, after := range afterCounts {
			if beforeCounts[artistID] != after {
				if _, err := tx.ExecContext(ctx, `UPDATE artists SET change_seq=? WHERE artist_id=?`, seq, artistID); err != nil {
					return false, err
				}
			}
		}
	}
	if result != nil {
		if err := recordMusicBrainzResultTx(ctx, tx, album.AlbumID, "matched", result.selected, result.candidates, result.next, ""); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed, nil
}

type artistCounts struct {
	albums int
	tracks int
}

func albumArtistCountsTx(ctx context.Context, tx *sql.Tx, albumID string) (map[string]artistCounts, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artist_id FROM albums WHERE album_id=?
		UNION SELECT artist_id FROM album_artist_credits WHERE album_id=?
		UNION SELECT artist_id FROM tracks WHERE album_id=?
		UNION SELECT c.artist_id FROM track_artist_credits c JOIN tracks t ON t.track_id=c.track_id WHERE t.album_id=?`, albumID, albumID, albumID, albumID)
	if err != nil {
		return nil, err
	}
	var artistIDs []string
	for rows.Next() {
		var artistID string
		if err := rows.Scan(&artistID); err != nil {
			rows.Close()
			return nil, err
		}
		artistIDs = append(artistIDs, artistID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	counts := make(map[string]artistCounts, len(artistIDs))
	for _, artistID := range artistIDs {
		var count artistCounts
		if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT count(DISTINCT al.album_id) FROM albums al JOIN album_artist_credits c ON c.album_id=al.album_id WHERE c.artist_id=? AND al.missing=0),
			(SELECT count(DISTINCT t.track_id) FROM tracks t JOIN track_artist_credits c ON c.track_id=t.track_id WHERE c.artist_id=? AND t.missing=0)`, artistID, artistID).Scan(&count.albums, &count.tracks); err != nil {
			return nil, err
		}
		counts[artistID] = count
	}
	return counts, nil
}

func musicBrainzSnapshotTx(ctx context.Context, tx *sql.Tx, albumID string) (MusicBrainzAlbum, error) {
	var album MusicBrainzAlbum
	err := tx.QueryRowContext(ctx, `SELECT album_id,title,COALESCE(year,0),COALESCE(musicbrainz_release_id,''),COALESCE(musicbrainz_release_group_id,''),change_seq FROM albums WHERE album_id=?`, albumID).Scan(&album.AlbumID, &album.Title, &album.Year, &album.ReleaseID, &album.ReleaseGroupID, &album.Version)
	if err != nil {
		return album, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT track_id,COALESCE(idx,0),COALESCE(disc,0),title,duration_ms FROM tracks WHERE album_id=? AND missing=0 ORDER BY track_id`, albumID)
	if err != nil {
		return album, err
	}
	defer rows.Close()
	for rows.Next() {
		var track TrackRow
		if err := rows.Scan(&track.TrackID, &track.Index, &track.Disc, &track.Title, &track.DurationMs); err != nil {
			return album, err
		}
		album.Tracks = append(album.Tracks, track)
	}
	album.TrackCount = len(album.Tracks)
	return album, rows.Err()
}

func sameMusicBrainzSnapshot(expected, current MusicBrainzAlbum) bool {
	if expected.AlbumID != current.AlbumID || expected.Title != current.Title || expected.Year != current.Year || expected.ReleaseID != current.ReleaseID || expected.ReleaseGroupID != current.ReleaseGroupID || expected.Version != current.Version || len(expected.Tracks) != len(current.Tracks) {
		return false
	}
	tracks := make(map[string]TrackRow, len(current.Tracks))
	for _, track := range current.Tracks {
		tracks[track.TrackID] = track
	}
	for _, track := range expected.Tracks {
		currentTrack, ok := tracks[track.TrackID]
		if !ok || track.Index != currentTrack.Index || track.Disc != currentTrack.Disc || track.Title != currentTrack.Title || track.DurationMs != currentTrack.DurationMs {
			return false
		}
	}
	return true
}

func replaceCreditsTx(ctx context.Context, tx *sql.Tx, table, ownerColumn, ownerID, primaryOwnerID string, credits []source.ArtistCredit, seq int64) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(a.musicbrainz_id,''),c.name,c.join_phrase FROM `+table+` c JOIN artists a ON a.artist_id=c.artist_id WHERE c.`+ownerColumn+`=? ORDER BY c.position`, ownerID)
	if err != nil {
		return false, err
	}
	type old struct{ mbid, name, join string }
	var existing []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.mbid, &o.name, &o.join); err != nil {
			rows.Close()
			return false, err
		}
		existing = append(existing, o)
	}
	rows.Close()
	if len(existing) == len(credits) {
		same := true
		for i, c := range credits {
			if existing[i] != (old{c.MBID, c.Name, c.JoinPhrase}) {
				same = false
				break
			}
		}
		if same {
			return false, nil
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+ownerColumn+`=?`, ownerID); err != nil {
		return false, err
	}
	for i, c := range credits {
		var artistID, name string
		err = tx.QueryRowContext(ctx, `SELECT artist_id,name FROM artists WHERE musicbrainz_id=?`, c.MBID).Scan(&artistID, &name)
		if errors.Is(err, sql.ErrNoRows) {
			if i == 0 && primaryOwnerID != "" {
				err = tx.QueryRowContext(ctx, `SELECT artist_id,name FROM artists WHERE artist_id=? AND musicbrainz_id IS NULL`, primaryOwnerID).Scan(&artistID, &name)
			} else if i > 0 {
				err = tx.QueryRowContext(ctx, `SELECT min(artist_id),min(name) FROM artists WHERE musicbrainz_id IS NULL AND name=? HAVING count(*)=1`, c.Name).Scan(&artistID, &name)
			}
			if errors.Is(err, sql.ErrNoRows) {
				artistID, name = NewID("art"), ""
				_, err = tx.ExecContext(ctx, `INSERT INTO artists(artist_id,name,musicbrainz_id,created_at,change_seq) VALUES(?,?,?,?,?)`, artistID, c.Name, c.MBID, time.Now().Unix(), seq)
			} else if err == nil {
				// Adoption grants fanart eligibility (MBID-keyed); clear any
				// pre-identity artwork miss schedule.
				_, err = tx.ExecContext(ctx, `UPDATE artists SET musicbrainz_id=?,missing=0,change_seq=?,art_next_attempt_at=0,art_miss_count=0 WHERE artist_id=?`, c.MBID, seq, artistID)
			}
		}
		if err != nil {
			return false, err
		}
		if c.Name != name && name != "" {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artist_aliases(artist_id,name) VALUES(?,?)`, artistID, name); err != nil {
				return false, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE artists SET name=?,change_seq=? WHERE artist_id=?`, c.Name, seq, artistID); err != nil {
				return false, err
			}
		}
		if i < len(existing) && existing[i].name != "" && existing[i].name != c.Name {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artist_aliases(artist_id,name) VALUES(?,?)`, artistID, existing[i].name); err != nil {
				return false, err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+table+`(`+ownerColumn+`,position,artist_id,name,join_phrase) VALUES(?,?,?,?,?)`, ownerID, i, artistID, c.Name, c.JoinPhrase); err != nil {
			return false, err
		}
	}
	return true, nil
}

func normalized(v string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, v)
	return strings.Join(strings.Fields(v), " ")
}
func discNumber(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}
func durationClose(a, b int) bool { return a == 0 || b == 0 || a-b < 3000 && b-a < 3000 }
