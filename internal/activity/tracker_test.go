package activity

import (
	"testing"
	"time"
)

func TestTrackerRejectsStaleRunsAndCopiesSnapshots(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tracker := NewWithClock(func() time.Time { return now })

	old := tracker.Start(StageFilesystemScan, Counts{Discovered: 1})
	snapshot := tracker.Snapshot()
	if snapshot == nil || snapshot.Stage != StageFilesystemScan || snapshot.State != StateRunning || snapshot.Counts.Discovered != 1 {
		t.Fatalf("initial snapshot: %+v", snapshot)
	}
	snapshot.Counts.Discovered = 99
	if got := tracker.Snapshot().Counts.Discovered; got != 1 {
		t.Fatalf("snapshot was not copied: discovered=%d", got)
	}

	now = now.Add(time.Second)
	current := tracker.Start(StageMusicBrainzResolution, Counts{Total: 4})
	tracker.Update(old, Counts{Discovered: 2})
	tracker.Fail(old, Counts{Failed: 1})
	tracker.Finish(old)
	tracker.RetainFailure(old, Snapshot{Stage: StageFilesystemScan, State: StateFailed, Counts: Counts{Failed: 1}})
	if got := tracker.Snapshot(); got == nil || got.Stage != StageMusicBrainzResolution || got.Counts.Total != 4 {
		t.Fatalf("stale run replaced current snapshot: %+v", got)
	}

	now = now.Add(time.Second)
	tracker.Fail(current, Counts{Total: 4, Processed: 2, Failed: 1})
	failed := tracker.Snapshot()
	if failed == nil || failed.State != StateFailed || failed.Reason != ReasonOperationFailed || !failed.UpdatedAt.Equal(now) {
		t.Fatalf("failed snapshot: %+v", failed)
	}

	next := tracker.Start(StageAlbumArtwork, Counts{Total: 3})
	tracker.RetainFailure(next, *failed)
	if got := tracker.Snapshot(); got == nil || got.Stage != StageMusicBrainzResolution || got.State != StateFailed || got.Counts.Processed != 2 {
		t.Fatalf("retained failure was not restored: %+v", got)
	}
	next = tracker.Start(StageAlbumArtwork, Counts{Total: 3})
	if got := tracker.Snapshot(); got == nil || got.State != StateRunning || got.Reason != "" {
		t.Fatalf("new run did not replace retained failure: %+v", got)
	}
	tracker.Finish(next)
	if got := tracker.Snapshot(); got != nil {
		t.Fatalf("successful idle transition retained activity: %+v", got)
	}
}
