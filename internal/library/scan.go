package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/activity"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/progress"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

var errSourceGenerationChanged = errors.New("source generation changed")

type scanJob struct {
	src           source.MusicSource
	sourceID      string
	generation    int64
	parserVersion int
}

type scanStats = activity.Counts

func (m *Manager) startScanLocked() {
	m.scanning = true
	job := scanJob{
		src: m.src, sourceID: m.sourceID, generation: m.sourceGeneration,
		parserVersion: m.src.ParserVersion(),
	}
	m.scanWG.Add(1)
	go m.scan(job)
}

func (m *Manager) scan(job scanJob) {
	defer m.scanWG.Done()
	if m.onScanStart != nil {
		m.onScanStart()
	}
	ctx := m.lifecycleCtx
	start := m.now()
	if !m.acquireOperation() {
		m.finishScanJob(ctx, job, activity.Token{}, scanStats{}, false, context.Canceled, start, false)
		return
	}
	token := m.activity.Start(activity.StageFilesystemScan, scanStats{})
	stats, mutated, err := m.runScan(ctx, job, token)
	m.finishScanJob(ctx, job, token, stats, mutated, err, start, true)
}

func (m *Manager) finishScanJob(ctx context.Context, job scanJob, token activity.Token, stats scanStats, mutated bool, scanErr error, start time.Time, operationHeld bool) {
	log := logging.From(ctx).With("component", "library.scan", "source", job.src.Kind())
	releaseOperation := func() {
		if operationHeld {
			m.releaseOperation()
			operationHeld = false
		}
	}
	m.mu.Lock()
	if !m.jobCurrentLocked(job) {
		if mutated {
			m.publishLibraryChangedLocked(context.WithoutCancel(ctx), log)
		}
		log.Info("filesystem scan cancelled", scanLogAttrs(stats, m.now().Sub(start).Milliseconds())...)
		m.startPendingScanLocked()
		m.mu.Unlock()
		releaseOperation()
		m.activity.Finish(token)
		return
	}
	if ctx.Err() != nil || m.closed || errors.Is(scanErr, context.Canceled) {
		log.Info("filesystem scan cancelled", scanLogAttrs(stats, m.now().Sub(start).Milliseconds())...)
		m.scanning = false
		m.mu.Unlock()
		releaseOperation()
		m.activity.Finish(token)
		return
	}
	_ = m.st.SetSetting(ctx, settingLastScanAt, time.Now().UTC().Format(time.RFC3339))
	if scanErr != nil {
		log.Error("filesystem scan failed", append(scanLogAttrs(stats, m.now().Sub(start).Milliseconds()), "err", scanErr)...)
		m.activity.Fail(token, stats)
		_ = m.st.SetSetting(ctx, settingLastScanError, scanErr.Error())
		if mutated {
			m.publishLibraryChangedLocked(ctx, log)
		}
		m.scanning = false
		m.mu.Unlock()
		releaseOperation()
		m.TriggerEnrichment()
		return
	}
	if stats.Failed > 0 {
		noun := "files"
		if stats.Failed == 1 {
			noun = "file"
		}
		aggregateError := fmt.Sprintf("%d %s failed", stats.Failed, noun)
		_ = m.st.SetSetting(ctx, settingLastScanError, aggregateError)
		log.Warn("filesystem scan completed with errors", append(scanLogAttrs(stats, m.now().Sub(start).Milliseconds()), "tracks_changed", stats.Changed, "tracks_removed", stats.Removed)...)
		m.activity.Fail(token, stats)
		m.publishLibraryChangedLocked(ctx, log)
		m.scanning = false
		m.mu.Unlock()
		releaseOperation()
		m.TriggerEnrichment()
		return
	}
	_ = m.st.SetSetting(ctx, settingLastScanError, "")
	log.Info("filesystem scan completed", append(scanLogAttrs(stats, m.now().Sub(start).Milliseconds()), "tracks_changed", stats.Changed, "tracks_removed", stats.Removed)...)
	m.publishLibraryChangedLocked(ctx, log)
	m.scanning = false
	if m.onScanBookkeeping != nil {
		m.onScanBookkeeping()
	}
	m.mu.Unlock()
	releaseOperation()
	m.TriggerEnrichment()
	m.activity.Finish(token)
}

func scanLogAttrs(stats scanStats, durationMS int64) []any {
	return []any{"discovered", stats.Discovered, "reused", stats.Reused, "probed", stats.Probed,
		"indexed", stats.Indexed, "failed", stats.Failed, "duration_ms", durationMS}
}

func (m *Manager) publishLibraryChangedLocked(ctx context.Context, log *slog.Logger) {
	if m.bus != nil {
		if sum, err := m.st.GetLibrarySummary(ctx); err != nil {
			log.Error("read library summary for scan event", "err", err)
		} else if libraryID, err := m.st.LibraryID(ctx); err != nil {
			log.Error("read library id for scan event", "err", err)
		} else if err := m.bus.Publish(ctx, "library.changed", "", map[string]any{
			"libraryId": libraryID, "updatedAt": sum.UpdatedAt,
		}); err != nil {
			log.Error("publish scan library change", "err", err)
		}
	}
}

func (m *Manager) startPendingScanLocked() {
	if m.scanPending && m.src != nil && !m.closed {
		m.scanPending = false
		m.startScanLocked()
		return
	}
	m.scanning = false
}

func (m *Manager) jobCurrentLocked(job scanJob) bool {
	return m.src == job.src && m.sourceID == job.sourceID && m.sourceGeneration == job.generation
}

func (m *Manager) withCurrentSource(job scanJob, fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.jobCurrentLocked(job) || m.closed {
		return errSourceGenerationChanged
	}
	return fn()
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

func (m *Manager) runScan(ctx context.Context, job scanJob, token activity.Token) (stats scanStats, mutated bool, resultErr error) {
	unlock := m.st.LockLibraryScan()
	defer unlock()
	seq, err := m.st.NextScanSeq(ctx)
	if err != nil {
		return stats, false, err
	}
	cache, err := m.st.LoadSourceFileCache(ctx, job.sourceID, job.src.Kind(), job.parserVersion)
	if err != nil {
		return stats, false, err
	}
	log := logging.From(ctx).With("component", "library.scan", "source", job.src.Kind())
	reporter := progress.New(m.now, m.scanProgressInterval)
	log.Info("filesystem scan started", "parser_version", job.parserVersion, "cached_candidates", len(cache))
	artDir := filepath.Join(m.dataDir, "art")
	artIDs := make(map[string]string)
	encountered := make(map[string]struct{})
	seen := make(map[string]struct{})

	err = job.src.Enumerate(ctx, func(candidate source.TrackCandidate) error {
		stats.Discovered++
		defer func() {
			if reporter.Due(stats.Discovered) {
				durationMS := reporter.DurationMS()
				m.activity.Update(token, stats)
				log.Info("filesystem scan progress", append(scanLogAttrs(stats, durationMS), "elapsed_ms", durationMS)...)
			}
		}()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.withCurrentSource(job, func() error { return nil }); err != nil {
			return err
		}
		encountered[candidate.NativeID] = struct{}{}
		if entry, ok := cache[candidate.NativeID]; ok && entry.CanonicalExists && entry.Candidate == candidate {
			stats.Reused++
			seen[candidate.NativeID] = struct{}{}
			if entry.ArtPending {
				attempted, failed, retryErr := m.retryPendingArt(ctx, job, candidate, entry, seq, artDir, artIDs)
				mutated = mutated || attempted
				if failed {
					stats.Failed++
				}
				if retryErr != nil {
					stats.Failed++
					return retryErr
				}
			}
			return nil
		}
		stats.Probed++
		meta, err := job.src.Parse(ctx, candidate)
		if err != nil {
			stats.Failed++
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		if meta.NativeID != candidate.NativeID {
			return fmt.Errorf("source parser returned mismatched native id")
		}
		var artData []byte
		var artMIME string
		artFailed := false
		if meta.ArtHash != "" {
			if _, ok := artIDs[meta.ArtHash]; !ok {
				artID, found, err := m.st.FindArtByHash(ctx, meta.ArtHash)
				if err != nil {
					stats.Failed++
					return err
				}
				if found {
					artIDs[meta.ArtHash] = artID
				} else {
					artData, artMIME, err = job.src.Art(ctx, candidate, meta.ArtHash)
					artFailed = err != nil || len(artData) == 0
				}
			}
		}
		if artFailed {
			stats.Failed++
		}
		return m.withCurrentSource(job, func() error {
			artID := artIDs[meta.ArtHash]
			mutated = true // UpsertTrack can commit early statements before a late error.
			if err := m.upsertTrack(ctx, job.src.Kind(), meta, "", seq); err != nil {
				stats.Failed++
				return err
			}
			stats.Indexed++
			seen[candidate.NativeID] = struct{}{}
			if artFailed {
				return m.st.PutSourceFileCacheState(ctx, job.sourceID, job.src.Kind(), job.parserVersion, candidate, meta, true)
			}
			if meta.ArtHash != "" && artID == "" && len(artData) > 0 {
				artID, err = m.st.UpsertArt(ctx, meta.ArtHash, artMIME, artData, artDir)
				if err != nil {
					stats.Failed++
					_ = m.st.PutSourceFileCacheState(ctx, job.sourceID, job.src.Kind(), job.parserVersion, candidate, meta, true)
					return err
				}
				artIDs[meta.ArtHash] = artID
			}
			if artID != "" {
				if _, err := m.st.AttachTrackArt(ctx, job.src.Kind(), candidate.NativeID, artID, seq); err != nil {
					stats.Failed++
					return err
				}
			}
			return m.st.PutSourceFileCacheState(ctx, job.sourceID, job.src.Kind(), job.parserVersion, candidate, meta, false)
		})
	})
	if err != nil {
		return stats, mutated, err
	}
	err = m.withCurrentSource(job, func() error {
		return m.st.FinalizeSourceScan(ctx, job.src.Kind(), seq, job.sourceID, encountered, seen)
	})
	if err == nil {
		counts, countErr := m.st.ScanChangeCounts(ctx, job.src.Kind(), seq)
		if countErr != nil {
			return stats, mutated, countErr
		}
		stats.Changed, stats.Removed = counts.Changed, counts.Removed
	}
	return stats, mutated, err
}

func (m *Manager) retryPendingArt(ctx context.Context, job scanJob, candidate source.TrackCandidate, entry store.SourceFileCacheEntry, seq int64, artDir string, artIDs map[string]string) (bool, bool, error) {
	hash := entry.Meta.ArtHash
	if hash == "" {
		return false, false, m.withCurrentSource(job, func() error {
			return m.st.PutSourceFileCacheState(ctx, job.sourceID, job.src.Kind(), job.parserVersion, candidate, entry.Meta, false)
		})
	}
	artID := artIDs[hash]
	if artID == "" {
		var found bool
		var err error
		artID, found, err = m.st.FindArtByHash(ctx, hash)
		if err != nil {
			return false, false, err
		}
		if !found {
			data, mime, artErr := job.src.Art(ctx, candidate, hash)
			if artErr != nil || len(data) == 0 {
				return false, true, nil
			}
			return true, false, m.withCurrentSource(job, func() error {
				id, err := m.st.UpsertArt(ctx, hash, mime, data, artDir)
				if err != nil {
					return err
				}
				artIDs[hash] = id
				if _, err := m.st.AttachTrackArt(ctx, job.src.Kind(), candidate.NativeID, id, seq); err != nil {
					return err
				}
				return m.st.PutSourceFileCacheState(ctx, job.sourceID, job.src.Kind(), job.parserVersion, candidate, entry.Meta, false)
			})
		}
	}
	return true, false, m.withCurrentSource(job, func() error {
		if _, err := m.st.AttachTrackArt(ctx, job.src.Kind(), candidate.NativeID, artID, seq); err != nil {
			return err
		}
		return m.st.PutSourceFileCacheState(ctx, job.sourceID, job.src.Kind(), job.parserVersion, candidate, entry.Meta, false)
	})
}
