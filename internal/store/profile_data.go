package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ── playlists ──────────────────────────────────────────────────

// PlaylistRow backs the contract's Playlist schema for native playlists.
type PlaylistRow struct {
	PlaylistID     string
	Title          string
	Visibility     string
	Origin         string
	OwnerProfileID string
	OwnerName      string
	TrackCount     int
	DurationMs     int
	ArtID          string
	UpdatedAt      int64
}

// PlaylistItem is one entry with its removal handle.
type PlaylistItem struct {
	ItemID string
	Track  TrackRow
}

const playlistSelect = `
	SELECT p.playlist_id, p.title, p.visibility, p.origin,
	       COALESCE(p.owner_profile_id, ''), COALESCE(pr.name, ''), p.updated_at,
	       (SELECT count(*) FROM playlist_items i WHERE i.playlist_id = p.playlist_id),
	       COALESCE((SELECT sum(t.duration_ms) FROM playlist_items i JOIN tracks t ON t.track_id = i.track_id
	                 WHERE i.playlist_id = p.playlist_id), 0),
	       COALESCE((SELECT COALESCE(t.art_id, al.art_id) FROM playlist_items i
	                 JOIN tracks t ON t.track_id = i.track_id
	                 JOIN albums al ON al.album_id = t.album_id
	                 WHERE i.playlist_id = p.playlist_id AND COALESCE(t.art_id, al.art_id) IS NOT NULL
	                 ORDER BY i.position LIMIT 1), '')
	FROM playlists p LEFT JOIN profiles pr ON pr.profile_id = p.owner_profile_id`

func scanPlaylist(scan func(...any) error) (PlaylistRow, error) {
	var p PlaylistRow
	err := scan(&p.PlaylistID, &p.Title, &p.Visibility, &p.Origin, &p.OwnerProfileID,
		&p.OwnerName, &p.UpdatedAt, &p.TrackCount, &p.DurationMs, &p.ArtID)
	return p, err
}

func (s *Store) CreatePlaylist(ctx context.Context, ownerProfileID, title, visibility string, trackIDs []string) (PlaylistRow, error) {
	if visibility == "" {
		visibility = "private"
	}
	now := time.Now().Unix()
	id := NewID("pl")
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO playlists (playlist_id, owner_profile_id, title, visibility, origin, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'blitterserver', ?, ?)`, id, ownerProfileID, title, visibility, now, now)
	if err != nil {
		return PlaylistRow{}, err
	}
	if len(trackIDs) > 0 {
		if err := s.AppendPlaylistTracks(ctx, id, trackIDs); err != nil {
			return PlaylistRow{}, err
		}
	}
	p, _, err := s.GetPlaylist(ctx, id)
	return p, err
}

func (s *Store) GetPlaylist(ctx context.Context, playlistID string) (PlaylistRow, bool, error) {
	row := s.db.QueryRowContext(ctx, playlistSelect+` WHERE p.playlist_id = ?`, playlistID)
	p, err := scanPlaylist(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return PlaylistRow{}, false, nil
	}
	if err != nil {
		return PlaylistRow{}, false, err
	}
	return p, true, nil
}

// ListPlaylists returns the profile's own playlists plus other profiles'
// shared/collaborative ones (and any read-only source playlists).
func (s *Store) ListPlaylists(ctx context.Context, profileID string) ([]PlaylistRow, error) {
	rows, err := s.db.QueryContext(ctx, playlistSelect+`
		WHERE p.owner_profile_id = ? OR p.visibility IN ('shared', 'collaborative') OR p.origin = 'source'
		ORDER BY p.title`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaylistRow
	for rows.Next() {
		p, err := scanPlaylist(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdatePlaylist(ctx context.Context, playlistID string, title, visibility *string) (PlaylistRow, error) {
	if title != nil {
		if _, err := s.exec1(ctx, `UPDATE playlists SET title = ?, updated_at = ? WHERE playlist_id = ?`,
			*title, time.Now().Unix(), playlistID); err != nil {
			return PlaylistRow{}, fmt.Errorf("playlist %s: %w", playlistID, err)
		}
	}
	if visibility != nil {
		if _, err := s.exec1(ctx, `UPDATE playlists SET visibility = ?, updated_at = ? WHERE playlist_id = ?`,
			*visibility, time.Now().Unix(), playlistID); err != nil {
			return PlaylistRow{}, fmt.Errorf("playlist %s: %w", playlistID, err)
		}
	}
	p, found, err := s.GetPlaylist(ctx, playlistID)
	if err != nil {
		return PlaylistRow{}, err
	}
	if !found {
		return PlaylistRow{}, fmt.Errorf("playlist %s: %w", playlistID, ErrNotFound)
	}
	return p, nil
}

func (s *Store) DeletePlaylist(ctx context.Context, playlistID string) error {
	if _, err := s.exec1(ctx, `DELETE FROM playlists WHERE playlist_id = ?`, playlistID); err != nil {
		return fmt.Errorf("playlist %s: %w", playlistID, err)
	}
	return nil
}

func (s *Store) AppendPlaylistTracks(ctx context.Context, playlistID string, trackIDs []string) error {
	if _, found, err := s.GetPlaylist(ctx, playlistID); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("playlist %s: %w", playlistID, ErrNotFound)
	}
	now := time.Now().Unix()
	for _, trackID := range trackIDs {
		if _, found, err := s.GetTrack(ctx, trackID); err != nil {
			return err
		} else if !found {
			return fmt.Errorf("track %s: %w", trackID, ErrNotFound)
		}
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO playlist_items (item_id, playlist_id, track_id, position, added_at)
			VALUES (?, ?, ?, COALESCE((SELECT max(position) FROM playlist_items WHERE playlist_id = ?), 0) + 1, ?)`,
			NewID("pli"), playlistID, trackID, playlistID, now)
		if err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE playlists SET updated_at = ? WHERE playlist_id = ?`, now, playlistID)
	return err
}

func (s *Store) RemovePlaylistItem(ctx context.Context, playlistID, itemID string) error {
	if _, err := s.exec1(ctx,
		`DELETE FROM playlist_items WHERE playlist_id = ? AND item_id = ?`, playlistID, itemID); err != nil {
		return fmt.Errorf("playlist item %s: %w", itemID, err)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE playlists SET updated_at = ? WHERE playlist_id = ?`, time.Now().Unix(), playlistID)
	return err
}

// ListPlaylistItems pages by position keyset.
func (s *Store) ListPlaylistItems(ctx context.Context, playlistID, cur string, limit int) ([]PlaylistItem, string, error) {
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	where := "i.playlist_id = ? AND t.missing = 0"
	args := []any{playlistID}
	if c.ID != "" {
		where += " AND (cast(i.position as text) > ? OR (cast(i.position as text) = ? AND i.item_id > ?))"
		args = append(args, c.K, c.K, c.ID)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT i.item_id, cast(i.position as text), %s%s
		JOIN playlist_items i ON i.track_id = t.track_id
		WHERE %s ORDER BY i.position, i.item_id LIMIT %d`,
		trackCols, trackFrom, where, limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []PlaylistItem
	var keys []string
	for rows.Next() {
		var item PlaylistItem
		var key string
		dest := []any{&item.ItemID, &key,
			&item.Track.TrackID, &item.Track.Title, &item.Track.Index, &item.Track.Disc,
			&item.Track.ArtistID, &item.Track.ArtistName, &item.Track.AlbumID, &item.Track.AlbumTitle,
			&item.Track.Genre, &item.Track.DurationMs, &item.Track.ArtID,
			&item.Track.Container, &item.Track.Codec, &item.Track.BitrateKbps, &item.Track.SizeBytes, &item.Track.MusicBrainzRecordingID}
		if err := rows.Scan(dest...); err != nil {
			return nil, "", err
		}
		out = append(out, item)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(keys[limit-1], out[limit-1].ItemID)
	}
	for i := range out {
		if err := s.hydrateTrackCredits(ctx, &out[i].Track); err != nil {
			return nil, "", err
		}
	}
	return out, next, nil
}

// ── loves ──────────────────────────────────────────────────────

// LoveRow backs the contract's LoveRecord.
type LoveRow struct {
	LoveID     string
	Ref        string
	Kind       string
	State      string
	Name       string
	ArtistName string
	Owned      bool
	UpdatedAt  time.Time
}

// resolveRef maps a canonical id to (kind, name, artistName, owned).
func (s *Store) resolveRef(ctx context.Context, ref string) (kind, name, artistName string, owned bool, err error) {
	switch {
	case strings.HasPrefix(ref, "art_"):
		a, found, err := s.GetArtist(ctx, ref)
		if err != nil {
			return "", "", "", false, err
		}
		if !found {
			var external string
			err = s.db.QueryRowContext(ctx, `SELECT name FROM external_artists WHERE artist_id=?`, ref).Scan(&external)
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", "", false, orNotFound(nil, ref)
			}
			if err != nil {
				return "", "", "", false, err
			}
			return "artist", external, "", false, nil
		}
		return "artist", a.Name, "", true, nil
	case strings.HasPrefix(ref, "alb_"):
		a, found, err := s.GetAlbum(ctx, ref)
		if err != nil || !found {
			return "", "", "", false, orNotFound(err, ref)
		}
		return "album", a.Title, a.ArtistName, true, nil
	case strings.HasPrefix(ref, "trk_"):
		t, found, err := s.GetTrack(ctx, ref)
		if err != nil || !found {
			return "", "", "", false, orNotFound(err, ref)
		}
		return "track", t.Title, t.ArtistName, true, nil
	case strings.HasPrefix(ref, "genre:"):
		requested := strings.TrimSpace(strings.TrimPrefix(ref, "genre:"))
		var name string
		err := s.db.QueryRowContext(ctx, `SELECT name FROM artist_genres WHERE name=? COLLATE NOCASE LIMIT 1`, requested).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", false, orNotFound(nil, ref)
		}
		if err != nil {
			return "", "", "", false, err
		}
		return "genre", name, "", true, nil
	}
	return "", "", "", false, fmt.Errorf("ref %s: %w", ref, ErrNotFound)
}

func orNotFound(err error, ref string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("ref %s: %w", ref, ErrNotFound)
}

// SetLove applies the tri-state. neutral deletes the row and returns a
// transient record with state neutral.
func (s *Store) SetLove(ctx context.Context, profileID, ref, state string) (LoveRow, error) {
	kind, name, artistName, owned, err := s.resolveRef(ctx, ref)
	if err != nil {
		return LoveRow{}, err
	}
	now := time.Now().UTC()
	rec := LoveRow{Ref: ref, Kind: kind, State: state, Name: name, ArtistName: artistName, Owned: owned, UpdatedAt: now}
	switch state {
	case "neutral":
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM loves WHERE profile_id = ? AND ref = ?`, profileID, ref); err != nil {
			return LoveRow{}, err
		}
		rec.LoveID = ""
		return rec, nil
	case "loved", "not_for_me":
		id := NewID("lov")
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO loves (love_id, profile_id, ref, kind, state, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(profile_id, ref) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
			id, profileID, ref, kind, state, now.Format(time.RFC3339))
		if err != nil {
			return LoveRow{}, err
		}
		if err := s.db.QueryRowContext(ctx,
			`SELECT love_id FROM loves WHERE profile_id = ? AND ref = ?`, profileID, ref).Scan(&rec.LoveID); err != nil {
			return LoveRow{}, err
		}
		return rec, nil
	}
	return LoveRow{}, fmt.Errorf("love state %q: %w", state, ErrNotFound)
}

// ListLoves pages the profile's non-neutral records, newest first.
func (s *Store) ListLoves(ctx context.Context, profileID, kind, state, cur string, limit int) ([]LoveRow, string, error) {
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	where := "profile_id = ?"
	args := []any{profileID}
	if kind != "" {
		where += " AND kind = ?"
		args = append(args, kind)
	}
	if state != "" {
		where += " AND state = ?"
		args = append(args, state)
	}
	if c.ID != "" {
		where += " AND (updated_at < ? OR (updated_at = ? AND love_id > ?))"
		args = append(args, c.K, c.K, c.ID)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT love_id, ref, kind, state, updated_at FROM loves
		WHERE %s ORDER BY updated_at DESC, love_id LIMIT %d`, where, limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []LoveRow
	var keys []string
	for rows.Next() {
		var l LoveRow
		var updated string
		if err := rows.Scan(&l.LoveID, &l.Ref, &l.Kind, &l.State, &updated); err != nil {
			return nil, "", err
		}
		if l.UpdatedAt, err = time.Parse(time.RFC3339, updated); err != nil {
			return nil, "", err
		}
		out = append(out, l)
		keys = append(keys, updated)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(keys[limit-1], out[limit-1].LoveID)
	}
	// Decorate with names/ownership (small pages; per-row lookups are fine).
	for i := range out {
		_, name, artistName, owned, err := s.resolveRef(ctx, out[i].Ref)
		if err == nil {
			out[i].Name, out[i].ArtistName, out[i].Owned = name, artistName, owned
		} else {
			out[i].Name, out[i].Owned = out[i].Ref, false
		}
	}
	return out, next, nil
}

// GetLoveStates batch-resolves refs to states for entity decoration; absent
// refs simply aren't in the map (neutral).
func (s *Store) GetLoveStates(ctx context.Context, profileID string, refs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(refs) == 0 {
		return out, nil
	}
	q := `SELECT ref, state FROM loves WHERE profile_id = ? AND ref IN (?` +
		strings.Repeat(",?", len(refs)-1) + `)`
	args := make([]any, 0, len(refs)+1)
	args = append(args, profileID)
	for _, r := range refs {
		args = append(args, r)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref, state string
		if err := rows.Scan(&ref, &state); err != nil {
			return nil, err
		}
		out[ref] = state
	}
	return out, rows.Err()
}

// ── ratings ────────────────────────────────────────────────────

func (s *Store) SetRating(ctx context.Context, profileID, itemType, itemID string, rating10 int) error {
	if _, _, _, _, err := s.resolveRef(ctx, itemID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ratings (profile_id, item_id, item_type, rating10) VALUES (?, ?, ?, ?)
		ON CONFLICT(profile_id, item_id) DO UPDATE SET rating10 = excluded.rating10`,
		profileID, itemID, itemType, rating10)
	return err
}

func (s *Store) ClearRating(ctx context.Context, profileID, itemID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ratings WHERE profile_id = ? AND item_id = ?`, profileID, itemID)
	return err
}

// GetRatings batch-resolves item ids to ratings (0 = unrated/absent).
func (s *Store) GetRatings(ctx context.Context, profileID string, itemIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	q := `SELECT item_id, rating10 FROM ratings WHERE profile_id = ? AND item_id IN (?` +
		strings.Repeat(",?", len(itemIDs)-1) + `)`
	args := make([]any, 0, len(itemIDs)+1)
	args = append(args, profileID)
	for _, id := range itemIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r int
		if err := rows.Scan(&id, &r); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// ── playback events + presence + taste ─────────────────────────

// PlaybackEventRecord is one client-reported event.
type PlaybackEventRecord struct {
	EventID       string
	PlaySessionID string
	Type          string
	TrackID       string
	PositionSec   *float64
	At            time.Time
}

// IngestPlaybackEvents stores a batch (deduped by client event id, unknown
// tracks skipped) and maintains presence. Returns how many were newly
// accepted.
func (s *Store) IngestPlaybackEvents(ctx context.Context, profileID, deviceID string, events []PlaybackEventRecord) (int, error) {
	n, _, err := s.IngestPlaybackEventsDetailed(ctx, profileID, deviceID, events)
	return n, err
}

// IngestPlaybackEventsDetailed also returns the newly accepted ids so provider
// relays cannot be triggered by replaying an old event id.
func (s *Store) IngestPlaybackEventsDetailed(ctx context.Context, profileID, deviceID string, events []PlaybackEventRecord) (int, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	accepted := 0
	var acceptedIDs []string
	now := time.Now().UTC().Format(time.RFC3339)
	var lastfmConnected int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lastfm_profiles WHERE profile_id=?)`, profileID).Scan(&lastfmConnected); err != nil {
		return 0, nil, err
	}
	for _, e := range events {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE track_id=?)`, e.TrackID).Scan(&exists); err != nil {
			return accepted, acceptedIDs, err
		} else if exists == 0 {
			continue
		}
		sessionID := ""
		if lastfmConnected != 0 {
			sessionID = e.PlaySessionID
		}
		if lastfmConnected != 0 && e.Type == "started" {
			if sessionID == "" {
				sessionID = e.EventID
			}
		} else if lastfmConnected != 0 && sessionID == "" {
			_ = tx.QueryRowContext(ctx, `SELECT play_session_id FROM lastfm_play_sessions WHERE profile_id=? AND device_id=? AND track_id=? AND started_at<=? ORDER BY started_at DESC LIMIT 1`, profileID, deviceID, e.TrackID, e.At.UTC().Format(time.RFC3339)).Scan(&sessionID)
		}
		// The event id is the outer idempotency boundary. Claim it before any
		// Last.fm session or presence mutation so a replay with altered fields is
		// inert rather than partially applied.
		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO playback_events (event_id, profile_id, device_id, type, track_id, position_sec, at, received_at, play_session_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.EventID, profileID, deviceID, e.Type, e.TrackID, e.PositionSec, e.At.UTC().Format(time.RFC3339), now, nullStr(sessionID))
		if err != nil {
			return accepted, acceptedIDs, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return accepted, acceptedIDs, err
		}
		if n == 0 {
			continue // duplicate
		}
		accepted++
		acceptedIDs = append(acceptedIDs, e.EventID)
		if lastfmConnected != 0 && e.Type == "started" {
			_, err = tx.ExecContext(ctx, `INSERT INTO lastfm_play_sessions(play_session_id,profile_id,device_id,track_id,started_at,position_sec,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(play_session_id) DO NOTHING`, sessionID, profileID, deviceID, e.TrackID, e.At.UTC().Format(time.RFC3339), positionValue(e.PositionSec), now)
			if err != nil {
				return accepted, acceptedIDs, err
			}
		} else if lastfmConnected != 0 && sessionID != "" && e.PositionSec != nil {
			_, err = tx.ExecContext(ctx, `UPDATE lastfm_play_sessions SET position_sec=max(position_sec,?),updated_at=? WHERE play_session_id=? AND profile_id=?`, *e.PositionSec, now, sessionID, profileID)
			if err != nil {
				return accepted, acceptedIDs, err
			}
		}

		playing := 0
		switch e.Type {
		case "started", "progress", "resumed":
			playing = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO presence (profile_id, track_id, playing, at) VALUES (?, ?, ?, ?)
			ON CONFLICT(profile_id) DO UPDATE SET track_id = excluded.track_id,
			    playing = excluded.playing, at = excluded.at`,
			profileID, e.TrackID, playing, e.At.UTC().Format(time.RFC3339)); err != nil {
			return accepted, acceptedIDs, err
		}
	}
	if err := tx.Commit(); err != nil {
		return accepted, acceptedIDs, err
	}
	return accepted, acceptedIDs, nil
}

func positionValue(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// PresenceRow is one member's now-playing state.
type PresenceRow struct {
	ProfileID   string
	ProfileName string
	AvatarColor string
	Track       TrackRow
	At          time.Time
}

// ListPresence returns currently-playing profiles that share their listening.
func (s *Store) ListPresence(ctx context.Context) ([]PresenceRow, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT pr.profile_id, pr.name, COALESCE(pr.avatar_color, ''), p.at, %s%s
		JOIN presence p ON p.track_id = t.track_id
		JOIN profiles pr ON pr.profile_id = p.profile_id
		WHERE p.playing = 1 AND pr.share_listening = 1 AND t.missing = 0`, trackCols, trackFrom))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PresenceRow
	for rows.Next() {
		var e PresenceRow
		var at string
		dest := []any{&e.ProfileID, &e.ProfileName, &e.AvatarColor, &at,
			&e.Track.TrackID, &e.Track.Title, &e.Track.Index, &e.Track.Disc,
			&e.Track.ArtistID, &e.Track.ArtistName, &e.Track.AlbumID, &e.Track.AlbumTitle,
			&e.Track.Genre, &e.Track.DurationMs, &e.Track.ArtID,
			&e.Track.Container, &e.Track.Codec, &e.Track.BitrateKbps, &e.Track.SizeBytes, &e.Track.MusicBrainzRecordingID}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(time.RFC3339, at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.hydrateTrackCredits(ctx, &out[i].Track); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TasteSnapshotData mirrors the contract's TasteSnapshot.
type TasteSnapshotData struct {
	Artists []TasteArtist
	Tracks  []TasteTrack
}

type TasteArtist struct {
	Name     string
	Affinity float64
}

type TasteTrack struct {
	Key          string
	Plays        int
	Skips        int
	LastPlayedAt *time.Time
}

// TasteSnapshot derives per-item stats from the profile's playback history.
// Plays = ended events; skips = skipped events. Affinity is play share
// normalized to the profile's top artist (proper decay math ports with the
// discovery arc).
func (s *Store) TasteSnapshot(ctx context.Context, profileID string) (TasteSnapshotData, error) {
	var out TasteSnapshotData
	rows, err := s.db.QueryContext(ctx, `
		SELECT lower(t.artist_name) || char(0x241F) || lower(t.title),
		       sum(CASE WHEN e.type = 'ended' THEN 1 ELSE 0 END),
		       sum(CASE WHEN e.type = 'skipped' THEN 1 ELSE 0 END),
		       max(CASE WHEN e.type = 'ended' THEN e.at ELSE NULL END)
		FROM playback_events e JOIN tracks t ON t.track_id = e.track_id
		WHERE e.profile_id = ? AND e.type IN ('ended', 'skipped')
		GROUP BY t.track_id ORDER BY 2 DESC LIMIT 500`, profileID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var tr TasteTrack
		var last sql.NullString
		if err := rows.Scan(&tr.Key, &tr.Plays, &tr.Skips, &last); err != nil {
			return out, err
		}
		if last.Valid {
			if t, err := time.Parse(time.RFC3339, last.String); err == nil {
				tr.LastPlayedAt = &t
			}
		}
		out.Tracks = append(out.Tracks, tr)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	arows, err := s.db.QueryContext(ctx, `
		SELECT t.artist_name, count(*) FROM playback_events e
		JOIN tracks t ON t.track_id = e.track_id
		WHERE e.profile_id = ? AND e.type = 'ended'
		GROUP BY t.artist_name ORDER BY 2 DESC LIMIT 100`, profileID)
	if err != nil {
		return out, err
	}
	defer arows.Close()
	maxPlays := 0
	type ac struct {
		name  string
		plays int
	}
	var artists []ac
	for arows.Next() {
		var a ac
		if err := arows.Scan(&a.name, &a.plays); err != nil {
			return out, err
		}
		if a.plays > maxPlays {
			maxPlays = a.plays
		}
		artists = append(artists, a)
	}
	if err := arows.Err(); err != nil {
		return out, err
	}
	for _, a := range artists {
		out.Artists = append(out.Artists, TasteArtist{Name: a.name, Affinity: float64(a.plays) / float64(maxPlays)})
	}
	return out, nil
}

// ── recommendations ────────────────────────────────────────────

// RecommendationRow backs the contract's Recommendation.
type RecommendationRow struct {
	RecommendationID string
	FromProfileID    string
	FromProfileName  string
	ToProfileID      string
	Ref              string
	Kind             string
	Name             string
	ArtistName       string
	ArtID            string
	Note             string
	Seen             bool
	CreatedAt        time.Time
}

func (s *Store) CreateRecommendation(ctx context.Context, fromProfileID, toProfileID, ref, note string) (RecommendationRow, error) {
	if _, found, err := s.GetProfileRecord(ctx, toProfileID); err != nil {
		return RecommendationRow{}, err
	} else if !found {
		return RecommendationRow{}, fmt.Errorf("profile %s: %w", toProfileID, ErrNotFound)
	}
	kind, name, artistName, _, err := s.resolveRef(ctx, ref)
	if err != nil {
		return RecommendationRow{}, err
	}
	from, found, err := s.GetProfileRecord(ctx, fromProfileID)
	if err != nil || !found {
		return RecommendationRow{}, fmt.Errorf("profile %s: %w", fromProfileID, ErrNotFound)
	}
	now := time.Now().UTC()
	rec := RecommendationRow{
		RecommendationID: NewID("rec"),
		FromProfileID:    fromProfileID,
		FromProfileName:  from.Name,
		ToProfileID:      toProfileID,
		Ref:              ref,
		Kind:             kind,
		Name:             name,
		ArtistName:       artistName,
		Note:             note,
		CreatedAt:        now,
	}
	var noteArg any
	if note != "" {
		noteArg = note
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO recommendations (recommendation_id, from_profile_id, to_profile_id, ref, kind, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.RecommendationID, fromProfileID, toProfileID, ref, kind, noteArg, now.Format(time.RFC3339))
	if err != nil {
		return RecommendationRow{}, err
	}
	return rec, nil
}

func (s *Store) ListRecommendations(ctx context.Context, profileID string, unseenOnly bool, cur string, limit int) ([]RecommendationRow, string, error) {
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	where := "r.to_profile_id = ?"
	args := []any{profileID}
	if unseenOnly {
		where += " AND r.seen = 0"
	}
	if c.ID != "" {
		where += " AND (r.created_at < ? OR (r.created_at = ? AND r.recommendation_id > ?))"
		args = append(args, c.K, c.K, c.ID)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT r.recommendation_id, r.from_profile_id, pr.name, r.to_profile_id,
		       r.ref, r.kind, COALESCE(r.note, ''), r.seen, r.created_at
		FROM recommendations r JOIN profiles pr ON pr.profile_id = r.from_profile_id
		WHERE %s ORDER BY r.created_at DESC, r.recommendation_id LIMIT %d`, where, limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []RecommendationRow
	var keys []string
	for rows.Next() {
		var r RecommendationRow
		var created string
		var seen int
		if err := rows.Scan(&r.RecommendationID, &r.FromProfileID, &r.FromProfileName, &r.ToProfileID,
			&r.Ref, &r.Kind, &r.Note, &seen, &created); err != nil {
			return nil, "", err
		}
		r.Seen = seen != 0
		if r.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
			return nil, "", err
		}
		out = append(out, r)
		keys = append(keys, created)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(keys[limit-1], out[limit-1].RecommendationID)
	}
	for i := range out {
		if _, name, artistName, _, err := s.resolveRef(ctx, out[i].Ref); err == nil {
			out[i].Name, out[i].ArtistName = name, artistName
		} else {
			out[i].Name = out[i].Ref
		}
	}
	return out, next, nil
}

func (s *Store) MarkRecommendationSeen(ctx context.Context, profileID, recommendationID string) error {
	if _, err := s.exec1(ctx, `
		UPDATE recommendations SET seen = 1
		WHERE recommendation_id = ? AND to_profile_id = ?`, recommendationID, profileID); err != nil {
		return fmt.Errorf("recommendation %s: %w", recommendationID, err)
	}
	return nil
}

// ── event log ──────────────────────────────────────────────────

// EventRow is one durable event; Seq feeds SSE Last-Event-ID resume.
type EventRow struct {
	Seq       int64
	Type      string
	ProfileID string
	At        time.Time
	Data      string
}

// AppendEvent stores an event (profileID "" = instance-wide) and returns its
// sequence number.
func (s *Store) AppendEvent(ctx context.Context, eventType, profileID, dataJSON string) (int64, error) {
	var pid any
	if profileID != "" {
		pid = profileID
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO events (type, profile_id, at, data) VALUES (?, ?, ?, ?)`,
		eventType, pid, time.Now().UTC().Format(time.RFC3339), dataJSON)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EventsSince returns events visible to a profile (instance-wide plus its
// own) with seq greater than since.
func (s *Store) EventsSince(ctx context.Context, profileID string, since int64, limit int) ([]EventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, type, COALESCE(profile_id, ''), at, data FROM events
		WHERE seq > ? AND (profile_id IS NULL OR profile_id = ?)
		ORDER BY seq LIMIT ?`, since, profileID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var at string
		if err := rows.Scan(&e.Seq, &e.Type, &e.ProfileID, &at, &e.Data); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(time.RFC3339, at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneEvents keeps the log bounded (retain the newest keep rows).
func (s *Store) PruneEvents(ctx context.Context, keep int64) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM events WHERE seq <= (SELECT COALESCE(max(seq), 0) FROM events) - ?`, keep)
	return err
}

// LatestEventSeq returns the newest persisted event sequence (0 when empty),
// so live-only SSE subscriptions can start at the present.
func (s *Store) LatestEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	return seq, err
}
