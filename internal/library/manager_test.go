package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func openStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	s, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dataDir
}

func musicDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Manager tests don't need real audio — an empty dir scans to an empty
	// library; adapter parsing is covered in the filesystem package.
	return dir
}

func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := m.Status(context.Background()); !st.Scanning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan never finished")
}

func TestConfigureValidatesAndScans(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)

	if st := m.Status(context.Background()); st.Configured {
		t.Fatal("fresh manager must be unconfigured")
	}
	if err := m.Rescan(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("rescan unconfigured: want ErrNotConfigured, got %v", err)
	}

	if err := m.Configure(context.Background(), filepath.Join(dataDir, "missing")); err == nil {
		t.Fatal("missing path must be rejected")
	}
	f := filepath.Join(dataDir, "file")
	os.WriteFile(f, []byte("x"), 0o644)
	if err := m.Configure(context.Background(), f); err == nil {
		t.Fatal("non-dir path must be rejected")
	}

	dir := musicDir(t)
	if err := m.Configure(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)
	st := m.Status(context.Background())
	if !st.Configured || st.Path != dir || st.LastScanAt == nil || st.LastScanError != "" {
		t.Fatalf("post-configure status: %+v", st)
	}
	if m.SourceKind(context.Background()) != "filesystem" {
		t.Fatalf("source kind: %q", m.SourceKind(context.Background()))
	}
}

func TestManagerRestoresFromSettings(t *testing.T) {
	s, dataDir := openStore(t)
	dir := musicDir(t)
	m := NewManager(s, dataDir)
	if err := m.Configure(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)

	// A new manager over the same store must come up configured.
	m2 := NewManager(s, dataDir)
	st := m2.Status(context.Background())
	if !st.Configured || st.Path != dir {
		t.Fatalf("restore: %+v", st)
	}
	if err := m2.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m2)
}

func TestUnlink(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	if err := m.Configure(context.Background(), musicDir(t)); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)
	if err := m.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(context.Background()); st.Configured {
		t.Fatalf("unlink must clear config: %+v", st)
	}
	if err := m.Rescan(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("rescan after unlink must be ErrNotConfigured")
	}
}
