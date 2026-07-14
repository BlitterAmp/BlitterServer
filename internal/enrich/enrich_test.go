package enrich

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/providercache"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func testMusicBrainzClient(t *testing.T, st *store.Store, baseURL string) *musicbrainz.Client {
	t.Helper()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: baseURL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

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
		NativeID: "n1", Title: "Song", PrimaryArtist: source.ArtistReference{Name: "The Band"}, TrackCredits: []source.ArtistCredit{{Name: "The Band"}}, AlbumCredits: []source.ArtistCredit{{Name: "The Band"}},
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
			_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-1","title":"Great Album"}]}`))
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

	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.CAABase = caa.URL
	e.Run(ctx)

	if need, _ := st.AlbumsNeedingArt(ctx, 10); len(need) != 0 {
		t.Fatalf("album still needs art after enrichment: %d", len(need))
	}
}

func TestFanartAlbumImageSelectsExactReleaseGroupAlbumCover(t *testing.T) {
	st, ctx := seedAlbum(t)
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer img.Close()

	var requestURL *url.URL
	fanart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL = r.URL
		_, _ = w.Write([]byte(`{"albums":[` +
			`{"release_group_id":"other","albumcover":[{"url":"` + img.URL + `/wrong"}]},` +
			`{"release_group_id":"rg-exact","cdart":[{"url":"` + img.URL + `/disc"}],"albumcover":[{"url":"` + img.URL + `/cover"}]}` +
			`]}`))
	}))
	defer fanart.Close()

	e := New(st, nil, t.TempDir(), Config{FanartKey: func(context.Context) string { return "key + value" }})
	e.FanartBase = fanart.URL
	data, _ := e.fanartAlbumArt(ctx, "key + value", "rg-exact")
	if string(data) != "/cover" {
		t.Fatalf("selected art=%q", data)
	}
	if requestURL.Path != "/music/albums/rg-exact" || requestURL.Query().Get("api_key") != "key + value" {
		t.Fatalf("fanart request URL=%q", requestURL.String())
	}
}

func TestFanartAlbumImageDoesNotFallback(t *testing.T) {
	st, ctx := seedAlbum(t)
	tests := []struct {
		name string
		body string
	}{
		{name: "different release group", body: `{"albums":[{"release_group_id":"other","albumcover":[{"url":"https://example.test/wrong"}]}]}`},
		{name: "cdart only", body: `{"albums":[{"release_group_id":"rg-exact","cdart":[{"url":"https://example.test/disc"}]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fanart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer fanart.Close()
			e := New(st, nil, t.TempDir(), Config{})
			e.FanartBase = fanart.URL
			if data, mime := e.fanartAlbumArt(ctx, "key", "rg-exact"); data != nil || mime != "" {
				t.Fatalf("unexpected fallback data=%q mime=%q", data, mime)
			}
		})
	}
}

func TestAlbumArtProviderOrder(t *testing.T) {
	st, ctx := seedAlbum(t)
	var calls []string
	providers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`{"album":{"image":[]}}`))
		case "/database/search":
			_, _ = w.Write([]byte(`{"results":[{"id":9,"type":"master","title":"artist - album"}]}`))
		case "/masters/9":
			_, _ = w.Write([]byte(`{"title":"album","artists":[{"name":"artist"}],"images":[]}`))
		case "/music/albums/rg-order":
			_, _ = w.Write([]byte(`{"albums":[{"release_group_id":"rg-order","albumcover":[]}]}`))
		case "/release-group/rg-order/front-500":
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer providers.Close()

	e := New(st, nil, t.TempDir(), Config{
		FanartKey:        func(context.Context) string { return "fanart-key" },
		LastfmKey:        func(context.Context) string { return "lastfm-key" },
		DiscogsToken:     func(context.Context) string { return "discogs-token" },
		DiscogsUserAgent: testDiscogsUA,
	})
	e.CAABase, e.FanartBase, e.LastfmBase, e.DiscogsBase = providers.URL, providers.URL, providers.URL, providers.URL
	e.providerPacers = map[string]*providerPacer{}
	e.albumArtOutcome(ctx, "rg-order", "artist", "album")
	want := []string{"/", "/database/search", "/masters/9", "/music/albums/rg-order", "/release-group/rg-order/front-500"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("provider calls=%v want %v", calls, want)
	}
}

func TestArtistArtProviderOrder(t *testing.T) {
	st, ctx := seedAlbum(t)
	var calls []string
	providers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`{"artist":{"image":[]}}`))
		case "/database/search":
			_, _ = w.Write([]byte(`{"results":[{"id":7,"type":"artist","title":"artist"}]}`))
		case "/artists/7":
			_, _ = w.Write([]byte(`{"name":"artist","images":[]}`))
		case "/music/mbid":
			_, _ = w.Write([]byte(`{"artistthumb":[]}`))
		}
	}))
	defer providers.Close()

	e := New(st, nil, t.TempDir(), Config{
		LastfmKey:        func(context.Context) string { return "lastfm-key" },
		DiscogsToken:     func(context.Context) string { return "discogs-token" },
		DiscogsUserAgent: testDiscogsUA,
		FanartKey:        func(context.Context) string { return "fanart-key" },
	})
	e.LastfmBase, e.DiscogsBase, e.FanartBase = providers.URL, providers.URL, providers.URL
	e.providerPacers = map[string]*providerPacer{}
	e.artistArtOutcome(ctx, "artist", "mbid")
	want := []string{"/", "/database/search", "/artists/7", "/music/mbid"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("provider calls=%v want %v", calls, want)
	}
}

func TestAlbumArtDefersMusicBrainzUntilLastfmAndDiscogsMiss(t *testing.T) {
	st, ctx := seedAlbum(t)
	var calls []string
	providers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "provider:"+r.URL.Path)
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`{"album":{"image":[]}}`))
		case "/database/search":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case "/music/albums/rg-late":
			_, _ = w.Write([]byte(`{"albums":[]}`))
		case "/release-group/rg-late/front-500":
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer providers.Close()
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls = append(calls, "musicbrainz")
		_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-late","title":"album"}]}`))
	}))
	defer mb.Close()

	e := New(st, nil, t.TempDir(), Config{
		LastfmKey:        func(context.Context) string { return "lastfm-key" },
		DiscogsToken:     func(context.Context) string { return "discogs-token" },
		DiscogsUserAgent: testDiscogsUA,
		FanartKey:        func(context.Context) string { return "fanart-key" },
		MusicBrainz:      testMusicBrainzClient(t, st, mb.URL),
	})
	e.LastfmBase, e.DiscogsBase, e.FanartBase, e.CAABase = providers.URL, providers.URL, providers.URL, providers.URL
	e.providerPacers = map[string]*providerPacer{}
	e.albumArtOutcome(ctx, "", "artist", "album")
	want := []string{"provider:/", "provider:/database/search", "musicbrainz", "provider:/music/albums/rg-late", "provider:/release-group/rg-late/front-500"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls=%v want %v", calls, want)
	}
}

func TestProviderTransientsDoNotPreventLaterAlbumSuccess(t *testing.T) {
	st, ctx := seedAlbum(t)
	var base string
	providers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusInternalServerError)
		case "/database/search":
			w.WriteHeader(http.StatusForbidden)
		case "/music/albums/rg":
			_, _ = w.Write([]byte(`{"albums":[{"release_group_id":"rg","albumcover":[{"url":"` + base + `/fanart-image"}]}]}`))
		case "/fanart-image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		}
	}))
	defer providers.Close()
	base = providers.URL
	e := New(st, nil, t.TempDir(), Config{
		LastfmKey:        func(context.Context) string { return "lastfm-key" },
		DiscogsToken:     func(context.Context) string { return "discogs-token" },
		DiscogsUserAgent: testDiscogsUA,
		FanartKey:        func(context.Context) string { return "fanart-key" },
	})
	e.LastfmBase, e.DiscogsBase, e.FanartBase, e.CAABase = providers.URL, providers.URL, providers.URL, providers.URL
	e.providerPacers = map[string]*providerPacer{}
	if data, _, outcome := e.albumArtOutcome(ctx, "rg", "artist", "album"); data == nil || outcome != lookupSuccess {
		t.Fatalf("later fanart success data=%d outcome=%v", len(data), outcome)
	}
}

func TestLastfmAlbumGetInfoUsesAutocorrect(t *testing.T) {
	st, ctx := seedAlbum(t)
	var query url.Values
	lastfm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"album":{"image":[]}}`))
	}))
	defer lastfm.Close()
	e := New(st, nil, t.TempDir(), Config{})
	e.LastfmBase = lastfm.URL
	e.lastfmAlbumImage(ctx, "key", "artist", "album")
	if query.Get("autocorrect") != "1" {
		t.Fatalf("autocorrect=%q", query.Get("autocorrect"))
	}
}

func TestLastfmArtistGetInfoSelectsLargestImage(t *testing.T) {
	st, ctx := seedAlbum(t)
	var base string
	lastfm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artist.jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("artist-photo"))
			return
		}
		q := r.URL.Query()
		if q.Get("method") != "artist.getinfo" || q.Get("artist") != "The Band" || q.Get("autocorrect") != "1" || q.Get("format") != "json" {
			t.Fatalf("artist.getInfo query=%v", q)
		}
		_, _ = w.Write([]byte(`{"artist":{"image":[{"#text":"` + base + `/small.jpg","size":"small"},{"#text":"` + base + `/artist.jpg","size":"extralarge"}]}}`))
	}))
	defer lastfm.Close()
	base = lastfm.URL
	e := New(st, nil, t.TempDir(), Config{LastfmKey: func(context.Context) string { return "key" }})
	e.LastfmBase = lastfm.URL
	e.providerPacers = map[string]*providerPacer{}
	data, mime, outcome := e.lastfmArtistArtOutcome(ctx, "The Band")
	if string(data) != "artist-photo" || mime != "image/jpeg" || outcome != lookupSuccess {
		t.Fatalf("data=%q mime=%q outcome=%v", data, mime, outcome)
	}
}

func TestArtistArtDefersMusicBrainzUntilFanart(t *testing.T) {
	st, ctx := seedAlbum(t)
	var calls []string
	providers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "provider:"+r.URL.Path)
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`{"artist":{"image":[]}}`))
		case "/database/search":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case "/music/artist-id":
			_, _ = w.Write([]byte(`{"artistthumb":[]}`))
		}
	}))
	defer providers.Close()
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls = append(calls, "musicbrainz")
		_, _ = w.Write([]byte(`{"artists":[{"id":"artist-id","name":"Artist"}]}`))
	}))
	defer mb.Close()
	e := New(st, nil, t.TempDir(), Config{
		LastfmKey:        func(context.Context) string { return "lastfm-key" },
		DiscogsToken:     func(context.Context) string { return "discogs-token" },
		DiscogsUserAgent: testDiscogsUA,
		FanartKey:        func(context.Context) string { return "fanart-key" },
		MusicBrainz:      testMusicBrainzClient(t, st, mb.URL),
	})
	e.LastfmBase, e.DiscogsBase, e.FanartBase = providers.URL, providers.URL, providers.URL
	e.providerPacers = map[string]*providerPacer{}
	e.artistArtOutcome(ctx, "Artist", "")
	want := []string{"provider:/", "provider:/database/search", "musicbrainz", "provider:/music/artist-id"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls=%v want %v", calls, want)
	}
}

func TestArtistStageRunsWithLastfmOrDiscogsConfigured(t *testing.T) {
	for _, provider := range []string{"lastfm", "discogs"} {
		t.Run(provider, func(t *testing.T) {
			st, ctx := seedAlbum(t)
			var base string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					_, _ = w.Write([]byte(`{"artist":{"image":[{"#text":"` + base + `/image","size":"extralarge"}]}}`))
				case "/database/search":
					_, _ = w.Write([]byte(`{"results":[{"id":1,"type":"artist","title":"The Band"}]}`))
				case "/artists/1":
					_, _ = w.Write([]byte(`{"name":"The Band","images":[{"type":"primary","uri":"` + base + `/image"}]}`))
				case "/image":
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(pngBytes)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			base = srv.URL
			cfg := Config{DiscogsUserAgent: testDiscogsUA}
			if provider == "lastfm" {
				cfg.LastfmKey = func(context.Context) string { return "key" }
			} else {
				cfg.DiscogsToken = func(context.Context) string { return "token" }
			}
			e := New(st, nil, t.TempDir(), cfg)
			e.LastfmBase, e.DiscogsBase = srv.URL, srv.URL
			e.providerPacers = map[string]*providerPacer{}
			e.Run(ctx)
			artists, _, err := st.ListArtists(ctx, "title", "", 10)
			if err != nil || len(artists) != 1 || artists[0].ArtID == "" {
				t.Fatalf("artist stage did not attach %s art: artists=%+v err=%v", provider, artists, err)
			}
		})
	}
}

func TestEnrichMarksTriedWhenNothingFound(t *testing.T) {
	st, ctx := seedAlbum(t)

	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"release-groups":[],"artists":[]}`))
	}))
	defer mb.Close()

	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.Run(ctx)

	// No art found, but it must be marked tried so we don't re-query forever.
	if need, _ := st.AlbumsNeedingArt(ctx, 10); len(need) != 0 {
		t.Fatalf("album should be marked tried, still listed: %d", len(need))
	}
}

func TestEnrichArtistPhotoFromFanart(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{
		NativeID: "compilation/song.flac", Title: "Song", Album: "Compilation",
		PrimaryArtist: source.ArtistReference{Name: "Photo Artist"},
		AlbumCredits:  []source.ArtistCredit{{Name: "Compilation Owner"}},
		TrackCredits:  []source.ArtistCredit{{Name: "Photo Artist"}},
		Container:     "flac", Codec: "flac", Version: 1,
	}, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	artists, _, err := st.ListArtists(ctx, "title", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var artistID string
	for _, artist := range artists {
		if artist.Name == "Photo Artist" {
			artistID = artist.ArtistID
		}
	}
	if artistID == "" {
		t.Fatalf("photo artist missing: %+v", artists)
	}
	if albums, err := st.ListArtistAlbums(ctx, artistID); err != nil || len(albums) != 0 {
		t.Fatalf("photo artist unexpectedly owns albums: %+v err=%v", albums, err)
	}

	mbCalls := 0
	photoArtistMBCalls := 0
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artist" {
			mbCalls++
			if strings.Contains(r.URL.Query().Get("query"), "Photo Artist") {
				photoArtistMBCalls++
			}
			_, _ = w.Write([]byte(`{"artists":[{"id":"ar-1","name":" photo artist "}]}`))
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
	fanartCalls := 0
	fanart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/music/ar-1") {
			fanartCalls++
			_, _ = w.Write([]byte(`{"artistthumb":[{"url":"` + img.URL + `/pic.jpg"}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer fanart.Close()

	e := New(st, nil, t.TempDir(), Config{
		FanartKey:   func(context.Context) string { return "test-key" },
		MusicBrainz: testMusicBrainzClient(t, st, mb.URL),
	})
	e.FanartBase = fanart.URL
	e.providerPacers = map[string]*providerPacer{}
	e.Run(ctx)

	artist, found, err := st.GetArtist(ctx, artistID)
	if err != nil || !found || artist.ArtID == "" {
		t.Fatalf("artist photo not attached: found=%v artist=%+v err=%v", found, artist, err)
	}
	if mbCalls == 0 || photoArtistMBCalls != 1 || fanartCalls != 1 {
		t.Fatalf("provider calls: musicbrainz=%d photo_artist=%d fanart=%d", mbCalls, photoArtistMBCalls, fanartCalls)
	}
}

func TestLastfmSemanticTransientIsNotCached(t *testing.T) {
	st, ctx := seedAlbum(t)
	requests := 0
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/apex.jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(pngBytes)
			return
		}
		if requests == 1 {
			_, _ = w.Write([]byte(`{"error":11,"message":"Service Offline"}`))
			return
		}
		_, _ = w.Write([]byte(`{"album":{"image":[{"#text":"` + base + `/apex.jpg","size":"extralarge"}]}}`))
	}))
	defer srv.Close()
	base = srv.URL
	dataDir := t.TempDir()
	e := New(st, nil, filepath.Join(dataDir, "art"), Config{
		LastfmKey:     func(context.Context) string { return "secret" },
		ProviderCache: providercache.New(filepath.Join(dataDir, "provider-cache")),
	})
	e.LastfmBase = srv.URL
	e.providerPacers = map[string]*providerPacer{}

	if data, _, outcome := e.lastfmAlbumArtOutcome(ctx, "artist", "album"); data != nil || outcome != lookupTransient || requests != 1 {
		t.Fatalf("first lookup data=%d outcome=%v requests=%d", len(data), outcome, requests)
	}
	data, _, outcome := e.lastfmAlbumArtOutcome(ctx, "artist", "album")
	if data == nil || outcome != lookupSuccess || requests != 3 {
		t.Fatalf("second lookup data=%d outcome=%v requests=%d", len(data), outcome, requests)
	}
}

func TestMusicBrainzRateWaitHonorsCancellation(t *testing.T) {
	st, _ := seedAlbum(t)
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: "https://example.invalid", UserAgent: "BlitterServer/test (mailto:test@example.com)", Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: client})
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

func TestEnricherUsesInjectedProcessMusicBrainzClient(t *testing.T) {
	st, _ := seedAlbum(t)
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: "https://example.invalid", UserAgent: "BlitterServer/test (mailto:test@example.com)"})
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: client})
	if e.mbClient != client {
		t.Fatal("enricher did not retain injected MusicBrainz client")
	}
}

func TestProvider503RetryAfterRetriesOnce(t *testing.T) {
	st, ctx := seedAlbum(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	e := New(st, nil, t.TempDir(), Config{})
	var body struct {
		OK bool `json:"ok"`
	}
	if !e.getJSON(ctx, srv.URL, "", &body) || !body.OK || requests != 2 {
		t.Fatalf("retry result ok=%v requests=%d", body.OK, requests)
	}
}

func TestTransient503DoesNotConsumeArtworkMissTier(t *testing.T) {
	st, ctx := seedAlbum(t)
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	needs, err := st.AlbumsNeedingArtAt(ctx, now, 1)
	if err != nil || len(needs) != 1 {
		t.Fatalf("album need: %+v, %v", needs, err)
	}
	if err := st.MarkAlbumArtAttempt(ctx, needs[0].AlbumID, store.ArtAttemptMiss, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	e := New(st, nil, t.TempDir(), Config{})
	e.CAABase = srv.URL
	e.RunAt(ctx, now)
	misses, next, err := st.ArtRetryState(ctx, false, needs[0].AlbumID)
	if err != nil {
		t.Fatal(err)
	}
	if misses != 1 || !next.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("503 state miss=%d next=%s", misses, next)
	}
}

func TestFreshProviderCacheBypassesPacer(t *testing.T) {
	st, ctx := seedAlbum(t)
	dataDir := t.TempDir()
	cache := providercache.New(filepath.Join(dataDir, "provider-cache"))
	requestURL := "https://coverartarchive.org/release-group/rg/front-500"
	key, err := providercache.CanonicalKey(http.MethodGet, requestURL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := cache.Put("caa", key, providercache.Entry{URL: key, Status: http.StatusNotFound, FetchedAt: now, FreshUntil: now.Add(time.Hour), Kind: providercache.KindMiss}); err != nil {
		t.Fatal(err)
	}
	e := New(st, nil, filepath.Join(dataDir, "art"), Config{ProviderCache: cache})
	e.providerPacers["caa"] = newProviderPacer(time.Hour)
	if data, _ := e.fetch(ctx, "caa", requestURL, ""); data != nil {
		t.Fatal("cached miss returned art")
	}
	if got := e.providerPacers["caa"].WaitCount(); got != 0 {
		t.Fatalf("cache hit entered pacer %d times", got)
	}
}

func TestCachedLastfmCodeSixSuccessIsRewrittenAsSevenDayMiss(t *testing.T) {
	st, ctx := seedAlbum(t)
	dataDir := t.TempDir()
	cache := providercache.New(filepath.Join(dataDir, "provider-cache"))
	requestURL := "https://ws.audioscrobbler.com/2.0/?method=album.getinfo&artist=Missing&album=Missing&api_key=secret&format=json"
	key, err := providercache.CanonicalKey(http.MethodGet, requestURL)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := cache.Put("lastfm", key, providercache.Entry{
		URL: key, Status: http.StatusOK, FetchedAt: now, FreshUntil: now.Add(30 * 24 * time.Hour),
		Kind: providercache.KindSuccess, Body: []byte(`{"error":6,"message":"Album not found"}`),
	}); err != nil {
		t.Fatal(err)
	}
	e := New(st, nil, filepath.Join(dataDir, "art"), Config{ProviderCache: cache})
	e.LastfmBase = "https://ws.audioscrobbler.com"
	var body any
	before := time.Now()
	if outcome := e.getJSONOutcome(ctx, requestURL, "", &body); outcome != lookupMiss {
		t.Fatalf("outcome=%v", outcome)
	}
	entry, ok := cache.Get("lastfm", key)
	if !ok || entry.Kind != providercache.KindMiss || entry.Status != http.StatusOK {
		t.Fatalf("rewritten entry=%+v ok=%v", entry, ok)
	}
	if entry.FreshUntil.Before(before.Add(7*24*time.Hour-time.Minute)) || entry.FreshUntil.After(time.Now().Add(7*24*time.Hour+time.Minute)) {
		t.Fatalf("FreshUntil=%s", entry.FreshUntil)
	}
}

func TestFreshProviderCacheAvoidsMetadataAndImageRequests(t *testing.T) {
	st, ctx := seedAlbum(t)
	requests := 0
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/music/albums/rg-cache":
			_, _ = w.Write([]byte(`{"albums":[{"release_group_id":"rg-cache","albumcover":[{"url":"` + base + `/image"}]}]}`))
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base = srv.URL
	dataDir := t.TempDir()
	e := New(st, nil, filepath.Join(dataDir, "art"), Config{ProviderCache: providercache.New(filepath.Join(dataDir, "provider-cache"))})
	e.FanartBase = srv.URL
	data, mime := e.fanartAlbumArt(ctx, "top-secret", "rg-cache")
	if data == nil {
		t.Fatal("first lookup missed")
	}
	if _, err := e.store(ctx, data, mime); err != nil {
		t.Fatal(err)
	}
	if data, _ := e.fanartAlbumArt(ctx, "top-secret", "rg-cache"); data == nil || requests != 2 {
		t.Fatalf("cached lookup data=%d requests=%d", len(data), requests)
	}
	if err := filepath.Walk(filepath.Join(dataDir, "provider-cache"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(body), "top-secret") || strings.Contains(string(body), string(pngBytes)) {
				t.Errorf("provider cache leaked credential or image bytes in %s", path)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderCacheRepopulatesArtAfterDatabaseResetWithoutNetworkOrRewrite(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	seed := func() *store.Store {
		st, err := store.Open(ctx, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		seq, _ := st.NextScanSeq(ctx)
		if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{NativeID: "n1", Title: "Song", PrimaryArtist: source.ArtistReference{Name: "The Band"}, TrackCredits: []source.ArtistCredit{{Name: "The Band"}}, AlbumCredits: []source.ArtistCredit{{Name: "The Band"}}, Album: "Great Album", Container: "flac", Codec: "flac", Version: 1}, "", seq); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
			t.Fatal(err)
		}
		return st
	}
	requests := 0
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/image" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		_, _ = w.Write([]byte(`{"album":{"image":[{"#text":"` + base + `/image","size":"large"}]}}`))
	}))
	base = srv.URL
	cache := providercache.New(filepath.Join(dataDir, "provider-cache"))
	st := seed()
	e := New(st, nil, filepath.Join(dataDir, "art"), Config{LastfmKey: func(context.Context) string { return "secret" }, ProviderCache: cache})
	e.LastfmBase = srv.URL
	e.Run(ctx)
	if requests != 3 {
		t.Fatalf("first run requests=%d", requests)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "art"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("art entries=%d err=%v", len(entries), err)
	}
	artPath := filepath.Join(dataDir, "art", entries[0].Name())
	before, _ := os.Stat(artPath)
	st.Close()
	if err := os.Remove(filepath.Join(dataDir, "blitterserver.db")); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	st = seed()
	t.Cleanup(func() { st.Close() })
	e = New(st, nil, filepath.Join(dataDir, "art"), Config{LastfmKey: func(context.Context) string { return "secret" }, ProviderCache: cache})
	e.LastfmBase = base
	e.Run(ctx)
	needs, err := st.AlbumsNeedingArt(ctx, 1)
	if err != nil || len(needs) != 0 {
		t.Fatalf("art row was not restored: needs=%d err=%v", len(needs), err)
	}
	after, _ := os.Stat(artPath)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("art blob rewritten: before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

func TestLibraryChangedOnlyWhenArtIsApplied(t *testing.T) {
	st, ctx := seedAlbum(t)
	bus := events.NewBus(st)
	sub, cancel := bus.Subscribe("", 0)
	defer cancel()
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"release-groups":[]}`))
	}))
	defer mb.Close()
	e := New(st, bus, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.Run(ctx)
	select {
	case event := <-sub:
		t.Fatalf("timestamp-only attempt published %s", event.Type)
	default:
	}
}

func TestLibraryChangedPublishedExactlyOnceForAppliedArt(t *testing.T) {
	st, ctx := seedAlbum(t)
	bus := events.NewBus(st)
	sub, cancel := bus.Subscribe("", 0)
	defer cancel()
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-event","title":"Great Album"}]}`))
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer caa.Close()
	e := New(st, bus, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.CAABase = caa.URL
	e.Run(ctx)
	select {
	case event := <-sub:
		if event.Type != "library.changed" {
			t.Fatalf("event type=%q", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("applied art did not publish library.changed")
	}
	select {
	case event := <-sub:
		t.Fatalf("extra event %s", event.Type)
	default:
	}
}

func TestAlbumEnrichmentDoesNotOverwriteArtAttachedDuringLookup(t *testing.T) {
	st, ctx := seedAlbum(t)
	needs, _ := st.AlbumsNeedingArt(ctx, 1)
	newerID, err := st.UpsertArt(ctx, "newer-race", "image/jpeg", []byte("newer"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var attachOnce sync.Once
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The art stage and the resolver both consult MusicBrainz now; the
		// mid-lookup attach must happen exactly once.
		attachOnce.Do(func() {
			if applied, err := st.SetAlbumArt(ctx, needs[0].AlbumID, newerID, 50); err != nil || !applied {
				t.Errorf("attach newer art applied=%v err=%v", applied, err)
			}
		})
		_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-race","title":"Great Album"}]}`))
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer caa.Close()
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.CAABase = caa.URL
	e.Run(ctx)
	album, found, err := st.GetAlbum(ctx, needs[0].AlbumID)
	if err != nil || !found || album.ArtID != newerID {
		t.Fatalf("newer art overwritten: found=%v art=%q err=%v", found, album.ArtID, err)
	}
}

func TestCommittedIdentityPublishesEventWhenLaterArtWorkIsCanceled(t *testing.T) {
	st, ctx := seedAlbum(t)
	seq, _ := st.NextScanSeq(ctx)
	if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{NativeID: "n1", Title: "Song", Index: 1, Disc: 1, PrimaryArtist: source.ArtistReference{Name: "The Band"}, TrackCredits: []source.ArtistCredit{{Name: "The Band"}}, AlbumCredits: []source.ArtistCredit{{Name: "The Band"}}, Album: "Great Album", ReleaseMBID: "resolved", Container: "flac", Codec: "flac", Version: 1}, "", seq); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(st)
	sub, unsubscribe := bus.Subscribe("", 0)
	defer unsubscribe()
	artStarted := make(chan struct{})
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/release/resolved" {
			_, _ = w.Write([]byte(`{"id":"resolved","title":"Great Album","release-group":{"id":"group"},"artist-credit":[{"name":"The Band","artist":{"id":"artist","name":"The Band"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"recording","title":"Song","artist-credit":[{"name":"The Band","artist":{"id":"artist","name":"The Band"}}]}}]}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(artStarted); <-r.Context().Done() }))
	defer caa.Close()
	e := New(st, bus, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.CAABase = caa.URL
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { e.Run(runCtx); close(done) }()
	<-artStarted
	cancel()
	<-done
	select {
	case event := <-sub:
		if event.Type != "library.changed" {
			t.Fatalf("event=%s", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("committed identity change was not published")
	}
	select {
	case event := <-sub:
		t.Fatalf("duplicate event %s", event.Type)
	default:
	}
}

func lastfmArtServer(t *testing.T, hit func()) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hit != nil {
			hit()
		}
		if r.URL.Path == "/img.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		_, _ = w.Write([]byte(`{"album":{"image":[{"#text":"` + srv.URL + `/img.png","size":"extralarge"}]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedArtlessAlbums(t *testing.T, st *store.Store, ctx context.Context, n int) {
	t.Helper()
	seq, _ := st.NextScanSeq(ctx)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Band %04d", i)
		if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{
			NativeID: fmt.Sprintf("bulk-%04d", i), Title: "Song", PrimaryArtist: source.ArtistReference{Name: name},
			TrackCredits: []source.ArtistCredit{{Name: name}}, AlbumCredits: []source.ArtistCredit{{Name: name}},
			Album: fmt.Sprintf("Album %04d", i), Container: "flac", Codec: "flac", Version: 1,
		}, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
}

// A fresh library must not wait behind a full identity drain for artwork:
// the art stage runs before the resolver in every pass.
func TestArtFetchedBeforeIdentityResolution(t *testing.T) {
	st, ctx := seedAlbum(t)
	var mu sync.Mutex
	var order []string
	record := func(kind string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, kind)
	}

	// Second album: matched with a release group, artless -> CAA path.
	seq, _ := st.NextScanSeq(ctx)
	if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{
		NativeID: "m1", Title: "Hit", PrimaryArtist: source.ArtistReference{Name: "Matched Band"},
		TrackCredits: []source.ArtistCredit{{Name: "Matched Band"}}, AlbumCredits: []source.ArtistCredit{{Name: "Matched Band"}},
		Album: "Matched Album", Container: "flac", Codec: "flac", Version: 1,
	}, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueMusicBrainzAlbums(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, album := range due {
		if album.Title != "Matched Album" {
			continue
		}
		credits := []source.ArtistCredit{{Name: "Matched Band", MBID: "mbid-matched"}}
		release := store.CanonicalRelease{ReleaseID: "rel-m", ReleaseGroupID: "rg-m", AlbumCredits: credits}
		for _, track := range album.Tracks {
			release.Tracks = append(release.Tracks, store.CanonicalTrack{Disc: track.Disc, Index: track.Index, Title: track.Title, DurationMs: track.DurationMs, RecordingID: "rec-m", Credits: credits})
		}
		applySeq, _ := st.NextScanSeq(ctx)
		if _, err := st.ApplyMusicBrainzRelease(ctx, album, release, applySeq); err != nil {
			t.Fatal(err)
		}
	}

	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("resolve")
		_, _ = w.Write([]byte(`{"releases":[],"release-groups":[],"artists":[]}`))
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("art")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer caa.Close()

	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.CAABase = caa.URL
	e.providerPacers = map[string]*providerPacer{}
	e.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	firstArt, firstResolve := -1, -1
	for i, kind := range order {
		if kind == "art" && firstArt == -1 {
			firstArt = i
		}
		if kind == "resolve" && firstResolve == -1 {
			firstResolve = i
		}
	}
	if firstArt == -1 || (firstResolve != -1 && firstArt > firstResolve) {
		t.Fatalf("artwork must be fetched before identity resolution: order=%v", order)
	}
}

// One pass drains every eligible artless album, not just the first page.
func TestAlbumArtStageDrainsBeyondPerRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const albums = perRun + 10
	seedArtlessAlbums(t, st, ctx, albums)
	srv := lastfmArtServer(t, nil)
	e := New(st, nil, t.TempDir(), Config{LastfmKey: func(context.Context) string { return "k" }})
	e.LastfmBase = srv.URL
	e.providerPacers = map[string]*providerPacer{}
	e.Run(ctx)
	need, err := st.AlbumsNeedingArt(ctx, albums)
	if err != nil {
		t.Fatal(err)
	}
	if len(need) != 0 {
		t.Fatalf("%d albums still artless after one pass", len(need))
	}
}

// Long passes publish intermediate library.changed events so clients see
// progress instead of a frozen library until the pass ends.
func TestProgressPublishesDuringLongPasses(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seedArtlessAlbums(t, st, ctx, 3)
	bus := events.NewBus(st)
	sub, cancel := bus.Subscribe("", 0)
	defer cancel()
	srv := lastfmArtServer(t, nil)
	e := New(st, bus, t.TempDir(), Config{LastfmKey: func(context.Context) string { return "k" }})
	e.LastfmBase = srv.URL
	e.providerPacers = map[string]*providerPacer{}
	e.ProgressInterval = 0 // publish on every progress point
	e.Run(ctx)
	events := 0
	for {
		select {
		case ev := <-sub:
			if ev.Type == "library.changed" {
				events++
			}
			continue
		default:
		}
		break
	}
	if events < 2 {
		t.Fatalf("expected intermediate progress events, got %d", events)
	}
}

// A zero artwork slice yields to identity matching immediately; the full
// artwork stage after the drain still attaches everything in the same pass.
func TestZeroArtSliceRunsIdentityBeforeArtwork(t *testing.T) {
	st, ctx := seedAlbum(t)
	var mu sync.Mutex
	var order []string
	record := func(kind string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, kind)
	}
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("resolve")
		_, _ = w.Write([]byte(`{"releases":[],"release-groups":[],"artists":[]}`))
	}))
	defer mb.Close()
	srv := lastfmArtServer(t, func() { record("art") })
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL), LastfmKey: func(context.Context) string { return "k" }})
	e.LastfmBase = srv.URL
	e.providerPacers = map[string]*providerPacer{}
	e.ArtSliceBudget = 0
	e.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	firstArt, firstResolve := -1, -1
	for i, kind := range order {
		if kind == "art" && firstArt == -1 {
			firstArt = i
		}
		if kind == "resolve" && firstResolve == -1 {
			firstResolve = i
		}
	}
	if firstResolve == -1 || firstArt == -1 || firstResolve > firstArt {
		t.Fatalf("zero slice must resolve identity before artwork: order=%v", order)
	}
	need, err := st.AlbumsNeedingArt(ctx, 10)
	if err != nil || len(need) != 0 {
		t.Fatalf("post-drain artwork stage must still run: %v err=%v", need, err)
	}
}
