package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// MigrationMismatchError means an applied migration differs from the embedded edition.
type MigrationMismatchError struct {
	Version int64
}

func (e *MigrationMismatchError) Error() string {
	return fmt.Sprintf("database was created from a different edition of migration %04d and must be reset", e.Version)
}

func verifyMigrationHashes(ctx context.Context, db *sql.DB) error {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return fmt.Errorf("invalid embedded migration name %q", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return fmt.Errorf("parse embedded migration %q: %w", entry.Name(), err)
		}
		raw, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read embedded migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(raw)
		current := hex.EncodeToString(sum[:])
		var recorded string
		err = db.QueryRowContext(ctx, `SELECT sha256 FROM migration_hashes WHERE version = ?`, version).Scan(&recorded)
		switch {
		case err == nil && recorded != current:
			return &MigrationMismatchError{Version: version}
		case err == nil:
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read migration %04d hash: %w", version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO migration_hashes (version, sha256) VALUES (?, ?)`, version, current); err != nil {
			return fmt.Errorf("record migration %04d hash: %w", version, err)
		}
	}
	return nil
}
