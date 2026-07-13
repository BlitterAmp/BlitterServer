package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

// ArtistRow / AlbumRow / TrackRow are the index's read models — everything
// the contract's Artist/Album/Track schemas need before per-profile overlays
// (loves, ratings) join in.
type ArtistRow struct {
	ArtistID      string
	Name          string
	ArtID         string
	AlbumCount    int
	TrackCount    int
	Genres        []string
	MusicBrainzID string
	Aliases       []string
}

type ArtistCreditRow struct {
	ArtistID      string
	Name          string
	JoinPhrase    string
	MusicBrainzID string
}

type AlbumRow struct {
	AlbumID                   string
	Title                     string
	ArtistID                  string
	ArtistName                string
	Year                      int
	ArtID                     string
	TrackCount                int
	Genres                    []string
	UpdatedAt                 int64
	PrimaryArtist             ArtistCreditRow
	ArtistCredits             []ArtistCreditRow
	MusicBrainzReleaseID      string
	MusicBrainzReleaseGroupID string
}

type TrackRow struct {
	TrackID                string
	Title                  string
	Index                  int
	Disc                   int
	ArtistID               string
	ArtistName             string
	AlbumID                string
	AlbumTitle             string
	Genre                  string
	DurationMs             int
	ArtID                  string
	Container              string
	Codec                  string
	BitrateKbps            int
	SizeBytes              int64
	PrimaryArtist          ArtistCreditRow
	ArtistCredits          []ArtistCreditRow
	MusicBrainzRecordingID string
}

// LibrarySummary backs GET /v1/library.
type LibrarySummary struct {
	UpdatedAt int64
	Version   int64 // scan seq; the delta-sync cursor (see ChangesSince)
	Artists   int
	Albums    int
	Tracks    int
}

// LibraryChange is one entity that changed since a client's last-known version.
// Kind is "artist" | "album" | "track"; Missing rows tell the client to drop it.
type LibraryChange struct {
	ChangeSeq int64
	Kind      string
	ID        string
	Missing   bool
}

type changeCursor struct {
	S  int64  `json:"s"`
	K  string `json:"k"`
	ID string `json:"id"`
}

// LibrarySearch groups SearchLibrary results.
type LibrarySearch struct {
	Artists []ArtistRow
	Albums  []AlbumRow
	Tracks  []TrackRow
}

// ── sync ───────────────────────────────────────────────────────

// NextScanSeq increments and returns the scan sequence number; UpsertTrack
// stamps rows with it and FinishScan marks everything older as missing.
func (s *Store) NextScanSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO settings (key, value) VALUES ('library_scan_seq', '1')
		ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1
		RETURNING CAST(value AS INTEGER)`).Scan(&seq)
	return seq, err
}

// UpsertTrack indexes one scanned source file, minting canonical ids on first
// sight. MusicBrainz ids identify tagged artists and releases; untagged albums
// fall back to (artist, title), and tracks remain keyed by (source, native id).
func (s *Store) UpsertTrack(ctx context.Context, kind string, m source.TrackMeta, artID string, seq int64) error {
	now := time.Now().Unix()
	var linkedArtistID, linkedAlbumID string
	err := s.db.QueryRowContext(ctx, `SELECT artist_id,album_id FROM tracks WHERE source_kind=? AND native_id=?`, kind, m.NativeID).Scan(&linkedArtistID, &linkedAlbumID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read source-linked identity: %w", err)
	}
	preserveResolved := false
	if linkedAlbumID != "" && m.ReleaseMBID == "" && m.ReleaseGroupMBID == "" && m.RecordingMBID == "" && !creditsHaveMBID(m.AlbumCredits) && !creditsHaveMBID(m.TrackCredits) {
		_ = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM album_musicbrainz_matches WHERE album_id=? AND state='matched')`, linkedAlbumID).Scan(&preserveResolved)
	}

	grouping := m.PrimaryArtist
	if grouping.Name == "" && len(m.TrackCredits) > 0 {
		grouping = source.ArtistReference{Name: m.TrackCredits[0].Name, MBID: m.TrackCredits[0].MBID}
	}
	artistID, err := s.upsertArtist(ctx, grouping.Name, grouping.MBID, linkedArtistID, now, seq)
	if err != nil {
		return fmt.Errorf("upsert artist: %w", err)
	}

	var albumID string
	if linkedAlbumID != "" && m.ReleaseMBID == "" {
		albumID, err = linkedAlbumID, nil
	} else if m.ReleaseMBID != "" {
		err = s.db.QueryRowContext(ctx, `SELECT album_id FROM albums WHERE musicbrainz_release_id = ?`, m.ReleaseMBID).Scan(&albumID)
		if errors.Is(err, sql.ErrNoRows) && linkedAlbumID != "" {
			res, updateErr := s.db.ExecContext(ctx, `UPDATE albums SET musicbrainz_release_id=?, musicbrainz_release_group_id=COALESCE(?,musicbrainz_release_group_id), change_seq=? WHERE album_id=?`, m.ReleaseMBID, nullStr(m.ReleaseGroupMBID), seq, linkedAlbumID)
			if updateErr == nil {
				if n, _ := res.RowsAffected(); n == 1 {
					albumID, err = linkedAlbumID, nil
				}
			} else {
				err = updateErr
			}
			if err != nil {
				lookupErr := s.db.QueryRowContext(ctx, `SELECT album_id FROM albums WHERE musicbrainz_release_id = ?`, m.ReleaseMBID).Scan(&albumID)
				if lookupErr == nil {
					err = nil
				} else if updateErr != nil {
					return fmt.Errorf("promote source-linked album: %w", updateErr)
				} else {
					err = lookupErr
				}
			}
		}
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT album_id FROM albums WHERE artist_id = ? AND title = ? AND musicbrainz_release_id IS NULL`, artistID, m.Album).Scan(&albumID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		albumID = NewID("alb")
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO albums (album_id, artist_id, title, year, musicbrainz_release_id, musicbrainz_release_group_id, created_at, change_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			albumID, artistID, m.Album, nullInt(m.Year), nullStr(m.ReleaseMBID), nullStr(m.ReleaseGroupMBID), now, seq)
	}
	if err != nil {
		return fmt.Errorf("upsert album: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE albums SET artist_id=CASE WHEN ? THEN artist_id WHEN ?='' AND musicbrainz_release_id IS NOT NULL THEN artist_id ELSE ? END, musicbrainz_release_id=COALESCE(?,musicbrainz_release_id), musicbrainz_release_group_id=COALESCE(?,musicbrainz_release_group_id), change_seq=CASE WHEN (NOT ? AND artist_id IS NOT ?) OR musicbrainz_release_id IS NOT COALESCE(?,musicbrainz_release_id) OR musicbrainz_release_group_id IS NOT COALESCE(?,musicbrainz_release_group_id) THEN ? ELSE change_seq END WHERE album_id=?`, preserveResolved, m.ReleaseMBID, artistID, nullStr(m.ReleaseMBID), nullStr(m.ReleaseGroupMBID), preserveResolved, artistID, nullStr(m.ReleaseMBID), nullStr(m.ReleaseGroupMBID), seq, albumID)
	if err != nil {
		return fmt.Errorf("update album identity: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tracks (track_id, album_id, artist_id, artist_name, musicbrainz_recording_id, title, idx, disc, genre,
		                    duration_ms, container, codec, bitrate_kbps, size_bytes,
		                    source_kind, native_id, version, art_id, seen_seq, missing, created_at, change_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
		ON CONFLICT(source_kind, native_id) DO UPDATE SET
		    album_id = excluded.album_id, artist_id = CASE WHEN ? THEN tracks.artist_id ELSE excluded.artist_id END,
		    artist_name = CASE WHEN ? THEN tracks.artist_name ELSE excluded.artist_name END,
		    musicbrainz_recording_id = CASE WHEN ? THEN tracks.musicbrainz_recording_id ELSE excluded.musicbrainz_recording_id END, title = excluded.title,
		    idx = excluded.idx, disc = excluded.disc, genre = excluded.genre,
		    duration_ms = excluded.duration_ms, container = excluded.container,
		    codec = excluded.codec, bitrate_kbps = excluded.bitrate_kbps,
		    size_bytes = excluded.size_bytes, version = excluded.version,
		    art_id = COALESCE(excluded.art_id, tracks.art_id),
		    seen_seq = excluded.seen_seq, missing = 0,
		    change_seq = CASE WHEN tracks.missing = 1
		        OR tracks.album_id IS NOT excluded.album_id
		        OR (NOT ? AND tracks.artist_id IS NOT excluded.artist_id)
			OR (NOT ? AND tracks.artist_name IS NOT excluded.artist_name)
			OR (NOT ? AND tracks.musicbrainz_recording_id IS NOT excluded.musicbrainz_recording_id)
		        OR tracks.title IS NOT excluded.title
		        OR tracks.idx IS NOT excluded.idx
		        OR tracks.disc IS NOT excluded.disc
		        OR tracks.genre IS NOT excluded.genre
		        OR tracks.duration_ms IS NOT excluded.duration_ms
		        OR tracks.container IS NOT excluded.container
		        OR tracks.codec IS NOT excluded.codec
		        OR tracks.bitrate_kbps IS NOT excluded.bitrate_kbps
		        OR tracks.size_bytes IS NOT excluded.size_bytes
		        OR tracks.version IS NOT excluded.version
		        OR (excluded.art_id IS NOT NULL AND excluded.art_id IS NOT tracks.art_id)
		        THEN ? ELSE tracks.change_seq END`,
		NewID("trk"), albumID, artistID, creditDisplay(m.TrackCredits), nullStr(m.RecordingMBID), m.Title, nullInt(m.Index), nullInt(m.Disc), m.Genre,
		m.DurationMs, m.Container, m.Codec, nullInt(m.BitrateKbps), m.SizeBytes,
		kind, m.NativeID, m.Version, nullStr(artID), seq, now, seq, preserveResolved, preserveResolved, preserveResolved, preserveResolved, preserveResolved, preserveResolved, seq)
	if err != nil {
		return fmt.Errorf("upsert track: %w", err)
	}
	var trackID string
	if err := s.db.QueryRowContext(ctx, `SELECT track_id FROM tracks WHERE source_kind=? AND native_id=?`, kind, m.NativeID).Scan(&trackID); err != nil {
		return err
	}
	albumCreditsChanged, trackCreditsChanged := false, false
	if !preserveResolved {
		albumCreditsChanged, err = s.replaceCredits(ctx, "album_artist_credits", "album_id", albumID, m.AlbumCredits, now, seq)
	}
	if err != nil {
		return err
	}
	if !preserveResolved {
		trackCreditsChanged, err = s.replaceCredits(ctx, "track_artist_credits", "track_id", trackID, m.TrackCredits, now, seq)
	}
	if err != nil {
		return err
	}
	if albumCreditsChanged {
		_, _ = s.db.ExecContext(ctx, `UPDATE albums SET change_seq=? WHERE album_id=?`, seq, albumID)
	}
	if trackCreditsChanged {
		_, _ = s.db.ExecContext(ctx, `UPDATE tracks SET change_seq=? WHERE track_id=?`, seq, trackID)
	}
	return nil
}

func creditsHaveMBID(credits []source.ArtistCredit) bool {
	for _, credit := range credits {
		if credit.MBID != "" {
			return true
		}
	}
	return false
}

func (s *Store) upsertArtist(ctx context.Context, name, mbid, linkedID string, now, seq int64) (string, error) {
	var id, canonical string
	var err error
	if mbid != "" {
		err = s.db.QueryRowContext(ctx, `SELECT artist_id,name FROM artists WHERE musicbrainz_id=?`, mbid).Scan(&id, &canonical)
		if errors.Is(err, sql.ErrNoRows) && linkedID != "" {
			_, updateErr := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id=?,change_seq=? WHERE artist_id=? AND musicbrainz_id IS NULL`, mbid, seq, linkedID)
			if updateErr == nil {
				err = s.db.QueryRowContext(ctx, `SELECT artist_id,name FROM artists WHERE artist_id=? AND musicbrainz_id=?`, linkedID, mbid).Scan(&id, &canonical)
			} else {
				lookupErr := s.db.QueryRowContext(ctx, `SELECT artist_id,name FROM artists WHERE musicbrainz_id=?`, mbid).Scan(&id, &canonical)
				if lookupErr != nil {
					return "", updateErr
				}
				err = nil
			}
		}
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT artist_id,name FROM artists WHERE musicbrainz_id IS NULL AND name=? ORDER BY created_at LIMIT 1`, name).Scan(&id, &canonical)
	}
	if errors.Is(err, sql.ErrNoRows) {
		id = NewID("art")
		_, err = s.db.ExecContext(ctx, `INSERT INTO artists (artist_id,name,musicbrainz_id,created_at,change_seq) VALUES (?,?,?,?,?)`, id, name, nullStr(mbid), now, seq)
		canonical = name
	}
	if err != nil {
		return "", err
	}
	if name != "" && !strings.EqualFold(name, canonical) {
		_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO artist_aliases (artist_id,name) VALUES (?,?)`, id, name)
	}
	return id, err
}

func creditDisplay(credits []source.ArtistCredit) string {
	var b strings.Builder
	for _, c := range credits {
		b.WriteString(c.Name)
		b.WriteString(c.JoinPhrase)
	}
	return b.String()
}

func (s *Store) replaceCredits(ctx context.Context, table, ownerColumn, ownerID string, credits []source.ArtistCredit, now, seq int64) (bool, error) {
	if len(credits) == 0 {
		return false, fmt.Errorf("%s requires at least one artist credit", ownerID)
	}
	existing, err := s.readCredits(ctx, table, ownerColumn, ownerID)
	if err != nil {
		return false, err
	}
	ids := make([]string, len(credits))
	for i, c := range credits {
		linkedID := ""
		if i < len(existing) {
			linkedID = existing[i].ArtistID
		}
		ids[i], err = s.upsertArtist(ctx, c.Name, c.MBID, linkedID, now, seq)
		if err != nil {
			return false, err
		}
	}
	if len(existing) == len(credits) {
		same := true
		for i, c := range credits {
			if existing[i].ArtistID != ids[i] || existing[i].Name != c.Name || existing[i].JoinPhrase != c.JoinPhrase {
				same = false
				break
			}
		}
		if same {
			return false, nil
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+ownerColumn+`=?`, ownerID); err != nil {
		return false, err
	}
	for i, c := range credits {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO `+table+` (`+ownerColumn+`,position,artist_id,name,join_phrase) VALUES (?,?,?,?,?)`, ownerID, i, ids[i], c.Name, c.JoinPhrase); err != nil {
			return false, err
		}
	}
	return true, nil
}

// FinishScan marks unseen rows missing, propagates art track→album→artist,
// and bumps the library freshness anchor.
func (s *Store) FinishScan(ctx context.Context, kind string, seq int64) error {
	seqStr := strconv.FormatInt(seq, 10)
	for _, q := range []string{
		// Newly-missing tracks: flip + stamp only rows that were present.
		`UPDATE tracks SET missing = 1, change_seq = ` + seqStr + `
		 WHERE source_kind = '` + kind + `' AND seen_seq < ` + seqStr + ` AND missing = 0`,
		// Recompute album/artist missing; stamp only rows whose flag actually flips.
		`UPDATE albums SET
		    change_seq = CASE WHEN missing != ((SELECT count(*) FROM tracks WHERE tracks.album_id = albums.album_id AND tracks.missing = 0) = 0) THEN ` + seqStr + ` ELSE change_seq END,
		    missing = (SELECT count(*) FROM tracks WHERE tracks.album_id = albums.album_id AND tracks.missing = 0) = 0`,
		`UPDATE artists SET
		    change_seq = CASE WHEN missing != (NOT EXISTS (SELECT 1 FROM tracks JOIN track_artist_credits c ON c.track_id=tracks.track_id WHERE c.artist_id=artists.artist_id AND tracks.missing=0) AND NOT EXISTS (SELECT 1 FROM albums JOIN album_artist_credits c ON c.album_id=albums.album_id WHERE c.artist_id=artists.artist_id AND albums.missing=0) AND NOT EXISTS (SELECT 1 FROM albums WHERE albums.artist_id=artists.artist_id AND albums.missing=0)) THEN ` + seqStr + ` ELSE change_seq END,
		    missing = NOT EXISTS (SELECT 1 FROM tracks JOIN track_artist_credits c ON c.track_id=tracks.track_id WHERE c.artist_id=artists.artist_id AND tracks.missing=0) AND NOT EXISTS (SELECT 1 FROM albums JOIN album_artist_credits c ON c.album_id=albums.album_id WHERE c.artist_id=artists.artist_id AND albums.missing=0) AND NOT EXISTS (SELECT 1 FROM albums WHERE albums.artist_id=artists.artist_id AND albums.missing=0)`,
		// Propagate art up track→album→artist; stamp rows that actually acquire art.
		`UPDATE albums SET
		    change_seq = CASE WHEN (SELECT t.art_id FROM tracks t WHERE t.album_id = albums.album_id AND t.art_id IS NOT NULL AND t.missing = 0 ORDER BY t.disc, t.idx LIMIT 1) IS NOT NULL THEN ` + seqStr + ` ELSE change_seq END,
		    art_id = (SELECT t.art_id FROM tracks t WHERE t.album_id = albums.album_id AND t.art_id IS NOT NULL AND t.missing = 0 ORDER BY t.disc, t.idx LIMIT 1)
		 WHERE art_id IS NULL`,
		`UPDATE artists SET
		    change_seq = CASE WHEN (SELECT a.art_id FROM albums a WHERE a.artist_id = artists.artist_id AND a.art_id IS NOT NULL AND a.missing = 0 ORDER BY a.year LIMIT 1) IS NOT NULL THEN ` + seqStr + ` ELSE change_seq END,
		    art_id = (SELECT a.art_id FROM albums a WHERE a.artist_id = artists.artist_id AND a.art_id IS NOT NULL AND a.missing = 0 ORDER BY a.year LIMIT 1)
		 WHERE art_id IS NULL`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("finish scan: %w", err)
		}
	}
	return s.SetSetting(ctx, "library_updated_at", strconv.FormatInt(time.Now().Unix(), 10))
}

// UpsertArt stores one image (deduped by hash) under artDir and returns its
// canonical art id.
func (s *Store) UpsertArt(ctx context.Context, hash, mime string, data []byte, artDir string) (string, error) {
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT art_id FROM art WHERE hash = ?`, hash).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(artDir, hash)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	artID := NewID("img")
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO art (art_id, hash, mime, path) VALUES (?, ?, ?, ?)`, artID, hash, mime, path); err != nil {
		return "", err
	}
	return artID, nil
}

// ── external art enrichment ────────────────────────────────────

// AlbumArtNeed / ArtistArtNeed identify entities to look up externally.
type AlbumArtNeed struct {
	AlbumID        string
	Title          string
	ArtistName     string
	ReleaseGroupID string
}

type ArtistArtNeed struct {
	ArtistID      string
	Name          string
	ArtID         string
	MusicBrainzID string
}

// AlbumsNeedingArt lists present albums with no cover we haven't tried yet.
func (s *Store) AlbumsNeedingArt(ctx context.Context, limit int) ([]AlbumArtNeed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT al.album_id, al.title, ar.name, COALESCE(al.musicbrainz_release_group_id,'')
		FROM albums al JOIN artists ar ON ar.artist_id = al.artist_id
		WHERE al.art_id IS NULL AND al.missing = 0 AND (al.art_tried = 0 OR al.art_tried_at < ?)
		LIMIT ?`, time.Now().Add(-24*time.Hour).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlbumArtNeed
	for rows.Next() {
		var a AlbumArtNeed
		if err := rows.Scan(&a.AlbumID, &a.Title, &a.ArtistName, &a.ReleaseGroupID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ArtistsNeedingArt lists present artists we haven't tried a real photo for
// (even those with an album-cover fallback — fanart.tv can upgrade them).
func (s *Store) ArtistsNeedingArt(ctx context.Context, limit int) ([]ArtistArtNeed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT artist_id, name, COALESCE(art_id, ''), COALESCE(musicbrainz_id,'') FROM artists
		WHERE missing = 0 AND art_id IS NULL AND (art_tried = 0 OR art_tried_at < ?)
		LIMIT ?`, time.Now().Add(-24*time.Hour).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistArtNeed
	for rows.Next() {
		var a ArtistArtNeed
		if err := rows.Scan(&a.ArtistID, &a.Name, &a.ArtID, &a.MusicBrainzID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAlbumArt/SetArtistArt attach fetched art and bump change_seq so clients
// pick the new cover up via delta sync. seq is a fresh NextScanSeq for the run.
func (s *Store) SetAlbumArt(ctx context.Context, albumID, artID string, seq int64) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE albums SET art_id = ?, art_tried = 1, art_tried_at = ?, change_seq = ? WHERE album_id = ? AND art_id IS NULL`, artID, time.Now().Unix(), seq, albumID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) SetArtistArt(ctx context.Context, artistID, expectedArtID, artID string, seq int64) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE artists SET art_id = ?, art_tried = 1, art_tried_at = ?, change_seq = ? WHERE artist_id = ? AND COALESCE(art_id, '') = ?`, artID, time.Now().Unix(), seq, artistID, expectedArtID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// MarkAlbumArtTried/MarkArtistArtTried record a miss so we don't re-query.
func (s *Store) MarkAlbumArtTried(ctx context.Context, albumID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE albums SET art_tried = 1, art_tried_at = ? WHERE album_id = ?`, time.Now().Unix(), albumID)
	return err
}

func (s *Store) MarkArtistArtTried(ctx context.Context, artistID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE artists SET art_tried = 1, art_tried_at = ? WHERE artist_id = ?`, time.Now().Unix(), artistID)
	return err
}

func (s *Store) ResetArtRetries(ctx context.Context, artists bool) error {
	table := "albums"
	if artists {
		table = "artists"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET art_tried=0, art_tried_at=0 WHERE art_id IS NULL`)
	return err
}

// GetArt returns the on-disk location of an image.
func (s *Store) GetArt(ctx context.Context, artID string) (path, mime string, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT path, mime FROM art WHERE art_id = ?`, artID).Scan(&path, &mime)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return path, mime, true, nil
}

// ── queries ────────────────────────────────────────────────────

func (s *Store) GetLibrarySummary(ctx context.Context) (LibrarySummary, error) {
	var sum LibrarySummary
	v, _, err := s.GetSetting(ctx, "library_updated_at")
	if err != nil {
		return sum, err
	}
	sum.UpdatedAt, _ = strconv.ParseInt(v, 10, 64)
	sv, _, err := s.GetSetting(ctx, "library_scan_seq")
	if err != nil {
		return sum, err
	}
	sum.Version, _ = strconv.ParseInt(sv, 10, 64)
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM artists WHERE missing = 0),
		       (SELECT count(*) FROM albums WHERE missing = 0),
		       (SELECT count(*) FROM tracks WHERE missing = 0)`).
		Scan(&sum.Artists, &sum.Albums, &sum.Tracks)
	return sum, err
}

// ChangesSince returns entities whose change_seq is greater than the client's
// last-known version, keyset-paginated over (change_seq, kind, id). Missing rows
// are included so the client learns to drop them. nextCursor is "" when drained.
func (s *Store) ChangesSince(ctx context.Context, since int64, cur string, limit int) ([]LibraryChange, string, error) {
	var c changeCursor
	if cur != "" {
		b, err := base64.RawURLEncoding.DecodeString(cur)
		if err != nil {
			return nil, "", fmt.Errorf("cursor: %w", err)
		}
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, "", fmt.Errorf("cursor: %w", err)
		}
	}
	where := "change_seq > ?"
	args := []any{since}
	if cur != "" {
		where += " AND (change_seq > ? OR (change_seq = ? AND (kind > ? OR (kind = ? AND id > ?))))"
		args = append(args, c.S, c.S, c.K, c.K, c.ID)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT change_seq, kind, id, missing FROM (
			SELECT change_seq, 'artist' AS kind, artist_id AS id, missing FROM artists
			UNION ALL SELECT change_seq, 'album', album_id, missing FROM albums
			UNION ALL SELECT change_seq, 'track', track_id, missing FROM tracks
		) WHERE %s ORDER BY change_seq, kind, id LIMIT %d`, where, limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []LibraryChange
	for rows.Next() {
		var ch LibraryChange
		var missing int
		if err := rows.Scan(&ch.ChangeSeq, &ch.Kind, &ch.ID, &missing); err != nil {
			return nil, "", err
		}
		ch.Missing = missing != 0
		out = append(out, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		b, _ := json.Marshal(changeCursor{S: last.ChangeSeq, K: last.Kind, ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, next, nil
}

// cursor is the stateless keyset token: last row's sort key + id.
type cursor struct {
	K  string `json:"k"`
	ID string `json:"id"`
}

func encodeCursor(k, id string) string {
	b, _ := json.Marshal(cursor{K: k, ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(raw string) (cursor, error) {
	var c cursor
	if raw == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, fmt.Errorf("cursor: %w", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("cursor: %w", err)
	}
	return c, nil
}

// keyset builds "WHERE (key, id) > (?, ?)" fragments for ascending sorts and
// the mirrored form for descending ones.
func keysetClause(keyCol, idCol string, desc bool) string {
	if desc {
		return fmt.Sprintf("(%s < ? OR (%s = ? AND %s > ?))", keyCol, keyCol, idCol)
	}
	return fmt.Sprintf("(%s > ? OR (%s = ? AND %s > ?))", keyCol, keyCol, idCol)
}

const artistArt = `COALESCE(a.art_id,
	(SELECT al.art_id FROM albums al
	 WHERE al.artist_id = a.artist_id AND al.art_id IS NOT NULL AND al.missing = 0
	 ORDER BY COALESCE(al.year, 9999), al.title, al.album_id LIMIT 1), '')`

func (s *Store) ListArtists(ctx context.Context, sort, cur string, limit int) ([]ArtistRow, string, error) {
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	keyCol, desc := "a.name", false
	if sort == "recentlyAdded" {
		keyCol, desc = "cast(a.created_at as text)", true
	}
	where := "a.missing = 0"
	args := []any{}
	if c.ID != "" {
		where += " AND " + keysetClause(keyCol, "a.artist_id", desc)
		args = append(args, c.K, c.K, c.ID)
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT a.artist_id, a.name, %s, %s, COALESCE(a.musicbrainz_id,''),
		       (SELECT count(DISTINCT al.album_id) FROM albums al JOIN album_artist_credits ac ON ac.album_id=al.album_id WHERE ac.artist_id = a.artist_id AND al.missing = 0),
		       (SELECT count(DISTINCT t.track_id) FROM tracks t JOIN track_artist_credits c ON c.track_id=t.track_id WHERE c.artist_id = a.artist_id AND t.missing = 0)
		FROM artists a WHERE %s ORDER BY %s %s, a.artist_id LIMIT %d`,
		artistArt, keyCol, where, keyCol, dir, limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []ArtistRow
	var keys []string
	for rows.Next() {
		var a ArtistRow
		var key string
		if err := rows.Scan(&a.ArtistID, &a.Name, &a.ArtID, &key, &a.MusicBrainzID, &a.AlbumCount, &a.TrackCount); err != nil {
			return nil, "", err
		}
		out = append(out, a)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(keys[limit-1], out[limit-1].ArtistID)
	}
	for i := range out {
		g, err := s.artistGenres(ctx, out[i].ArtistID)
		if err != nil {
			return nil, "", err
		}
		out[i].Genres = g
		out[i].Aliases, err = s.artistAliases(ctx, out[i].ArtistID)
		if err != nil {
			return nil, "", err
		}
	}
	return out, next, nil
}

func (s *Store) artistGenres(ctx context.Context, artistID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.genre FROM tracks t JOIN track_artist_credits c ON c.track_id=t.track_id WHERE c.artist_id = ? AND t.missing = 0 AND t.genre != ''
		GROUP BY genre ORDER BY count(*) DESC LIMIT 5`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GetArtist(ctx context.Context, artistID string) (ArtistRow, bool, error) {
	var a ArtistRow
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT a.artist_id, a.name, %s, COALESCE(a.musicbrainz_id,''),
		       (SELECT count(DISTINCT al.album_id) FROM albums al JOIN album_artist_credits ac ON ac.album_id=al.album_id WHERE ac.artist_id = a.artist_id AND al.missing = 0),
		       (SELECT count(DISTINCT t.track_id) FROM tracks t JOIN track_artist_credits c ON c.track_id=t.track_id WHERE c.artist_id = a.artist_id AND t.missing = 0)
		FROM artists a WHERE a.artist_id = ? AND a.missing = 0`, artistArt), artistID).
		Scan(&a.ArtistID, &a.Name, &a.ArtID, &a.MusicBrainzID, &a.AlbumCount, &a.TrackCount)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtistRow{}, false, nil
	}
	if err != nil {
		return ArtistRow{}, false, err
	}
	g, err := s.artistGenres(ctx, artistID)
	if err != nil {
		return ArtistRow{}, false, err
	}
	a.Genres = g
	a.Aliases, err = s.artistAliases(ctx, artistID)
	if err != nil {
		return ArtistRow{}, false, err
	}
	return a, true, nil
}

func (s *Store) artistAliases(ctx context.Context, artistID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM artist_aliases WHERE artist_id=? ORDER BY name`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

const albumCols = `al.album_id, al.title, al.artist_id, ar.name, COALESCE(al.year, 0),
	       COALESCE(al.art_id, ''), al.created_at,
	       (SELECT count(*) FROM tracks t WHERE t.album_id = al.album_id AND t.missing = 0),
	       COALESCE(al.musicbrainz_release_id,''), COALESCE(al.musicbrainz_release_group_id,'')`

const albumFrom = ` FROM albums al JOIN artists ar ON ar.artist_id = al.artist_id`

const albumSelect = `SELECT ` + albumCols + albumFrom

func scanAlbum(scan func(...any) error) (AlbumRow, string, error) {
	var a AlbumRow
	err := scan(&a.AlbumID, &a.Title, &a.ArtistID, &a.ArtistName, &a.Year, &a.ArtID, &a.UpdatedAt, &a.TrackCount, &a.MusicBrainzReleaseID, &a.MusicBrainzReleaseGroupID)
	return a, "", err
}

func (s *Store) ListAlbums(ctx context.Context, sort, cur string, limit int) ([]AlbumRow, string, error) {
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	keyCol, desc := "al.title", false
	switch sort {
	case "artist":
		keyCol = "ar.name"
	case "year":
		keyCol = "cast(COALESCE(al.year,0) as text)"
	case "recentlyAdded":
		keyCol, desc = "cast(al.created_at as text)", true
	}
	where := "al.missing = 0"
	args := []any{}
	if c.ID != "" {
		where += " AND " + keysetClause(keyCol, "al.album_id", desc)
		args = append(args, c.K, c.K, c.ID)
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT %s, (%s) AS sortkey%s WHERE %s ORDER BY sortkey %s, al.album_id LIMIT %d",
		albumCols, keyCol, albumFrom, where, dir, limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []AlbumRow
	var keys []string
	for rows.Next() {
		var a AlbumRow
		var key string
		if err := rows.Scan(&a.AlbumID, &a.Title, &a.ArtistID, &a.ArtistName, &a.Year, &a.ArtID, &a.UpdatedAt, &a.TrackCount, &a.MusicBrainzReleaseID, &a.MusicBrainzReleaseGroupID, &key); err != nil {
			return nil, "", err
		}
		out = append(out, a)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(keys[limit-1], out[limit-1].AlbumID)
	}
	for i := range out {
		if err := s.hydrateAlbumCredits(ctx, &out[i]); err != nil {
			return nil, "", err
		}
	}
	return out, next, nil
}

func (s *Store) GetAlbum(ctx context.Context, albumID string) (AlbumRow, bool, error) {
	row := s.db.QueryRowContext(ctx, albumSelect+` WHERE al.album_id = ? AND al.missing = 0`, albumID)
	a, _, err := scanAlbum(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return AlbumRow{}, false, nil
	}
	if err != nil {
		return AlbumRow{}, false, err
	}
	if err := s.hydrateAlbumCredits(ctx, &a); err != nil {
		return AlbumRow{}, false, err
	}
	return a, true, nil
}

func (s *Store) ListArtistAlbums(ctx context.Context, artistID string) ([]AlbumRow, error) {
	rows, err := s.db.QueryContext(ctx,
		albumSelect+` WHERE al.missing = 0 AND EXISTS (SELECT 1 FROM album_artist_credits c WHERE c.album_id=al.album_id AND c.artist_id=?) ORDER BY COALESCE(al.year, 9999), al.title`, artistID)
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

const trackCols = `t.track_id, t.title, COALESCE(t.idx, 0), COALESCE(t.disc, 0),
	       t.artist_id, ar.name, t.album_id, al.title, t.genre,
	       t.duration_ms, COALESCE(t.art_id, al.art_id, ''), t.container, t.codec,
	       COALESCE(t.bitrate_kbps, 0), COALESCE(t.size_bytes, 0), COALESCE(t.musicbrainz_recording_id,'')`

const trackFrom = ` FROM tracks t JOIN albums al ON al.album_id = t.album_id JOIN artists ar ON ar.artist_id = t.artist_id`

const trackSelect = `SELECT ` + trackCols + trackFrom

func scanTrack(scan func(...any) error) (TrackRow, error) {
	var tr TrackRow
	err := scan(&tr.TrackID, &tr.Title, &tr.Index, &tr.Disc, &tr.ArtistID, &tr.ArtistName,
		&tr.AlbumID, &tr.AlbumTitle, &tr.Genre, &tr.DurationMs, &tr.ArtID,
		&tr.Container, &tr.Codec, &tr.BitrateKbps, &tr.SizeBytes, &tr.MusicBrainzRecordingID)
	return tr, err
}

func (s *Store) listTracksWhere(ctx context.Context, where, order string, args ...any) ([]TrackRow, error) {
	rows, err := s.db.QueryContext(ctx, trackSelect+" WHERE t.missing = 0 AND "+where+" ORDER BY "+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackRow
	for rows.Next() {
		tr, err := scanTrack(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.hydrateTrackCredits(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) ListTracks(ctx context.Context, sort, cur string, limit int) ([]TrackRow, string, error) {
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	keyCol, desc := "t.title", false
	switch sort {
	case "artist":
		keyCol = "t.artist_name"
	case "recentlyAdded":
		keyCol, desc = "cast(t.created_at as text)", true
	}
	where := "t.missing = 0"
	args := []any{}
	if c.ID != "" {
		where += " AND " + keysetClause(keyCol, "t.track_id", desc)
		args = append(args, c.K, c.K, c.ID)
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		"SELECT %s, (%s) AS sortkey%s WHERE %s ORDER BY sortkey %s, t.track_id LIMIT %d",
		trackCols, keyCol, trackFrom, where, dir, limit+1), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []TrackRow
	var keys []string
	for rows.Next() {
		var tr TrackRow
		var key string
		if err := rows.Scan(&tr.TrackID, &tr.Title, &tr.Index, &tr.Disc, &tr.ArtistID, &tr.ArtistName,
			&tr.AlbumID, &tr.AlbumTitle, &tr.Genre, &tr.DurationMs, &tr.ArtID,
			&tr.Container, &tr.Codec, &tr.BitrateKbps, &tr.SizeBytes, &tr.MusicBrainzRecordingID, &key); err != nil {
			return nil, "", err
		}
		out = append(out, tr)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(keys[limit-1], out[limit-1].TrackID)
	}
	for i := range out {
		if err := s.hydrateTrackCredits(ctx, &out[i]); err != nil {
			return nil, "", err
		}
	}
	return out, next, nil
}

func (s *Store) GetTrack(ctx context.Context, trackID string) (TrackRow, bool, error) {
	row := s.db.QueryRowContext(ctx, trackSelect+` WHERE t.track_id = ? AND t.missing = 0`, trackID)
	tr, err := scanTrack(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackRow{}, false, nil
	}
	if err != nil {
		return TrackRow{}, false, err
	}
	if err := s.hydrateTrackCredits(ctx, &tr); err != nil {
		return TrackRow{}, false, err
	}
	return tr, true, nil
}

func (s *Store) hydrateAlbumCredits(ctx context.Context, album *AlbumRow) error {
	credits, err := s.readCredits(ctx, "album_artist_credits", "album_id", album.AlbumID)
	if err != nil {
		return err
	}
	album.ArtistCredits = credits
	album.PrimaryArtist = ArtistCreditRow{ArtistID: album.ArtistID, Name: album.ArtistName}
	return nil
}

func (s *Store) hydrateTrackCredits(ctx context.Context, track *TrackRow) error {
	credits, err := s.readCredits(ctx, "track_artist_credits", "track_id", track.TrackID)
	if err != nil {
		return err
	}
	track.ArtistCredits = credits
	track.PrimaryArtist = ArtistCreditRow{ArtistID: track.ArtistID, Name: track.ArtistName}
	if len(track.ArtistCredits) == 0 {
		return fmt.Errorf("track %s has no artist credits", track.TrackID)
	}
	return nil
}

func (s *Store) readCredits(ctx context.Context, table, ownerColumn, ownerID string) ([]ArtistCreditRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.artist_id,c.name,c.join_phrase,COALESCE(a.musicbrainz_id,'') FROM `+table+` c JOIN artists a ON a.artist_id=c.artist_id WHERE c.`+ownerColumn+`=? ORDER BY c.position`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistCreditRow
	for rows.Next() {
		var c ArtistCreditRow
		if err := rows.Scan(&c.ArtistID, &c.Name, &c.JoinPhrase, &c.MusicBrainzID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListAlbumTracks(ctx context.Context, albumID string) ([]TrackRow, error) {
	return s.listTracksWhere(ctx, "t.album_id = ?", "COALESCE(t.disc,1), COALESCE(t.idx,0), t.title", albumID)
}

func (s *Store) ListArtistTracks(ctx context.Context, artistID string) ([]TrackRow, error) {
	return s.listTracksWhere(ctx, "EXISTS (SELECT 1 FROM track_artist_credits c WHERE c.track_id=t.track_id AND c.artist_id=?)", "al.title, COALESCE(t.disc,1), COALESCE(t.idx,0)", artistID)
}

func (s *Store) ListGenreTracks(ctx context.Context, genre string) ([]TrackRow, error) {
	return s.listTracksWhere(ctx, "t.genre = ?", "t.artist_name, al.title, COALESCE(t.idx,0)", genre)
}

// Genre backs the contract's Genre schema.
type Genre struct {
	Name       string
	AlbumCount int
	ArtID      string
}

func (s *Store) ListGenres(ctx context.Context) ([]Genre, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.genre, count(DISTINCT t.album_id),
		       COALESCE((SELECT al.art_id FROM albums al JOIN tracks t2 ON t2.album_id = al.album_id
		                 WHERE t2.genre = t.genre AND al.art_id IS NOT NULL LIMIT 1), '')
		FROM tracks t WHERE t.missing = 0 AND t.genre != ''
		GROUP BY t.genre ORDER BY t.genre`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Genre
	for rows.Next() {
		var g Genre
		if err := rows.Scan(&g.Name, &g.AlbumCount, &g.ArtID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SearchLibrary is a case-insensitive substring search across the index —
// LIKE is plenty at household scale.
func (s *Store) SearchLibrary(ctx context.Context, q string) (LibrarySearch, error) {
	var res LibrarySearch
	pattern := "%" + strings.ReplaceAll(strings.ReplaceAll(q, "%", `\%`), "_", `\_`) + "%"

	arows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT a.artist_id, a.name, %s, COALESCE(a.musicbrainz_id,''),
		       (SELECT count(DISTINCT al.album_id) FROM albums al JOIN album_artist_credits ac ON ac.album_id=al.album_id WHERE ac.artist_id = a.artist_id AND al.missing = 0),
		       (SELECT count(DISTINCT t.track_id) FROM tracks t JOIN track_artist_credits c ON c.track_id=t.track_id WHERE c.artist_id=a.artist_id AND t.missing=0)
		FROM artists a WHERE a.missing = 0 AND (a.name LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM artist_aliases aa WHERE aa.artist_id=a.artist_id AND aa.name LIKE ? ESCAPE '\')) ORDER BY a.name LIMIT 25`, artistArt), pattern, pattern)
	if err != nil {
		return res, err
	}
	defer arows.Close()
	for arows.Next() {
		var a ArtistRow
		if err := arows.Scan(&a.ArtistID, &a.Name, &a.ArtID, &a.MusicBrainzID, &a.AlbumCount, &a.TrackCount); err != nil {
			return res, err
		}
		res.Artists = append(res.Artists, a)
	}
	if err := arows.Err(); err != nil {
		return res, err
	}

	brows, err := s.db.QueryContext(ctx,
		albumSelect+` WHERE al.missing = 0 AND (al.title LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM album_artist_credits c WHERE c.album_id=al.album_id AND c.name LIKE ? ESCAPE '\')) ORDER BY al.title LIMIT 25`, pattern, pattern)
	if err != nil {
		return res, err
	}
	defer brows.Close()
	for brows.Next() {
		a, _, err := scanAlbum(brows.Scan)
		if err != nil {
			return res, err
		}
		res.Albums = append(res.Albums, a)
	}
	if err := brows.Err(); err != nil {
		return res, err
	}
	for i := range res.Albums {
		if err := s.hydrateAlbumCredits(ctx, &res.Albums[i]); err != nil {
			return res, err
		}
	}

	tracks, err := s.listTracksWhere(ctx, `(t.title LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM track_artist_credits c WHERE c.track_id=t.track_id AND c.name LIKE ? ESCAPE '\'))`, "t.title LIMIT 50", pattern, pattern)
	if err != nil {
		return res, err
	}
	res.Tracks = tracks
	return res, nil
}

// ResolveTrackNative maps a canonical track id back to its source coordinates
// for streaming.
func (s *Store) ResolveTrackNative(ctx context.Context, trackID string) (kind, nativeID string, found bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT source_kind, native_id FROM tracks WHERE track_id = ? AND missing = 0`, trackID).
		Scan(&kind, &nativeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return kind, nativeID, true, nil
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}
