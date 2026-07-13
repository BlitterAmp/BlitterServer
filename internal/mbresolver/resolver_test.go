package mbresolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
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
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: st, Interval: time.Nanosecond})
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
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: st, Interval: time.Nanosecond})
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
	client, _ := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: st, Interval: time.Nanosecond})
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
