package server

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/activity"
	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestGetPingReportsVersionAndSetup(t *testing.T) {
	srv := New(testStore(t), "1.2.3-test")
	resp, err := srv.GetPing(context.Background(), api.GetPingRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := resp.(api.GetPing200JSONResponse)
	if !ok {
		t.Fatalf("want 200 response, got %T", resp)
	}
	if r.Name != "BlitterServer" || r.Version != "1.2.3-test" {
		t.Fatalf("bad identity: %+v", r)
	}
	if r.SetupComplete == nil || *r.SetupComplete {
		t.Fatal("fresh store must report setupComplete=false")
	}
}

func TestGetStatusHonestZeros(t *testing.T) {
	srv := New(testStore(t), "v")
	resp, err := srv.GetStatus(context.Background(), api.GetStatusRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	r := resp.(api.GetStatus200JSONResponse)
	if r.Source.Connected || r.Source.Kind != "none" || r.SetupComplete {
		t.Fatalf("dishonest status: %+v", r)
	}
	if r.Activity != nil {
		t.Fatalf("idle server reported activity: %+v", r.Activity)
	}
}

func TestGetStatusMapsRunningAndFailedLibraryActivity(t *testing.T) {
	st := testStore(t)
	mgr := library.NewManager(st, t.TempDir())
	srv := NewWithLibrary(st, mgr, "v")
	tracker := mgr.ActivityTracker()
	token := tracker.Start(activity.StageFilesystemScan, activity.Counts{Discovered: 7, Reused: 5, Probed: 2})

	resp, err := srv.GetStatus(context.Background(), api.GetStatusRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	running := resp.(api.GetStatus200JSONResponse).Activity
	if running == nil || running.Stage != api.FilesystemScan || running.State != api.LibraryActivityStateRunning || running.Counts.Discovered == nil || *running.Counts.Discovered != 7 || running.Counts.Processed != nil {
		t.Fatalf("running status activity: %+v", running)
	}
	if running.StartedAt.Location() != time.UTC || running.UpdatedAt.Location() != time.UTC {
		t.Fatalf("activity dates are not UTC: %+v", running)
	}

	tracker.Fail(token, activity.Counts{Discovered: 7, Reused: 5, Probed: 2, Failed: 1})
	resp, _ = srv.GetStatus(context.Background(), api.GetStatusRequestObject{})
	failed := resp.(api.GetStatus200JSONResponse).Activity
	if failed == nil || failed.State != api.LibraryActivityStateFailed || failed.Reason == nil || *failed.Reason != api.OperationFailed || failed.Counts.Failed == nil || *failed.Counts.Failed != 1 {
		t.Fatalf("failed status activity: %+v", failed)
	}
}

func TestGetCapabilitiesFFmpegBranch(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg
	srv := New(testStore(t), "v")
	resp, _ := srv.GetCapabilities(context.Background(), api.GetCapabilitiesRequestObject{})
	r := resp.(api.GetCapabilities200JSONResponse)
	if len(r.TranscodeFormats) != 1 || r.TranscodeFormats[0] != "original" {
		t.Fatalf("without ffmpeg want [original], got %v", r.TranscodeFormats)
	}
	if r.Acquisition || r.Lastfm {
		t.Fatalf("integrations must be false: %+v", r)
	}
}

func TestUnimplementedInheritance(t *testing.T) {
	srv := New(testStore(t), "v")
	_, err := srv.AdminStartPlexPin(context.Background(), api.AdminStartPlexPinRequestObject{})
	if err != api.ErrNotImplemented {
		t.Fatalf("unoverridden ops must flow to Unimplemented, got %v", err)
	}
}
