package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SetAlbumArtAtNextSequence conditionally attaches album art and allocates its
// change sequence in the same library-identity transaction.
func (s *Store) SetAlbumArtAtNextSequence(ctx context.Context, albumID, artID string) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("attach album artwork: begin transaction: %w", err)
	}
	defer tx.Rollback()
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM albums WHERE album_id=? AND art_id IS NULL
	)`, albumID).Scan(&eligible); err != nil {
		return false, fmt.Errorf("attach album artwork: check eligibility: %w", err)
	}
	if !eligible {
		return false, nil
	}
	seq, err := nextScanSeqTx(ctx, tx)
	if err != nil {
		return false, fmt.Errorf("attach album artwork: allocate change sequence: %w", err)
	}
	updated, err := setAlbumArtTx(ctx, tx, albumID, artID, seq)
	if err != nil {
		return false, fmt.Errorf("attach album artwork: %w", err)
	}
	if !updated {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("attach album artwork: commit: %w", err)
	}
	return true, nil
}

func setAlbumArtTx(ctx context.Context, tx *sql.Tx, albumID, artID string, seq int64) (bool, error) {
	result, err := tx.ExecContext(ctx, `UPDATE albums SET art_id=?,art_tried=1,art_tried_at=?,change_seq=?
		WHERE album_id=? AND art_id IS NULL`, artID, time.Now().Unix(), seq, albumID)
	if err != nil {
		return false, fmt.Errorf("update album: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tracks SET change_seq=? WHERE album_id=? AND missing=0 AND art_id IS NULL`, seq, albumID); err != nil {
		return false, fmt.Errorf("stamp dependent tracks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artists SET change_seq=?
		WHERE missing=0 AND art_id IS NULL AND artist_id=(SELECT artist_id FROM albums WHERE album_id=?)
		AND ?=(SELECT al.album_id FROM albums al WHERE al.artist_id=artists.artist_id AND al.art_id IS NOT NULL AND al.missing=0
			ORDER BY COALESCE(al.year,9999),al.title,al.album_id LIMIT 1)`, seq, albumID, albumID); err != nil {
		return false, fmt.Errorf("stamp dependent artist: %w", err)
	}
	return true, nil
}

// SetArtistArtAtNextSequence conditionally attaches artist art and allocates
// its change sequence in the same library-identity transaction.
func (s *Store) SetArtistArtAtNextSequence(ctx context.Context, artistID, expectedArtID, artID string) (bool, error) {
	s.libraryIdentity.Lock()
	defer s.libraryIdentity.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("attach artist artwork: begin transaction: %w", err)
	}
	defer tx.Rollback()
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM artists WHERE artist_id=? AND COALESCE(art_id,'')=?
	)`, artistID, expectedArtID).Scan(&eligible); err != nil {
		return false, fmt.Errorf("attach artist artwork: check eligibility: %w", err)
	}
	if !eligible {
		return false, nil
	}
	seq, err := nextScanSeqTx(ctx, tx)
	if err != nil {
		return false, fmt.Errorf("attach artist artwork: allocate change sequence: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE artists SET art_id=?,art_tried=1,art_tried_at=?,change_seq=?
		WHERE artist_id=? AND COALESCE(art_id,'')=?`, artID, time.Now().Unix(), seq, artistID, expectedArtID)
	if err != nil {
		return false, fmt.Errorf("attach artist artwork: update: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("attach artist artwork: rows affected: %w", err)
	}
	if updated != 1 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("attach artist artwork: commit: %w", err)
	}
	return true, nil
}
