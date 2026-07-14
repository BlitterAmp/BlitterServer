package enrich

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/musicbrainz"
	"github.com/BlitterAmp/BlitterServer/internal/providercache"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type artistIdentityClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *artistIdentityClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *artistIdentityClock) Sleep(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, duration)
	c.now = c.now.Add(duration)
	c.mu.Unlock()
	return nil
}

func TestArtistIdentityStageUsesMusicBrainzCacheAndPacing(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	seq, _ := st.NextScanSeq(ctx)
	for i := 1; i <= 2; i++ {
		mbid := fmt.Sprintf("artist-mbid-%d", i)
		name := fmt.Sprintf("Local Artist %d", i)
		meta := source.TrackMeta{
			NativeID: fmt.Sprintf("artist-%d.flac", i), Title: fmt.Sprintf("Track %d", i), Album: fmt.Sprintf("Album %d", i),
			PrimaryArtist: source.ArtistReference{Name: name, MBID: mbid},
			AlbumCredits:  []source.ArtistCredit{{Name: name, MBID: mbid}},
			TrackCredits:  []source.ArtistCredit{{Name: name, MBID: mbid}},
			Container:     "flac", Codec: "flac", Version: 1,
		}
		if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	albums, _, err := st.ListAlbums(ctx, "title", "", 10)
	if err != nil || len(albums) != 2 {
		t.Fatalf("albums=%+v err=%v", albums, err)
	}
	for _, album := range albums {
		if err := st.MarkAlbumArtAttempt(ctx, album.AlbumID, store.ArtAttemptMiss, now); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordMusicBrainzResult(ctx, album.AlbumID, "matched", store.MusicBrainzCandidate{}, nil, now.Add(7*24*time.Hour), ""); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	requests := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.RequestURI())
		mu.Unlock()
		if r.URL.Query().Get("inc") != "aliases" || r.URL.Query().Get("fmt") != "json" {
			t.Errorf("artist identity query=%q", r.URL.RawQuery)
		}
		var index int
		if _, err := fmt.Sscanf(r.URL.Path, "/artist/artist-mbid-%d", &index); err != nil || index < 1 || index > 2 {
			t.Errorf("artist identity path=%q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		aliases := ""
		if index == 1 {
			aliases = `,"aliases":[{"name":"Local Artist 1"}]`
		}
		_, _ = fmt.Fprintf(w, `{"id":"artist-mbid-%d","name":"Canonical Artist %d"%s}`, index, index, aliases)
	}))
	defer srv.Close()
	clock := &artistIdentityClock{now: now}
	cache := musicbrainz.NewFilesystemCache(providercache.New(t.TempDir()))
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Clock: clock, Cache: cache, Interval: time.Second, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, nil, t.TempDir(), Config{MusicBrainz: client})
	e.RunAt(ctx, now)

	mu.Lock()
	requestCount := len(requests)
	mu.Unlock()
	if requestCount != 2 {
		t.Fatalf("identity requests=%v", requests)
	}
	clock.mu.Lock()
	sleeps := append([]time.Duration(nil), clock.sleeps...)
	clock.mu.Unlock()
	if len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("MusicBrainz pacing sleeps=%v", sleeps)
	}
	for i := 1; i <= 2; i++ {
		search, err := st.SearchLibrary(ctx, fmt.Sprintf("Canonical Artist %d", i))
		if err != nil || len(search.Artists) != 1 || search.Artists[0].MusicBrainzID != fmt.Sprintf("artist-mbid-%d", i) {
			t.Fatalf("canonical artist %d search=%+v err=%v", i, search.Artists, err)
		}
	}

	var cached struct {
		ID string `json:"id"`
	}
	if err := client.GetJSON(ctx, "/artist/artist-mbid-1?inc=aliases&fmt=json", &cached); err != nil || cached.ID != "artist-mbid-1" {
		t.Fatalf("cached identity=%+v err=%v", cached, err)
	}
	e.RunAt(ctx, now)
	mu.Lock()
	requestCount = len(requests)
	mu.Unlock()
	clock.mu.Lock()
	sleeps = append([]time.Duration(nil), clock.sleeps...)
	clock.mu.Unlock()
	if requestCount != 2 || len(sleeps) != 1 {
		t.Fatalf("terminal aliases refetched: requests=%v sleeps=%v", requests, sleeps)
	}
}

func TestArtistIdentityStageLeavesTransientFailuresPending(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	meta := source.TrackMeta{NativeID: "track.flac", Title: "Track", Album: "Album", PrimaryArtist: source.ArtistReference{Name: "Local", MBID: "pending-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Local", MBID: "pending-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Local", MBID: "pending-mbid"}}, Container: "flac", Codec: "flac", Version: 1}
	if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	albums, _, _ := st.ListAlbums(ctx, "title", "", 1)
	_ = st.MarkAlbumArtAttempt(ctx, albums[0].AlbumID, store.ArtAttemptMiss, now)
	_ = st.RecordMusicBrainzResult(ctx, albums[0].AlbumID, "matched", store.MusicBrainzCandidate{}, nil, now.Add(7*24*time.Hour), "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	New(st, nil, t.TempDir(), Config{MusicBrainz: client}).RunAt(ctx, now)
	pending, _, err := st.PendingMusicBrainzArtists(ctx, "", 10)
	if err != nil || len(pending) != 1 || pending[0].MusicBrainzID != "pending-mbid" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestArtistIdentityStageCollectsCompleteEvidenceBeforeConsolidating(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	metas := []source.TrackMeta{
		{NativeID: "canonical-1.flac", Title: "Canonical Track 1", Album: "Canonical Album 1", PrimaryArtist: source.ArtistReference{Name: "Canonical 1", MBID: "mbid-1"}, AlbumCredits: []source.ArtistCredit{{Name: "Canonical 1", MBID: "mbid-1"}}, TrackCredits: []source.ArtistCredit{{Name: "Canonical 1", MBID: "mbid-1"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "canonical-2.flac", Title: "Canonical Track 2", Album: "Canonical Album 2", PrimaryArtist: source.ArtistReference{Name: "Canonical 2", MBID: "mbid-2"}, AlbumCredits: []source.ArtistCredit{{Name: "Canonical 2", MBID: "mbid-2"}}, TrackCredits: []source.ArtistCredit{{Name: "Canonical 2", MBID: "mbid-2"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "local.flac", Title: "Local Track", Album: "Local Album", PrimaryArtist: source.ArtistReference{Name: "Shared Evidence"}, AlbumCredits: []source.ArtistCredit{{Name: "Shared Evidence"}}, TrackCredits: []source.ArtistCredit{{Name: "Shared Evidence"}}, Container: "flac", Codec: "flac", Version: 1},
	}
	for _, meta := range metas {
		if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	markArtistIdentityAlbumsMatched(t, st, ctx)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mbid := strings.TrimPrefix(r.URL.Path, "/artist/")
		_, _ = fmt.Fprintf(w, `{"id":%q,"name":%q,"aliases":[{"name":"Shared Evidence"}]}`, mbid, "Canonical "+strings.TrimPrefix(mbid, "mbid-"))
	}))
	defer srv.Close()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	New(st, nil, t.TempDir(), Config{MusicBrainz: client}).RunAt(ctx, time.Now())
	albums, _, err := st.ListAlbums(ctx, "title", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, album := range albums {
		if album.Title == "Local Album" && album.ArtistName != "Shared Evidence" {
			t.Fatalf("ambiguous owner merged: %+v", album)
		}
	}
}

func TestArtistIdentityStageDefersConsolidationWhileMetadataPending(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	metas := []source.TrackMeta{
		{NativeID: "canonical-ok.flac", Title: "Canonical OK Track", Album: "Canonical OK Album", PrimaryArtist: source.ArtistReference{Name: "Canonical OK", MBID: "ok-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Canonical OK", MBID: "ok-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Canonical OK", MBID: "ok-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "canonical-pending.flac", Title: "Canonical Pending Track", Album: "Canonical Pending Album", PrimaryArtist: source.ArtistReference{Name: "Canonical Pending", MBID: "pending-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Canonical Pending", MBID: "pending-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Canonical Pending", MBID: "pending-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "local.flac", Title: "Local Track", Album: "Local Album", PrimaryArtist: source.ArtistReference{Name: "Unique Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Unique Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Unique Alias"}}, Container: "flac", Codec: "flac", Version: 1},
	}
	for _, meta := range metas {
		if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	markArtistIdentityAlbumsMatched(t, st, ctx)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "pending-mbid") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"ok-mbid","name":"Canonical OK","aliases":[{"name":"Unique Alias"}]}`)
	}))
	defer srv.Close()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	New(st, nil, t.TempDir(), Config{MusicBrainz: client}).RunAt(ctx, time.Now())
	albums, _, _ := st.ListAlbums(ctx, "title", "", 10)
	for _, album := range albums {
		if album.Title == "Local Album" && album.ArtistName != "Unique Alias" {
			t.Fatalf("owner merged while metadata pending: %+v", album)
		}
	}
	pending, _, err := st.PendingMusicBrainzArtists(ctx, "", 10)
	if err != nil || len(pending) != 1 || pending[0].MusicBrainzID != "pending-mbid" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestArtistIdentityStageMarks404TerminalAndConsolidatesValidEvidence(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seq, _ := st.NextScanSeq(ctx)
	metas := []source.TrackMeta{
		{NativeID: "valid.flac", Title: "Valid Track", Album: "Valid Album", PrimaryArtist: source.ArtistReference{Name: "Valid Canonical", MBID: "valid-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Valid Canonical", MBID: "valid-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Valid Canonical", MBID: "valid-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "stale.flac", Title: "Stale Track", Album: "Stale Album", PrimaryArtist: source.ArtistReference{Name: "Unique Alias", MBID: "stale-mbid"}, AlbumCredits: []source.ArtistCredit{{Name: "Unique Alias", MBID: "stale-mbid"}}, TrackCredits: []source.ArtistCredit{{Name: "Unique Alias", MBID: "stale-mbid"}}, Container: "flac", Codec: "flac", Version: 1},
		{NativeID: "local.flac", Title: "Local Track", Album: "Local Album", PrimaryArtist: source.ArtistReference{Name: "Unique Alias"}, AlbumCredits: []source.ArtistCredit{{Name: "Unique Alias"}}, TrackCredits: []source.ArtistCredit{{Name: "Unique Alias"}}, Container: "flac", Codec: "flac", Version: 1},
	}
	for _, meta := range metas {
		if err := st.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	markArtistIdentityAlbumsMatched(t, st, ctx)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "stale-mbid") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"valid-mbid","name":"Valid Canonical","aliases":[{"name":"Unique Alias"}]}`)
	}))
	defer srv.Close()
	client, err := musicbrainz.NewClient(musicbrainz.Options{BaseURL: srv.URL, UserAgent: "BlitterServer/test (mailto:test@example.com)", Cache: musicbrainz.NewFilesystemCache(providercache.New(t.TempDir())), Interval: time.Nanosecond, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	New(st, nil, t.TempDir(), Config{MusicBrainz: client}).RunAt(ctx, time.Now())
	pending, _, err := st.PendingMusicBrainzArtists(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("terminal metadata pending=%+v err=%v", pending, err)
	}
	albums, _, _ := st.ListAlbums(ctx, "title", "", 10)
	for _, album := range albums {
		if album.Title == "Local Album" && album.ArtistName != "Valid Canonical" {
			t.Fatalf("valid duplicate not consolidated: %+v", album)
		}
	}
}

func markArtistIdentityAlbumsMatched(t *testing.T, st *store.Store, ctx context.Context) {
	t.Helper()
	now := time.Now()
	albums, _, err := st.ListAlbums(ctx, "title", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, album := range albums {
		if err := st.MarkAlbumArtAttempt(ctx, album.AlbumID, store.ArtAttemptMiss, now); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordMusicBrainzResult(ctx, album.AlbumID, "matched", store.MusicBrainzCandidate{}, nil, now.Add(7*24*time.Hour), ""); err != nil {
			t.Fatal(err)
		}
	}
}
