package enrich

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func testMusicBrainzClient(t *testing.T, st *store.Store, baseURL string) *musicbrainz.Client {
	t.Helper()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: baseURL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: st, Interval: time.Nanosecond})
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
	var providerURL string
	providers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/release-group/rg-order/front-500":
			w.WriteHeader(http.StatusNotFound)
		case "/music/albums/rg-order":
			_, _ = w.Write([]byte(`{"albums":[{"release_group_id":"rg-order","albumcover":[{"url":"` + providerURL + `/fanart-image"}]}]}`))
		case "/fanart-image":
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(`{"album":{"image":[]}}`))
		}
	}))
	defer providers.Close()
	providerURL = providers.URL

	e := New(st, nil, t.TempDir(), Config{
		FanartKey: func(context.Context) string { return "fanart-key" },
		LastfmKey: func(context.Context) string { return "lastfm-key" },
	})
	e.CAABase, e.FanartBase, e.LastfmBase = providers.URL, providers.URL, providers.URL
	e.albumArtForReleaseGroup(ctx, "rg-order", "artist", "album")
	want := []string{"/release-group/rg-order/front-500", "/music/albums/rg-order", "/fanart-image", "/"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("provider calls=%v want %v", calls, want)
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
		FanartKey:   func(context.Context) string { return "test-key" },
		MusicBrainz: testMusicBrainzClient(t, st, mb.URL),
	})
	e.FanartBase = fanart.URL
	e.Run(ctx)

	if need, _ := st.ArtistsNeedingArt(ctx, 10); len(need) != 0 {
		t.Fatalf("artist should have a photo (or be tried): %d remain", len(need))
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
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if applied, err := st.SetAlbumArt(ctx, needs[0].AlbumID, newerID, 50); err != nil || !applied {
			t.Errorf("attach newer art applied=%v err=%v", applied, err)
		}
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
