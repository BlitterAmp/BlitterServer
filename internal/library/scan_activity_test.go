package library

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/activity"
	"github.com/BlitterAmp/BlitterServer/internal/source"
)

type activityBlockingEnricher struct {
	tracker *activity.Tracker
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *activityBlockingEnricher) Run(ctx context.Context) {
	token := e.tracker.Start(activity.StageAlbumArtwork, activity.Counts{Total: 1, Remaining: 1})
	e.once.Do(func() {
		close(e.started)
		select {
		case <-e.release:
		case <-ctx.Done():
		}
	})
	e.tracker.Finish(token)
}

func TestScanActivityUsesScanCountersAndRetainsPathFreeFailure(t *testing.T) {
	st, dataDir := openStore(t)
	m := NewManager(st, dataDir)
	src := &sequenceSource{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		meta: source.TrackMeta{
			NativeID: "private/path/song.flac", Title: "Private Song", Album: "Private Album",
			PrimaryArtist: source.ArtistReference{Name: "Private Artist"},
			TrackCredits:  []source.ArtistCredit{{Name: "Private Artist"}},
			AlbumCredits:  []source.ArtistCredit{{Name: "Private Artist"}},
			Container:     "flac", Codec: "flac", Version: 1,
		},
		err: errors.New("walk /private/music: permission denied"),
	}
	m.mu.Lock()
	m.src = src
	m.sourceID = "src_private"
	m.sourceGeneration = 1
	m.scanProgressInterval = 0
	m.mu.Unlock()

	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-src.entered
	running := m.ActivityTracker().Snapshot()
	if running == nil || running.Stage != activity.StageFilesystemScan || running.State != activity.StateRunning || running.Counts.Discovered != 1 || running.Counts.Probed != 1 || running.Counts.Indexed != 1 {
		t.Fatalf("running activity: %+v", running)
	}
	close(src.release)
	deadline := time.Now().Add(time.Second)
	for m.Status(context.Background()).Scanning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	failed := m.ActivityTracker().Snapshot()
	if failed == nil || failed.State != activity.StateFailed || failed.Reason != activity.ReasonOperationFailed {
		t.Fatalf("failed activity: %+v", failed)
	}
	encoded, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private/path", "Private Song", "Private Album", "Private Artist", "/private/music", "permission denied", "src_private"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("activity leaked %q: %s", private, encoded)
		}
	}
}

func TestScanActivityOwnsOperationThroughBookkeepingThenHandsOff(t *testing.T) {
	st, dataDir := openStore(t)
	m := NewManager(st, dataDir)
	src := &sequenceSource{
		entered: make(chan struct{}), release: make(chan struct{}),
		meta: source.TrackMeta{
			NativeID: "song.flac", Title: "Song", Album: "Album",
			PrimaryArtist: source.ArtistReference{Name: "Artist"},
			TrackCredits:  []source.ArtistCredit{{Name: "Artist"}},
			AlbumCredits:  []source.ArtistCredit{{Name: "Artist"}},
			Container:     "flac", Codec: "flac", Version: 1,
		},
	}
	enricher := &activityBlockingEnricher{tracker: m.ActivityTracker(), started: make(chan struct{}), release: make(chan struct{})}
	ready := make(chan struct{})
	bookkeeping := make(chan struct{})
	releaseBookkeeping := make(chan struct{})
	var readyOnce sync.Once
	m.onEnrichmentReady = func() { readyOnce.Do(func() { close(ready) }) }
	m.onScanBookkeeping = func() {
		close(bookkeeping)
		<-releaseBookkeeping
	}
	m.mu.Lock()
	m.src = src
	m.sourceID = "src_test"
	m.sourceGeneration = 1
	m.enricher = enricher
	m.mu.Unlock()

	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-src.entered
	m.TriggerEnrichment()
	<-ready
	select {
	case <-enricher.started:
		t.Fatal("enrichment started activity while scan owned the operation")
	default:
	}
	if snapshot := m.ActivityTracker().Snapshot(); snapshot == nil || snapshot.Stage != activity.StageFilesystemScan {
		t.Fatalf("scan activity was replaced while scan ran: %+v", snapshot)
	}

	close(src.release)
	<-bookkeeping
	if snapshot := m.ActivityTracker().Snapshot(); snapshot == nil || snapshot.Stage != activity.StageFilesystemScan {
		t.Fatalf("scan activity cleared before bookkeeping completed: %+v", snapshot)
	}
	close(releaseBookkeeping)
	<-enricher.started
	m.scanWG.Wait()
	if status := m.Status(context.Background()); status.Scanning {
		t.Fatalf("enrichment started before scan status became idle: %+v", status)
	}
	if snapshot := m.ActivityTracker().Snapshot(); snapshot == nil || snapshot.Stage != activity.StageAlbumArtwork {
		t.Fatalf("scan completion cleared newer enrichment activity: %+v", snapshot)
	}
	close(enricher.release)
	m.Close()
}
