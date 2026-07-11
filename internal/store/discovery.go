package store

import (
	"context"
	"fmt"
	"strings"
)

// notForMeFilter excludes tracks the profile marked not_for_me at any level
// (track, album, or artist) — the contract's "never surface in generated
// content" rule. Callers append profileID three times.
const notForMeFilter = ` AND NOT EXISTS (
	SELECT 1 FROM loves l WHERE l.state = 'not_for_me' AND l.profile_id = ?
	AND l.ref IN (t.track_id, t.album_id, t.artist_id))`

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
	return out, rows.Err()
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

// mixWhere returns the SQL fragment + args selecting a mix's members.
// The profile arg for notForMeFilter is appended by the caller.
func mixWhere(profileID, mixID string) (where, order string, args []any, err error) {
	switch {
	case mixID == "forYou":
		// Artists the profile plays or loves, minus explicit rejections.
		return `(t.artist_id IN (
			SELECT t2.artist_id FROM playback_events e JOIN tracks t2 ON t2.track_id = e.track_id
			WHERE e.profile_id = ? AND e.type = 'ended')
			OR EXISTS (SELECT 1 FROM loves lv WHERE lv.profile_id = ? AND lv.state = 'loved'
			           AND lv.ref IN (t.track_id, t.album_id, t.artist_id)))`,
			"random()", []any{profileID, profileID}, nil
	case mixID == "topRated":
		return `EXISTS (SELECT 1 FROM ratings r WHERE r.profile_id = ? AND r.item_id = t.track_id AND r.rating10 >= 8)`,
			`(SELECT r.rating10 FROM ratings r WHERE r.profile_id = ? AND r.item_id = t.track_id) DESC`,
			[]any{profileID, profileID}, nil
	case mixID == "heavyRotation":
		return `(SELECT count(*) FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id
			AND e.type = 'ended' AND e.at > datetime('now', '-30 days')) > 0`,
			`(SELECT count(*) FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id
			AND e.type = 'ended' AND e.at > datetime('now', '-30 days')) DESC`,
			[]any{profileID, profileID}, nil
	case mixID == "rediscover":
		return `EXISTS (SELECT 1 FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id AND e.type = 'ended')
			AND NOT EXISTS (SELECT 1 FROM playback_events e WHERE e.profile_id = ? AND e.track_id = t.track_id
			                AND e.type = 'ended' AND e.at > datetime('now', '-60 days'))`,
			"random()", []any{profileID, profileID}, nil
	case strings.HasPrefix(mixID, "genre:"):
		return `t.genre = ?`, "random()", []any{strings.TrimPrefix(mixID, "genre:")}, nil
	}
	return "", "", nil, fmt.Errorf("mix %s: %w", mixID, ErrNotFound)
}

// MixTracks materializes a mix for the profile.
func (s *Store) MixTracks(ctx context.Context, profileID, mixID string) ([]TrackRow, error) {
	where, order, args, err := mixWhere(profileID, mixID)
	if err != nil {
		return nil, err
	}
	args = append(args, profileID) // notForMeFilter
	rows, err := s.listTracksWhere(ctx,
		where+notForMeFilter, order+fmt.Sprintf(" LIMIT %d", mixLimit), args...)
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
		"forYou": "For You", "topRated": "Top Rated",
		"heavyRotation": "Heavy Rotation", "rediscover": "Rediscover",
	}
	for _, kind := range []string{"forYou", "topRated", "heavyRotation", "rediscover"} {
		tracks, err := s.MixTracks(ctx, profileID, kind)
		if err != nil {
			continue // empty mixes simply don't appear
		}
		out = append(out, mixInfo(kind, kind, titles[kind], tracks))
	}
	genres, err := s.ListGenres(ctx)
	if err != nil {
		return nil, err
	}
	for i, g := range genres {
		if i >= 4 {
			break
		}
		tracks, err := s.MixTracks(ctx, profileID, "genre:"+g.Name)
		if err != nil {
			continue
		}
		out = append(out, mixInfo("genre:"+g.Name, "genre", g.Name, tracks))
	}
	return out, nil
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
			exclude+` AND (t.artist_id IN (`+in+`) OR t.genre IN (SELECT t2.genre FROM tracks t2 WHERE t2.artist_id IN (`+in+`)))`+
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
