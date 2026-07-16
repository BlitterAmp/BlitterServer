package store

import (
	"context"

	"github.com/BlitterAmp/BlitterServer/internal/auth"
)

// InitializeAdminPassword stores the first admin password atomically. It
// returns false without changing the hash when setup is already complete.
func (s *Store) InitializeAdminPassword(ctx context.Context, password string) (bool, error) {
	if err := auth.ValidateAdminPassword(password); err != nil {
		return false, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('admin_password_hash', ?)
		ON CONFLICT(key) DO NOTHING`, hash)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}
