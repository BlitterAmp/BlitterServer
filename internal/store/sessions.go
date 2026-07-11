package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ignoreNoRows collapses sql.ErrNoRows into nil for boolean lookups.
func ignoreNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

const adminSessionTTL = 7 * 24 * time.Hour

// CreateAdminSession mints a raw session token (returned once, stored hashed).
func (s *Store) CreateAdminSession(ctx context.Context) (string, error) {
	raw := newRawToken()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_sessions (token_hash, expires_at) VALUES (?, ?)`,
		HashToken(raw), time.Now().UTC().Add(adminSessionTTL).Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ValidateAdminSession reports whether raw is a live, unexpired session.
func (s *Store) ValidateAdminSession(ctx context.Context, raw string) (bool, error) {
	var expiresAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT expires_at FROM admin_sessions WHERE token_hash = ?`, HashToken(raw)).Scan(&expiresAt)
	if err != nil {
		return false, ignoreNoRows(err)
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false, err
	}
	return time.Now().UTC().Before(exp), nil
}

func (s *Store) DeleteAdminSession(ctx context.Context, raw string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE token_hash = ?`, HashToken(raw))
	return err
}
