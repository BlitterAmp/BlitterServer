package library

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

type candidateSource struct {
	mu               sync.Mutex
	parserVersion    int
	candidates       []source.TrackCandidate
	metadata         map[string]source.TrackMeta
	parseFailures    map[string]int
	parseErrors      map[string]error
	enumerateErr     error
	artErr           error
	artData          map[string][]byte
	parseCalls       int
	artCalls         int
	parseStarted     chan struct{}
	parseRelease     chan struct{}
	enumStarted      chan struct{}
	enumRelease      chan struct{}
	afterEmitStarted chan struct{}
	afterEmitRelease chan struct{}
}

func (s *candidateSource) Kind() string { return "filesystem" }
func (s *candidateSource) ParserVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parserVersion
}
func (s *candidateSource) Enumerate(ctx context.Context, emit func(source.TrackCandidate) error) error {
	s.mu.Lock()
	candidates := append([]source.TrackCandidate(nil), s.candidates...)
	err := s.enumerateErr
	started, release := s.enumStarted, s.enumRelease
	s.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, candidate := range candidates {
		if err := emit(candidate); err != nil {
			return err
		}
	}
	if s.afterEmitStarted != nil {
		close(s.afterEmitStarted)
	}
	if s.afterEmitRelease != nil {
		select {
		case <-s.afterEmitRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func (s *candidateSource) Parse(ctx context.Context, candidate source.TrackCandidate) (source.TrackMeta, error) {
	s.mu.Lock()
	s.parseCalls++
	started, release := s.parseStarted, s.parseRelease
	if s.parseFailures[candidate.NativeID] > 0 {
		s.parseFailures[candidate.NativeID]--
		err := s.parseErrors[candidate.NativeID]
		if err == nil {
			err = errors.New("synthetic parse failure")
		}
		s.mu.Unlock()
		return source.TrackMeta{}, err
	}
	meta, ok := s.metadata[candidate.NativeID]
	s.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return source.TrackMeta{}, ctx.Err()
		}
	}
	if !ok {
		return source.TrackMeta{}, errors.New("missing synthetic metadata")
	}
	return meta, nil
}
func (s *candidateSource) Open(context.Context, string) (io.ReadSeekCloser, error) {
	return nil, errors.New("unused")
}
func (s *candidateSource) Art(_ context.Context, _ source.TrackCandidate, hash string) ([]byte, string, error) {
	s.mu.Lock()
	s.artCalls++
	err := s.artErr
	data := append([]byte(nil), s.artData[hash]...)
	s.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	if data == nil {
		data = []byte("art")
	}
	return data, "image/jpeg", nil
}
func (s *candidateSource) counts() (parse, art int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parseCalls, s.artCalls
}

func candidateMeta(native, title string, size, mtime int64) (source.TrackCandidate, source.TrackMeta) {
	candidate := source.TrackCandidate{NativeID: native, SizeBytes: size, MtimeNS: mtime}
	meta := source.TrackMeta{
		NativeID: native, Title: title, PrimaryArtist: source.ArtistReference{Name: "Artist"},
		TrackCredits: []source.ArtistCredit{{Name: "Artist"}}, AlbumCredits: []source.ArtistCredit{{Name: "Artist"}},
		Album: "Album", Container: "flac", Codec: "flac", SizeBytes: size, Version: mtime,
	}
	return candidate, meta
}

func incrementalManager(t *testing.T, candidates ...source.TrackCandidate) (*store.Store, *Manager, *candidateSource, store.SourceInstance) {
	t.Helper()
	s, dataDir := openStore(t)
	instance, _, err := s.ConfigureFilesystemSource(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &candidateSource{parserVersion: 1, candidates: candidates, metadata: map[string]source.TrackMeta{}, parseFailures: map[string]int{}, parseErrors: map[string]error{}}
	for _, candidate := range candidates {
		_, meta := candidateMeta(candidate.NativeID, candidate.NativeID, candidate.SizeBytes, candidate.MtimeNS)
		fake.metadata[candidate.NativeID] = meta
	}
	m := NewManager(s, dataDir)
	m.mu.Lock()
	m.src = fake
	m.sourceID = instance.ID
	m.sourceGeneration = instance.Generation
	m.mu.Unlock()
	t.Cleanup(m.Close)
	return s, m, fake, instance
}

func rescanAndWait(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)
}

func TestUnchangedSecondScanSkipsParseArtAndCanonicalUpsert(t *testing.T) {
	candidate, _ := candidateMeta("song.flac", "Raw Title", 100, 123456789)
	s, m, fake, instance := incrementalManager(t, candidate)
	fake.metadata[candidate.NativeID] = completeManagerMeta(candidate, "Raw Title", "art-hash")
	upserts := 0
	m.upsertTrack = func(ctx context.Context, kind string, meta source.TrackMeta, artID string, seq int64) error {
		upserts++
		return s.UpsertTrack(ctx, kind, meta, artID, seq)
	}
	rescanAndWait(t, m)
	if parse, art := fake.counts(); parse != 1 || art != 1 || upserts != 1 {
		t.Fatalf("first scan parse=%d art=%d upsert=%d", parse, art, upserts)
	}
	tracks, _, _ := s.ListTracks(context.Background(), "title", "", 10)
	if len(tracks) != 1 {
		t.Fatalf("tracks=%+v", tracks)
	}
	due, err := s.DueMusicBrainzAlbums(context.Background(), time.Now(), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("due album=%+v err=%v", due, err)
	}
	canonicalCredits := []source.ArtistCredit{{Name: "Canonical Artist", MBID: "artist-mbid"}}
	canonical := store.CanonicalRelease{
		ReleaseID: "release-mbid", ReleaseGroupID: "release-group-mbid",
		AlbumCredits: canonicalCredits, Authoritative: true,
	}
	for _, local := range due[0].Tracks {
		canonical.Tracks = append(canonical.Tracks, store.CanonicalTrack{
			Disc: local.Disc, Index: local.Index, DurationMs: local.DurationMs,
			Title: "Canonical Title", RecordingID: "recording-mbid", Credits: canonicalCredits,
		})
	}
	enrichSeq, _ := s.NextScanSeq(context.Background())
	if changed, err := s.ApplyMusicBrainzRelease(context.Background(), due[0], canonical, enrichSeq); err != nil || !changed {
		t.Fatalf("canonical enrichment changed=%v err=%v", changed, err)
	}
	enriched, found, err := s.GetTrack(context.Background(), tracks[0].TrackID)
	if err != nil || !found || enriched.ArtistCredits[0].Name != "Canonical Artist" || enriched.MusicBrainzRecordingID != "recording-mbid" {
		t.Fatalf("enriched track=%+v found=%v err=%v", enriched, found, err)
	}
	if err := s.SetSetting(context.Background(), "test_marker", tracks[0].TrackID); err != nil {
		t.Fatal(err)
	}
	beforeChanges, _, _ := s.ChangesSince(context.Background(), 0, "", 100)
	rescanAndWait(t, m)
	if parse, art := fake.counts(); parse != 1 || art != 1 || upserts != 1 {
		t.Fatalf("unchanged scan parse=%d art=%d upsert=%d", parse, art, upserts)
	}
	afterChanges, _, _ := s.ChangesSince(context.Background(), 0, "", 100)
	if len(afterChanges) != len(beforeChanges) {
		t.Fatalf("cache hit emitted delta: before=%+v after=%+v", beforeChanges, afterChanges)
	}
	after, found, err := s.GetTrack(context.Background(), tracks[0].TrackID)
	if err != nil || !found || after.ArtistCredits[0].Name != enriched.ArtistCredits[0].Name || after.MusicBrainzRecordingID != enriched.MusicBrainzRecordingID {
		t.Fatalf("cache hit replaced canonical identity: before=%+v after=%+v err=%v", enriched, after, err)
	}
	cache, err := s.LoadSourceFileCache(context.Background(), instance.ID, "filesystem", 1)
	if err != nil || len(cache) != 1 {
		t.Fatalf("cache=%+v err=%v", cache, err)
	}
}

func completeManagerMeta(candidate source.TrackCandidate, title, artHash string) source.TrackMeta {
	return source.TrackMeta{
		NativeID: candidate.NativeID, Title: title, PrimaryArtist: source.ArtistReference{Name: "Artist"},
		TrackCredits: []source.ArtistCredit{{Name: "Artist"}}, AlbumCredits: []source.ArtistCredit{{Name: "Artist"}},
		Album: "Album", Container: "flac", Codec: "flac", SizeBytes: candidate.SizeBytes,
		Version: candidate.MtimeNS, ArtHash: artHash,
	}
}

func TestFingerprintAndParserChangesReparse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*candidateSource)
	}{
		{name: "mtime-ns", mutate: func(s *candidateSource) { s.candidates[0].MtimeNS++ }},
		{name: "size", mutate: func(s *candidateSource) { s.candidates[0].SizeBytes++ }},
		{name: "parser-version", mutate: func(s *candidateSource) { s.parserVersion++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, _ := candidateMeta("song.flac", "Song", 100, 900000001)
			_, m, fake, _ := incrementalManager(t, candidate)
			rescanAndWait(t, m)
			fake.mu.Lock()
			tc.mutate(fake)
			changed := fake.candidates[0]
			fake.metadata[changed.NativeID] = completeManagerMeta(changed, "Changed", "")
			fake.mu.Unlock()
			rescanAndWait(t, m)
			if parse, _ := fake.counts(); parse != 2 {
				t.Fatalf("parse calls=%d", parse)
			}
		})
	}
}

func TestDeletionPrunesCacheAndRenameIsDeleteAdd(t *testing.T) {
	first, _ := candidateMeta("first.flac", "First", 10, 11)
	second, _ := candidateMeta("second.flac", "Second", 20, 21)
	s, m, fake, instance := incrementalManager(t, first, second)
	rescanAndWait(t, m)
	fake.mu.Lock()
	renamed := source.TrackCandidate{NativeID: "renamed.flac", SizeBytes: first.SizeBytes, MtimeNS: first.MtimeNS}
	fake.candidates = []source.TrackCandidate{renamed}
	fake.metadata[renamed.NativeID] = completeManagerMeta(renamed, "Renamed", "")
	fake.mu.Unlock()
	rescanAndWait(t, m)
	tracks, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(tracks) != 1 || tracks[0].Title != "Renamed" {
		t.Fatalf("tracks=%+v err=%v", tracks, err)
	}
	cache, err := s.LoadSourceFileCache(context.Background(), instance.ID, "filesystem", 1)
	if err != nil || len(cache) != 1 || cache[renamed.NativeID].Meta.NativeID != renamed.NativeID {
		t.Fatalf("cache after rename=%+v err=%v", cache, err)
	}
}

func TestParseFailureRetriesAndCacheWithoutCanonicalReparses(t *testing.T) {
	candidate, meta := candidateMeta("song.flac", "Song", 10, 12)
	s, m, fake, instance := incrementalManager(t, candidate)
	fake.parseFailures[candidate.NativeID] = 1
	rescanAndWait(t, m)
	cache, _ := s.LoadSourceFileCache(context.Background(), instance.ID, "filesystem", 1)
	if len(cache) != 0 {
		t.Fatalf("failed parse cached: %+v", cache)
	}
	rescanAndWait(t, m)
	if parse, _ := fake.counts(); parse != 2 {
		t.Fatalf("parse retry calls=%d", parse)
	}

	missingCanonical := source.TrackCandidate{NativeID: "orphan.flac", SizeBytes: 3, MtimeNS: 4}
	orphanMeta := completeManagerMeta(missingCanonical, "Orphan", "")
	if err := s.PutSourceFileCache(context.Background(), instance.ID, "filesystem", 1, missingCanonical, orphanMeta); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.candidates = append(fake.candidates, missingCanonical)
	fake.metadata[missingCanonical.NativeID] = orphanMeta
	fake.mu.Unlock()
	before, _ := fake.counts()
	rescanAndWait(t, m)
	after, _ := fake.counts()
	if after != before+1 {
		t.Fatalf("orphan cache did not reparse: before=%d after=%d meta=%+v", before, after, meta)
	}
}

func TestCanonicalUpsertFailureDoesNotCacheParse(t *testing.T) {
	candidate, _ := candidateMeta("song.flac", "Song", 10, 12)
	s, m, _, instance := incrementalManager(t, candidate)
	wantErr := errors.New("synthetic canonical upsert failure")
	m.upsertTrack = func(context.Context, string, source.TrackMeta, string, int64) error { return wantErr }
	rescanAndWait(t, m)
	cache, err := s.LoadSourceFileCache(context.Background(), instance.ID, "filesystem", 1)
	if err != nil || len(cache) != 0 {
		t.Fatalf("failed canonical upsert cached parse: cache=%+v err=%v", cache, err)
	}
	if status := m.Status(context.Background()); !strings.Contains(status.LastScanError, wantErr.Error()) {
		t.Fatalf("scan error=%q", status.LastScanError)
	}
}

func TestFailedAndCancelledScansDoNotFinishOrPrune(t *testing.T) {
	candidate, _ := candidateMeta("song.flac", "Song", 10, 12)
	s, m, fake, instance := incrementalManager(t, candidate)
	rescanAndWait(t, m)
	fake.mu.Lock()
	fake.candidates = nil
	fake.enumerateErr = errors.New("synthetic enumeration failure")
	fake.mu.Unlock()
	rescanAndWait(t, m)
	assertTrackAndCachePresent(t, s, instance)

	fake.mu.Lock()
	fake.enumerateErr = nil
	fake.enumStarted = make(chan struct{})
	fake.enumRelease = make(chan struct{})
	started := fake.enumStarted
	fake.mu.Unlock()
	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	m.Close()
	assertTrackAndCachePresent(t, s, instance)
}

func assertTrackAndCachePresent(t *testing.T, s *store.Store, instance store.SourceInstance) {
	t.Helper()
	tracks, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks=%+v err=%v", tracks, err)
	}
	cache, err := s.LoadSourceFileCache(context.Background(), instance.ID, "filesystem", 1)
	if err != nil || len(cache) != 1 {
		t.Fatalf("cache=%+v err=%v", cache, err)
	}
}

func TestConfigureAndUnlinkInvalidateActiveScan(t *testing.T) {
	for _, action := range []string{"configure", "unlink"} {
		t.Run(action, func(t *testing.T) {
			candidate, _ := candidateMeta("stale.flac", "Stale", 10, 12)
			s, m, fake, _ := incrementalManager(t, candidate)
			fake.parseStarted = make(chan struct{}, 1)
			fake.parseRelease = make(chan struct{})
			if err := m.Rescan(context.Background()); err != nil {
				t.Fatal(err)
			}
			<-fake.parseStarted
			var err error
			if action == "configure" {
				err = m.Configure(context.Background(), t.TempDir())
			} else {
				err = m.Unlink(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			close(fake.parseRelease)
			waitIdle(t, m)
			tracks, _, err := s.ListTracks(context.Background(), "title", "", 10)
			if err != nil || len(tracks) != 0 {
				t.Fatalf("stale scan published after %s: tracks=%+v err=%v", action, tracks, err)
			}
		})
	}
}

func TestUnlinkMarksExistingCanonicalRowsMissing(t *testing.T) {
	candidate, _ := candidateMeta("song.flac", "Song", 10, 12)
	s, m, _, _ := incrementalManager(t, candidate)
	rescanAndWait(t, m)
	before, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("before unlink=%+v err=%v", before, err)
	}
	if err := m.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("unlink left unplayable catalog rows: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestParseFailureCompletesScanWithAggregateError(t *testing.T) {
	candidate, _ := candidateMeta("bad.flac", "Bad", 1, 2)
	_, m, fake, _ := incrementalManager(t, candidate)
	fake.parseFailures[candidate.NativeID] = 1
	rescanAndWait(t, m)
	if status := m.Status(context.Background()); status.Scanning || status.LastScanError != "1 file failed" {
		t.Fatalf("parse failure aggregate status: %+v", status)
	}
}

func TestWaitIdleDoesNotMaskQueuedReplacementScan(t *testing.T) {
	candidate, _ := candidateMeta("stale.flac", "Stale", 10, 12)
	_, m, fake, _ := incrementalManager(t, candidate)
	fake.parseStarted = make(chan struct{}, 1)
	fake.parseRelease = make(chan struct{})
	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-fake.parseStarted
	if err := m.Configure(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	close(fake.parseRelease)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !m.Status(context.Background()).Scanning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queued replacement scan never completed")
}
