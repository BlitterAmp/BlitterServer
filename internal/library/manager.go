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

	// Test seams for deterministic lifecycle concurrency coverage.
	onScanStart      func()
	onOpenResolved   func()
	onConfigureReady func()

	mu                   sync.Mutex
	sourceConfig         sync.Mutex
	src                  source.MusicSource
	sourceID             string
	sourceGeneration     int64
	scanning             bool
	scanPending          bool
	enriching            bool
	enrichmentPending    bool
	resetArtists         bool
	resetAlbums          bool
	closed               bool
	lifecycleCtx         context.Context
	cancelLifecycle      context.CancelFunc
	enrichmentWG         sync.WaitGroup
	schedulerWG          sync.WaitGroup
	scanWG               sync.WaitGroup
	resetArtRetries      func(context.Context, bool) error
	waitResetRetry       func(context.Context, int) bool
	waitEnrichment       func(context.Context) bool
	schedulerStarted     bool
	operation            chan struct{}
	upsertTrack          func(context.Context, string, source.TrackMeta, string, int64) error
	now                  func() time.Time
	scanProgressInterval time.Duration
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
		waitResetRetry:       waitForResetRetry,
		waitEnrichment:       waitForEnrichment,
		operation:            make(chan struct{}, 1),
		upsertTrack:          st.UpsertTrack,
		now:                  time.Now,
		scanProgressInterval: 15 * time.Second,
	}
	m.operation <- struct{}{}
	ctx := context.Background()
	if kind, _, _ := st.GetSetting(ctx, settingSourceKind); kind == "filesystem" {
		if path, ok, _ := st.GetSetting(ctx, settingFilesystemPath); ok && path != "" {
			if src, err := filesystem.New(path); err == nil {
				if instance, _, err := st.ConfigureFilesystemSource(ctx, path); err == nil {
					m.src = src
					m.sourceID = instance.ID
					m.sourceGeneration = instance.Generation
				}
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
	m.sourceConfig.Lock()
	defer m.sourceConfig.Unlock()
	if m.onConfigureReady != nil {
		m.onConfigureReady()
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	instance, _, err := m.st.ConfigureFilesystemSource(ctx, path)
	if err != nil {
		return err
	}
	if instance.Replaced {
		log := logging.From(ctx).With("component", "library.configure", "source", "filesystem")
		m.publishLibraryChangedLocked(ctx, log)
	}
	m.src = src
	m.sourceID = instance.ID
	m.sourceGeneration = instance.Generation
	if m.scanning {
		m.scanPending = true
		return nil
	}
	m.startScanLocked()
	return nil
}

// Unlink removes the source and marks its canonical library rows missing.
func (m *Manager) Unlink(ctx context.Context) error {
	m.sourceConfig.Lock()
	defer m.sourceConfig.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	previousSource := m.src
	previousID := m.sourceID
	m.sourceGeneration++
	fencedGeneration := m.sourceGeneration
	m.src = nil
	m.sourceID = ""
	m.scanPending = false
	m.mu.Unlock()

	generation, err := m.st.UnlinkFilesystemSource(ctx)
	if err != nil {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return err
		}
		m.src = previousSource
		m.sourceID = previousID
		m.sourceGeneration = fencedGeneration
		if previousSource != nil {
			if m.scanning {
				m.scanPending = true
			} else {
				m.startScanLocked()
			}
		}
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	m.sourceGeneration = generation
	log := logging.From(ctx).With("component", "library.unlink", "source", "filesystem")
	m.publishLibraryChangedLocked(ctx, log)
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
	m.startScanLocked()
	return nil
}

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
	m.mu.Lock()
	defer m.mu.Unlock()
	kind, nativeID, found, err := m.st.ResolveTrackNative(ctx, trackID)
	if err != nil || !found {
		return nil, found, err
	}
	if m.onOpenResolved != nil {
		m.onOpenResolved()
	}
	src := m.src
	if src == nil || src.Kind() != kind {
		return nil, false, ErrNotConfigured
	}
	f, err := src.Open(ctx, nativeID)
	if err != nil {
		return nil, false, err
	}
	return f, true, nil
}
