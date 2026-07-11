package artifacts

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// fixture: store + configured library over one real flac + running manager.
func fixture(t *testing.T) (*Manager, *store.Store, string, <-chan events.Event) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; fixture tests skipped")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	music := filepath.Join(t.TempDir(), "m")
	os.MkdirAll(filepath.Join(music, "A", "Al"), 0o755)
	out := filepath.Join(music, "A", "Al", "01 one.flac")
	if b, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-metadata", "title=One", "-metadata", "artist=A", "-metadata", "album=Al", out).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, b)
	}

	lib := library.NewManager(st, dataDir)
	if err := lib.Configure(ctx, music); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := lib.Status(ctx); !s.Scanning && s.LastScanAt != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	bus := events.NewBus(st)
	sub, cancel := bus.Subscribe("", 0) // instance-wide events
	t.Cleanup(cancel)

	mgr := NewManager(st, lib, bus, dataDir)
	mgr.Start()
	t.Cleanup(mgr.Stop)

	tracks, _, err := st.ListTracks(ctx, "title", "", 1)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("scan: %v %d tracks", err, len(tracks))
	}
	return mgr, st, tracks[0].TrackID, sub
}

func awaitStatus(t *testing.T, st *store.Store, artifactID, want string) store.ArtifactRow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		a, found, err := st.GetArtifact(context.Background(), artifactID)
		if err != nil || !found {
			t.Fatalf("artifact vanished: %v", err)
		}
		if a.Status == want {
			return a
		}
		if a.Status == "failed" && want != "failed" {
			t.Fatalf("artifact failed: %s", a.Error)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("artifact never reached %s", want)
	return store.ArtifactRow{}
}

func TestRequestTranscodesAndPublishes(t *testing.T) {
	mgr, st, trackID, sub := fixture(t)
	ctx := context.Background()

	rows, err := mgr.Request(ctx, []string{trackID}, "aac", 128)
	if err != nil || len(rows) != 1 || rows[0].Status == "" {
		t.Fatalf("request: %v %+v", err, rows)
	}
	ready := awaitStatus(t, st, rows[0].ArtifactID, "ready")

	// Exact bytes match the cache file.
	fi, err := os.Stat(ready.Path)
	if err != nil || fi.Size() != ready.Bytes || ready.Bytes == 0 {
		t.Fatalf("bytes: %v %d vs %d", err, fi.Size(), ready.Bytes)
	}

	// artifact.updated events flowed (at least processing + ready).
	seen := map[string]bool{}
	timeout := time.After(5 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-sub:
			if ev.Type == "artifact.updated" {
				seen[ev.Data] = true
			}
		case <-timeout:
			t.Fatalf("artifact.updated events missing: %d", len(seen))
		}
	}

	// Idempotent re-request returns the ready artifact.
	again, err := mgr.Request(ctx, []string{trackID}, "aac", 128)
	if err != nil || again[0].ArtifactID != rows[0].ArtifactID || again[0].Status != "ready" {
		t.Fatalf("re-request: %v %+v", err, again)
	}

	// Open serves the cached file.
	rc, _, err := mgr.Open(ctx, rows[0].ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	head := make([]byte, 8)
	if _, err := io.ReadFull(rc, head); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalArtifactsInstantAndOpenFromSource(t *testing.T) {
	mgr, _, trackID, _ := fixture(t)
	ctx := context.Background()
	rows, err := mgr.Request(ctx, []string{trackID}, "original", 0)
	if err != nil || rows[0].Status != "ready" || rows[0].Bytes == 0 {
		t.Fatalf("original: %v %+v", err, rows)
	}
	rc, size, err := mgr.Open(ctx, rows[0].ArtifactID)
	if err != nil || size != rows[0].Bytes {
		t.Fatalf("open original: %v %d", err, size)
	}
	defer rc.Close()
	head := make([]byte, 4)
	io.ReadFull(rc, head)
	if string(head) != "fLaC" {
		t.Fatalf("original bytes: %q", head)
	}
}

func TestEvictionRespectsBudget(t *testing.T) {
	mgr, st, trackID, _ := fixture(t)
	ctx := context.Background()

	rows, _ := mgr.Request(ctx, []string{trackID}, "aac", 128)
	a := awaitStatus(t, st, rows[0].ArtifactID, "ready")

	// A budget smaller than the artifact forces eviction of released rows.
	if err := st.ReleaseArtifact(ctx, a.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.EnforceBudget(ctx, a.Bytes-1); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.GetArtifact(ctx, a.ArtifactID); found {
		t.Fatal("over-budget released artifact must be evicted")
	}
	if _, err := os.Stat(a.Path); err == nil {
		t.Fatal("evicted cache file must be deleted")
	}
}
