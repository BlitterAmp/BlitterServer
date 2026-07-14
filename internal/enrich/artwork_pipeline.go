package enrich

import (
	"context"
	"log/slog"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/progress"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func (e *Enricher) runAlbumArtStage(ctx context.Context, now, deadline time.Time, log *slog.Logger, total *runSummary, markDirty func(), publish func(bool)) {
	stats := runSummary{}
	due, countErr := e.countAlbumsNeedingArt(ctx, now)
	if countErr != nil {
		log.Error("album artwork failed", "err", countErr)
		total.failed++
		return
	}
	reporter := progress.New(e.Now, e.LogProgressInterval)
	log.Info("album artwork started", "due", due)
	reportProgress := func() {
		if reporter.Due(stats.attempted) {
			log.Info("album artwork progress", "due", due, "attempted", stats.attempted, "succeeded", stats.succeeded, "skipped", stats.skipped, "missed", stats.missed, "transient", stats.transient, "failed", stats.failed, "duration_ms", reporter.DurationMS())
		}
	}
	stageFailed := false
	defer func() {
		remaining, remainingErr := e.countAlbumsNeedingArt(context.WithoutCancel(ctx), now)
		if remainingErr != nil {
			remaining = -1
			stats.failed++
			stageFailed = true
		}
		total.attempted += stats.attempted
		total.succeeded += stats.succeeded
		total.skipped += stats.skipped
		total.missed += stats.missed
		total.transient += stats.transient
		total.failed += stats.failed
		message := "album artwork completed"
		if ctx.Err() != nil {
			message = "album artwork cancelled"
		} else if stageFailed {
			message = "album artwork failed"
		}
		args := []any{"due", due, "attempted", stats.attempted, "succeeded", stats.succeeded, "skipped", stats.skipped, "missed", stats.missed, "transient", stats.transient, "failed", stats.failed, "remaining", remaining, "duration_ms", reporter.DurationMS()}
		if stageFailed && ctx.Err() == nil {
			log.Error(message, args...)
		} else {
			log.Info(message, args...)
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			log.Info("album artwork yielded", "attempted", stats.attempted)
			return
		}
		albums, err := e.st.AlbumsNeedingArtAt(ctx, now, perRun)
		if err != nil {
			log.Error("list albums needing artwork", "err", err)
			stats.failed++
			stageFailed = true
			return
		}
		if len(albums) == 0 {
			return
		}
		progressed := false
		for _, a := range albums {
			if ctx.Err() != nil {
				return
			}
			stats.attempted++
			data, mime, outcome := e.albumArtOutcome(ctx, a.ReleaseGroupID, a.ArtistName, a.Title)
			if ctx.Err() != nil {
				return
			}
			if data != nil {
				if id, err := e.store(ctx, data, mime); err == nil {
					applied, err := e.attachAlbumArtFn(ctx, a.AlbumID, id)
					if applied {
						markDirty()
						publish(false)
					}
					if err != nil {
						log.Error("attach album artwork", "album_id", a.AlbumID, "err", err)
						stats.failed++
						stageFailed = true
					} else if applied {
						stats.succeeded++
						progressed = true
					} else {
						stats.skipped++
						progressed = true
					}
					reportProgress()
					continue
				} else {
					log.Error("store album artwork", "album_id", a.AlbumID, "err", err)
					stats.failed++
					stageFailed = true
					outcome = lookupTransient
				}
			}
			if outcome == lookupTransient {
				stats.transient++
				err = e.st.MarkAlbumArtAttempt(ctx, a.AlbumID, store.ArtAttemptTransient, now)
			} else {
				stats.missed++
				err = e.st.MarkAlbumArtAttempt(ctx, a.AlbumID, store.ArtAttemptMiss, now)
			}
			if err != nil {
				log.Error("schedule album artwork retry", "album_id", a.AlbumID, "err", err)
				stats.failed++
				stageFailed = true
			} else {
				progressed = true
			}
			reportProgress()
		}
		publish(false)
		if !progressed {
			log.Error("album artwork stage stalled; ending stage")
			stageFailed = true
			return
		}
	}
}

func (e *Enricher) runArtistArtStage(ctx context.Context, now, deadline time.Time, log *slog.Logger, total *runSummary, markDirty func(), publish func(bool)) {
	if e.lastfmKey(ctx) == "" && e.discogsToken(ctx) == "" && e.fanartKey(ctx) == "" {
		log.Info("artist artwork skipped", "reason", "no_provider_configured")
		return
	}
	stats := runSummary{}
	due, countErr := e.countArtistsNeedingArt(ctx, now)
	if countErr != nil {
		log.Error("artist artwork failed", "err", countErr)
		total.failed++
		return
	}
	reporter := progress.New(e.Now, e.LogProgressInterval)
	log.Info("artist artwork started", "due", due)
	reportProgress := func() {
		if reporter.Due(stats.attempted) {
			log.Info("artist artwork progress", "due", due, "attempted", stats.attempted, "succeeded", stats.succeeded, "skipped", stats.skipped, "missed", stats.missed, "transient", stats.transient, "failed", stats.failed, "duration_ms", reporter.DurationMS())
		}
	}
	stageFailed := false
	defer func() {
		remaining, remainingErr := e.countArtistsNeedingArt(context.WithoutCancel(ctx), now)
		if remainingErr != nil {
			remaining = -1
			stats.failed++
			stageFailed = true
		}
		total.attempted += stats.attempted
		total.succeeded += stats.succeeded
		total.skipped += stats.skipped
		total.missed += stats.missed
		total.transient += stats.transient
		total.failed += stats.failed
		message := "artist artwork completed"
		if ctx.Err() != nil {
			message = "artist artwork cancelled"
		} else if stageFailed {
			message = "artist artwork failed"
		}
		args := []any{"due", due, "attempted", stats.attempted, "succeeded", stats.succeeded, "skipped", stats.skipped, "missed", stats.missed, "transient", stats.transient, "failed", stats.failed, "remaining", remaining, "duration_ms", reporter.DurationMS()}
		if stageFailed && ctx.Err() == nil {
			log.Error(message, args...)
		} else {
			log.Info(message, args...)
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			log.Info("artist artwork yielded", "attempted", stats.attempted)
			return
		}
		artists, err := e.st.ArtistsNeedingArtAt(ctx, now, perRun)
		if err != nil {
			log.Error("list artists needing artwork", "err", err)
			stats.failed++
			stageFailed = true
			return
		}
		if len(artists) == 0 {
			return
		}
		progressed := false
		for _, ar := range artists {
			if ctx.Err() != nil {
				return
			}
			stats.attempted++
			data, mime, outcome := e.artistArtOutcome(ctx, ar.Name, ar.MusicBrainzID)
			if ctx.Err() != nil {
				return
			}
			if data != nil {
				if id, err := e.store(ctx, data, mime); err == nil {
					applied, err := e.attachArtistArtFn(ctx, ar.ArtistID, ar.ArtID, id)
					if applied {
						markDirty()
						publish(false)
					}
					if err != nil {
						log.Error("attach artist artwork", "artist_id", ar.ArtistID, "err", err)
						stats.failed++
						stageFailed = true
					} else if applied {
						stats.succeeded++
						progressed = true
					} else {
						stats.skipped++
						progressed = true
					}
					reportProgress()
					continue
				} else {
					log.Error("store artist artwork", "artist_id", ar.ArtistID, "err", err)
					stats.failed++
					stageFailed = true
					outcome = lookupTransient
				}
			}
			if outcome == lookupTransient {
				stats.transient++
				err = e.st.MarkArtistArtAttempt(ctx, ar.ArtistID, store.ArtAttemptTransient, now)
			} else {
				stats.missed++
				err = e.st.MarkArtistArtAttempt(ctx, ar.ArtistID, store.ArtAttemptMiss, now)
			}
			if err != nil {
				log.Error("schedule artist artwork retry", "artist_id", ar.ArtistID, "err", err)
				stats.failed++
				stageFailed = true
			} else {
				progressed = true
			}
			reportProgress()
		}
		publish(false)
		if !progressed {
			log.Error("artist artwork stage stalled; ending stage")
			stageFailed = true
			return
		}
	}
}
