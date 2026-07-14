package server

import (
	"context"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/library"
)

func TestAdminDeleteFilesystemSourceMarksCatalogMissing(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	seedLibrary(t, st)
	if _, _, err := st.ConfigureFilesystemSource(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetLibrarySummary(ctx)
	if err != nil || before.Tracks != 3 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	mgr := library.NewManager(st, t.TempDir())
	t.Cleanup(mgr.Close)
	srv := NewWithLibrary(st, mgr, "test")
	resp, err := srv.AdminDeleteFilesystemSource(ctx, api.AdminDeleteFilesystemSourceRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(api.AdminDeleteFilesystemSource204Response); !ok {
		t.Fatalf("response=%T", resp)
	}
	tracks, err := srv.ListTracks(ctx, api.ListTracksRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if page := tracks.(api.ListTracks200JSONResponse); len(page.Items) != 0 {
		t.Fatalf("unlink left tracks visible: %+v", page.Items)
	}
	changes, _, err := st.ChangesSince(ctx, before.Version, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	missingTracks := 0
	for _, change := range changes {
		if change.Kind == "track" && change.Missing {
			missingTracks++
		}
	}
	if missingTracks != 3 {
		t.Fatalf("unlink track removals=%d changes=%+v", missingTracks, changes)
	}
}
