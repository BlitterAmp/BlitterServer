package enrich

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func observabilityContext() (context.Context, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logging.With(context.Background(), logger), &buf
}

func steppingEnricher(e *Enricher) {
	now := time.Unix(1_700_000_000, 0)
	e.Now = func() time.Time { now = now.Add(16 * time.Second); return now }
	e.LogProgressInterval = 15 * time.Second
	e.ArtSliceBudget = time.Hour
}

func TestEmptyEnrichmentLogsStageTerminalsAndArtistSkip(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, buf := observabilityContext()
	e := New(st, nil, t.TempDir(), Config{})
	steppingEnricher(e)
	e.RunAt(ctx, time.Now())
	out := buf.String()
	for _, text := range []string{"album artwork started", "album artwork completed", "artist artwork skipped", "reason=no_provider_configured", "library enrichment completed"} {
		if !strings.Contains(out, text) {
			t.Fatalf("missing %q: %s", text, out)
		}
	}
}

func TestAlbumArtworkLogsStageLocalProgressAndSuccess(t *testing.T) {
	st, _ := seedAlbum(t)
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "release-group") {
			_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-1","title":"Great Album"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"artists":[]}`))
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer caa.Close()
	ctx, buf := observabilityContext()
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.CAABase = caa.URL
	steppingEnricher(e)
	e.RunAt(ctx, time.Now())
	out := buf.String()
	for _, text := range []string{"album artwork progress", "attempted=1", "succeeded=1", "album artwork completed"} {
		if !strings.Contains(out, text) {
			t.Fatalf("missing %q: %s", text, out)
		}
	}
	for _, private := range []string{"Great Album", "The Band", mb.URL, caa.URL} {
		if strings.Contains(out, private) {
			t.Fatalf("log leaked %q: %s", private, out)
		}
	}
}

func TestArtworkTransientCountersRemainStageLocal(t *testing.T) {
	st, _ := seedAlbum(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer provider.Close()
	ctx, buf := observabilityContext()
	e := New(st, nil, t.TempDir(), Config{LastfmKey: func(context.Context) string { return "configured" }})
	e.LastfmBase = provider.URL
	steppingEnricher(e)
	e.RunAt(ctx, time.Now())
	out := buf.String()
	if !strings.Contains(out, "msg=\"album artwork progress\"") || !strings.Contains(out, "transient=1") {
		t.Fatalf("album transient counters: %s", out)
	}
	if !strings.Contains(out, "msg=\"artist artwork progress\"") {
		t.Fatalf("artist stage lacked independent progress: %s", out)
	}
}

func TestMusicBrainzArtistMetadataLogsSuccessAndTransientProgress(t *testing.T) {
	for _, transient := range []bool{false, true} {
		name := "success"
		if transient {
			name = "transient"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			seq, _ := st.NextScanSeq(ctx)
			credit := source.ArtistCredit{Name: "Private Artist", MBID: "private-mbid"}
			meta := source.TrackMeta{NativeID: "private.flac", Title: "Private Song", Album: "Private Album", PrimaryArtist: source.ArtistReference{Name: credit.Name, MBID: credit.MBID}, TrackCredits: []source.ArtistCredit{credit}, AlbumCredits: []source.ArtistCredit{credit}, Container: "flac", Codec: "flac"}
			if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
				t.Fatal(err)
			}
			if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if transient {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(`{"id":"private-mbid","name":"Canonical","aliases":[{"name":"Alias"}]}`))
			}))
			defer server.Close()
			client := testMusicBrainzClient(t, st, server.URL)
			logCtx, buf := observabilityContext()
			e := New(st, nil, t.TempDir(), Config{MusicBrainz: client})
			steppingEnricher(e)
			e.runArtistIdentityStage(logCtx, func() {}, func(bool) {})
			out := buf.String()
			for _, text := range []string{"musicbrainz artist metadata started", "musicbrainz artist metadata progress"} {
				if !strings.Contains(out, text) {
					t.Fatalf("missing %q: %s", text, out)
				}
			}
			if transient && !strings.Contains(out, "msg=\"musicbrainz artist metadata failed\"") {
				t.Fatalf("transient terminal: %s", out)
			}
			if !transient && (!strings.Contains(out, "msg=\"musicbrainz artist metadata completed\"") || !strings.Contains(out, "changed=1")) {
				t.Fatalf("success terminal: %s", out)
			}
			for _, private := range []string{"Private Artist", "Private Song", "Private Album", "private-mbid", server.URL} {
				if strings.Contains(out, private) {
					t.Fatalf("metadata log leaked %q: %s", private, out)
				}
			}
		})
	}
}

func TestArtistIdentityFailuresPropagateToStageAndOverall(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seq, _ := st.NextScanSeq(ctx)
	credit := source.ArtistCredit{Name: "Private Artist", MBID: "private-mbid"}
	meta := source.TrackMeta{NativeID: "private.flac", Title: "Private", Album: "Private", PrimaryArtist: source.ArtistReference{Name: credit.Name, MBID: credit.MBID}, TrackCredits: []source.ArtistCredit{credit}, AlbumCredits: []source.ArtistCredit{credit}, Container: "flac", Codec: "flac"}
	if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, server.URL)})
	steppingEnricher(e)
	e.markArtistMetadataTerminal = func(context.Context, string, string) error { return errors.New("synthetic terminal write failure") }
	logCtx, buf := observabilityContext()
	summary := e.runArtistIdentityStage(logCtx, func() {}, func(bool) {})
	if summary.Failed != 1 || summary.Terminal != 0 || !strings.Contains(buf.String(), "msg=\"musicbrainz artist metadata failed\"") {
		t.Fatalf("terminal failure summary=%+v log=%s", summary, buf.String())
	}

	empty, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	overall := New(empty, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, empty, server.URL)})
	steppingEnricher(overall)
	overall.consolidateArtists = func(context.Context) (bool, error) { return false, errors.New("synthetic consolidation failure") }
	overallCtx, overallBuf := observabilityContext()
	overall.RunAt(overallCtx, time.Now())
	if !strings.Contains(overallBuf.String(), "msg=\"musicbrainz artist metadata failed\"") || !strings.Contains(overallBuf.String(), "msg=\"library enrichment failed\"") {
		t.Fatalf("consolidation failure did not propagate: %s", overallBuf.String())
	}
}

func TestArtworkSkippedAndRemainingFailureCounters(t *testing.T) {
	st, _ := seedAlbum(t)
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "release-group") {
			_, _ = w.Write([]byte(`{"release-groups":[{"id":"rg-1","title":"Great Album"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"artists":[]}`))
	}))
	defer mb.Close()
	caa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer caa.Close()
	ctx, buf := observabilityContext()
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: testMusicBrainzClient(t, st, mb.URL)})
	e.CAABase = caa.URL
	steppingEnricher(e)
	e.attachAlbumArtFn = func(ctx context.Context, albumID, _ string) (bool, error) {
		_, err := st.SetAlbumArtAtNextSequence(ctx, albumID, "img_competing")
		return false, err
	}
	countCalls := 0
	e.countAlbumsNeedingArt = func(ctx context.Context, now time.Time) (int, error) {
		countCalls++
		if countCalls > 1 {
			return 0, errors.New("synthetic remaining count failure")
		}
		return st.CountAlbumsNeedingArtAt(ctx, now)
	}
	total := runSummary{}
	e.runAlbumArtStage(ctx, time.Now(), time.Time{}, slog.New(slog.NewTextHandler(buf, nil)), &total, func() {}, func(bool) {})
	out := buf.String()
	if !strings.Contains(out, "skipped=1") || strings.Contains(out, "succeeded=1") || !strings.Contains(out, "remaining=-1") || !strings.Contains(out, "msg=\"album artwork failed\"") {
		t.Fatalf("artwork skipped/remaining counters: %s", out)
	}
}
