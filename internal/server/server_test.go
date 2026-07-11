package server

import (
	"context"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/api"
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
	_, err := srv.GetHome(context.Background(), api.GetHomeRequestObject{})
	if err != api.ErrNotImplemented {
		t.Fatalf("unoverridden ops must flow to Unimplemented, got %v", err)
	}
}
