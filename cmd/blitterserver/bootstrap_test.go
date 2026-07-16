package main

import (
	"context"
	"strings"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func bootstrapTestStore(t *testing.T) (*store.Store, *library.Manager) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := library.NewManager(st, dir)
	t.Cleanup(func() {
		mgr.Close()
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st, mgr
}

func bootstrapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestBootstrapFromEnvironmentInitializesFreshState(t *testing.T) {
	st, mgr := bootstrapTestStore(t)
	musicDir := t.TempDir()
	password := "correct horse battery staple"

	err := bootstrapFromEnvironment(context.Background(), st, mgr, bootstrapEnv(map[string]string{
		"BLITTER_ADMIN_PASSWORD": password,
		"BLITTER_MUSIC_DIR":      musicDir,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := st.SetupComplete(context.Background()); err != nil || !done {
		t.Fatalf("admin setup complete = %v, %v", done, err)
	}
	hash, ok, err := st.GetSetting(context.Background(), "admin_password_hash")
	if err != nil || !ok {
		t.Fatalf("admin hash missing: %v", err)
	}
	if matches, err := auth.VerifyPassword(password, hash); err != nil || !matches {
		t.Fatalf("bootstrap password does not match: %v", err)
	}
	if status := mgr.Status(context.Background()); !status.Configured || status.Path != musicDir {
		t.Fatalf("filesystem source = %+v", status)
	}
}

func TestBootstrapFromEnvironmentNeverOverwritesExistingState(t *testing.T) {
	st, mgr := bootstrapTestStore(t)
	ctx := context.Background()
	originalPassword := "the original admin password"
	initialized, err := st.InitializeAdminPassword(ctx, originalPassword)
	if err != nil || !initialized {
		t.Fatalf("initialize admin: %v, %v", initialized, err)
	}
	originalMusicDir := t.TempDir()
	if err := mgr.Configure(ctx, originalMusicDir); err != nil {
		t.Fatal(err)
	}

	err = bootstrapFromEnvironment(ctx, st, mgr, bootstrapEnv(map[string]string{
		"BLITTER_ADMIN_PASSWORD": "short",
		"BLITTER_MUSIC_DIR":      "/path/that/does/not/exist",
	}))
	if err != nil {
		t.Fatalf("existing state must ignore bootstrap values: %v", err)
	}
	hash, _, err := st.GetSetting(ctx, "admin_password_hash")
	if err != nil {
		t.Fatal(err)
	}
	if matches, err := auth.VerifyPassword(originalPassword, hash); err != nil || !matches {
		t.Fatalf("existing admin password was overwritten: %v", err)
	}
	if status := mgr.Status(ctx); !status.Configured || status.Path != originalMusicDir {
		t.Fatalf("existing source was overwritten: %+v", status)
	}
}

func TestBootstrapFromEnvironmentRejectsInvalidFreshPasswordWithoutLeakingIt(t *testing.T) {
	st, mgr := bootstrapTestStore(t)
	password := "short"
	err := bootstrapFromEnvironment(context.Background(), st, mgr, bootstrapEnv(map[string]string{
		"BLITTER_ADMIN_PASSWORD": password,
	}))
	if err == nil || !strings.Contains(err.Error(), "BLITTER_ADMIN_PASSWORD") {
		t.Fatalf("expected named bootstrap error, got %v", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("bootstrap error leaked password: %v", err)
	}
	if done, checkErr := st.SetupComplete(context.Background()); checkErr != nil || done {
		t.Fatalf("invalid password changed setup state: %v, %v", done, checkErr)
	}
}

func TestBootstrapFromEnvironmentRejectsInvalidFreshMusicDirectory(t *testing.T) {
	st, mgr := bootstrapTestStore(t)
	err := bootstrapFromEnvironment(context.Background(), st, mgr, bootstrapEnv(map[string]string{
		"BLITTER_MUSIC_DIR": "/path/that/does/not/exist",
	}))
	if err == nil || !strings.Contains(err.Error(), "BLITTER_MUSIC_DIR") {
		t.Fatalf("expected named bootstrap error, got %v", err)
	}
	if status := mgr.Status(context.Background()); status.Configured {
		t.Fatalf("invalid directory configured a source: %+v", status)
	}
}

func TestBootstrapFromEnvironmentPreservesInteractiveSetupWithoutVariables(t *testing.T) {
	st, mgr := bootstrapTestStore(t)
	ctx := context.Background()

	if err := bootstrapFromEnvironment(ctx, st, mgr, bootstrapEnv(nil)); err != nil {
		t.Fatal(err)
	}
	if done, err := st.SetupComplete(ctx); err != nil || done {
		t.Fatalf("setup complete = %v, %v", done, err)
	}
	if status := mgr.Status(ctx); status.Configured {
		t.Fatalf("unexpected filesystem source: %+v", status)
	}
}
