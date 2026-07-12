package enrich

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func seedAlbum(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{
		NativeID: "n1", Title: "Song", Artist: "The Band", AlbumArtist: "The Band",
		Album: "Great Album", Container: "flac", Codec: "flac", Version: 1,
	}, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n---fake-image-data---")

func TestEnrichAlbumArtFromCoverArtArchive(t *testing.T) {
	st, ctx := seedAlbum(t)

	if need, _ := st.AlbumsNeedingArt(ctx, 10); len(need) != 1 {
		t.Fatalf("expected 1 album needing art, got %d", len(need))
	}

	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "release-group") {
			_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-1"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"artists":[]}`))
		}
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/release-group/rg-1/front-500" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		w.WriteHeader(404)
	}))
	defer caa.Close()

	e := New(st, nil, t.TempDir(), Config{})
	e.MBBase, e.CAABase = mb.URL, caa.URL
	e.mbInterval = 0
	e.Run(ctx)

	if need, _ := st.AlbumsNeedingArt(ctx, 10); len(need) != 0 {
		t.Fatalf("album still needs art after enrichment: %d", len(need))
	}
}

func TestEnrichMarksTriedWhenNothingFound(t *testing.T) {
	st, ctx := seedAlbum(t)

	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"release-groups":[],"artists":[]}`))
	}))
	defer mb.Close()

	e := New(st, nil, t.TempDir(), Config{})
	e.MBBase = mb.URL
	e.mbInterval = 0
	e.Run(ctx)

	// No art found, but it must be marked tried so we don't re-query forever.
	if need, _ := st.AlbumsNeedingArt(ctx, 10); len(need) != 0 {
		t.Fatalf("album should be marked tried, still listed: %d", len(need))
	}
}

func TestEnrichArtistPhotoFromFanart(t *testing.T) {
	st, ctx := seedAlbum(t)

	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "artist") {
			_, _ = w.Write([]byte(`{"artists":[{"id":"ar-1"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"release-groups":[]}`))
		}
	}))
	defer mb.Close()
	// The image server fanart.tv points at.
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(pngBytes)
	}))
	defer img.Close()
	fanart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/music/ar-1") {
			_, _ = w.Write([]byte(`{"artistthumb":[{"url":"` + img.URL + `/pic.jpg"}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer fanart.Close()

	e := New(st, nil, t.TempDir(), Config{
		FanartKey: func(context.Context) string { return "test-key" },
	})
	e.MBBase, e.FanartBase = mb.URL, fanart.URL
	e.mbInterval = 0
	e.Run(ctx)

	if need, _ := st.ArtistsNeedingArt(ctx, 10); len(need) != 0 {
		t.Fatalf("artist should have a photo (or be tried): %d remain", len(need))
	}
}

func TestMusicBrainzRateWaitHonorsCancellation(t *testing.T) {
	st, _ := seedAlbum(t)
	e := New(st, nil, t.TempDir(), Config{})
	e.mbInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enricher ignored cancellation during MusicBrainz rate wait")
	}
}

func TestAlbumEnrichmentDoesNotOverwriteArtAttachedDuringLookup(t *testing.T) {
	st, ctx := seedAlbum(t)
	needs, _ := st.AlbumsNeedingArt(ctx, 1)
	newerID, err := st.UpsertArt(ctx, "newer-race", "image/jpeg", []byte("newer"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if applied, err := st.SetAlbumArt(ctx, needs[0].AlbumID, newerID, 50); err != nil || !applied {
			t.Errorf("attach newer art applied=%v err=%v", applied, err)
		}
		_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-race"}]}`))
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer caa.Close()
	e := New(st, nil, t.TempDir(), Config{})
	e.MBBase, e.CAABase, e.mbInterval = mb.URL, caa.URL, 0
	e.Run(ctx)
	album, found, err := st.GetAlbum(ctx, needs[0].AlbumID)
	if err != nil || !found || album.ArtID != newerID {
		t.Fatalf("newer art overwritten: found=%v art=%q err=%v", found, album.ArtID, err)
	}
}
