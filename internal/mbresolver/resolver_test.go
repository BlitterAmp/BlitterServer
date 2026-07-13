package mbresolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/providercache"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func TestScoreReleaseRequiresUniqueHighConfidenceCandidate(t *testing.T) {
	local := store.MusicBrainzAlbum{Title: "Kind of Blue", Year: 1959, TrackCount: 1, PrimaryArtist: store.ArtistCreditRow{Name: "Miles Davis"}, Tracks: []store.TrackRow{{Disc: 1, Index: 1, Title: "So What", DurationMs: 545000}}}
	var exact release
	exact.ID = "release-1"
	exact.Title = "Kind of Blue"
	exact.Date = "1959-08-17"
	exact.Credits = []artistCredit{{Name: "Miles Davis"}}
	exact.Media = []medium{{Position: 1, Tracks: []track{{Position: 1, Recording: recording{Title: "So What", Length: 545500}}}}}
	score, evidence := scoreRelease(local, exact)
	if score < acceptScore || evidence["trackEvidence"] != 1 {
		t.Fatalf("score=%v evidence=%v", score, evidence)
	}
	near := exact
	near.ID = "release-2"
	near.Date = "1960"
	near.Media[0].Tracks[0].Recording.Title = "Other"
	runner, _ := scoreRelease(local, near)
	if score-runner < acceptMargin {
		t.Fatalf("margin=%v", score-runner)
	}
}

func TestRunnerUpAmbiguityThreshold(t *testing.T) {
	if 90.0-85.0 >= acceptMargin {
		t.Fatal("close runner-up must remain ambiguous")
	}
}

func TestSearchRejectsLoneWeakCandidateAndIndistinguishableReissue(t *testing.T) {
	local := store.MusicBrainzAlbum{Title: "Same", Year: 2000, TrackCount: 2, PrimaryArtist: store.ArtistCreditRow{Name: "Artist"}, Tracks: []store.TrackRow{{Disc: 1, Index: 1, Title: "One", DurationMs: 1000}, {Disc: 1, Index: 2, Title: "Two", DurationMs: 2000}}}
	var weak release
	weak.Title, weak.Date, weak.Credits = "Same", "2000", []artistCredit{{Name: "Artist"}}
	weak.Media = []medium{{Position: 1, TrackCount: 2}}
	score, evidence := scoreRelease(local, weak)
	if strongSearchMatch(local, scoredRelease{release: weak, score: score, evidence: evidence}) {
		t.Fatal("lone title/artist/year candidate waived track evidence")
	}
	exact := weak
	exact.Media = []medium{{Position: 1, Tracks: []track{{Position: 1, Recording: recording{Title: "One", Length: 1000}}, {Position: 2, Recording: recording{Title: "Two", Length: 2000}}}}}
	a, ae := scoreRelease(local, exact)
	b, be := scoreRelease(local, exact)
	if !strongSearchMatch(local, scoredRelease{release: exact, score: a, evidence: ae}) || a-b >= acceptMargin || !strongSearchMatch(local, scoredRelease{release: exact, score: b, evidence: be}) {
		t.Fatal("indistinguishable same-title reissues must remain ambiguous by margin")
	}
}

func TestEmbeddedReleaseIDUsesDirectLookup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seq, _ := st.NextScanSeq(ctx)
	meta := source.TrackMeta{NativeID: "one", Title: "Local spelling", Album: "Local title", Index: 1, Disc: 1, DurationMs: 1000, ReleaseMBID: "embedded-release", PrimaryArtist: source.ArtistReference{Name: "Local"}, AlbumCredits: []source.ArtistCredit{{Name: "Local"}}, TrackCredits: []source.ArtistCredit{{Name: "Local"}}, Container: "flac", Codec: "flac", Version: 1}
	if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"embedded-release","title":"Canonical title","release-group":{"id":"group"},"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"recording","title":"Localized canonical title","length":9000,"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}]}}]}]}`))
	}))
	defer srv.Close()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	r := New(st, client)
	r.now = func() time.Time { return time.Unix(1000, 0) }
	changed, err := r.Run(ctx)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.HasSuffix(path, "/release/embedded-release") {
		t.Fatalf("path=%q", path)
	}
	tracks, _, err := st.ListTracks(ctx, "title", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].MusicBrainzRecordingID != "recording" {
		t.Fatalf("tracks=%+v", tracks)
	}
}

func TestUntaggedSearchFetchesBoundedDetailsBeforeStrongMatch(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seq, _ := st.NextScanSeq(ctx)
	for i, title := range []string{"One", "Two"} {
		meta := source.TrackMeta{NativeID: title, Title: title, Album: "Real Album", Year: 2001, Index: i + 1, Disc: 1, DurationMs: 100000 + i*1000, PrimaryArtist: source.ArtistReference{Name: "Real Artist"}, AlbumCredits: []source.ArtistCredit{{Name: "Real Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Real Artist"}}, Container: "flac", Codec: "flac", Version: 1}
		if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		paths = append(paths, req.URL.RequestURI())
		switch req.URL.Path {
		case "/release":
			if req.URL.Query().Get("inc") != "" {
				t.Error("search request used unsupported inc expansion")
			}
			_, _ = w.Write([]byte(`{"releases":[{"id":"best","title":"Real Album","date":"2001","artist-credit":[{"name":"Real Artist","artist":{"id":"artist","name":"Real Artist"}}],"media":[{"position":1,"track-count":2}]},{"id":"weak","title":"Real Album","date":"2001","artist-credit":[{"name":"Real Artist","artist":{"id":"artist","name":"Real Artist"}}],"media":[{"position":1,"track-count":2}]}]}`))
		case "/release/best":
			_, _ = w.Write([]byte(`{"id":"best","title":"Real Album","date":"2001","release-group":{"id":"group"},"artist-credit":[{"name":"Real Artist","artist":{"id":"artist","name":"Real Artist"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"rec-1","title":"One","length":100000,"artist-credit":[{"name":"Real Artist","artist":{"id":"artist","name":"Real Artist"}}]}},{"position":2,"recording":{"id":"rec-2","title":"Two","length":101000,"artist-credit":[{"name":"Real Artist","artist":{"id":"artist","name":"Real Artist"}}]}}]}]}`))
		case "/release/weak":
			_, _ = w.Write([]byte(`{"id":"weak","title":"Real Album","date":"2001","release-group":{"id":"other"},"artist-credit":[{"name":"Real Artist","artist":{"id":"artist","name":"Real Artist"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"other-1","title":"Wrong","length":50000}},{"position":2,"recording":{"id":"other-2","title":"Wrong","length":50000}}]}]}`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	r := New(st, client)
	changed, err := r.Run(ctx)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v paths=%v", changed, err, paths)
	}
	if len(paths) != 3 {
		t.Fatalf("requests=%v", paths)
	}
}

func TestResolverDoesNotApplyResultAfterConcurrentScanChange(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	meta := source.TrackMeta{NativeID: "one", Title: "Before", Album: "Album", Index: 1, Disc: 1, DurationMs: 1000, ReleaseMBID: "embedded", PrimaryArtist: source.ArtistReference{Name: "Local"}, AlbumCredits: []source.ArtistCredit{{Name: "Local"}}, TrackCredits: []source.ArtistCredit{{Name: "Local"}}, Container: "flac", Codec: "flac", Version: 1}
	seq, _ := st.NextScanSeq(ctx)
	if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	started, releaseRequest := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-releaseRequest
		_, _ = w.Write([]byte(`{"id":"embedded","release-group":{"id":"group"},"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"recording","title":"Before","length":1000,"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}]}}]}]}`))
	}))
	defer srv.Close()
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	result := make(chan struct {
		changed bool
		err     error
	}, 1)
	go func() {
		changed, err := New(st, client).Run(ctx)
		result <- struct {
			changed bool
			err     error
		}{changed, err}
	}()
	<-started
	meta.Title, meta.Version = "After", 2
	seq, _ = st.NextScanSeq(ctx)
	if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	close(releaseRequest)
	gotResult := <-result
	if gotResult.err != nil || gotResult.changed {
		t.Fatalf("stale result changed=%v err=%v", gotResult.changed, gotResult.err)
	}
	tracks, _, err := st.ListTracks(ctx, "title", "", 10)
	if err != nil || len(tracks) != 1 || tracks[0].Title != "After" || tracks[0].MusicBrainzRecordingID != "" || tracks[0].ArtistName != "Local" {
		t.Fatalf("stale identity applied after concurrent scan: tracks=%+v err=%v", tracks, err)
	}
}

func TestResolverDrainsMoreThanFiveAlbumsAndCancelsPromptly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{"drain", false}, {"cancel", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st, err := store.Open(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			seq, _ := st.NextScanSeq(ctx)
			for i := 0; i < 7; i++ {
				name := fmt.Sprintf("Artist %d", i)
				m := source.TrackMeta{NativeID: fmt.Sprintf("track-%d", i), Title: "Track", Album: fmt.Sprintf("Album %d", i), Index: 1, Disc: 1, DurationMs: 1000, ReleaseMBID: fmt.Sprintf("release-%d", i), PrimaryArtist: source.ArtistReference{Name: name}, AlbumCredits: []source.ArtistCredit{{Name: name}}, TrackCredits: []source.ArtistCredit{{Name: name}}, Container: "flac", Codec: "flac", Version: 1}
				if err := st.UpsertTrack(ctx, "filesystem", m, "", seq); err != nil {
					t.Fatal(err)
				}
			}
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests++
				if tc.cancel && requests == 2 {
					cancel()
					<-req.Context().Done()
					return
				}
				id := strings.TrimPrefix(req.URL.Path, "/release/")
				_, _ = fmt.Fprintf(w, `{"id":%q,"release-group":{"id":"group"},"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"recording","title":"Track","length":1000,"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}]}}]}]}`, id)
			}))
			defer srv.Close()
			client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
			_, err = New(st, client).Run(ctx)
			if tc.cancel {
				if err == nil || requests != 2 {
					t.Fatalf("cancel err=%v requests=%d", err, requests)
				}
				return
			}
			if err != nil || requests != 7 {
				t.Fatalf("drain err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestResolverDrainsPastStaleFirstBatch(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seq, _ := st.NextScanSeq(ctx)
	metas := make(map[string]source.TrackMeta)
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("Artist %d", i)
		m := source.TrackMeta{NativeID: fmt.Sprintf("track-%d", i), Title: "Track", Album: fmt.Sprintf("Album %d", i), Index: 1, Disc: 1, DurationMs: 1000, ReleaseMBID: fmt.Sprintf("release-%d", i), PrimaryArtist: source.ArtistReference{Name: name}, AlbumCredits: []source.ArtistCredit{{Name: name}}, TrackCredits: []source.ArtistCredit{{Name: name}}, Container: "flac", Codec: "flac", Version: 1}
		metas[m.ReleaseMBID] = m
		if err := st.UpsertTrack(ctx, "filesystem", m, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	requests := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id := strings.TrimPrefix(req.URL.Path, "/release/")
		requests[id]++
		if len(requests) <= 5 {
			m := metas[id]
			m.Title = "Changed during lookup"
			m.Version++
			concurrentSeq, _ := st.NextScanSeq(req.Context())
			if err := st.UpsertTrack(req.Context(), "filesystem", m, "", concurrentSeq); err != nil {
				t.Errorf("change stale snapshot: %v", err)
			}
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"release-group":{"id":"group"},"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"recording","title":"Track","length":1000,"artist-credit":[{"name":"Canonical","artist":{"id":"artist","name":"Canonical"}}]}}]}]}`, id)
	}))
	defer srv.Close()
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	if _, err := New(st, client).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 7 {
		t.Fatalf("attempted %d albums, want 7: %v", len(requests), requests)
	}
}

func consensusFixture(t *testing.T, albumTitle string) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	meta := source.TrackMeta{NativeID: "one", Title: "Song", Album: albumTitle, Index: 1, Disc: 1, DurationMs: 200000, PrimaryArtist: source.ArtistReference{Name: "The Band"}, AlbumCredits: []source.ArtistCredit{{Name: "The Band"}}, TrackCredits: []source.ArtistCredit{{Name: "The Band"}}, Container: "flac", Codec: "flac", Version: 1}
	if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func consensusRelease(id, title, artistID, rgID string) string {
	return `{"id":"` + id + `","title":"` + title + `","release-group":{"id":"` + rgID + `"},"artist-credit":[{"name":"The Band","artist":{"id":"` + artistID + `","name":"The Band"}}],"media":[{"position":1,"tracks":[{"position":1,"recording":{"id":"rec-` + id + `","title":"Song","length":200000,"artist-credit":[{"name":"The Band","artist":{"id":"` + artistID + `","name":"The Band"}}]}}]}]}`
}

// Edition ambiguity with artist and release-group agreement still applies
// that shared identity; the edition stays parked as ambiguous.
func TestResolverAppliesConsensusFromAmbiguousEditions(t *testing.T) {
	st, ctx := consensusFixture(t, "Classic Album")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/release/ed-") {
			id := strings.TrimPrefix(r.URL.Path, "/release/")
			_, _ = w.Write([]byte(consensusRelease(id, "Classic Album", "mbid-band", "rg-classic")))
			return
		}
		_, _ = w.Write([]byte(`{"releases":[` + consensusRelease("ed-1", "Classic Album", "mbid-band", "rg-classic") + `,` + consensusRelease("ed-2", "Classic Album", "mbid-band", "rg-classic") + `]}`))
	}))
	defer srv.Close()
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	changed, err := New(st, client).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("consensus application must report change")
	}
	albums, _, err := st.ListAlbums(ctx, "title", "", 10)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums=%v err=%v", albums, err)
	}
	if albums[0].MusicBrainzReleaseGroupID != "rg-classic" || albums[0].MusicBrainzReleaseID != "" {
		t.Fatalf("consensus identity: rg=%q release=%q", albums[0].MusicBrainzReleaseGroupID, albums[0].MusicBrainzReleaseID)
	}
	due, err := st.DueMusicBrainzAlbums(ctx, time.Now(), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("album must stay parked after consensus: %v err=%v", due, err)
	}
	artists, _, err := st.ListArtists(ctx, "name", "", 10)
	if err != nil || len(artists) != 1 || artists[0].MusicBrainzID != "mbid-band" {
		t.Fatalf("artist consensus adoption: %v err=%v", artists, err)
	}
}

// Candidates that disagree on the artist yield no partial application.
func TestResolverConsensusRequiresArtistAgreement(t *testing.T) {
	st, ctx := consensusFixture(t, "Split Album")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/release/ed-") {
			id := strings.TrimPrefix(r.URL.Path, "/release/")
			artist := "mbid-a"
			if id == "ed-2" {
				artist = "mbid-b"
			}
			_, _ = w.Write([]byte(consensusRelease(id, "Split Album", artist, "rg-"+id)))
			return
		}
		_, _ = w.Write([]byte(`{"releases":[` + consensusRelease("ed-1", "Split Album", "mbid-a", "rg-ed-1") + `,` + consensusRelease("ed-2", "Split Album", "mbid-b", "rg-ed-2") + `]}`))
	}))
	defer srv.Close()
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	if _, err := New(st, client).Run(ctx); err != nil {
		t.Fatal(err)
	}
	albums, _, err := st.ListAlbums(ctx, "title", "", 10)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums=%v err=%v", albums, err)
	}
	if albums[0].MusicBrainzReleaseGroupID != "" {
		t.Fatalf("disagreeing candidates must not apply identity: rg=%q", albums[0].MusicBrainzReleaseGroupID)
	}
	artists, _, err := st.ListArtists(ctx, "name", "", 10)
	if err != nil || len(artists) != 1 || artists[0].MusicBrainzID != "" {
		t.Fatalf("artist must stay unidentified: %v err=%v", artists, err)
	}
}

// Disc/edition designators in local titles are stripped for the search query.
func TestResolverNormalizesSearchTitles(t *testing.T) {
	st, ctx := consensusFixture(t, "Movement in Still Life CD01")
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/release/") {
			id := strings.TrimPrefix(r.URL.Path, "/release/")
			_, _ = w.Write([]byte(consensusRelease(id, "Movement in Still Life", "mbid-band", "rg-misl")))
			return
		}
		query = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"releases":[` + consensusRelease("ed-1", "Movement in Still Life", "mbid-band", "rg-misl") + `]}`))
	}))
	defer srv.Close()
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond})
	if _, err := New(st, client).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, `release:"Movement in Still Life"`) {
		t.Fatalf("search must use the normalized title, got %q", query)
	}
}

func TestSearchTitleNormalization(t *testing.T) {
	for input, want := range map[string]string{
		"Movement in Still Life CD01":                     "Movement in Still Life",
		"1995 - Ima (Disc 01)":                            "1995 - Ima",
		"Electronica 2- The Heart of Noise [88875196672]": "Electronica 2- The Heart of Noise",
		"Zoolook (1997 remaster)":                         "Zoolook",
		"Revolutions (Remastered 1997)":                   "Revolutions",
		"Plain Title":                                     "Plain Title",
		"CD02":                                            "CD02",
	} {
		if got := searchTitle(input); got != want {
			t.Errorf("searchTitle(%q) = %q, want %q", input, got, want)
		}
	}
}
