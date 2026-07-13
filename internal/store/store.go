// Package store owns BlitterServer's SQLite persistence: schema, migrations,
// and typed accessors. No SQL leaks out of this package.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db              *sql.DB
	secret          cipher.AEAD
	dataDir         string
	credentials     sync.Mutex
	libraryIdentity sync.Mutex
}

// LockLibraryScan prevents a canonical resolver apply from interleaving with a source scan.
func (s *Store) LockLibraryScan() func() {
	s.libraryIdentity.Lock()
	return s.libraryIdentity.Unlock
}

// Open creates dataDir if needed, opens (or creates) the database, and runs
// pending migrations before returning.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "blitterserver.db")
	_, statErr := os.Stat(dbPath)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, fmt.Errorf("stat sqlite database: %w", statErr)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_txlock=immediate", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		db.Close()
		return nil, err
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := verifyMigrationHashes(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("verify migrations: %w", err)
	}
	key, err := loadOrCreateLocalKey(filepath.Join(dataDir, "server-local.key"))
	if err != nil {
		db.Close()
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		db.Close()
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		db.Close()
		return nil, err
	}
	st := &Store{db: db, secret: aead, dataDir: dataDir}
	if created {
		st.seedIntegrationCredentials(ctx)
	}
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value) VALUES ('library_id', ?)`, NewID("lib")); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize library id: %w", err)
	}
	return st, nil
}

// loadOrCreateLocalKey uses O_EXCL so concurrent server opens cannot overwrite
// one another's key. This adjacent file is a practical self-hosted safeguard
// against database-only disclosure; copying both files defeats that safeguard.
func loadOrCreateLocalKey(path string) ([]byte, error) {
	for {
		key, err := os.ReadFile(path)
		if err == nil {
			if len(key) != 32 {
				return nil, fmt.Errorf("server-local encryption key: invalid length %d", len(key))
			}
			if err := os.Chmod(path, 0o600); err != nil {
				return nil, fmt.Errorf("server-local encryption key permissions: %w", err)
			}
			return key, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("server-local encryption key: %w", err)
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate server-local encryption key: %w", err)
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".server-local.key-*")
		if err != nil {
			return nil, fmt.Errorf("create temporary server-local encryption key: %w", err)
		}
		tmp := f.Name()
		cleanup := func() { _ = f.Close(); _ = os.Remove(tmp) }
		if err = os.Chmod(tmp, 0o600); err == nil {
			_, err = f.Write(key)
		}
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("write server-local encryption key: %w", err)
		}
		// Linking a fully synced temporary file publishes the key atomically and
		// cannot replace a key won by another concurrent opener.
		err = os.Link(tmp, path)
		_ = os.Remove(tmp)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("publish server-local encryption key: %w", err)
		}
		return key, nil
	}
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if isIntegrationCredential(key) {
		s.credentials.Lock()
		defer s.credentials.Unlock()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil || !isIntegrationCredential(key) {
		return err
	}
	return s.writeCredentialSidecar(ctx)
}

// LibraryID returns the stable identity created with this database.
func (s *Store) LibraryID(ctx context.Context) (string, error) {
	id, ok, err := s.GetSetting(ctx, "library_id")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("library id is not initialized")
	}
	return id, nil
}

// SetLastfmCredentials atomically persists the instance-wide provider pair.
func (s *Store) SetLastfmCredentials(ctx context.Context, apiKey, sharedSecret string) error {
	s.credentials.Lock()
	defer s.credentials.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{"lastfm_api_key": apiKey, "lastfm_shared_secret": sharedSecret} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.writeCredentialSidecar(ctx)
}

// SetupComplete reports whether first-run setup has happened: the admin
// password hash is the marker (written by the admin setup endpoint, spec 2).
func (s *Store) SetupComplete(ctx context.Context) (bool, error) {
	_, ok, err := s.GetSetting(ctx, "admin_password_hash")
	return ok, err
}
