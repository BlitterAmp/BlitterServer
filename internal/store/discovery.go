package store

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

// notForMeFilter excludes tracks the profile marked not_for_me at any level
// (track, album, or artist) — the contract's "never surface in generated
// content" rule. Callers append profileID three times.
const notForMeFilter = ` AND NOT EXISTS (
	SELECT 1 FROM loves l WHERE l.state = 'not_for_me' AND l.profile_id = ?
	AND (l.ref IN (t.track_id, t.album_id, t.artist_id)
		OR EXISTS (SELECT 1 FROM track_artist_credits c WHERE c.track_id=t.track_id AND c.artist_id=l.ref)
		OR (l.ref LIKE 'genre:%' AND EXISTS (
			SELECT 1 FROM track_artist_credits c JOIN artist_genres g ON g.artist_id=c.artist_id
			WHERE c.track_id=t.track_id AND l.ref='genre:' || g.name))))`

// RecentlyPlayedTracks returns distinct tracks by most recent completed play.
func (s *Store) RecentlyPlayedTracks(ctx context.Context, profileID string, limit int) ([]TrackRow, error) {
	return s.listTracksWhere(ctx, `t.track_id IN (
		SELECT e.track_id FROM playback_events e
		WHERE e.profile_id = ? AND e.type = 'ended'
		GROUP BY e.track_id)`+notForMeFilter,
		`(SELECT max(e2.at) FROM playback_events e2
		  WHERE e2.profile_id = ? AND e2.track_id = t.track_id AND e2.type = 'ended') DESC
		 LIMIT `+fmt.Sprint(limit), profileID, profileID, profileID)
}

// RecentlyAddedAlbums returns the newest non-missing albums.
func (s *Store) RecentlyAddedAlbums(ctx context.Context, limit int) ([]AlbumRow, error) {
	rows, err := s.db.QueryContext(ctx,
		albumSelect+fmt.Sprintf(` WHERE al.missing = 0 ORDER BY al.created_at DESC, al.album_id LIMIT %d`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlbumRow
	for rows.Next() {
		a, _, err := scanAlbum(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.hydrateAlbumCredits(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MixInfo describes one available mix for the calling profile.
type MixInfo struct {
	MixID         string
	Kind          string
	Title         string
	TrackCount    int
	CollageArtIDs []string
}

const mixLimit = 50
const generatedMixLimit = 100

type mixQuery struct {
	where, order         string
	whereArgs, orderArgs []any
	limit                int
}

func periodOffset(profileID, period string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(profileID + "\x00" + period))
	return int(h.Sum32()%8) + 5
}

// mixWhere returns the SQL fragment + args selecting a mix's members.
// The profile arg for notForMeFilter is appended by the caller.
func mixWhere(profileID, mixID string) (mixQuery, error) {
	now := time.Now().UTC()
	switch {
	case mixID == "dailyMix":
		offset := periodOffset(profileID, now.Format("2006-01-02"))
		return mixQuery{where: "1=1", order: fmt.Sprintf(`CASE WHEN EXISTS (
			SELECT 1 FROM playback_events e WHERE e.profile_id=? AND e.track_id=t.track_id AND e.type='ended')
			OR EXISTS (SELECT 1 FROM loves l WHERE l.profile_id=? AND l.state='loved' AND
				(l.ref IN (t.track_id,t.album_id,t.artist_id) OR EXISTS (SELECT 1 FROM track_artist_credits c WHERE c.track_id=t.track_id AND c.artist_id=l.ref)))
			THEN 0 ELSE 1 END, substr(t.track_id,%d),t.track_id`, offset), orderArgs: []any{profileID, profileID}, limit: generatedMixLimit}, nil
	case mixID == "discoverWeekly":
		year, week := now.ISOWeek()
		offset := periodOffset(profileID, fmt.Sprintf("%04d-W%02d", year, week))
		return mixQuery{where: "1=1", order: fmt.Sprintf(`CASE WHEN EXISTS (
			SELECT 1 FROM playback_events e WHERE e.profile_id=? AND e.track_id=t.track_id AND e.type='ended')
			THEN 1 ELSE 0 END, substr(t.track_id,%d),t.track_id`, offset), orderArgs: []any{profileID}, limit: generatedMixLimit}, nil
	case mixID == "releaseRadar":
		return mixQuery{where: `length(al.release_date)=10 AND date(al.release_date) BETWEEN date('now','-1 month') AND date('now')
			AND t.created_at >= CAST(strftime('%s','now','-1 month') AS INTEGER)`, order: "date(al.release_date) DESC,t.created_at DESC,al.album_id,COALESCE(t.disc,1),COALESCE(t.idx,0)", limit: generatedMixLimit}, nil
	case mixID == "forYou":
		// Artists the profile plays or loves, minus explicit rejections.
		return mixQuery{where: `(EXISTS (SELECT 1 FROM track_artist_credits tc WHERE tc.track_id=t.track_id AND tc.artist_id IN (
			SELECT pc.artist_id FROM playback_events e JOIN track_artist_credits pc ON pc.track_id=e.track_id
			WHERE e.profile_id = ? AND e.type = 'ended'))
			OR EXISTS (SELECT 1 FROM loves lv WHERE lv.profile_id = ? AND lv.state = 'loved'
			           AND (lv.ref IN (t.track_id, t.album_id, t.artist_id) OR EXISTS (SELECT 1 FROM track_artist_credits c WHERE c.track_id=t.track_id AND c.artist_id=lv.ref))))`,
			order: "random()", whereArgs: []any{profileID, profileID}, limit: mixLimit}, nil
	case mixID == "topRated":
		return mixQuery{where: `EXISTS (SELECT 1 FROM ratings r WHERE r.profile_id = ? AND r.item_id = t.track_id AND r.rating10 >= 8)`, order: `(SELECT r.rating10 FROM ratings r WHERE r.profile_id = ? AND r.item_id = t.track_id) DESC`, whereArgs: []any{profileID}, orderArgs: []any{profileID}, limit: mixLimit}, nil
	case mixID == "heavyRotation":
		return mixQuery{where: `(SELECT count(*) FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id
			AND e.type = 'ended' AND e.at > datetime('now', '-30 days')) > 0`,
			order: `(SELECT count(*) FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id AND e.type = 'ended' AND e.at > datetime('now', '-30 days')) DESC`, whereArgs: []any{profileID}, orderArgs: []any{profileID}, limit: mixLimit}, nil
	case mixID == "rediscover":
		return mixQuery{where: `EXISTS (SELECT 1 FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id AND e.type = 'ended')
			AND NOT EXISTS (SELECT 1 FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id
			                AND e.type = 'ended' AND e.at > datetime('now', '-60 days'))`,
			order: "random()", whereArgs: []any{profileID, profileID}, limit: mixLimit}, nil
	case strings.HasPrefix(mixID, "genre:"):
		return mixQuery{where: `EXISTS (SELECT 1 FROM track_artist_credits c JOIN artist_genres g ON g.artist_id=c.artist_id WHERE c.track_id=t.track_id AND g.name=? COLLATE NOCASE)`, order: "random()", whereArgs: []any{strings.TrimPrefix(mixID, "genre:")}, limit: generatedMixLimit}, nil
	}
	return mixQuery{}, fmt.Errorf("mix %s: %w", mixID, ErrNotFound)
}

// MixTracks materializes a mix for the profile.
func (s *Store) MixTracks(ctx context.Context, profileID, mixID string) ([]TrackRow, error) {
	query, err := mixWhere(profileID, mixID)
	if err != nil {
		return nil, err
	}
	args := append(query.whereArgs, profileID)
	args = append(args, query.orderArgs...)
	rows, err := s.listTracksWhere(ctx,
		query.where+notForMeFilter, query.order+fmt.Sprintf(" LIMIT %d", query.limit), args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("mix %s is empty: %w", mixID, ErrNotFound)
	}
	return rows, nil
}

// AvailableMixes lists the mixes that currently materialize non-empty.
func (s *Store) AvailableMixes(ctx context.Context, profileID string) ([]MixInfo, error) {
	var out []MixInfo
	titles := map[string]string{
		"dailyMix": "Daily Mix", "discoverWeekly": "Discover Weekly", "releaseRadar": "Release Radar",
		"forYou": "For You", "topRated": "Top Rated",
		"heavyRotation": "Heavy Rotation", "rediscover": "Rediscover",
	}
	for _, kind := range []string{"dailyMix", "discoverWeekly", "releaseRadar", "forYou", "topRated", "heavyRotation", "rediscover"} {
		tracks, err := s.MixTracks(ctx, profileID, kind)
		if err != nil {
			continue // empty mixes simply don't appear
		}
		out = append(out, mixInfo(kind, kind, titles[kind], tracks))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT substr(ref,7) FROM loves WHERE profile_id=? AND kind='genre' AND state='loved' ORDER BY updated_at DESC`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var genre string
		if err := rows.Scan(&genre); err != nil {
			return nil, err
		}
		tracks, err := s.MixTracks(ctx, profileID, "genre:"+genre)
		if err != nil {
			continue
		}
		out = append(out, mixInfo("genre:"+genre, "genre", genre, tracks))
	}
	return out, rows.Err()
}

func mixInfo(mixID, kind, title string, tracks []TrackRow) MixInfo {
	info := MixInfo{MixID: mixID, Kind: kind, Title: title, TrackCount: len(tracks)}
	seen := map[string]bool{}
	for _, tr := range tracks {
		if tr.ArtID != "" && !seen[tr.ArtID] && len(info.CollageArtIDs) < 4 {
			seen[tr.ArtID] = true
			info.CollageArtIDs = append(info.CollageArtIDs, tr.ArtID)
		}
	}
	return info
}

// RadioNext picks playable tracks for the seeds: seed artists (and their
// genres) weighted first, library-wide fallback, excluding the given ids and
// anything not_for_me.
func (s *Store) RadioNext(ctx context.Context, profileID string, seedArtistIDs, excludeTrackIDs []string, count int) ([]TrackRow, error) {
	exclude := "1=1"
	args := []any{}
	if len(excludeTrackIDs) > 0 {
		exclude = "t.track_id NOT IN (?" + strings.Repeat(",?", len(excludeTrackIDs)-1) + ")"
		for _, id := range excludeTrackIDs {
			args = append(args, id)
		}
	}

	var out []TrackRow
	seen := map[string]bool{}
	add := func(rows []TrackRow) {
		for _, r := range rows {
			if len(out) >= count {
				return
			}
			if !seen[r.TrackID] {
				seen[r.TrackID] = true
				out = append(out, r)
			}
		}
	}

	if len(seedArtistIDs) > 0 {
		in := "?" + strings.Repeat(",?", len(seedArtistIDs)-1)
		seedArgs := append([]any{}, args...) // exclusions
		for _, id := range seedArtistIDs {   // artist IN (...)
			seedArgs = append(seedArgs, id)
		}
		for _, id := range seedArtistIDs { // genre subquery IN (...)
			seedArgs = append(seedArgs, id)
		}
		seedArgs = append(seedArgs, profileID) // notForMeFilter
		rows, err := s.listTracksWhere(ctx,
			exclude+` AND (EXISTS (SELECT 1 FROM track_artist_credits c WHERE c.track_id=t.track_id AND c.artist_id IN (`+in+`)) OR t.genre IN (SELECT t2.genre FROM tracks t2 JOIN track_artist_credits c2 ON c2.track_id=t2.track_id WHERE c2.artist_id IN (`+in+`)))`+
				notForMeFilter, fmt.Sprintf("random() LIMIT %d", count),
			seedArgs...)
		if err != nil {
			return nil, err
		}
		add(rows)
	}
	if len(out) < count {
		fallbackArgs := append([]any{}, args...)
		fallbackArgs = append(fallbackArgs, profileID)
		rows, err := s.listTracksWhere(ctx,
			exclude+notForMeFilter, fmt.Sprintf("random() LIMIT %d", count),
			fallbackArgs...)
		if err != nil {
			return nil, err
		}
		add(rows)
	}
	return out, nil
}
