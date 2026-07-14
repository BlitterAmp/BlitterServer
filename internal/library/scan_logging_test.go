package library

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/source/filesystem"
)

func captureManagerLogs(m *Manager) *bytes.Buffer {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m.lifecycleCtx = logging.With(m.lifecycleCtx, logger)
	base := time.Unix(1_700_000_000, 0)
	m.now = func() time.Time {
		base = base.Add(16 * time.Second)
		return base
	}
	m.scanProgressInterval = 15 * time.Second
	return &buf
}

func TestFilesystemScanLogsAggregateStartProgressAndCompletion(t *testing.T) {
	first, _ := candidateMeta("private/root/one.flac", "One", 10, 11)
	second, _ := candidateMeta("private/root/two.flac", "Two", 20, 21)
	_, m, fake, _ := incrementalManager(t, first, second)
	rescanAndWait(t, m)
	fake.mu.Lock()
	fake.candidates[1].MtimeNS++
	changed := fake.candidates[1]
	fake.metadata[changed.NativeID] = completeManagerMeta(changed, "Private Title", "")
	fake.mu.Unlock()
	buf := captureManagerLogs(m)
	rescanAndWait(t, m)
	out := buf.String()
	for _, message := range []string{"filesystem scan started", "filesystem scan progress", "filesystem scan completed"} {
		if !strings.Contains(out, "msg=\""+message+"\"") {
			t.Fatalf("missing %q: %s", message, out)
		}
	}
	for _, field := range []string{"parser_version=1", "cached_candidates=2", "discovered=2", "reused=1", "probed=1", "indexed=1", "failed=0", "tracks_changed=1", "tracks_removed=0", "duration_ms="} {
		if !strings.Contains(out, field) {
			t.Fatalf("missing %q: %s", field, out)
		}
	}
	for _, private := range []string{"private/root", "one.flac", "two.flac", "Private Title"} {
		if strings.Contains(out, private) {
			t.Fatalf("scan log leaked %q: %s", private, out)
		}
	}
}

func TestFilesystemScanParseFailureCompletesWithErrorsAndClearsOnCleanScan(t *testing.T) {
	candidate, _ := candidateMeta("secret/native.flac", "Secret", 10, 11)
	_, m, fake, _ := incrementalManager(t, candidate)
	fake.parseFailures[candidate.NativeID] = 1
	buf := captureManagerLogs(m)
	rescanAndWait(t, m)
	out := buf.String()
	if !strings.Contains(out, "failed=1") || strings.Contains(out, candidate.NativeID) {
		t.Fatalf("parse aggregate/privacy log=%s", out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "msg=\"filesystem scan completed with errors\"") {
		t.Fatalf("partial-success terminal log: %s", out)
	}
	if status := m.Status(context.Background()); status.LastScanError != "1 file failed" {
		t.Fatalf("aggregate last scan error=%q", status.LastScanError)
	}
	buf.Reset()
	rescanAndWait(t, m)
	if status := m.Status(context.Background()); status.LastScanError != "" {
		t.Fatalf("clean scan retained aggregate error=%q", status.LastScanError)
	}
	if !strings.Contains(buf.String(), "msg=\"filesystem scan completed\"") {
		t.Fatalf("clean terminal log=%s", buf.String())
	}
}

func TestFilesystemWalkFailureDoesNotLogRoot(t *testing.T) {
	s, dataDir := openStore(t)
	root := t.TempDir()
	src, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := s.ConfigureFilesystemSource(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, dataDir)
	m.mu.Lock()
	m.src, m.sourceID, m.sourceGeneration = src, instance.ID, instance.Generation
	m.mu.Unlock()
	t.Cleanup(m.Close)
	buf := captureManagerLogs(m)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	rescanAndWait(t, m)
	out := buf.String()
	if !strings.Contains(out, "filesystem scan failed") || strings.Contains(out, root) {
		t.Fatalf("walk failure privacy log=%s", out)
	}
}

func TestScanArtStorageFailureDoesNotLogOrPersistPath(t *testing.T) {
	s, dataDir := openStore(t)
	if err := os.WriteFile(filepath.Join(dataDir, "art"), []byte("blocking file"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, _ := candidateMeta("art.flac", "Art", 10, 11)
	instance, _, err := s.ConfigureFilesystemSource(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &candidateSource{parserVersion: 1, candidates: []source.TrackCandidate{candidate}, metadata: map[string]source.TrackMeta{candidate.NativeID: completeManagerMeta(candidate, "Art", "hash")}, parseFailures: map[string]int{}, parseErrors: map[string]error{}}
	m := NewManager(s, dataDir)
	t.Cleanup(m.Close)
	m.mu.Lock()
	m.src, m.sourceID, m.sourceGeneration = fake, instance.ID, instance.Generation
	m.mu.Unlock()
	buf := captureManagerLogs(m)
	rescanAndWait(t, m)
	status := m.Status(context.Background())
	if strings.Contains(buf.String(), dataDir) || strings.Contains(status.LastScanError, dataDir) {
		t.Fatalf("art storage path leaked: log=%s status=%q", buf.String(), status.LastScanError)
	}
}

func TestFilesystemScanFailureAndCancellationHaveTruthfulTerminalLogs(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		candidate, _ := candidateMeta("secret.flac", "Secret", 10, 11)
		_, m, fake, _ := incrementalManager(t, candidate)
		fake.enumerateErr = context.DeadlineExceeded
		buf := captureManagerLogs(m)
		rescanAndWait(t, m)
		out := buf.String()
		if !strings.Contains(out, "msg=\"filesystem scan failed\"") || strings.Contains(out, "msg=\"filesystem scan completed\"") {
			t.Fatalf("failed terminal log=%s", out)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		s, dataDir := openStore(t)
		m := NewManager(s, dataDir)
		src := &blockedSource{started: make(chan struct{}), done: make(chan struct{})}
		m.mu.Lock()
		m.src = src
		m.mu.Unlock()
		buf := captureManagerLogs(m)
		if err := m.Rescan(context.Background()); err != nil {
			t.Fatal(err)
		}
		<-src.started
		m.Close()
		out := buf.String()
		if !strings.Contains(out, "msg=\"filesystem scan cancelled\"") || strings.Contains(out, "msg=\"filesystem scan completed\"") {
			t.Fatalf("cancelled terminal log=%s", out)
		}
	})
}
