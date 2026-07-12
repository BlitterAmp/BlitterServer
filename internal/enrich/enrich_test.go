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
	e.mb = time.NewTicker(time.Millisecond).C // don't wait a real second in tests
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
	e.mb = time.NewTicker(time.Millisecond).C
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
	e.mb = time.NewTicker(time.Millisecond).C
	e.Run(ctx)

	if need, _ := st.ArtistsNeedingArt(ctx, 10); len(need) != 0 {
		t.Fatalf("artist should have a photo (or be tried): %d remain", len(need))
	}
}
