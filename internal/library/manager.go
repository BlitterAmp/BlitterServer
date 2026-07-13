// Package library orchestrates the music source: which adapter is active,
// scan lifecycle, and resolving tracks for streaming. Handlers talk to the
// Manager; the Manager talks to the MusicSource port and the store.
package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/source/filesystem"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// ErrNotConfigured means no source is linked yet.
var ErrNotConfigured = errors.New("no source configured")

// ErrClosed means the manager has begun shutdown and accepts no new work.
var ErrClosed = errors.New("library manager closed")

const (
	settingSourceKind     = "source_kind"
	settingFilesystemPath = "filesystem_path"
	settingLastScanAt     = "library_last_scan_at"
	settingLastScanError  = "library_last_scan_error"
)

// Status is the admin view of the source.
type Status struct {
	Configured    bool
	Path          string
	Scanning      bool
	LastScanAt    *time.Time
	LastScanError string
}

// Enricher fills art gaps from external sources; injected to avoid a cycle.
type Enricher interface {
	Run(ctx context.Context)
}

type Manager struct {
	st       *store.Store
	dataDir  string
	bus      *events.Bus
	enricher Enricher

	mu                sync.Mutex
	src               source.MusicSource
	scanning          bool
	enriching         bool
	enrichmentPending bool
	resetArtists      bool
	resetAlbums       bool
	closed            bool
	lifecycleCtx      context.Context
	cancelLifecycle   context.CancelFunc
	enrichmentWG      sync.WaitGroup
	schedulerWG       sync.WaitGroup
	scanWG            sync.WaitGroup
	resetArtRetries   func(context.Context, bool) error
	waitResetRetry    func(context.Context, int) bool
	waitEnrichment    func(context.Context) bool
	schedulerStarted  bool
	operation         chan struct{}
}

// SetBus wires the event bus so completed scans publish library.changed. The
// bus is constructed after the manager, so it's injected rather than passed in.
func (m *Manager) SetBus(bus *events.Bus) { m.bus = bus }

// SetEnricher wires external art enrichment and starts its lifecycle scheduler.
func (m *Manager) SetEnricher(e Enricher) {
	m.mu.Lock()
	m.enricher = e
	start := e != nil && !m.closed && !m.schedulerStarted
	if start {
		m.schedulerStarted = true
		m.schedulerWG.Add(1)
	}
	m.mu.Unlock()
	if start {
		m.mu.Lock()
		configured := m.src != nil
		m.mu.Unlock()
		if configured {
			_ = m.Rescan(m.lifecycleCtx)
		} else {
			m.TriggerEnrichment()
		}
		go m.scheduleEnrichment()
	}
}

func (m *Manager) scheduleEnrichment() {
	defer m.schedulerWG.Done()
	for m.waitEnrichment(m.lifecycleCtx) {
		m.TriggerEnrichment()
	}
}

// TriggerEnrichment queues an asynchronous enrichment pass. Triggers received
// during a pass are coalesced into one follow-up pass.
func (m *Manager) TriggerEnrichment() {
	m.triggerEnrichment(false, false)
}

// TriggerArtistEnrichment resets artist retry state immediately before the
// next pass, after any stale in-flight pass has finished.
func (m *Manager) TriggerArtistEnrichment() {
	m.triggerEnrichment(true, false)
}

// TriggerAlbumEnrichment resets album retry state immediately before the next
// pass, after any stale in-flight pass has finished.
func (m *Manager) TriggerAlbumEnrichment() {
	m.triggerEnrichment(false, true)
}

func (m *Manager) triggerEnrichment(resetArtists, resetAlbums bool) {
	m.mu.Lock()
	if m.closed || m.enricher == nil {
		m.mu.Unlock()
		return
	}
	m.enrichmentPending = true
	m.resetArtists = m.resetArtists || resetArtists
	m.resetAlbums = m.resetAlbums || resetAlbums
	if m.enriching {
		m.mu.Unlock()
		return
	}
	m.enriching = true
	m.enrichmentWG.Add(1)
	m.mu.Unlock()
	go m.runEnrichment()
}

func (m *Manager) runEnrichment() {
	defer m.enrichmentWG.Done()
	retryAttempt := 0
	for {
		m.mu.Lock()
		if m.closed {
			m.enriching = false
			m.mu.Unlock()
			return
		}
		m.enrichmentPending = false
		resetArtists, resetAlbums := m.resetArtists, m.resetAlbums
		enricher := m.enricher
		m.resetArtists, m.resetAlbums = false, false
		if enricher == nil {
			m.enriching = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		resetFailed := false
		if resetArtists {
			if err := m.resetArtRetries(m.lifecycleCtx, true); err != nil {
				logging.From(m.lifecycleCtx).Warn("reset artist art retries", "err", err)
				resetFailed = true
				m.mu.Lock()
				m.resetArtists = true
				m.mu.Unlock()
			}
		}
		if resetAlbums {
			if err := m.resetArtRetries(m.lifecycleCtx, false); err != nil {
				logging.From(m.lifecycleCtx).Warn("reset album art retries", "err", err)
				resetFailed = true
				m.mu.Lock()
				m.resetAlbums = true
				m.mu.Unlock()
			}
		}
		if !resetFailed {
			m.mu.Lock()
			moreResets := m.resetArtists || m.resetAlbums
			m.mu.Unlock()
			if moreResets {
				continue
			}
		}
		if !resetFailed && m.lifecycleCtx.Err() == nil {
			retryAttempt = 0
			enricher.Run(m.lifecycleCtx)
		}

		m.mu.Lock()
		if m.closed {
			m.enriching = false
			m.mu.Unlock()
			return
		}
		if resetFailed {
			retryAttempt++
			m.mu.Unlock()
			if !m.waitResetRetry(m.lifecycleCtx, retryAttempt) {
				m.mu.Lock()
				m.enriching = false
				m.mu.Unlock()
				return
			}
			continue
		}
		if !m.enrichmentPending && !m.resetArtists && !m.resetAlbums {
			m.enriching = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

// Close cancels and joins enrichment work. It is safe to call more than once.
func (m *Manager) Close() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancelLifecycle()
	}
	m.mu.Unlock()
	m.scanWG.Wait()
	m.enrichmentWG.Wait()
	m.schedulerWG.Wait()
}

// NewManager restores the configured source (if any) from settings.
func NewManager(st *store.Store, dataDir string) *Manager {
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	m := &Manager{
		st: st, dataDir: dataDir, lifecycleCtx: lifecycleCtx,
		cancelLifecycle: cancelLifecycle, resetArtRetries: st.ResetArtRetries,
		waitResetRetry: waitForResetRetry,
		waitEnrichment: waitForEnrichment,
		operation:      make(chan struct{}, 1),
	}
	m.operation <- struct{}{}
	ctx := context.Background()
	if kind, _, _ := st.GetSetting(ctx, settingSourceKind); kind == "filesystem" {
		if path, ok, _ := st.GetSetting(ctx, settingFilesystemPath); ok && path != "" {
			if src, err := filesystem.New(path); err == nil {
				m.src = src
			}
		}
	}
	return m
}

func waitForEnrichment(ctx context.Context) bool {
	// Frequent staggered passes let newly overdue rows flow through while the
	// store remains the authority on the 24-hour per-entity eligibility window.
	delay := 4*time.Hour + time.Duration(rand.Int64N(int64(4*time.Hour)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Configure points the filesystem source at path and queues the initial scan.
func (m *Manager) Configure(ctx context.Context, path string) error {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("source path: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("source path %s is not a directory", path)
	}
	src, err := filesystem.New(path)
	if err != nil {
		return err
	}
	if err := m.st.SetSetting(ctx, settingSourceKind, "filesystem"); err != nil {
		return err
	}
	if err := m.st.SetSetting(ctx, settingFilesystemPath, path); err != nil {
		return err
	}
	m.mu.Lock()
	m.src = src
	m.mu.Unlock()
	return m.Rescan(ctx)
}

// Unlink removes the source; library rows keep their missing flags from the
// last scan (never deleted).
func (m *Manager) Unlink(ctx context.Context) error {
	if err := m.st.SetSetting(ctx, settingSourceKind, ""); err != nil {
		return err
	}
	if err := m.st.SetSetting(ctx, settingFilesystemPath, ""); err != nil {
		return err
	}
	m.mu.Lock()
	m.src = nil
	m.mu.Unlock()
	return nil
}

// Rescan queues a background scan; a second request while one runs is a
// no-op (the running scan already covers it).
func (m *Manager) Rescan(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.src == nil {
		return ErrNotConfigured
	}
	if m.scanning {
		return nil
	}
	m.scanning = true
	src := m.src
	m.scanWG.Add(1)
	go m.scan(src)
	return nil
}

func (m *Manager) scan(src source.MusicSource) {
	defer m.scanWG.Done()
	ctx := m.lifecycleCtx
	log := logging.From(ctx).With("component", "library.scan", "source", src.Kind())
	start := time.Now()

	if !m.acquireOperation() {
		m.mu.Lock()
		m.scanning = false
		m.mu.Unlock()
		return
	}
	err := m.runScan(ctx, src)
	m.releaseOperation()
	m.mu.Lock()
	m.scanning = false
	m.mu.Unlock()
	if ctx.Err() != nil {
		return
	}

	_ = m.st.SetSetting(ctx, settingLastScanAt, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		log.Error("scan failed", "err", err, "duration_ms", time.Since(start).Milliseconds())
		_ = m.st.SetSetting(ctx, settingLastScanError, err.Error())
		return
	}
	_ = m.st.SetSetting(ctx, settingLastScanError, "")
	log.Info("scan complete", "duration_ms", time.Since(start).Milliseconds())

	// Nudge connected clients to pull the catalog delta (GET /v1/changes).
	if m.bus != nil {
		if sum, err := m.st.GetLibrarySummary(ctx); err != nil {
			log.Error("read library summary for scan event", "err", err)
		} else if libraryID, err := m.st.LibraryID(ctx); err != nil {
			log.Error("read library id for scan event", "err", err)
		} else {
			if err := m.bus.Publish(ctx, "library.changed", "", map[string]any{
				"libraryId": libraryID, "updatedAt": sum.UpdatedAt,
			}); err != nil {
				log.Error("publish scan library change", "err", err)
			}
		}
	}

	// Fill any art gaps from external sources in the background; enrichment
	// emits its own library.changed when it attaches new covers/photos.
	m.TriggerEnrichment()
}

func (m *Manager) acquireOperation() bool {
	select {
	case <-m.lifecycleCtx.Done():
		return false
	case <-m.operation:
		if m.lifecycleCtx.Err() != nil {
			m.releaseOperation()
			return false
		}
		return true
	}
}

func (m *Manager) releaseOperation() { m.operation <- struct{}{} }

func waitForResetRetry(ctx context.Context, attempt int) bool {
	if attempt > 8 {
		attempt = 8
	}
	delay := 100 * time.Millisecond * time.Duration(1<<(attempt-1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *Manager) runScan(ctx context.Context, src source.MusicSource) error {
	unlock := m.st.LockLibraryScan()
	defer unlock()
	seq, err := m.st.NextScanSeq(ctx)
	if err != nil {
		return err
	}
	artDir := filepath.Join(m.dataDir, "art")
	artIDs := map[string]string{} // art hash → art id, per scan

	err = src.Scan(ctx, func(meta source.TrackMeta) error {
		artID := ""
		if meta.ArtHash != "" {
			if id, ok := artIDs[meta.ArtHash]; ok {
				artID = id
			} else if data, mime, err := src.Art(ctx, meta.NativeID); err == nil {
				id, err := m.st.UpsertArt(ctx, meta.ArtHash, mime, data, artDir)
				if err != nil {
					return err
				}
				artIDs[meta.ArtHash] = id
				artID = id
			}
		}
		return m.st.UpsertTrack(ctx, src.Kind(), meta, artID, seq)
	})
	if err != nil {
		return err
	}
	return m.st.FinishScan(ctx, src.Kind(), seq)
}

// Status reports the admin view.
func (m *Manager) Status(ctx context.Context) Status {
	m.mu.Lock()
	scanning := m.scanning
	src := m.src
	m.mu.Unlock()

	var out Status
	out.Scanning = scanning
	out.Configured = src != nil
	if path, ok, _ := m.st.GetSetting(ctx, settingFilesystemPath); ok && path != "" && out.Configured {
		out.Path = path
	}
	if v, ok, _ := m.st.GetSetting(ctx, settingLastScanAt); ok && v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			out.LastScanAt = &t
		}
	}
	if v, _, _ := m.st.GetSetting(ctx, settingLastScanError); v != "" {
		out.LastScanError = v
	}
	return out
}

// SourceKind returns filesystem|plex|"" for status surfaces.
func (m *Manager) SourceKind(ctx context.Context) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.src == nil {
		return ""
	}
	return m.src.Kind()
}

// Connected reports whether the source is reachable right now (for the
// filesystem adapter: the directory still exists).
func (m *Manager) Connected(ctx context.Context) bool {
	m.mu.Lock()
	src := m.src
	m.mu.Unlock()
	if src == nil {
		return false
	}
	if path, ok, _ := m.st.GetSetting(ctx, settingFilesystemPath); ok && path != "" {
		st, err := os.Stat(path)
		return err == nil && st.IsDir()
	}
	return false
}

// Open resolves a canonical track id and opens its audio.
func (m *Manager) Open(ctx context.Context, trackID string) (rc io.ReadSeekCloser, found bool, err error) {
	kind, nativeID, found, err := m.st.ResolveTrackNative(ctx, trackID)
	if err != nil || !found {
		return nil, found, err
	}
	m.mu.Lock()
	src := m.src
	m.mu.Unlock()
	if src == nil || src.Kind() != kind {
		return nil, false, ErrNotConfigured
	}
	f, err := src.Open(ctx, nativeID)
	if err != nil {
		return nil, false, err
	}
	return f, true, nil
}
