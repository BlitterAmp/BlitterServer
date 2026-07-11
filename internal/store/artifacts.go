package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ArtifactRow backs the contract's Artifact schema.
type ArtifactRow struct {
	ArtifactID  string
	TrackID     string
	AlbumID     string
	ArtID       string
	Format      string
	BitrateKbps int
	Status      string
	Bytes       int64
	Path        string
	Error       string
	Released    bool
}

const artifactSelect = `
	SELECT a.artifact_id, a.track_id, t.album_id, COALESCE(t.art_id, al.art_id, ''),
	       a.format, a.bitrate_kbps, a.status,
	       COALESCE(a.bytes, 0), COALESCE(a.path, ''), COALESCE(a.error, ''), a.released
	FROM artifacts a
	JOIN tracks t ON t.track_id = a.track_id
	JOIN albums al ON al.album_id = t.album_id`

func scanArtifact(scan func(...any) error) (ArtifactRow, error) {
	var a ArtifactRow
	var released int
	err := scan(&a.ArtifactID, &a.TrackID, &a.AlbumID, &a.ArtID, &a.Format, &a.BitrateKbps,
		&a.Status, &a.Bytes, &a.Path, &a.Error, &released)
	a.Released = released != 0
	return a, err
}

// UpsertArtifact returns the artifact for (track, format, bitrate) at the
// track's current source version, creating it when absent. `original`
// artifacts are born ready with the source's exact size.
func (s *Store) UpsertArtifact(ctx context.Context, trackID, format string, bitrateKbps int) (ArtifactRow, bool, error) {
	tr, found, err := s.GetTrack(ctx, trackID)
	if err != nil {
		return ArtifactRow{}, false, err
	}
	if !found {
		return ArtifactRow{}, false, fmt.Errorf("track %s: %w", trackID, ErrNotFound)
	}
	var version int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT version FROM tracks WHERE track_id = ?`, trackID).Scan(&version); err != nil {
		return ArtifactRow{}, false, err
	}
	if format == "original" {
		bitrateKbps = 0
	}

	var existingID string
	err = s.db.QueryRowContext(ctx, `
		SELECT artifact_id FROM artifacts
		WHERE track_id = ? AND format = ? AND bitrate_kbps = ? AND source_version = ?`,
		trackID, format, bitrateKbps, version).Scan(&existingID)
	if err == nil {
		now := time.Now().Unix()
		if _, err := s.db.ExecContext(ctx,
			`UPDATE artifacts SET last_access = ?, released = 0 WHERE artifact_id = ?`, now, existingID); err != nil {
			return ArtifactRow{}, false, err
		}
		a, _, err := s.GetArtifact(ctx, existingID)
		return a, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ArtifactRow{}, false, err
	}

	id := NewID("arf")
	now := time.Now().Unix()
	status := "queued"
	var bytes any
	if format == "original" {
		status = "ready"
		bytes = tr.SizeBytes
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO artifacts (artifact_id, track_id, format, bitrate_kbps, source_version, status, bytes, created_at, last_access)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, trackID, format, bitrateKbps, version, status, bytes, now, now); err != nil {
		return ArtifactRow{}, false, err
	}
	a, _, err := s.GetArtifact(ctx, id)
	return a, true, err
}

func (s *Store) GetArtifact(ctx context.Context, artifactID string) (ArtifactRow, bool, error) {
	row := s.db.QueryRowContext(ctx, artifactSelect+` WHERE a.artifact_id = ?`, artifactID)
	a, err := scanArtifact(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactRow{}, false, nil
	}
	if err != nil {
		return ArtifactRow{}, false, err
	}
	return a, true, nil
}

func (s *Store) MarkArtifactProcessing(ctx context.Context, artifactID string) error {
	if _, err := s.exec1(ctx,
		`UPDATE artifacts SET status = 'processing' WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("artifact %s: %w", artifactID, err)
	}
	return nil
}

func (s *Store) MarkArtifactReady(ctx context.Context, artifactID string, bytes int64, path string) error {
	if _, err := s.exec1(ctx, `
		UPDATE artifacts SET status = 'ready', bytes = ?, path = ?, error = NULL, last_access = ?
		WHERE artifact_id = ?`, bytes, path, time.Now().Unix(), artifactID); err != nil {
		return fmt.Errorf("artifact %s: %w", artifactID, err)
	}
	return nil
}

func (s *Store) MarkArtifactFailed(ctx context.Context, artifactID, errMsg string) error {
	if _, err := s.exec1(ctx,
		`UPDATE artifacts SET status = 'failed', error = ? WHERE artifact_id = ?`, errMsg, artifactID); err != nil {
		return fmt.Errorf("artifact %s: %w", artifactID, err)
	}
	return nil
}

// NextQueuedArtifact returns the oldest queued artifact, or nil.
func (s *Store) NextQueuedArtifact(ctx context.Context) (*ArtifactRow, error) {
	row := s.db.QueryRowContext(ctx,
		artifactSelect+` WHERE a.status = 'queued' ORDER BY a.created_at, a.artifact_id LIMIT 1`)
	a, err := scanArtifact(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ReleaseArtifact marks the artifact evictable (client hint).
func (s *Store) ReleaseArtifact(ctx context.Context, artifactID string) error {
	if _, err := s.exec1(ctx,
		`UPDATE artifacts SET released = 1 WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("artifact %s: %w", artifactID, err)
	}
	return nil
}

// TouchArtifact bumps last_access for LRU ordering.
func (s *Store) TouchArtifact(ctx context.Context, artifactID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE artifacts SET last_access = ? WHERE artifact_id = ?`, time.Now().Unix(), artifactID)
	return err
}

// ArtifactCacheUsage sums cached transcode bytes (originals occupy no cache).
func (s *Store) ArtifactCacheUsage(ctx context.Context) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(bytes), 0) FROM artifacts
		WHERE status = 'ready' AND format != 'original'`).Scan(&total)
	return total, err
}

// ArtifactEvictionCandidates returns ready transcode artifacts to evict, in
// preference order (released first, then coldest), until freeing `need`
// bytes.
func (s *Store) ArtifactEvictionCandidates(ctx context.Context, need int64) ([]ArtifactRow, error) {
	rows, err := s.db.QueryContext(ctx, artifactSelect+`
		WHERE a.status = 'ready' AND a.format != 'original'
		ORDER BY a.released DESC, a.last_access, a.artifact_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtifactRow
	var freed int64
	for rows.Next() && freed < need {
		a, err := scanArtifact(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		freed += a.Bytes
	}
	return out, rows.Err()
}

// DeleteArtifact removes the row (the caller deletes the cache file).
func (s *Store) DeleteArtifact(ctx context.Context, artifactID string) error {
	if _, err := s.exec1(ctx, `DELETE FROM artifacts WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("artifact %s: %w", artifactID, err)
	}
	return nil
}
