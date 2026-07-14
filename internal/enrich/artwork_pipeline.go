package enrich

import (
	"context"
	"log/slog"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/activity"
	"github.com/BlitterAmp/BlitterServer/internal/progress"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func (e *Enricher) runAlbumArtStage(ctx context.Context, now, deadline time.Time, log *slog.Logger, total *runSummary, markDirty func(), publish func(bool)) activity.Token {
	stats := runSummary{}
	defer func() { addRunSummary(total, stats) }()
	token := e.activity.Start(activity.StageAlbumArtwork, stats)
	due, countErr := e.countAlbumsNeedingArt(ctx, now)
	if countErr != nil {
		log.Error("album artwork failed", "err", countErr)
		stats.Failed++
		e.activity.Fail(token, stats)
		return token
	}
	stats.Total, stats.Remaining = due, due
	e.activity.Update(token, stats)
	reporter := progress.New(e.Now, e.LogProgressInterval)
	log.Info("album artwork started", "due", due)
	reportProgress := func() {
		if reporter.Due(stats.Attempted) {
			e.activity.Update(token, stats)
			log.Info("album artwork progress", "due", due, "attempted", stats.Attempted, "succeeded", stats.Succeeded, "skipped", stats.Skipped, "missed", stats.Missed, "transient", stats.Transient, "failed", stats.Failed, "duration_ms", reporter.DurationMS())
		}
	}
	stageFailed := false
	defer func() {
		remaining, remainingErr := e.countAlbumsNeedingArt(context.WithoutCancel(ctx), now)
		if remainingErr != nil {
			remaining = -1
			stats.Failed++
			stageFailed = true
		}
		stats.Remaining = remaining
		message := "album artwork completed"
		if ctx.Err() != nil {
			message = "album artwork cancelled"
		} else if stageFailed {
			message = "album artwork failed"
		}
		args := []any{"due", due, "attempted", stats.Attempted, "succeeded", stats.Succeeded, "skipped", stats.Skipped, "missed", stats.Missed, "transient", stats.Transient, "failed", stats.Failed, "remaining", remaining, "duration_ms", reporter.DurationMS()}
		if stageFailed && ctx.Err() == nil {
			e.activity.Fail(token, stats)
			log.Error(message, args...)
		} else {
			e.activity.Update(token, stats)
			log.Info(message, args...)
		}
	}()
	for {
		if ctx.Err() != nil {
			return token
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			log.Info("album artwork yielded", "attempted", stats.Attempted)
			return token
		}
		albums, err := e.st.AlbumsNeedingArtAt(ctx, now, perRun)
		if err != nil {
			log.Error("list albums needing artwork", "err", err)
			stats.Failed++
			stageFailed = true
			return token
		}
		if len(albums) == 0 {
			return token
		}
		progressed := false
		for _, a := range albums {
			if ctx.Err() != nil {
				return token
			}
			stats.Attempted++
			stats.Remaining = due - stats.Attempted
			if stats.Remaining < 0 {
				stats.Remaining = 0
			}
			e.activity.Update(token, stats)
			data, mime, outcome := e.albumArtOutcome(ctx, a.ReleaseGroupID, a.ArtistName, a.Title)
			if ctx.Err() != nil {
				return token
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
						stats.Failed++
						stageFailed = true
					} else if applied {
						stats.Succeeded++
						progressed = true
					} else {
						stats.Skipped++
						progressed = true
					}
					reportProgress()
					continue
				} else {
					log.Error("store album artwork", "album_id", a.AlbumID, "err", err)
					stats.Failed++
					stageFailed = true
					outcome = lookupTransient
				}
			}
			if outcome == lookupTransient {
				stats.Transient++
				err = e.st.MarkAlbumArtAttempt(ctx, a.AlbumID, store.ArtAttemptTransient, now)
			} else {
				stats.Missed++
				err = e.st.MarkAlbumArtAttempt(ctx, a.AlbumID, store.ArtAttemptMiss, now)
			}
			if err != nil {
				log.Error("schedule album artwork retry", "album_id", a.AlbumID, "err", err)
				stats.Failed++
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
			return token
		}
	}
}

func (e *Enricher) runArtistArtStage(ctx context.Context, now, deadline time.Time, log *slog.Logger, total *runSummary, markDirty func(), publish func(bool)) activity.Token {
	stats := runSummary{}
	defer func() { addRunSummary(total, stats) }()
	token := e.activity.Start(activity.StageArtistArtwork, stats)
	if e.lastfmKey(ctx) == "" && e.discogsToken(ctx) == "" && e.fanartKey(ctx) == "" {
		log.Info("artist artwork skipped", "reason", "no_provider_configured")
		e.activity.Update(token, stats)
		return token
	}
	due, countErr := e.countArtistsNeedingArt(ctx, now)
	if countErr != nil {
		log.Error("artist artwork failed", "err", countErr)
		stats.Failed++
		e.activity.Fail(token, stats)
		return token
	}
	stats.Total, stats.Remaining = due, due
	e.activity.Update(token, stats)
	reporter := progress.New(e.Now, e.LogProgressInterval)
	log.Info("artist artwork started", "due", due)
	reportProgress := func() {
		if reporter.Due(stats.Attempted) {
			e.activity.Update(token, stats)
			log.Info("artist artwork progress", "due", due, "attempted", stats.Attempted, "succeeded", stats.Succeeded, "skipped", stats.Skipped, "missed", stats.Missed, "transient", stats.Transient, "failed", stats.Failed, "duration_ms", reporter.DurationMS())
		}
	}
	stageFailed := false
	defer func() {
		remaining, remainingErr := e.countArtistsNeedingArt(context.WithoutCancel(ctx), now)
		if remainingErr != nil {
			remaining = -1
			stats.Failed++
			stageFailed = true
		}
		stats.Remaining = remaining
		message := "artist artwork completed"
		if ctx.Err() != nil {
			message = "artist artwork cancelled"
		} else if stageFailed {
			message = "artist artwork failed"
		}
		args := []any{"due", due, "attempted", stats.Attempted, "succeeded", stats.Succeeded, "skipped", stats.Skipped, "missed", stats.Missed, "transient", stats.Transient, "failed", stats.Failed, "remaining", remaining, "duration_ms", reporter.DurationMS()}
		if stageFailed && ctx.Err() == nil {
			e.activity.Fail(token, stats)
			log.Error(message, args...)
		} else {
			e.activity.Update(token, stats)
			log.Info(message, args...)
		}
	}()
	for {
		if ctx.Err() != nil {
			return token
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			log.Info("artist artwork yielded", "attempted", stats.Attempted)
			return token
		}
		artists, err := e.st.ArtistsNeedingArtAt(ctx, now, perRun)
		if err != nil {
			log.Error("list artists needing artwork", "err", err)
			stats.Failed++
			stageFailed = true
			return token
		}
		if len(artists) == 0 {
			return token
		}
		progressed := false
		for _, ar := range artists {
			if ctx.Err() != nil {
				return token
			}
			stats.Attempted++
			stats.Remaining = due - stats.Attempted
			if stats.Remaining < 0 {
				stats.Remaining = 0
			}
			e.activity.Update(token, stats)
			data, mime, outcome := e.artistArtOutcome(ctx, ar.Name, ar.MusicBrainzID)
			if ctx.Err() != nil {
				return token
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
						stats.Failed++
						stageFailed = true
					} else if applied {
						stats.Succeeded++
						progressed = true
					} else {
						stats.Skipped++
						progressed = true
					}
					reportProgress()
					continue
				} else {
					log.Error("store artist artwork", "artist_id", ar.ArtistID, "err", err)
					stats.Failed++
					stageFailed = true
					outcome = lookupTransient
				}
			}
			if outcome == lookupTransient {
				stats.Transient++
				err = e.st.MarkArtistArtAttempt(ctx, ar.ArtistID, store.ArtAttemptTransient, now)
			} else {
				stats.Missed++
				err = e.st.MarkArtistArtAttempt(ctx, ar.ArtistID, store.ArtAttemptMiss, now)
			}
			if err != nil {
				log.Error("schedule artist artwork retry", "artist_id", ar.ArtistID, "err", err)
				stats.Failed++
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
			return token
		}
	}
}

func addRunSummary(total *runSummary, stats runSummary) {
	total.Attempted += stats.Attempted
	total.Succeeded += stats.Succeeded
	total.Skipped += stats.Skipped
	total.Missed += stats.Missed
	total.Transient += stats.Transient
	total.Failed += stats.Failed
}
