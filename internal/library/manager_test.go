package library

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type blockingEnricher struct {
	mu      sync.Mutex
	runs    int
	active  int
	maxRuns int
	started chan int
	release chan struct{}
}

func newBlockingEnricher() *blockingEnricher {
	return &blockingEnricher{started: make(chan int, 10), release: make(chan struct{}, 10)}
}

func setEnricherWithoutStart(m *Manager, e Enricher) {
	m.mu.Lock()
	m.enricher = e
	m.mu.Unlock()
}

func (e *blockingEnricher) Run(context.Context) {
	e.mu.Lock()
	e.runs++
	e.active++
	if e.active > e.maxRuns {
		e.maxRuns = e.active
	}
	run := e.runs
	e.mu.Unlock()
	e.started <- run
	<-e.release
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
}

func (e *blockingEnricher) counts() (runs, maxRuns int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs, e.maxRuns
}

type cancelEnricher struct {
	started chan struct{}
	done    chan struct{}
}

func (e *cancelEnricher) Run(ctx context.Context) {
	close(e.started)
	<-ctx.Done()
	close(e.done)
}

func openStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	s, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dataDir
}

func musicDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Manager tests don't need real audio — an empty dir scans to an empty
	// library; adapter parsing is covered in the filesystem package.
	return dir
}

func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := m.Status(context.Background()); !st.Scanning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan never finished")
}

func TestConfigureValidatesAndScans(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)

	if st := m.Status(context.Background()); st.Configured {
		t.Fatal("fresh manager must be unconfigured")
	}
	if err := m.Rescan(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("rescan unconfigured: want ErrNotConfigured, got %v", err)
	}

	if err := m.Configure(context.Background(), filepath.Join(dataDir, "missing")); err == nil {
		t.Fatal("missing path must be rejected")
	}
	f := filepath.Join(dataDir, "file")
	os.WriteFile(f, []byte("x"), 0o644)
	if err := m.Configure(context.Background(), f); err == nil {
		t.Fatal("non-dir path must be rejected")
	}

	dir := musicDir(t)
	if err := m.Configure(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)
	st := m.Status(context.Background())
	if !st.Configured || st.Path != dir || st.LastScanAt == nil || st.LastScanError != "" {
		t.Fatalf("post-configure status: %+v", st)
	}
	if m.SourceKind(context.Background()) != "filesystem" {
		t.Fatalf("source kind: %q", m.SourceKind(context.Background()))
	}
}

func TestManagerRestoresFromSettings(t *testing.T) {
	s, dataDir := openStore(t)
	dir := musicDir(t)
	m := NewManager(s, dataDir)
	if err := m.Configure(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)

	// A new manager over the same store must come up configured.
	m2 := NewManager(s, dataDir)
	st := m2.Status(context.Background())
	if !st.Configured || st.Path != dir {
		t.Fatalf("restore: %+v", st)
	}
	if err := m2.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m2)
}

func TestUnlink(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	if err := m.Configure(context.Background(), musicDir(t)); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)
	if err := m.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(context.Background()); st.Configured {
		t.Fatalf("unlink must clear config: %+v", st)
	}
	if err := m.Rescan(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("rescan after unlink must be ErrNotConfigured")
	}
}

func TestTriggerEnrichmentCoalescesWithoutConcurrentRuns(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	e := newBlockingEnricher()
	setEnricherWithoutStart(m, e)

	m.TriggerEnrichment()
	if run := <-e.started; run != 1 {
		t.Fatalf("first run=%d", run)
	}
	for range 20 {
		m.TriggerEnrichment()
	}
	e.release <- struct{}{}
	if run := <-e.started; run != 2 {
		t.Fatalf("follow-up run=%d", run)
	}
	e.release <- struct{}{}

	if runs, maxRuns := e.counts(); runs != 2 || maxRuns != 1 {
		t.Fatalf("runs=%d max concurrent=%d", runs, maxRuns)
	}
}

func TestSetEnricherTriggersStartupAndPeriodicPasses(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	wake := make(chan struct{}, 1)
	m.waitEnrichment = func(ctx context.Context) bool {
		select {
		case <-wake:
			return true
		case <-ctx.Done():
			return false
		}
	}
	e := newBlockingEnricher()
	m.SetEnricher(e)

	if run := <-e.started; run != 1 {
		t.Fatalf("startup run=%d", run)
	}
	e.release <- struct{}{}
	wake <- struct{}{}
	if run := <-e.started; run != 2 {
		t.Fatalf("periodic run=%d", run)
	}
	e.release <- struct{}{}
	m.Close()
}

func TestSetEnricherScansConfiguredSourceBeforeStartupPass(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	src := &sequenceSource{
		entered: make(chan struct{}), release: make(chan struct{}),
		meta: source.TrackMeta{
			NativeID: "startup.flac", Title: "Startup", Album: "Startup Album",
			PrimaryArtist: source.ArtistReference{Name: "Artist"},
			AlbumCredits:  []source.ArtistCredit{{Name: "Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Artist"}},
			Container: "flac", Codec: "flac", Version: 1,
		},
	}
	m.mu.Lock()
	m.src = src
	m.mu.Unlock()
	e := newBlockingEnricher()
	m.SetEnricher(e)
	<-src.entered
	select {
	case <-e.started:
		t.Fatal("startup enrichment ran before configured scan completed")
	default:
	}
	close(src.release)
	if run := <-e.started; run != 1 {
		t.Fatalf("post-scan run=%d", run)
	}
	e.release <- struct{}{}
	m.Close()
}

func TestCloseCancelsPeriodicEnrichmentWait(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	waiting := make(chan struct{})
	m.waitEnrichment = func(ctx context.Context) bool {
		close(waiting)
		<-ctx.Done()
		return false
	}
	e := newBlockingEnricher()
	m.SetEnricher(e)
	<-waiting
	<-e.started
	e.release <- struct{}{}

	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel periodic enrichment wait")
	}
}

func TestCompletedScanUsesEnrichmentTrigger(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	e := newBlockingEnricher()
	setEnricherWithoutStart(m, e)

	if err := m.Configure(context.Background(), musicDir(t)); err != nil {
		t.Fatal(err)
	}
	if run := <-e.started; run != 1 {
		t.Fatalf("post-scan run=%d", run)
	}
	m.TriggerAlbumEnrichment()
	m.TriggerEnrichment()
	e.release <- struct{}{}
	if run := <-e.started; run != 2 {
		t.Fatalf("coalesced post-scan follow-up=%d", run)
	}
	e.release <- struct{}{}
}

type exitTriggerEnricher struct {
	manager *Manager
	runs    chan int
	count   int
}

func (e *exitTriggerEnricher) Run(context.Context) {
	e.count++
	e.runs <- e.count
	if e.count == 1 {
		e.manager.TriggerEnrichment()
	}
}

func TestTriggerAtWorkerExitStartsFollowUp(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	e := &exitTriggerEnricher{manager: m, runs: make(chan int, 2)}
	setEnricherWithoutStart(m, e)
	m.TriggerEnrichment()
	if run := <-e.runs; run != 1 {
		t.Fatalf("first run=%d", run)
	}
	if run := <-e.runs; run != 2 {
		t.Fatalf("exit-boundary follow-up=%d", run)
	}
	m.Close()
}

func TestCloseCancelsWaitsAndRejectsNewEnrichment(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	e := &cancelEnricher{started: make(chan struct{}), done: make(chan struct{})}
	setEnricherWithoutStart(m, e)
	m.TriggerEnrichment()
	<-e.started

	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-e.done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel enrichment")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not join enrichment")
	}
	m.Close()
	m.TriggerEnrichment()
}

type blockedSource struct {
	started chan struct{}
	done    chan struct{}
}

type sequenceSource struct {
	entered chan struct{}
	release chan struct{}
	meta    source.TrackMeta
	err     error
}

func (s *sequenceSource) Kind() string { return "filesystem" }
func (s *sequenceSource) Scan(ctx context.Context, emit func(source.TrackMeta) error) error {
	if err := emit(s.meta); err != nil {
		return err
	}
	close(s.entered)
	select {
	case <-s.release:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *sequenceSource) Open(context.Context, string) (io.ReadSeekCloser, error) {
	return nil, errors.New("unused")
}
func (s *sequenceSource) Art(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("unused")
}

type sequenceEnricher struct {
	st      *store.Store
	albumID string
	artID   string
	entered chan struct{}
	release chan struct{}
	runs    int
}

func (e *sequenceEnricher) Run(ctx context.Context) {
	e.runs++
	seq, err := e.st.NextScanSeq(ctx)
	if err != nil {
		return
	}
	_, _ = e.st.SetAlbumArt(ctx, e.albumID, e.artID, seq)
	if e.runs == 1 {
		close(e.entered)
		select {
		case <-e.release:
		case <-ctx.Done():
		}
	}
}

func sequenceFixture(t *testing.T) (*store.Store, *Manager, *sequenceSource, *sequenceEnricher, string) {
	t.Helper()
	s, dataDir := openStore(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	_ = s.UpsertTrack(ctx, "filesystem", source.TrackMeta{NativeID: "old", Title: "Old", PrimaryArtist: source.ArtistReference{Name: "Artist"}, TrackCredits: []source.ArtistCredit{{Name: "Artist"}}, AlbumCredits: []source.ArtistCredit{{Name: "Artist"}}, Album: "Album", Container: "flac", Codec: "flac", Version: 1}, "", seq)
	_ = s.FinishScan(ctx, "filesystem", seq)
	albums, _ := s.AlbumsNeedingArt(ctx, 1)
	artID, _ := s.UpsertArt(ctx, "sequence-art", "image/jpeg", []byte("art"), dataDir)
	m := NewManager(s, dataDir)
	src := &sequenceSource{entered: make(chan struct{}), release: make(chan struct{}), meta: source.TrackMeta{NativeID: "new", Title: "New", PrimaryArtist: source.ArtistReference{Name: "Artist"}, TrackCredits: []source.ArtistCredit{{Name: "Artist"}}, AlbumCredits: []source.ArtistCredit{{Name: "Artist"}}, Album: "New Album", Container: "flac", Codec: "flac", Version: 1}}
	e := &sequenceEnricher{st: s, albumID: albums[0].AlbumID, artID: artID, entered: make(chan struct{}), release: make(chan struct{})}
	m.mu.Lock()
	m.src = src
	m.mu.Unlock()
	setEnricherWithoutStart(m, e)
	return s, m, src, e, albums[0].AlbumID
}

func assertChangeOrder(t *testing.T, st *store.Store, albumID string, albumBeforeTrack bool) {
	t.Helper()
	tracks, _, err := st.ListTracks(context.Background(), "title", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var trackID string
	for _, track := range tracks {
		if track.Title == "New" {
			trackID = track.TrackID
		}
	}
	changes, _, err := st.ChangesSince(context.Background(), 0, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var albumSeq, trackSeq int64
	for _, change := range changes {
		if change.Kind == "album" && change.ID == albumID {
			albumSeq = change.ChangeSeq
		}
		if change.Kind == "track" && change.ID == trackID {
			trackSeq = change.ChangeSeq
		}
	}
	if albumSeq == 0 || trackSeq == 0 || (albumSeq < trackSeq) != albumBeforeTrack {
		t.Fatalf("change order album=%d track=%d want albumBeforeTrack=%v", albumSeq, trackSeq, albumBeforeTrack)
	}
}

func TestLibraryOperationsSerializeEnrichmentBeforeScan(t *testing.T) {
	s, m, src, e, albumID := sequenceFixture(t)
	m.TriggerEnrichment()
	<-e.entered
	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-src.entered:
		t.Fatal("scan overlapped enrichment")
	default:
	}
	m.SetEnricher(nil)
	close(e.release)
	<-src.entered
	close(src.release)
	m.Close()
	assertChangeOrder(t, s, albumID, true)
}

func TestLibraryOperationsSerializeScanBeforeEnrichment(t *testing.T) {
	s, m, src, e, albumID := sequenceFixture(t)
	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-src.entered
	m.TriggerAlbumEnrichment()
	select {
	case <-e.entered:
		t.Fatal("enrichment overlapped scan")
	default:
	}
	close(src.release)
	<-e.entered
	close(e.release)
	m.Close()
	assertChangeOrder(t, s, albumID, false)
}

func (s *blockedSource) Kind() string { return "blocked" }
func (s *blockedSource) Scan(ctx context.Context, _ func(source.TrackMeta) error) error {
	close(s.started)
	<-ctx.Done()
	close(s.done)
	return ctx.Err()
}
func (s *blockedSource) Open(context.Context, string) (io.ReadSeekCloser, error) {
	return nil, errors.New("unused")
}
func (s *blockedSource) Art(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("unused")
}

func TestCloseCancelsAndJoinsScanAndRejectsNewScans(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	src := &blockedSource{started: make(chan struct{}), done: make(chan struct{})}
	m.mu.Lock()
	m.src = src
	m.mu.Unlock()
	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-src.started
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-src.done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel active scan")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not join active scan")
	}
	if err := m.Rescan(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("post-close rescan: %v", err)
	}
	if err := m.Configure(context.Background(), musicDir(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("post-close configure: %v", err)
	}
}

type retryOrderingEnricher struct {
	st          *store.Store
	albumID     string
	artistID    string
	started     chan int
	release     chan struct{}
	runs        int
	seenAlbums  []bool
	seenArtists []bool
}

func (e *retryOrderingEnricher) Run(ctx context.Context) {
	e.runs++
	run := e.runs
	albums, _ := e.st.AlbumsNeedingArt(ctx, 10)
	artists, _ := e.st.ArtistsNeedingArt(ctx, 10)
	e.seenAlbums = append(e.seenAlbums, len(albums) == 1)
	e.seenArtists = append(e.seenArtists, len(artists) == 1)
	e.started <- run
	if run == 1 {
		<-e.release
		_ = e.st.MarkAlbumArtTried(ctx, e.albumID)
		_ = e.st.MarkArtistArtTried(ctx, e.artistID)
	}
}

func TestResetsWaitForStalePassAndPreserveArtistAndAlbumIntents(t *testing.T) {
	s, dataDir := openStore(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	_ = s.UpsertTrack(ctx, "filesystem", source.TrackMeta{NativeID: "n1", Title: "Song", PrimaryArtist: source.ArtistReference{Name: "Artist"}, TrackCredits: []source.ArtistCredit{{Name: "Artist"}}, AlbumCredits: []source.ArtistCredit{{Name: "Artist"}}, Album: "Album", Container: "flac", Codec: "flac", Version: 1}, "", seq)
	_ = s.FinishScan(ctx, "filesystem", seq)
	// Fanart selection requires an MBID; identify the artist via a match.
	due, err := s.DueMusicBrainzAlbums(ctx, time.Now(), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("due albums: %v err=%v", due, err)
	}
	applySeq, _ := s.NextScanSeq(ctx)
	credits := []source.ArtistCredit{{Name: "Artist", MBID: "mbid-artist"}}
	release := store.CanonicalRelease{ReleaseID: "rel-1", ReleaseGroupID: "rg-1", AlbumCredits: credits}
	for _, track := range due[0].Tracks {
		release.Tracks = append(release.Tracks, store.CanonicalTrack{Disc: track.Disc, Index: track.Index, Title: track.Title, DurationMs: track.DurationMs, RecordingID: "rec-" + track.TrackID, Credits: credits})
	}
	if _, err := s.ApplyMusicBrainzRelease(ctx, due[0], release, applySeq); err != nil {
		t.Fatal(err)
	}
	album, _ := s.AlbumsNeedingArt(ctx, 1)
	artist, _ := s.ArtistsNeedingArt(ctx, 1)
	_ = s.MarkAlbumArtTried(ctx, album[0].AlbumID)
	_ = s.MarkArtistArtTried(ctx, artist[0].ArtistID)

	m := NewManager(s, dataDir)
	e := &retryOrderingEnricher{st: s, albumID: album[0].AlbumID, artistID: artist[0].ArtistID, started: make(chan int, 2), release: make(chan struct{})}
	setEnricherWithoutStart(m, e)
	m.TriggerEnrichment()
	<-e.started
	m.TriggerAlbumEnrichment()
	m.TriggerArtistEnrichment()
	close(e.release)
	if run := <-e.started; run != 2 {
		t.Fatalf("follow-up run=%d", run)
	}
	m.Close()
	if len(e.seenAlbums) != 2 || e.seenAlbums[0] || !e.seenAlbums[1] {
		t.Fatalf("album retry visibility by pass: %v", e.seenAlbums)
	}
	if len(e.seenArtists) != 2 || e.seenArtists[0] || !e.seenArtists[1] {
		t.Fatalf("artist retry visibility by pass: %v", e.seenArtists)
	}
}

func TestFailedResetRemainsRetryableOnLaterTrigger(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	e := newBlockingEnricher()
	setEnricherWithoutStart(m, e)
	attempted := make(chan struct{}, 2)
	backoff := make(chan struct{}, 1)
	retry := make(chan struct{})
	m.waitResetRetry = func(context.Context, int) bool {
		backoff <- struct{}{}
		<-retry
		return true
	}
	attempts := 0
	m.resetArtRetries = func(context.Context, bool) error {
		attempts++
		attempted <- struct{}{}
		if attempts == 1 {
			return errors.New("synthetic reset failure")
		}
		return nil
	}

	m.TriggerAlbumEnrichment()
	<-attempted
	<-backoff
	select {
	case <-e.started:
		t.Fatal("enrichment ran after failed reset")
	default:
	}
	close(retry)
	<-attempted
	if run := <-e.started; run != 1 {
		t.Fatalf("run after reset retry=%d", run)
	}
	e.release <- struct{}{}
	m.Close()
}

func TestCloseCancelsResetRetryBackoff(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	setEnricherWithoutStart(m, newBlockingEnricher())
	m.resetArtRetries = func(context.Context, bool) error { return errors.New("synthetic reset failure") }
	waiting := make(chan struct{})
	m.waitResetRetry = func(ctx context.Context, attempt int) bool {
		close(waiting)
		return waitForResetRetry(ctx, attempt)
	}
	m.TriggerAlbumEnrichment()
	<-waiting
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel reset retry backoff")
	}
}

func TestStartupScanTriggersEnrichmentAfterCompletion(t *testing.T) {
	s, dataDir := openStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.flac"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, settingSourceKind, "filesystem"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, settingFilesystemPath, dir); err != nil {
		t.Fatal(err)
	}

	m := NewManager(s, dataDir)
	defer m.Close()

	var mu sync.Mutex
	var order []string
	enrichmentStarted := make(chan struct{}, 1)
	enricher := enricherFunc(func(context.Context) {
		mu.Lock()
		order = append(order, "enrich")
		mu.Unlock()
		enrichmentStarted <- struct{}{}
	})
	scanStarted := make(chan struct{}, 1)
	m.onScanStart = func() {
		mu.Lock()
		order = append(order, "scan")
		mu.Unlock()
		select {
		case scanStarted <- struct{}{}:
		default:
		}
	}
	m.SetEnricher(enricher)

	select {
	case <-scanStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("startup scan never ran")
	}
	select {
	case <-enrichmentStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("successful startup scan did not trigger enrichment")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "scan" || order[1] != "enrich" {
		t.Fatalf("startup order = %v, want scan before enrichment", order)
	}
}

func TestStartupScanFailureTriggersEnrichmentAfterScanEnds(t *testing.T) {
	s, dataDir := openStore(t)
	m := NewManager(s, dataDir)
	defer m.Close()
	src := &sequenceSource{
		entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("synthetic scan failure"),
		meta: source.TrackMeta{
			NativeID: "startup.flac", Title: "Startup", Album: "Startup Album",
			PrimaryArtist: source.ArtistReference{Name: "Artist"},
			AlbumCredits:  []source.ArtistCredit{{Name: "Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Artist"}},
			Container: "flac", Codec: "flac", Version: 1,
		},
	}
	m.mu.Lock()
	m.src = src
	m.mu.Unlock()
	e := newBlockingEnricher()
	m.SetEnricher(e)

	select {
	case <-src.entered:
	case <-time.After(time.Second):
		t.Fatal("startup scan did not begin")
	}
	select {
	case <-e.started:
		t.Fatal("enrichment ran concurrently with failed startup scan")
	default:
	}
	close(src.release)
	select {
	case run := <-e.started:
		if run != 1 {
			t.Fatalf("post-failure run=%d", run)
		}
	case <-time.After(time.Second):
		t.Fatal("startup enrichment did not run after scan failure")
	}
	if status := m.Status(context.Background()); status.Scanning || status.LastScanError == "" {
		t.Fatalf("post-failure status=%+v", status)
	}
	e.release <- struct{}{}
}

type enricherFunc func(context.Context)

func (f enricherFunc) Run(ctx context.Context) { f(ctx) }
