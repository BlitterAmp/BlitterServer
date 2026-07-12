// Package library orchestrates the music source: which adapter is active,
// scan lifecycle, and resolving tracks for streaming. Handlers talk to the
// Manager; the Manager talks to the MusicSource port and the store.
package library

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	mu       sync.Mutex
	src      source.MusicSource
	scanning bool
}

// SetBus wires the event bus so completed scans publish library.changed. The
// bus is constructed after the manager, so it's injected rather than passed in.
func (m *Manager) SetBus(bus *events.Bus) { m.bus = bus }

// SetEnricher wires external art enrichment, run after each completed scan.
func (m *Manager) SetEnricher(e Enricher) { m.enricher = e }

// NewManager restores the configured source (if any) from settings.
func NewManager(st *store.Store, dataDir string) *Manager {
	m := &Manager{st: st, dataDir: dataDir}
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

// Configure points the filesystem source at path and queues the initial scan.
func (m *Manager) Configure(ctx context.Context, path string) error {
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
	if m.src == nil {
		return ErrNotConfigured
	}
	if m.scanning {
		return nil
	}
	m.scanning = true
	src := m.src
	go m.scan(src)
	return nil
}

func (m *Manager) scan(src source.MusicSource) {
	ctx := context.Background()
	log := logging.From(ctx).With("component", "library.scan", "source", src.Kind())
	start := time.Now()

	err := m.runScan(ctx, src)
	m.mu.Lock()
	m.scanning = false
	m.mu.Unlock()

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
		if sum, err := m.st.GetLibrarySummary(ctx); err == nil {
			_ = m.bus.Publish(ctx, "library.changed", "", map[string]any{
				"libraryId": "lib_local", "updatedAt": sum.UpdatedAt,
			})
		}
	}

	// Fill any art gaps from external sources in the background; enrichment
	// emits its own library.changed when it attaches new covers/photos.
	if m.enricher != nil {
		go m.enricher.Run(context.Background())
	}
}

func (m *Manager) runScan(ctx context.Context, src source.MusicSource) error {
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
