package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type changesQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ChangesSnapshot returns one change page and the library version from the
// same SQLite read snapshot, so the version can never advance beyond its rows.
func (s *Store) ChangesSnapshot(ctx context.Context, since int64, cur string, limit int) ([]LibraryChange, string, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", 0, fmt.Errorf("begin changes snapshot: %w", err)
	}
	defer tx.Rollback()
	version, err := libraryChangesVersionTx(ctx, tx)
	if err != nil {
		return nil, "", 0, fmt.Errorf("read changes snapshot version: %w", err)
	}
	changes, next, err := changesSinceQuery(ctx, tx, since, cur, limit)
	if err != nil {
		return nil, "", 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", 0, fmt.Errorf("finish changes snapshot: %w", err)
	}
	return changes, next, version, nil
}

func libraryChangesVersionTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((
		SELECT CAST(value AS INTEGER) FROM settings WHERE key='library_scan_seq'
	),0)`).Scan(&version)
	return version, err
}

func changesSinceQuery(ctx context.Context, q changesQuerier, since int64, cur string, limit int) ([]LibraryChange, string, error) {
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
	rows, err := q.QueryContext(ctx, fmt.Sprintf(`
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
		var change LibraryChange
		var missing int
		if err := rows.Scan(&change.ChangeSeq, &change.Kind, &change.ID, &missing); err != nil {
			return nil, "", err
		}
		change.Missing = missing != 0
		out = append(out, change)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		encoded, _ := json.Marshal(changeCursor{S: last.ChangeSeq, K: last.Kind, ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return out, next, nil
}
