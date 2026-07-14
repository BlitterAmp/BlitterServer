package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type duplicateArtist struct {
	id, name string
	art      sql.NullString
}

type artistMergeGroup struct {
	canonicalID   string
	mbid          string
	canonicalName string
	canonicalArt  sql.NullString
	sources       []duplicateArtist
	collides      bool
}

const artistConsolidationEvaluatedSeq = "artist_consolidation_evaluated_seq"

func readyArtistMergeGroupsTx(ctx context.Context, tx *sql.Tx) (bool, int64, []artistMergeGroup, error) {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artists WHERE musicbrainz_id IS NOT NULL AND musicbrainz_aliases_fetched_at=0`).Scan(&pending); err != nil {
		return false, 0, nil, fmt.Errorf("consolidate MusicBrainz artists: count pending metadata: %w", err)
	}
	if pending != 0 {
		return false, 0, nil, nil
	}
	currentSeq, err := currentLibraryScanSeqTx(ctx, tx)
	if err != nil {
		return false, 0, nil, fmt.Errorf("consolidate MusicBrainz artists: read current sequence: %w", err)
	}
	var evaluatedSeq int64
	err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM settings WHERE key=?`, artistConsolidationEvaluatedSeq).Scan(&evaluatedSeq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil, fmt.Errorf("consolidate MusicBrainz artists: read evaluation marker: %w", err)
	}
	if err == nil && evaluatedSeq == currentSeq {
		return false, currentSeq, nil, nil
	}
	groups, err := artistMergeGroupsTx(ctx, tx, "")
	if err != nil {
		return false, 0, nil, fmt.Errorf("consolidate MusicBrainz artists: plan groups: %w", err)
	}
	return true, currentSeq, groups, nil
}

func artistMergeGroupsTx(ctx context.Context, tx *sql.Tx, onlyMBID string) ([]artistMergeGroup, error) {
	evidence, err := musicBrainzArtistEvidenceTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	query := `SELECT artist_id,musicbrainz_id,name,art_id FROM artists
		WHERE musicbrainz_id IS NOT NULL AND musicbrainz_aliases_fetched_at>0`
	var args []any
	if onlyMBID != "" {
		query += ` AND musicbrainz_id=?`
		args = append(args, onlyMBID)
	}
	query += ` ORDER BY artist_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []artistMergeGroup
	for rows.Next() {
		var group artistMergeGroup
		if err := rows.Scan(&group.canonicalID, &group.mbid, &group.canonicalName, &group.canonicalArt); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range groups {
		owners, err := matchingAlbumOwnersTx(ctx, tx, evidence, groups[i].mbid)
		if err != nil {
			return nil, err
		}
		groups[i].sources, err = linkedMatchingArtistsTx(ctx, tx, evidence, groups[i].mbid, owners)
		if err != nil {
			return nil, err
		}
		groups[i].collides, err = artistGroupAlbumCollisionTx(ctx, tx, groups[i])
		if err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func matchingAlbumOwnersTx(ctx context.Context, tx *sql.Tx, evidence map[string]map[string]bool, mbid string) ([]duplicateArtist, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.artist_id,a.name,a.art_id FROM artists a
		WHERE a.musicbrainz_id IS NULL AND EXISTS (
			SELECT 1 FROM albums al WHERE al.artist_id=a.artist_id AND al.missing=0
		) ORDER BY a.artist_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []duplicateArtist
	for rows.Next() {
		var artist duplicateArtist
		if err := rows.Scan(&artist.id, &artist.name, &artist.art); err != nil {
			return nil, err
		}
		matches := evidence[artistEvidenceKey(artist.name)]
		if len(matches) == 1 && matches[mbid] {
			owners = append(owners, artist)
		}
	}
	return owners, rows.Err()
}

func linkedMatchingArtistsTx(ctx context.Context, tx *sql.Tx, evidence map[string]map[string]bool, mbid string, owners []duplicateArtist) ([]duplicateArtist, error) {
	seen := map[string]bool{}
	var artists []duplicateArtist
	for _, owner := range owners {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.artist_id,a.name,a.art_id FROM artists a
			WHERE a.musicbrainz_id IS NULL AND (
				a.artist_id=?
				OR EXISTS (SELECT 1 FROM album_artist_credits c JOIN albums al ON al.album_id=c.album_id WHERE al.artist_id=? AND c.artist_id=a.artist_id)
				OR EXISTS (SELECT 1 FROM tracks t JOIN albums al ON al.album_id=t.album_id WHERE al.artist_id=? AND t.artist_id=a.artist_id)
				OR EXISTS (SELECT 1 FROM track_artist_credits c JOIN tracks t ON t.track_id=c.track_id JOIN albums al ON al.album_id=t.album_id WHERE al.artist_id=? AND c.artist_id=a.artist_id)
			) ORDER BY a.artist_id`, owner.id, owner.id, owner.id, owner.id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var artist duplicateArtist
			if err := rows.Scan(&artist.id, &artist.name, &artist.art); err != nil {
				rows.Close()
				return nil, err
			}
			matches := evidence[artistEvidenceKey(artist.name)]
			if len(matches) == 1 && matches[mbid] && !seen[artist.id] {
				seen[artist.id] = true
				artists = append(artists, artist)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return artists, nil
}

func artistGroupAlbumCollisionTx(ctx context.Context, tx *sql.Tx, group artistMergeGroup) (bool, error) {
	ids := map[string]bool{group.canonicalID: true}
	for _, source := range group.sources {
		ids[source.id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT artist_id,title FROM albums WHERE musicbrainz_release_id IS NULL ORDER BY album_id`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	titles := map[string]bool{}
	for rows.Next() {
		var artistID, title string
		if err := rows.Scan(&artistID, &title); err != nil {
			return false, err
		}
		if !ids[artistID] {
			continue
		}
		key := artistEvidenceKey(title)
		if titles[key] {
			return true, nil
		}
		titles[key] = true
	}
	return false, rows.Err()
}

func consolidateArtistGroupsTx(ctx context.Context, tx *sql.Tx, groups []artistMergeGroup, seq int64) (bool, error) {
	changed := false
	for _, group := range groups {
		if group.collides || len(group.sources) == 0 {
			continue
		}
		canonicalArt := group.canonicalArt
		for _, source := range group.sources {
			if _, err := tx.ExecContext(ctx, `UPDATE albums SET change_seq=? WHERE missing=0 AND EXISTS (
				SELECT 1 FROM album_artist_credits c WHERE c.album_id=albums.album_id AND c.artist_id=?
			)`, seq, source.id); err != nil {
				return false, fmt.Errorf("stamp album credit parents for %s: %w", source.id, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tracks SET change_seq=? WHERE missing=0 AND EXISTS (
				SELECT 1 FROM track_artist_credits c WHERE c.track_id=tracks.track_id AND c.artist_id=?
			)`, seq, source.id); err != nil {
				return false, fmt.Errorf("stamp track credit parents for %s: %w", source.id, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE albums SET artist_id=?,change_seq=? WHERE artist_id=?`, group.canonicalID, seq, source.id); err != nil {
				return false, fmt.Errorf("move albums from %s: %w", source.id, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE album_artist_credits SET artist_id=? WHERE artist_id=?`, group.canonicalID, source.id); err != nil {
				return false, fmt.Errorf("move album credits from %s: %w", source.id, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tracks SET artist_id=?,artist_name=?,change_seq=? WHERE artist_id=?`, group.canonicalID, group.canonicalName, seq, source.id); err != nil {
				return false, fmt.Errorf("move tracks from %s: %w", source.id, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE track_artist_credits SET artist_id=? WHERE artist_id=?`, group.canonicalID, source.id); err != nil {
				return false, fmt.Errorf("move track credits from %s: %w", source.id, err)
			}
			if !canonicalArt.Valid && source.art.Valid {
				if _, err := tx.ExecContext(ctx, `UPDATE artists SET art_id=? WHERE artist_id=? AND art_id IS NULL`, source.art.String, group.canonicalID); err != nil {
					return false, fmt.Errorf("transfer art from %s: %w", source.id, err)
				}
				canonicalArt = source.art
			}
			aliases, err := artistAliasesTx(ctx, tx, source.id)
			if err != nil {
				return false, fmt.Errorf("read aliases from %s: %w", source.id, err)
			}
			for _, alias := range append([]string{source.name}, aliases...) {
				if strings.TrimSpace(alias) == group.canonicalName {
					continue
				}
				if _, err := insertArtistAliasTx(ctx, tx, group.canonicalID, alias); err != nil {
					return false, fmt.Errorf("preserve alias from %s: %w", source.id, err)
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE artists SET missing=1,change_seq=? WHERE artist_id=?`, seq, source.id); err != nil {
				return false, fmt.Errorf("retire duplicate %s: %w", source.id, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artists SET missing=NOT EXISTS (
			SELECT 1 FROM albums WHERE albums.artist_id=artists.artist_id AND albums.missing=0
		),change_seq=? WHERE artist_id=?`, seq, group.canonicalID); err != nil {
			return false, fmt.Errorf("stamp canonical artist %s: %w", group.canonicalID, err)
		}
		changed = true
	}
	return changed, nil
}

func hasSafeArtistMergeGroup(groups []artistMergeGroup) bool {
	for _, group := range groups {
		if !group.collides && len(group.sources) > 0 {
			return true
		}
	}
	return false
}

func currentLibraryScanSeqTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((
		SELECT CAST(value AS INTEGER) FROM settings WHERE key='library_scan_seq'
	),0)`).Scan(&seq)
	return seq, err
}

func setArtistConsolidationMarkerTx(ctx context.Context, tx *sql.Tx, seq int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, artistConsolidationEvaluatedSeq, seq)
	return err
}
