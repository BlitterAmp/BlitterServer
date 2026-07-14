package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

// SourceFileCacheEntry is one reusable raw source parse and its fingerprint.
type SourceFileCacheEntry struct {
	Candidate       source.TrackCandidate
	Meta            source.TrackMeta
	CanonicalExists bool
	ArtPending      bool
}

// SourceInstance identifies one configured generation of a filesystem root.
type SourceInstance struct {
	ID         string
	Root       string
	Generation int64
	Replaced   bool
}

// LoadSourceFileCache loads one source/parser edition. Invalid JSON rows are
// deliberately omitted so callers treat them as parse misses.
func (s *Store) LoadSourceFileCache(ctx context.Context, sourceID, kind string, parserVersion int) (map[string]SourceFileCacheEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.native_id,c.size_bytes,c.mtime_ns,c.parsed_meta_json,c.art_pending,
		       EXISTS(SELECT 1 FROM tracks t WHERE t.source_kind=c.source_kind AND t.native_id=c.native_id)
		FROM source_file_cache c
		WHERE c.source_instance_id=? AND c.source_kind=? AND c.parser_version=?`, sourceID, kind, parserVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SourceFileCacheEntry)
	for rows.Next() {
		var entry SourceFileCacheEntry
		var raw []byte
		if err := rows.Scan(&entry.Candidate.NativeID, &entry.Candidate.SizeBytes, &entry.Candidate.MtimeNS, &raw, &entry.ArtPending, &entry.CanonicalExists); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &entry.Meta); err != nil || entry.Meta.NativeID != entry.Candidate.NativeID {
			continue
		}
		out[entry.Candidate.NativeID] = entry
	}
	return out, rows.Err()
}

// PutSourceFileCache publishes a parse only after its canonical upsert succeeds.
func (s *Store) PutSourceFileCache(ctx context.Context, sourceID, kind string, parserVersion int, candidate source.TrackCandidate, meta source.TrackMeta) error {
	return s.PutSourceFileCacheState(ctx, sourceID, kind, parserVersion, candidate, meta, false)
}

func (s *Store) PutSourceFileCacheState(ctx context.Context, sourceID, kind string, parserVersion int, candidate source.TrackCandidate, meta source.TrackMeta, artPending bool) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode source file metadata: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO source_file_cache
		(source_instance_id,source_kind,native_id,size_bytes,mtime_ns,parser_version,parsed_meta_json,art_pending)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(source_instance_id,source_kind,native_id) DO UPDATE SET
			size_bytes=excluded.size_bytes,mtime_ns=excluded.mtime_ns,
			parser_version=excluded.parser_version,parsed_meta_json=excluded.parsed_meta_json,art_pending=excluded.art_pending`,
		sourceID, kind, candidate.NativeID, candidate.SizeBytes, candidate.MtimeNS, parserVersion, raw, artPending)
	return err
}

// PruneSourceFileCache removes cache rows not encountered by a successful scan.
func (s *Store) PruneSourceFileCache(ctx context.Context, sourceID, kind string, encountered map[string]struct{}) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := pruneSourceFileCache(ctx, tx, sourceID, kind, encountered); err != nil {
		return err
	}
	return tx.Commit()
}

func pruneSourceFileCache(ctx context.Context, tx *sql.Tx, sourceID, kind string, encountered map[string]struct{}) error {
	rows, err := tx.QueryContext(ctx, `SELECT native_id FROM source_file_cache WHERE source_instance_id=? AND source_kind=?`, sourceID, kind)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var nativeID string
		if err := rows.Scan(&nativeID); err != nil {
			rows.Close()
			return err
		}
		if _, ok := encountered[nativeID]; !ok {
			stale = append(stale, nativeID)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, nativeID := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM source_file_cache WHERE source_instance_id=? AND source_kind=? AND native_id=?`, sourceID, kind, nativeID); err != nil {
			return err
		}
	}
	return nil
}

// FinalizeSourceScan atomically marks missing rows and prunes absent cache rows.
func (s *Store) FinalizeSourceScan(ctx context.Context, kind string, seq int64, sourceID string, encountered, seen map[string]struct{}) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := markTracksSeen(ctx, tx, kind, mapKeys(seen), seq); err != nil {
		return err
	}
	if err := pruneSourceFileCache(ctx, tx, sourceID, kind, encountered); err != nil {
		return err
	}
	if err := finishScan(ctx, tx, kind, seq); err != nil {
		return err
	}
	return tx.Commit()
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// ConfigureFilesystemSource atomically retains or rotates the source identity.
func (s *Store) ConfigureFilesystemSource(ctx context.Context, root string) (SourceInstance, bool, error) {
	normalized, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return SourceInstance{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SourceInstance{}, false, err
	}
	defer tx.Rollback()
	currentRoot, _ := txSetting(ctx, tx, "filesystem_path")
	id, _ := txSetting(ctx, tx, "filesystem_source_id")
	generationRaw, _ := txSetting(ctx, tx, "filesystem_source_generation")
	generation, _ := strconv.ParseInt(generationRaw, 10, 64)
	changed := currentRoot != normalized || id == ""
	replaced := currentRoot != "" && currentRoot != normalized
	if changed {
		id = NewID("src")
		generation++
		if _, err := tx.ExecContext(ctx, `DELETE FROM source_file_cache WHERE source_kind='filesystem'`); err != nil {
			return SourceInstance{}, false, err
		}
		if replaced {
			var seq int64
			if err := tx.QueryRowContext(ctx, `INSERT INTO settings(key,value) VALUES('library_scan_seq','1')
				ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1 RETURNING CAST(value AS INTEGER)`).Scan(&seq); err != nil {
				return SourceInstance{}, false, err
			}
			if err := finishScan(ctx, tx, "filesystem", seq); err != nil {
				return SourceInstance{}, false, err
			}
		}
	}
	for key, value := range map[string]string{
		"source_kind": "filesystem", "filesystem_path": normalized,
		"filesystem_source_id": id, "filesystem_source_generation": strconv.FormatInt(generation, 10),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return SourceInstance{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SourceInstance{}, false, err
	}
	return SourceInstance{ID: id, Root: normalized, Generation: generation, Replaced: replaced}, changed, nil
}

func txSetting(ctx context.Context, tx *sql.Tx, key string) (string, bool) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&value)
	return value, err == nil
}

// FilesystemSourceInstance restores the configured filesystem identity.
func (s *Store) FilesystemSourceInstance(ctx context.Context) (SourceInstance, bool, error) {
	var instance SourceInstance
	var generation string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.value,i.value,g.value FROM settings p
		JOIN settings i ON i.key='filesystem_source_id'
		JOIN settings g ON g.key='filesystem_source_generation'
		WHERE p.key='filesystem_path' AND p.value<>''`).Scan(&instance.Root, &instance.ID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceInstance{}, false, nil
	}
	if err != nil {
		return SourceInstance{}, false, err
	}
	instance.Generation, err = strconv.ParseInt(generation, 10, 64)
	return instance, err == nil, err
}

// UnlinkFilesystemSource clears configuration and invalidates active scans.
func (s *Store) UnlinkFilesystemSource(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	raw, _ := txSetting(ctx, tx, "filesystem_source_generation")
	generation, _ := strconv.ParseInt(raw, 10, 64)
	generation++
	var seq int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO settings (key, value) VALUES ('library_scan_seq', '1')
		ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1
		RETURNING CAST(value AS INTEGER)`).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_file_cache WHERE source_kind='filesystem'`); err != nil {
		return 0, err
	}
	for key, value := range map[string]string{
		"source_kind": "", "filesystem_path": "", "filesystem_source_id": "",
		"filesystem_source_generation": strconv.FormatInt(generation, 10),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return 0, err
		}
	}
	if err := finishScan(ctx, tx, "filesystem", seq); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return generation, nil
}
