package httpserver_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/source"
)

func TestContractChangesDelta(t *testing.T) {
	ts, st, tok := setup(t)
	ctx := context.Background()
	c, _ := api.NewClientWithResponses(ts.URL)

	// Empty library: version 0.
	lib, err := c.GetLibraryWithResponse(ctx, bearer(tok))
	if err != nil || lib.JSON200 == nil {
		t.Fatalf("get library: %v", err)
	}
	if lib.JSON200.Version != 0 {
		t.Fatalf("empty library version=%d want 0", lib.JSON200.Version)
	}

	// Index one track (artist + album minted with it).
	seq, _ := st.NextScanSeq(ctx)
	if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{
		NativeID: "n1", Title: "T1", PrimaryArtist: source.ArtistReference{Name: "A"}, TrackCredits: []source.ArtistCredit{{Name: "A"}}, AlbumCredits: []source.ArtistCredit{{Name: "A"}}, Album: "Al",
		Container: "flac", Codec: "flac", DurationMs: 1000, Version: 1,
	}, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}

	// since=0 bootstraps the whole catalog; version advances to 1.
	all, err := c.ListChangesWithResponse(ctx, &api.ListChangesParams{}, bearer(tok))
	if err != nil || all.JSON200 == nil {
		t.Fatalf("changes since 0: %v (status %d)", err, all.StatusCode())
	}
	got := all.JSON200
	if len(got.Artists) != 1 || len(got.Albums) != 1 || len(got.Tracks) != 1 {
		t.Fatalf("delta counts artists=%d albums=%d tracks=%d want 1/1/1", len(got.Artists), len(got.Albums), len(got.Tracks))
	}
	if got.Version != 1 {
		t.Fatalf("version=%d want 1", got.Version)
	}
	if len(got.RemovedTrackIds) != 0 {
		t.Fatalf("unexpected removals %v", got.RemovedTrackIds)
	}
	if len(got.Tracks[0].ArtistCredits) != 1 || got.Tracks[0].ArtistCredits[0].Name != "A" || got.Tracks[0].PrimaryArtist.Name != "A" || len(got.Albums[0].ArtistCredits) != 1 {
		t.Fatalf("structured credit JSON: track=%+v album=%+v", got.Tracks[0], got.Albums[0])
	}

	// A caught-up client (since=version) gets an empty delta.
	since := got.Version
	none, err := c.ListChangesWithResponse(ctx, &api.ListChangesParams{Since: &since}, bearer(tok))
	if err != nil || none.JSON200 == nil {
		t.Fatalf("changes since version: %v", err)
	}
	if len(none.JSON200.Artists)+len(none.JSON200.Albums)+len(none.JSON200.Tracks) != 0 {
		t.Fatalf("since=version returned changes: %+v", none.JSON200)
	}

	// Auth is required.
	unauth, _ := c.ListChangesWithResponse(ctx, &api.ListChangesParams{})
	if unauth.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d want 401", unauth.StatusCode())
	}
}
