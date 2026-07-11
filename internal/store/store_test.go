package store

import (
	"context"
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
