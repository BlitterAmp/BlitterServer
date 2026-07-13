package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/store"
	_ "modernc.org/sqlite"
)

func corruptMigrationHash(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "blitterserver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE migration_hashes SET sha256 = 'wrong' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStoreResetPreservesMismatchedDatabase(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	oldID, err := s.LibraryID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "fanart_api_key", "survives-reset"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	corruptMigrationHash(t, dir)

	s, err = openStore(ctx, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	newID, err := s.LibraryID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newID == oldID {
		t.Fatal("reset did not create a fresh database")
	}
	if key, ok, err := s.GetSetting(ctx, "fanart_api_key"); err != nil || !ok || key != "survives-reset" {
		t.Fatalf("reset did not seed integration credentials: %q, %v, %v", key, ok, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "blitterserver.db.corrupt-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("preserved databases: %v, %v", matches, err)
	}
	if info, err := os.Stat(matches[0]); err != nil || info.Size() == 0 {
		t.Fatalf("preserved database invalid: %v", err)
	}
}

func TestOpenStoreDoesNotResetByDefault(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	corruptMigrationHash(t, dir)
	if _, err := openStore(ctx, dir, false); err == nil {
		t.Fatal("schema mismatch unexpectedly reset without opt-in")
	}
	if _, err := os.Stat(filepath.Join(dir, "blitterserver.db")); err != nil {
		t.Fatalf("original database not preserved in place: %v", err)
	}
}
