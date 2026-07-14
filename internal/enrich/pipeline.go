package enrich

import (
	"context"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/activity"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/mbresolver"
	"github.com/BlitterAmp/BlitterServer/internal/progress"
)

type runSummary = activity.Counts

func (e *Enricher) Run(ctx context.Context) {
	e.RunAt(ctx, time.Now())
}

func (e *Enricher) RunAt(ctx context.Context, now time.Time) {
	log := logging.From(ctx).With("component", "enrich")
	started := e.Now()
	total := runSummary{}
	var lastToken activity.Token
	var failedSnapshot *activity.Snapshot
	captureFailure := func() {
		if snapshot := e.activity.Snapshot(); snapshot != nil && snapshot.State == activity.StateFailed {
			failedSnapshot = snapshot
		}
	}
	dirty, anyChange := false, false
	lastPublish := time.Now()
	markDirty := func() { dirty, anyChange = true, true }
	publish := func(force bool) {
		if !dirty || e.bus == nil {
			return
		}
		if !force && time.Since(lastPublish) < e.ProgressInterval {
			return
		}
		publishCtx := context.WithoutCancel(ctx)
		if sum, err := e.st.GetLibrarySummary(publishCtx); err != nil {
			log.Error("read library summary for enrichment event", "err", err)
		} else if libraryID, err := e.st.LibraryID(publishCtx); err != nil {
			log.Error("read library id for enrichment event", "err", err)
		} else if err := e.bus.Publish(publishCtx, "library.changed", "", map[string]any{"libraryId": libraryID, "updatedAt": sum.UpdatedAt}); err != nil {
			log.Error("publish enrichment library change", "err", err)
		} else {
			dirty = false
			lastPublish = time.Now()
		}
	}
	defer func() {
		message := "library enrichment completed"
		if ctx.Err() != nil {
			message = "library enrichment cancelled"
		} else if total.Failed > 0 {
			message = "library enrichment failed"
		}
		args := []any{"attempted", total.Attempted, "succeeded", total.Succeeded, "skipped", total.Skipped, "missed", total.Missed, "transient", total.Transient, "failed", total.Failed, "duration_ms", e.Now().Sub(started).Milliseconds()}
		if message == "library enrichment failed" {
			log.Error(message, args...)
		} else {
			log.Info(message, args...)
		}
		publish(true)
		if anyChange {
			log.Info("enrichment updated the library")
		}
		if ctx.Err() != nil || total.Failed == 0 {
			e.activity.Finish(lastToken)
		} else if failedSnapshot != nil {
			e.activity.RetainFailure(lastToken, *failedSnapshot)
		}
	}()

	// Artwork first — but only a bounded slice: covers appear quickly on a
	// fresh library without starving identity matching, which restarts would
	// otherwise re-park behind a full artwork drain forever.
	artDeadline := time.Now().Add(e.ArtSliceBudget)
	lastToken = e.runAlbumArtStage(ctx, now, artDeadline, log, &total, markDirty, publish)
	captureFailure()
	lastToken = e.runArtistArtStage(ctx, now, artDeadline, log, &total, markDirty, publish)
	captureFailure()

	if e.resolver != nil {
		lastToken = e.activity.Start(activity.StageMusicBrainzResolution, activity.Counts{})
		due, _ := e.st.CountDueMusicBrainzAlbums(ctx, now)
		resolutionCounts := activity.Counts{Total: int(due), Remaining: int(due)}
		e.activity.Update(lastToken, resolutionCounts)
		reporter := progress.New(e.Now, e.LogProgressInterval)
		log.Info("musicbrainz resolution started", "due", due)
		var resolution mbresolver.ResolutionProgress
		previousApplied := 0
		resolved, err := e.resolver.RunWithProgress(ctx, func(current mbresolver.ResolutionProgress) {
			resolution = current
			resolutionCounts.Processed = current.Processed
			resolutionCounts.Changed = current.Applied
			resolutionCounts.Failed = current.Failed
			if current.Applied > previousApplied {
				markDirty()
				previousApplied = current.Applied
			}
			publish(false)
			if reporter.Due(current.Processed) {
				remaining, _ := e.st.CountDueMusicBrainzAlbums(ctx, now)
				resolutionCounts.Remaining = int(remaining)
				e.activity.Update(lastToken, resolutionCounts)
				log.Info("musicbrainz resolution progress", "due", due, "processed", current.Processed, "applied", current.Applied, "failed", current.Failed, "remaining", remaining, "duration_ms", reporter.DurationMS())
			}
		})
		if resolved {
			markDirty()
		}
		if err != nil {
			log.Warn("musicbrainz resolution failed", "err", err)
			total.Failed++
		}
		remaining, _ := e.st.CountDueMusicBrainzAlbums(ctx, now)
		resolutionCounts.Remaining = int(remaining)
		message := "musicbrainz resolution completed"
		if ctx.Err() != nil {
			message = "musicbrainz resolution cancelled"
		} else if err != nil {
			message = "musicbrainz resolution failed"
		}
		args := []any{"due", due, "processed", resolution.Processed, "applied", resolution.Applied, "failed", resolution.Failed, "remaining", remaining, "duration_ms", reporter.DurationMS()}
		if err != nil && ctx.Err() == nil {
			e.activity.Fail(lastToken, resolutionCounts)
			captureFailure()
			log.Error(message, args...)
		} else {
			e.activity.Update(lastToken, resolutionCounts)
			log.Info(message, args...)
		}
	}
	publish(false)
	artistIdentity, artistToken := e.runArtistIdentityStage(ctx, markDirty, publish)
	lastToken = artistToken
	captureFailure()
	total.Failed += artistIdentity.Failed
	publish(false)

	// Identity landed above: newly matched albums and newly identified
	// artists had their art schedules reset, so give them their first
	// identity-aware fetch in the same pass — unbounded this time.
	lastToken = e.runAlbumArtStage(ctx, now, time.Time{}, log, &total, markDirty, publish)
	captureFailure()
	lastToken = e.runArtistArtStage(ctx, now, time.Time{}, log, &total, markDirty, publish)
	captureFailure()
}
