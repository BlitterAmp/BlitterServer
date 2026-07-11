// Package artifacts runs the download pipeline: request → dedupe → transcode
// (ffmpeg) → cache with an LRU byte budget → exact-size downloads. Progress
// is published as artifact.updated events.
package artifacts

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
	"github.com/BlitterAmp/BlitterServer/internal/transcode"
)

// Manager owns the worker and the cache directory.
type Manager struct {
	st      *store.Store
	lib     *library.Manager
	bus     *events.Bus
	tc      transcode.Transcoder
	dataDir string

	mu      sync.Mutex
	wake    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	running bool
}

func NewManager(st *store.Store, lib *library.Manager, bus *events.Bus, dataDir string) *Manager {
	return &Manager{
		st: st, lib: lib, bus: bus, tc: transcode.NewFFmpeg(), dataDir: dataDir,
		wake: make(chan struct{}, 1),
	}
}

func (m *Manager) cacheDir() string { return filepath.Join(m.dataDir, "artifacts") }

// Start launches the single transcode worker.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return
	}
	m.running = true
	m.stop = make(chan struct{})
	m.stopped = make(chan struct{})
	go m.work()
	m.kick()
}

func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	close(m.stop)
	stopped := m.stopped
	m.mu.Unlock()
	<-stopped
}

func (m *Manager) kick() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) publish(ctx context.Context, a store.ArtifactRow) {
	if err := m.bus.Publish(ctx, "artifact.updated", "", apiPayload(a)); err != nil {
		logging.From(ctx).Warn("publish artifact.updated", "err", err)
	}
}

// apiPayload mirrors the contract's Artifact schema for event data.
func apiPayload(a store.ArtifactRow) map[string]any {
	out := map[string]any{
		"artifactId": a.ArtifactID,
		"trackId":    a.TrackID,
		"format":     a.Format,
		"status":     a.Status,
	}
	if a.AlbumID != "" {
		out["albumId"] = a.AlbumID
	}
	if a.ArtID != "" {
		out["artId"] = a.ArtID
	}
	if a.BitrateKbps > 0 {
		out["bitrateKbps"] = a.BitrateKbps
	}
	if a.Bytes > 0 {
		out["bytes"] = a.Bytes
	}
	if a.Error != "" {
		out["error"] = a.Error
	}
	return out
}

// Request upserts artifacts for the batch and wakes the worker. original
// artifacts come back ready immediately.
func (m *Manager) Request(ctx context.Context, trackIDs []string, format string, bitrateKbps int) ([]store.ArtifactRow, error) {
	out := make([]store.ArtifactRow, 0, len(trackIDs))
	queuedAny := false
	for _, trackID := range trackIDs {
		a, created, err := m.st.UpsertArtifact(ctx, trackID, format, bitrateKbps)
		if err != nil {
			return nil, err
		}
		if created {
			m.publish(ctx, a)
			if a.Status == "queued" {
				queuedAny = true
			}
		}
		out = append(out, a)
	}
	if queuedAny {
		m.kick()
	}
	return out, nil
}

// Open returns the artifact's bytes and exact size: cached file for
// transcodes, the source stream for originals.
func (m *Manager) Open(ctx context.Context, artifactID string) (io.ReadSeekCloser, int64, error) {
	a, found, err := m.st.GetArtifact(ctx, artifactID)
	if err != nil {
		return nil, 0, err
	}
	if !found {
		return nil, 0, fmt.Errorf("artifact %s: %w", artifactID, store.ErrNotFound)
	}
	if a.Status != "ready" {
		return nil, 0, fmt.Errorf("artifact %s is %s: %w", artifactID, a.Status, store.ErrGone)
	}
	_ = m.st.TouchArtifact(ctx, artifactID)
	if a.Format == "original" {
		rc, found, err := m.lib.Open(ctx, a.TrackID)
		if err != nil || !found {
			return nil, 0, fmt.Errorf("artifact %s source: %w", artifactID, orErr(err))
		}
		return rc, a.Bytes, nil
	}
	f, err := os.Open(a.Path)
	if err != nil {
		return nil, 0, err
	}
	return f, a.Bytes, nil
}

func orErr(err error) error {
	if err != nil {
		return err
	}
	return store.ErrNotFound
}

// EnforceBudget evicts ready transcodes (released first, then coldest) until
// cache usage fits the budget.
func (m *Manager) EnforceBudget(ctx context.Context, budgetBytes int64) error {
	used, err := m.st.ArtifactCacheUsage(ctx)
	if err != nil {
		return err
	}
	if used <= budgetBytes {
		return nil
	}
	victims, err := m.st.ArtifactEvictionCandidates(ctx, used-budgetBytes)
	if err != nil {
		return err
	}
	for _, v := range victims {
		if v.Path != "" {
			if err := os.Remove(v.Path); err != nil && !os.IsNotExist(err) {
				logging.From(ctx).Warn("evict artifact file", "path", v.Path, "err", err)
			}
		}
		if err := m.st.DeleteArtifact(ctx, v.ArtifactID); err != nil {
			return err
		}
		logging.From(ctx).Info("artifact evicted", "artifact_id", v.ArtifactID, "bytes", v.Bytes)
	}
	return nil
}

// budget reads the admin transcode settings' cache budget.
func (m *Manager) budget(ctx context.Context) int64 {
	if v, ok, _ := m.st.GetSetting(ctx, "artifact_cache_max_bytes"); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return int64(10) << 30
}

func (m *Manager) work() {
	defer close(m.stopped)
	ctx := context.Background()
	for {
		a, err := m.st.NextQueuedArtifact(ctx)
		if err != nil {
			logging.From(ctx).Error("artifact queue", "err", err)
		}
		if a == nil {
			select {
			case <-m.wake:
				continue
			case <-m.stop:
				return
			}
		}
		select {
		case <-m.stop:
			return
		default:
		}
		m.process(ctx, *a)
	}
}

func (m *Manager) process(ctx context.Context, a store.ArtifactRow) {
	log := logging.From(ctx).With("component", "artifacts", "artifact_id", a.ArtifactID, "track_id", a.TrackID)
	fail := func(err error) {
		log.Error("transcode failed", "err", err)
		if serr := m.st.MarkArtifactFailed(ctx, a.ArtifactID, err.Error()); serr != nil {
			log.Error("mark failed", "err", serr)
			return
		}
		if row, found, _ := m.st.GetArtifact(ctx, a.ArtifactID); found {
			m.publish(ctx, row)
		}
	}

	if err := m.st.MarkArtifactProcessing(ctx, a.ArtifactID); err != nil {
		log.Error("mark processing", "err", err)
		return
	}
	if row, found, _ := m.st.GetArtifact(ctx, a.ArtifactID); found {
		m.publish(ctx, row)
	}

	src, found, err := m.lib.Open(ctx, a.TrackID)
	if err != nil || !found {
		fail(fmt.Errorf("open source: %w", orErr(err)))
		return
	}
	defer src.Close()

	if err := os.MkdirAll(m.cacheDir(), 0o755); err != nil {
		fail(err)
		return
	}
	dst := filepath.Join(m.cacheDir(), a.ArtifactID+".m4a")
	if err := m.tc.TranscodeAAC(ctx, src, dst, a.BitrateKbps); err != nil {
		fail(err)
		return
	}
	fi, err := os.Stat(dst)
	if err != nil {
		fail(err)
		return
	}
	if err := m.st.MarkArtifactReady(ctx, a.ArtifactID, fi.Size(), dst); err != nil {
		log.Error("mark ready", "err", err)
		return
	}
	if row, found, _ := m.st.GetArtifact(ctx, a.ArtifactID); found {
		m.publish(ctx, row)
	}
	log.Info("artifact ready", "bytes", fi.Size())

	if err := m.EnforceBudget(ctx, m.budget(ctx)); err != nil {
		log.Warn("budget enforcement", "err", err)
	}
}
