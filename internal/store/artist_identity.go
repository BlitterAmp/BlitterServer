package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// PendingMusicBrainzArtist identifies an MBID-backed artist whose canonical
// name and aliases have not yet been fetched.
type PendingMusicBrainzArtist struct {
	ArtistID      string
	Name          string
	MusicBrainzID string
}

// PendingMusicBrainzArtists returns one artist-id-keyset page. Missing artists
// remain eligible because they may be canonical targets for present duplicates.
func (s *Store) PendingMusicBrainzArtists(ctx context.Context, cursor string, limit int) ([]PendingMusicBrainzArtist, string, error) {
	if limit <= 0 {
		return nil, "", fmt.Errorf("list pending MusicBrainz artists: limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT artist_id,name,musicbrainz_id FROM artists
		WHERE artist_id>? AND musicbrainz_id IS NOT NULL AND musicbrainz_aliases_fetched_at=0
		ORDER BY artist_id LIMIT ?`, cursor, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list pending MusicBrainz artists: %w", err)
	}
	defer rows.Close()
	artists := make([]PendingMusicBrainzArtist, 0, limit+1)
	for rows.Next() {
		var artist PendingMusicBrainzArtist
		if err := rows.Scan(&artist.ArtistID, &artist.Name, &artist.MusicBrainzID); err != nil {
			return nil, "", fmt.Errorf("scan pending MusicBrainz artist: %w", err)
		}
		artists = append(artists, artist)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate pending MusicBrainz artists: %w", err)
	}
	if len(artists) <= limit {
		return artists, "", nil
	}
	return artists[:limit], artists[limit-1].ArtistID, nil
}

// MarkMusicBrainzArtistMetadataTerminal records an invalid or permanently
// unavailable MBID without treating its local name as canonical evidence.
func (s *Store) MarkMusicBrainzArtistMetadataTerminal(ctx context.Context, artistID, mbid string) error {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_aliases_fetched_at=-1
		WHERE artist_id=? AND musicbrainz_id=? AND musicbrainz_aliases_fetched_at=0`, artistID, mbid)
	if err != nil {
		return fmt.Errorf("mark MusicBrainz artist metadata terminal: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark MusicBrainz artist metadata terminal: rows affected: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("mark MusicBrainz artist metadata terminal: pending artist %s with MBID %s not found", artistID, mbid)
	}
	return nil
}

// PersistMusicBrainzArtistMetadata commits one successful provider response.
// Consolidation is deliberately separate so uniqueness is evaluated only
// after every canonical MBID has complete persisted evidence.
func (s *Store) PersistMusicBrainzArtistMetadata(ctx context.Context, artistID, mbid, canonicalName string, aliases []string, seq int64) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: begin transaction: %w", err)
	}
	defer tx.Rollback()
	changed, err := persistMusicBrainzArtistMetadataTx(ctx, tx, artistID, mbid, canonicalName, aliases, seq)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: commit: %w", err)
	}
	return changed, nil
}

// PersistMusicBrainzArtistMetadataAtNextSequence allocates and applies the
// metadata sequence under the library identity lock, preventing a later
// operation from publishing a higher cursor before these writes commit.
func (s *Store) PersistMusicBrainzArtistMetadataAtNextSequence(ctx context.Context, artistID, mbid, canonicalName string, aliases []string) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: begin transaction: %w", err)
	}
	defer tx.Rollback()
	seq, err := nextScanSeqTx(ctx, tx)
	if err != nil {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: allocate change sequence: %w", err)
	}
	changed, err := persistMusicBrainzArtistMetadataTx(ctx, tx, artistID, mbid, canonicalName, aliases, seq)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: commit: %w", err)
	}
	return changed, nil
}

// ApplyMusicBrainzArtistMetadata preserves the strict direct-call contract:
// metadata and consolidation either both commit or both roll back.
func (s *Store) ApplyMusicBrainzArtistMetadata(ctx context.Context, artistID, mbid, canonicalName string, aliases []string, seq int64) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("apply MusicBrainz artist metadata: begin transaction: %w", err)
	}
	defer tx.Rollback()
	metadataChanged, err := persistMusicBrainzArtistMetadataTx(ctx, tx, artistID, mbid, canonicalName, aliases, seq)
	if err != nil {
		return false, err
	}
	groups, err := artistMergeGroupsTx(ctx, tx, mbid)
	if err != nil {
		return false, fmt.Errorf("apply MusicBrainz artist metadata: plan consolidation: %w", err)
	}
	for _, group := range groups {
		if group.collides {
			return false, fmt.Errorf("apply MusicBrainz artist metadata: album identity collision for MBID %s", mbid)
		}
	}
	merged, err := consolidateArtistGroupsTx(ctx, tx, groups, seq)
	if err != nil {
		return false, fmt.Errorf("apply MusicBrainz artist metadata: consolidate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("apply MusicBrainz artist metadata: commit: %w", err)
	}
	return metadataChanged || merged, nil
}

// ConsolidateMusicBrainzArtists merges every safe unique-evidence group in one
// transaction and sequence. It does nothing while canonical metadata is pending.
func (s *Store) ConsolidateMusicBrainzArtists(ctx context.Context, seq int64) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("consolidate MusicBrainz artists: begin transaction: %w", err)
	}
	defer tx.Rollback()
	ready, currentSeq, groups, err := readyArtistMergeGroupsTx(ctx, tx)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}
	changed, err := consolidateArtistGroupsTx(ctx, tx, groups, seq)
	if err != nil {
		return false, fmt.Errorf("consolidate MusicBrainz artists: %w", err)
	}
	markerSeq := currentSeq
	if changed {
		markerSeq = seq
	}
	if err := setArtistConsolidationMarkerTx(ctx, tx, markerSeq); err != nil {
		return false, fmt.Errorf("consolidate MusicBrainz artists: store evaluation marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("consolidate MusicBrainz artists: commit: %w", err)
	}
	return changed, nil
}

// ConsolidateMusicBrainzArtistsAtNextSequence allocates a sequence in the same
// transaction only when at least one safe group will change client-visible data.
func (s *Store) ConsolidateMusicBrainzArtistsAtNextSequence(ctx context.Context) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("consolidate MusicBrainz artists: begin transaction: %w", err)
	}
	defer tx.Rollback()
	ready, currentSeq, groups, err := readyArtistMergeGroupsTx(ctx, tx)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}
	changed := false
	markerSeq := currentSeq
	if hasSafeArtistMergeGroup(groups) {
		seq, err := nextScanSeqTx(ctx, tx)
		if err != nil {
			return false, fmt.Errorf("consolidate MusicBrainz artists: allocate change sequence: %w", err)
		}
		changed, err = consolidateArtistGroupsTx(ctx, tx, groups, seq)
		if err != nil {
			return false, fmt.Errorf("consolidate MusicBrainz artists: %w", err)
		}
		markerSeq = seq
	}
	if err := setArtistConsolidationMarkerTx(ctx, tx, markerSeq); err != nil {
		return false, fmt.Errorf("consolidate MusicBrainz artists: store evaluation marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("consolidate MusicBrainz artists: commit: %w", err)
	}
	return changed, nil
}

func persistMusicBrainzArtistMetadataTx(ctx context.Context, tx *sql.Tx, artistID, mbid, canonicalName string, aliases []string, seq int64) (bool, error) {
	canonicalName = strings.TrimSpace(canonicalName)
	mbid = strings.TrimSpace(mbid)
	if artistID == "" || mbid == "" || canonicalName == "" {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: artist id, MBID, and canonical name are required")
	}
	var oldName, storedMBID string
	if err := tx.QueryRowContext(ctx, `SELECT name,musicbrainz_id FROM artists WHERE artist_id=?`, artistID).Scan(&oldName, &storedMBID); err != nil {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: validate canonical artist %s: %w", artistID, err)
	}
	if storedMBID != mbid {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: canonical artist %s has MBID %q, not %q", artistID, storedMBID, mbid)
	}
	renamed := oldName != canonicalName
	changed := renamed
	for _, alias := range uniqueArtistNames(append([]string{oldName}, aliases...)) {
		if alias == canonicalName {
			continue
		}
		inserted, err := insertArtistAliasTx(ctx, tx, artistID, alias)
		if err != nil {
			return false, fmt.Errorf("persist MusicBrainz artist metadata: store alias %q: %w", alias, err)
		}
		changed = changed || inserted
	}
	if renamed {
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET change_seq=? WHERE artist_id=? AND missing=0`, seq, artistID); err != nil {
			return false, fmt.Errorf("persist MusicBrainz artist metadata: stamp renamed artist albums: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tracks SET artist_name=?,change_seq=CASE WHEN missing=0 THEN ? ELSE change_seq END WHERE artist_id=?`, canonicalName, seq, artistID); err != nil {
			return false, fmt.Errorf("persist MusicBrainz artist metadata: rename primary tracks: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artists SET name=?,change_seq=CASE WHEN ? THEN ? ELSE change_seq END,musicbrainz_aliases_fetched_at=? WHERE artist_id=?`, canonicalName, changed, seq, time.Now().Unix(), artistID); err != nil {
		return false, fmt.Errorf("persist MusicBrainz artist metadata: update canonical artist: %w", err)
	}
	return changed, nil
}

func nextScanSeqTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `INSERT INTO settings(key,value) VALUES('library_scan_seq','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1 RETURNING CAST(value AS INTEGER)`).Scan(&seq)
	return seq, err
}

func musicBrainzArtistEvidenceTx(ctx context.Context, tx *sql.Tx) (map[string]map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.musicbrainz_id,a.name,aa.name
		FROM artists a LEFT JOIN artist_aliases aa ON aa.artist_id=a.artist_id
		WHERE a.musicbrainz_id IS NOT NULL AND a.musicbrainz_aliases_fetched_at>0
		ORDER BY a.artist_id,aa.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type names struct {
		canonical string
		aliases   []string
	}
	byMBID := map[string]*names{}
	owners := map[string]map[string]bool{}
	for rows.Next() {
		var mbid, canonical string
		var alias sql.NullString
		if err := rows.Scan(&mbid, &canonical, &alias); err != nil {
			return nil, err
		}
		entry := byMBID[mbid]
		if entry == nil {
			entry = &names{canonical: canonical}
			byMBID[mbid] = entry
		}
		addArtistEvidenceOwner(owners, canonical, mbid)
		if alias.Valid {
			entry.aliases = append(entry.aliases, alias.String)
			addArtistEvidenceOwner(owners, alias.String, mbid)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for mbid, entry := range byMBID {
		canonicalOwners := owners[artistEvidenceKey(entry.canonical)]
		if len(canonicalOwners) != 1 || !canonicalOwners[mbid] {
			continue
		}
		for _, alias := range entry.aliases {
			aliasOwners := owners[artistEvidenceKey(alias)]
			if len(aliasOwners) == 1 && aliasOwners[mbid] {
				addArtistEvidenceOwner(owners, strings.TrimSpace(entry.canonical)+" ("+strings.TrimSpace(alias)+")", mbid)
			}
		}
	}
	return owners, nil
}

func addArtistEvidenceOwner(owners map[string]map[string]bool, name, mbid string) {
	key := artistEvidenceKey(name)
	if key == "" {
		return
	}
	if owners[key] == nil {
		owners[key] = map[string]bool{}
	}
	owners[key][mbid] = true
}

func artistEvidenceKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func uniqueArtistNames(names []string) []string {
	var unique []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		found := false
		for _, existing := range unique {
			found = found || strings.EqualFold(existing, name)
		}
		if !found {
			unique = append(unique, name)
		}
	}
	return unique
}

func insertArtistAliasTx(ctx context.Context, tx *sql.Tx, artistID, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO artist_aliases(artist_id,name) VALUES(?,?)`, artistID, name)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	return inserted == 1, err
}

func artistAliasesTx(ctx context.Context, tx *sql.Tx, artistID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM artist_aliases WHERE artist_id=?`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var aliases []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}
