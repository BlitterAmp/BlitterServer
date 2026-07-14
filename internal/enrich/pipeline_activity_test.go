package enrich

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/activity"
)

func TestAlbumArtworkActivityIsCurrentAndClearsAfterSuccessfulPipeline(t *testing.T) {
	st, _ := seedAlbum(t)
	requestStarted := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProvider)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		startOnce.Do(func() { close(requestStarted) })
		<-release
		w.WriteHeader(http.StatusNotFound)
	}))
	defer provider.Close()
	tracker := activity.New()
	e := New(st, nil, t.TempDir(), Config{
		LastfmKey: func(context.Context) string { return "configured" },
		Activity:  tracker,
	})
	e.LastfmBase = provider.URL
	e.ArtSliceBudget = time.Hour
	done := make(chan struct{})
	go func() {
		e.RunAt(context.Background(), time.Now())
		close(done)
	}()
	<-requestStarted
	snapshot := tracker.Snapshot()
	if snapshot == nil || snapshot.Stage != activity.StageAlbumArtwork || snapshot.State != activity.StateRunning || snapshot.Counts.Total != 1 || snapshot.Counts.Attempted != 1 || snapshot.Counts.Remaining != 0 {
		releaseProvider()
		t.Fatalf("album artwork activity: %+v", snapshot)
	}
	releaseProvider()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enrichment did not finish")
	}
	if snapshot := tracker.Snapshot(); snapshot != nil {
		t.Fatalf("successful pipeline did not become idle: %+v", snapshot)
	}
}

func TestArtworkInitialCountFailuresIncrementOverallOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage activity.Stage
		run   func(*Enricher, context.Context, *runSummary)
	}{
		{
			name: "album", stage: activity.StageAlbumArtwork,
			run: func(e *Enricher, ctx context.Context, total *runSummary) {
				e.countAlbumsNeedingArt = func(context.Context, time.Time) (int, error) { return 0, errors.New("count failed") }
				e.runAlbumArtStage(ctx, time.Now(), time.Time{}, slog.New(slog.NewTextHandler(io.Discard, nil)), total, func() {}, func(bool) {})
			},
		},
		{
			name: "artist", stage: activity.StageArtistArtwork,
			run: func(e *Enricher, ctx context.Context, total *runSummary) {
				e.cfg.LastfmKey = func(context.Context) string { return "configured" }
				e.countArtistsNeedingArt = func(context.Context, time.Time) (int, error) { return 0, errors.New("count failed") }
				e.runArtistArtStage(ctx, time.Now(), time.Time{}, slog.New(slog.NewTextHandler(io.Discard, nil)), total, func() {}, func(bool) {})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := seedAlbum(t)
			e := New(st, nil, t.TempDir(), Config{})
			total := runSummary{}
			tc.run(e, context.Background(), &total)
			snapshot := e.activity.Snapshot()
			if total.Failed != 1 || snapshot == nil || snapshot.Stage != tc.stage || snapshot.State != activity.StateFailed || snapshot.Counts.Failed != 1 {
				t.Fatalf("count failure total=%+v activity=%+v", total, snapshot)
			}
		})
	}
}
