package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenMigratesAndCreatesFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, "blitterserver.db")); err != nil {
		t.Fatal("db file missing")
	}
}

func TestOpenRejectsChangedAppliedMigration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE migration_hashes SET sha256 = 'wrong' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(context.Background(), dir)
	var mismatch *MigrationMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want MigrationMismatchError, got %v", err)
	}
	if mismatch.Version != 1 || !strings.Contains(err.Error(), "must be reset") {
		t.Fatalf("mismatch must be actionable: %+v", mismatch)
	}
}

func TestIntegrationCredentialSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastfmCredentials(ctx, "lastfm-visible-marker", "secret-visible-marker"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "fanart_api_key", "fanart-visible-marker"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "integration-credentials.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{"lastfm-visible-marker", "secret-visible-marker", "fanart-visible-marker"} {
		if strings.Contains(string(raw), plaintext) {
			t.Fatalf("sidecar contains plaintext credential %q", plaintext)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "integration-credentials.enc")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar permissions: %v, %v", info, err)
	}
	if err := os.Remove(filepath.Join(dir, "blitterserver.db")); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for key, want := range map[string]string{
		"lastfm_api_key":       "lastfm-visible-marker",
		"lastfm_shared_secret": "secret-visible-marker",
		"fanart_api_key":       "fanart-visible-marker",
	} {
		got, ok, err := s.GetSetting(ctx, key)
		if err != nil || !ok || got != want {
			t.Fatalf("seed %s: got %q, %v, %v", key, got, ok, err)
		}
	}
}

func TestIntegrationCredentialDeleteUpdatesSidecar(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "fanart_api_key", "fanart-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "fanart_api_key", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "blitterserver.db")); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, ok, err := s.GetSetting(ctx, "fanart_api_key"); err != nil || ok || got != "" {
		t.Fatalf("deleted credential restored: %q, %v, %v", got, ok, err)
	}
}

func TestCorruptIntegrationCredentialSidecarIsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integration-credentials.enc"), []byte("not ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("corrupt sidecar must not fail startup: %v", err)
	}
	defer s.Close()
	if _, ok, err := s.GetSetting(context.Background(), "lastfm_api_key"); err != nil || ok {
		t.Fatalf("corrupt sidecar seeded data: %v, %v", ok, err)
	}
}

func TestSetLastfmCredentialsRollsBackWhenSecondWriteFails(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SetLastfmCredentials(ctx, "old-key", "old-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_lastfm_secret BEFORE UPDATE ON settings
		WHEN NEW.key = 'lastfm_shared_secret' BEGIN SELECT RAISE(FAIL, 'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastfmCredentials(ctx, "new-key", "new-secret"); err == nil {
		t.Fatal("credential transaction unexpectedly succeeded")
	}
	key, _, _ := s.GetSetting(ctx, "lastfm_api_key")
	secret, _, _ := s.GetSetting(ctx, "lastfm_shared_secret")
	if key != "old-key" || secret != "old-secret" {
		t.Fatalf("partial credential update: key=%q secret=%q", key, secret)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, ok, _ := s.GetSetting(ctx, "canonical_url"); ok {
		t.Fatal("unset key must report absent")
	}
	if err := s.SetSetting(ctx, "canonical_url", "https://music.example.net"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.GetSetting(ctx, "canonical_url")
	if err != nil || !ok || v != "https://music.example.net" {
		t.Fatalf("round trip: %v %v %q", err, ok, v)
	}
	if err := s.SetSetting(ctx, "canonical_url", "https://b"); err != nil {
		t.Fatal("upsert must overwrite")
	}
}

func TestSetupCompleteFollowsAdminHash(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if done, _ := s.SetupComplete(ctx); done {
		t.Fatal("fresh store must not be setupComplete")
	}
	s.SetSetting(ctx, "admin_password_hash", "argon2id$fake")
	if done, _ := s.SetupComplete(ctx); !done {
		t.Fatal("admin hash present must mean setupComplete")
	}
}

func TestNewIDShape(t *testing.T) {
	id := NewID("dev")
	if !strings.HasPrefix(id, "dev_") || len(id) != 4+8 {
		t.Fatalf("bad id %q", id)
	}
	if NewID("dev") == id {
		t.Fatal("ids must be random")
	}
}
