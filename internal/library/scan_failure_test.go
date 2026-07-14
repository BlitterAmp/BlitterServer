package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/source/filesystem"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func scanArtHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestChangedTrackParseFailureRemovesThenRestoresWithNewArtifactVersion(t *testing.T) {
	candidate, _ := candidateMeta("unstable.flac", "Stable", 10, 100)
	s, m, fake, _ := incrementalManager(t, candidate)
	rescanAndWait(t, m)
	before, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	summary, err := s.GetLibrarySummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	oldArtifact, _, err := s.UpsertArtifact(context.Background(), before[0].TrackID, "original", 0)
	if err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	fake.candidates[0].MtimeNS++
	fake.parseFailures[candidate.NativeID] = 1
	fake.parseErrors[candidate.NativeID] = filesystem.ErrCandidateChanged
	fake.mu.Unlock()
	rescanAndWait(t, m)

	after, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("changed parse failure remained playable: before=%+v after=%+v err=%v", before, after, err)
	}
	if _, found, err := m.Open(context.Background(), before[0].TrackID); err != nil || found {
		t.Fatalf("raw stream remained available: found=%v err=%v", found, err)
	}
	if _, _, err := s.UpsertArtifact(context.Background(), before[0].TrackID, "original", 0); err == nil {
		t.Fatal("artifact request accepted missing track")
	}
	changes, _, err := s.ChangesSince(context.Background(), summary.Version, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, change := range changes {
		if change.Kind == "track" && change.ID == before[0].TrackID && change.Missing {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("parse failure did not emit removal: %+v", changes)
	}
	fake.mu.Lock()
	updated := fake.candidates[0]
	fake.metadata[updated.NativeID] = completeManagerMeta(updated, "Restored", "")
	fake.mu.Unlock()
	rescanAndWait(t, m)
	restored, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(restored) != 1 || restored[0].TrackID != before[0].TrackID {
		t.Fatalf("restoration=%+v err=%v", restored, err)
	}
	newArtifact, _, err := s.UpsertArtifact(context.Background(), restored[0].TrackID, "original", 0)
	if err != nil || newArtifact.ArtifactID == oldArtifact.ArtifactID {
		t.Fatalf("artifact version not partitioned: old=%s new=%s err=%v", oldArtifact.ArtifactID, newArtifact.ArtifactID, err)
	}
	if parse, _ := fake.counts(); parse != 3 {
		t.Fatalf("unstable candidate was not retried: parse calls=%d", parse)
	}
}

func TestEmbeddedArtFailureCachesMetadataAndRetriesOnlyArt(t *testing.T) {
	candidate, _ := candidateMeta("art.flac", "Art", 10, 100)
	s, m, fake, instance := incrementalManager(t, candidate)
	fake.metadata[candidate.NativeID] = completeManagerMeta(candidate, "Art", "expected-art-hash")
	fake.artErr = errors.New("synthetic art extraction failure")
	upserts := 0
	m.upsertTrack = func(ctx context.Context, kind string, meta source.TrackMeta, artID string, seq int64) error {
		upserts++
		return s.UpsertTrack(ctx, kind, meta, artID, seq)
	}
	rescanAndWait(t, m)
	cache, err := s.LoadSourceFileCache(context.Background(), instance.ID, "filesystem", 1)
	if err != nil || len(cache) != 1 || !cache[candidate.NativeID].ArtPending {
		t.Fatalf("art-pending metadata cache=%+v err=%v", cache, err)
	}
	tracks, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(tracks) != 1 || tracks[0].Title != "Art" {
		t.Fatalf("valid audio metadata was not indexed after optional art failure: tracks=%+v err=%v", tracks, err)
	}
	rescanAndWait(t, m)
	if parse, art := fake.counts(); parse != 1 || art != 2 || upserts != 1 {
		t.Fatalf("pending art re-probed metadata: parse=%d art=%d upserts=%d", parse, art, upserts)
	}
	fake.mu.Lock()
	fake.artErr = nil
	fake.mu.Unlock()
	rescanAndWait(t, m)
	if parse, art := fake.counts(); parse != 1 || art != 3 || upserts != 1 {
		t.Fatalf("art attach retried metadata: parse=%d art=%d upserts=%d", parse, art, upserts)
	}
	cache, err = s.LoadSourceFileCache(context.Background(), instance.ID, "filesystem", 1)
	if err != nil || cache[candidate.NativeID].ArtPending {
		t.Fatalf("pending state not cleared: cache=%+v err=%v", cache, err)
	}
	rescanAndWait(t, m)
	if parse, art := fake.counts(); parse != 1 || art != 3 || upserts != 1 {
		t.Fatalf("complete hit did work: parse=%d art=%d upserts=%d", parse, art, upserts)
	}
}

func TestReparsedSourceArtReplacesExistingArtAndEmitsChange(t *testing.T) {
	ctx := context.Background()
	candidate, _ := candidateMeta("art.flac", "Art", 10, 100)
	s, m, fake, _ := incrementalManager(t, candidate)
	oldData, newData := []byte("old embedded art"), []byte("new embedded art")
	oldHash, newHash := scanArtHash(oldData), scanArtHash(newData)
	fake.artData = map[string][]byte{oldHash: oldData, newHash: newData}
	fake.metadata[candidate.NativeID] = completeManagerMeta(candidate, "Art", oldHash)
	rescanAndWait(t, m)
	tracks, _, err := s.ListTracks(ctx, "title", "", 10)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("initial tracks=%+v err=%v", tracks, err)
	}
	oldArtID := tracks[0].ArtID
	before, err := s.GetLibrarySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	fake.candidates[0].MtimeNS++
	changedCandidate := fake.candidates[0]
	fake.metadata[candidate.NativeID] = completeManagerMeta(changedCandidate, "Art", newHash)
	fake.mu.Unlock()
	rescanAndWait(t, m)
	track, found, err := s.GetTrack(ctx, tracks[0].TrackID)
	if err != nil || !found || track.ArtID == "" || track.ArtID == oldArtID {
		t.Fatalf("reparsed art track=%+v found=%v err=%v", track, found, err)
	}
	changes, _, err := s.ChangesSince(ctx, before.Version, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for _, change := range changes {
		changed = changed || change.Kind == "track" && change.ID == track.TrackID
	}
	if !changed {
		t.Fatalf("replacement did not emit track change: %+v", changes)
	}
}

func TestPendingSourceArtRetryReplacesExistingArt(t *testing.T) {
	ctx := context.Background()
	candidate, _ := candidateMeta("art.flac", "Art", 10, 100)
	s, m, fake, instance := incrementalManager(t, candidate)
	oldData, newData := []byte("old embedded art"), []byte("new embedded art")
	oldHash, newHash := scanArtHash(oldData), scanArtHash(newData)
	fake.artData = map[string][]byte{oldHash: oldData, newHash: newData}
	fake.metadata[candidate.NativeID] = completeManagerMeta(candidate, "Art", oldHash)
	rescanAndWait(t, m)
	tracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	oldArtID := tracks[0].ArtID

	fake.mu.Lock()
	fake.candidates[0].MtimeNS++
	changedCandidate := fake.candidates[0]
	fake.metadata[candidate.NativeID] = completeManagerMeta(changedCandidate, "Art", newHash)
	fake.artErr = errors.New("synthetic replacement extraction failure")
	fake.mu.Unlock()
	rescanAndWait(t, m)
	cache, err := s.LoadSourceFileCache(ctx, instance.ID, "filesystem", 1)
	if err != nil || !cache[candidate.NativeID].ArtPending {
		t.Fatalf("replacement pending cache=%+v err=%v", cache, err)
	}

	fake.mu.Lock()
	fake.artErr = nil
	fake.mu.Unlock()
	rescanAndWait(t, m)
	track, found, err := s.GetTrack(ctx, tracks[0].TrackID)
	if err != nil || !found || track.ArtID == "" || track.ArtID == oldArtID {
		t.Fatalf("retried replacement track=%+v found=%v err=%v", track, found, err)
	}
	cache, err = s.LoadSourceFileCache(ctx, instance.ID, "filesystem", 1)
	if err != nil || cache[candidate.NativeID].ArtPending {
		t.Fatalf("successful replacement retained pending cache=%+v err=%v", cache, err)
	}
}

func TestDamagedDeduplicatedArtIsRepairedAndThenFullyCached(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(string) error
	}{
		{name: "missing", damage: os.Remove},
		{name: "corrupt", damage: func(path string) error { return os.WriteFile(path, []byte("corrupt blob"), 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			candidate, _ := candidateMeta("art.flac", "Art", 10, 100)
			s, m, fake, instance := incrementalManager(t, candidate)
			data := []byte("verified embedded art")
			hash := scanArtHash(data)
			fake.artData = map[string][]byte{hash: data}
			fake.metadata[candidate.NativeID] = completeManagerMeta(candidate, "Art", hash)
			rescanAndWait(t, m)
			tracks, _, _ := s.ListTracks(ctx, "title", "", 10)
			originalArtID := tracks[0].ArtID
			if err := tc.damage(filepath.Join(m.dataDir, "art", hash)); err != nil {
				t.Fatal(err)
			}

			fake.mu.Lock()
			fake.parserVersion++
			fake.mu.Unlock()
			rescanAndWait(t, m)
			if parse, art := fake.counts(); parse != 2 || art != 2 {
				t.Fatalf("repair work parse=%d art=%d", parse, art)
			}
			track, found, err := s.GetTrack(ctx, tracks[0].TrackID)
			if err != nil || !found || track.ArtID != originalArtID {
				t.Fatalf("repair changed canonical art id: track=%+v found=%v err=%v", track, found, err)
			}
			cache, err := s.LoadSourceFileCache(ctx, instance.ID, "filesystem", 2)
			if err != nil || cache[candidate.NativeID].ArtPending {
				t.Fatalf("repair cache=%+v err=%v", cache, err)
			}
			rescanAndWait(t, m)
			if parse, art := fake.counts(); parse != 2 || art != 2 {
				t.Fatalf("post-repair scan was not fully cached: parse=%d art=%d", parse, art)
			}
		})
	}
}

func TestArtPersistenceFailuresCountInTerminalScanStats(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *store.Store, string)
	}{
		{name: "upsert", setup: func(t *testing.T, _ *store.Store, dataDir string) {
			if err := os.WriteFile(filepath.Join(dataDir, "art"), []byte("blocking file"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "attach", setup: func(t *testing.T, _ *store.Store, dataDir string) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "blitterserver.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(context.Background(), `CREATE TRIGGER fail_track_art_attachment BEFORE UPDATE OF art_id ON tracks BEGIN SELECT RAISE(ABORT,'art attachment failed'); END`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dataDir := openStore(t)
			tc.setup(t, s, dataDir)
			candidate, _ := candidateMeta("art.flac", "Art", 10, 11)
			instance, _, err := s.ConfigureFilesystemSource(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("verified art")
			hash := scanArtHash(data)
			fake := &candidateSource{parserVersion: 1, candidates: []source.TrackCandidate{candidate}, metadata: map[string]source.TrackMeta{candidate.NativeID: completeManagerMeta(candidate, "Art", hash)}, parseFailures: map[string]int{}, parseErrors: map[string]error{}, artData: map[string][]byte{hash: data}}
			m := NewManager(s, dataDir)
			t.Cleanup(m.Close)
			m.mu.Lock()
			m.src, m.sourceID, m.sourceGeneration = fake, instance.ID, instance.Generation
			m.mu.Unlock()
			buf := captureManagerLogs(m)
			rescanAndWait(t, m)
			out := buf.String()
			if !strings.Contains(out, `msg="filesystem scan failed"`) || !strings.Contains(out, "failed=1") {
				t.Fatalf("terminal scan stats=%s", out)
			}
			if strings.Contains(out, dataDir) || strings.Contains(m.Status(context.Background()).LastScanError, dataDir) {
				t.Fatalf("storage path leaked: log=%s status=%q", out, m.Status(context.Background()).LastScanError)
			}
		})
	}
}

func TestStaleScanPublishesMutationBeforeFailedReplacement(t *testing.T) {
	candidate, _ := candidateMeta("old.flac", "Old", 10, 100)
	s, m, fake, _ := incrementalManager(t, candidate)
	fake.afterEmitStarted = make(chan struct{})
	fake.afterEmitRelease = make(chan struct{})
	bus := events.NewBus(s)
	m.SetBus(bus)
	since := bus.LatestSeq(context.Background())
	sub, cancel := bus.Subscribe("", since)
	defer cancel()
	if err := m.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-fake.afterEmitStarted
	newRoot := t.TempDir()
	if err := m.Configure(context.Background(), newRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(newRoot); err != nil {
		t.Fatal(err)
	}
	close(fake.afterEmitRelease)
	waitIdle(t, m)
	select {
	case event := <-sub:
		if event.Type != "library.changed" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("stale scan discarded its committed mutation event")
	}
	changes, _, err := s.ChangesSince(context.Background(), 0, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range changes {
		found = found || change.Kind == "track"
	}
	if !found {
		t.Fatalf("old committed delta not visible: %+v", changes)
	}
}

func TestExistingDeduplicatedArtSkipsExtractionOnReparse(t *testing.T) {
	candidate, _ := candidateMeta("art.flac", "Art", 10, 100)
	_, m, fake, _ := incrementalManager(t, candidate)
	fake.metadata[candidate.NativeID] = completeManagerMeta(candidate, "Art", scanArtHash([]byte("art")))
	rescanAndWait(t, m)
	fake.mu.Lock()
	fake.parserVersion++
	fake.mu.Unlock()
	rescanAndWait(t, m)
	if parse, art := fake.counts(); parse != 2 || art != 1 {
		t.Fatalf("deduplicated art was re-extracted: parse=%d art=%d", parse, art)
	}
}

func TestFailedScanPublishesCommittedPartialMutation(t *testing.T) {
	candidate, _ := candidateMeta("partial.flac", "Partial", 10, 100)
	s, m, fake, _ := incrementalManager(t, candidate)
	fake.enumerateErr = errors.New("synthetic failure after candidate")
	bus := events.NewBus(s)
	m.SetBus(bus)
	since := bus.LatestSeq(context.Background())
	sub, cancel := bus.Subscribe("", since)
	defer cancel()

	rescanAndWait(t, m)
	tracks, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("partial mutation not committed: tracks=%+v err=%v", tracks, err)
	}
	select {
	case event := <-sub:
		if event.Type != "library.changed" {
			t.Fatalf("partial mutation event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("failed scan did not publish committed partial mutation")
	}
}

func TestUnlinkPublishesRemovalDelta(t *testing.T) {
	candidate, _ := candidateMeta("song.flac", "Song", 10, 100)
	s, m, _, _ := incrementalManager(t, candidate)
	rescanAndWait(t, m)
	bus := events.NewBus(s)
	m.SetBus(bus)
	since := bus.LatestSeq(context.Background())
	sub, cancel := bus.Subscribe("", since)
	defer cancel()
	if err := m.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sub:
		if event.Type != "library.changed" {
			t.Fatalf("unlink event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("unlink did not publish library.changed")
	}
	tracks, _, err := s.ListTracks(context.Background(), "title", "", 10)
	if err != nil || len(tracks) != 0 {
		t.Fatalf("unlink tracks=%+v err=%v", tracks, err)
	}
}

var _ source.MusicSource = (*candidateSource)(nil)

func TestRootReplacementRemovesOldIdentityBeforeNewScan(t *testing.T) {
	ctx := context.Background()
	s, dataDir := openStore(t)
	oldRoot, newRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(oldRoot, "same.flac"), []byte("old bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "same.flac"), []byte("new bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ConfigureFilesystemSource(ctx, oldRoot); err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	meta := completeManagerMeta(source.TrackCandidate{NativeID: "same.flac", SizeBytes: 9, MtimeNS: 1}, "Old Identity", "")
	if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	tracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	m := NewManager(s, dataDir)
	t.Cleanup(m.Close)
	rc, found, err := m.Open(ctx, tracks[0].TrackID)
	if err != nil || !found {
		t.Fatalf("old open found=%v err=%v", found, err)
	}
	oldBytes, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(oldBytes) != "old bytes" {
		t.Fatalf("old bytes=%q", oldBytes)
	}
	bus := events.NewBus(s)
	m.SetBus(bus)
	sub, cancel := bus.Subscribe("", bus.LatestSeq(ctx))
	defer cancel()
	<-m.operation
	if err := m.Configure(ctx, newRoot); err != nil {
		t.Fatal(err)
	}
	if _, found, err := m.Open(ctx, tracks[0].TrackID); err != nil || found {
		t.Fatalf("old identity resolved through replacement root: found=%v err=%v", found, err)
	}
	visible, _, err := s.ListTracks(ctx, "title", "", 10)
	if err != nil || len(visible) != 0 {
		t.Fatalf("old catalog remained visible: %+v err=%v", visible, err)
	}
	select {
	case event := <-sub:
		if event.Type != "library.changed" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("root replacement did not publish removals")
	}
	m.operation <- struct{}{}
}

func TestOpenDoesNotResolveOldIdentityThroughReplacementRoot(t *testing.T) {
	ctx := context.Background()
	s, dataDir := openStore(t)
	oldRoot, newRoot := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(oldRoot, "same.flac"), []byte("old bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "same.flac"), []byte("new bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ConfigureFilesystemSource(ctx, oldRoot); err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	meta := completeManagerMeta(source.TrackCandidate{NativeID: "same.flac", SizeBytes: 9, MtimeNS: 1}, "Old Identity", "")
	if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	tracks, _, err := s.ListTracks(ctx, "title", "", 10)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("old tracks=%+v err=%v", tracks, err)
	}

	m := NewManager(s, dataDir)
	t.Cleanup(m.Close)
	resolved := make(chan struct{})
	releaseOpen := make(chan struct{})
	configureReady := make(chan struct{})
	m.onOpenResolved = func() {
		close(resolved)
		<-releaseOpen
	}
	m.onConfigureReady = func() { close(configureReady) }
	<-m.operation // Keep the replacement scan from introducing a new identity.
	defer func() { m.operation <- struct{}{} }()

	type openResult struct {
		data  []byte
		found bool
		err   error
	}
	opened := make(chan openResult, 1)
	go func() {
		rc, found, err := m.Open(ctx, tracks[0].TrackID)
		if err != nil || !found {
			opened <- openResult{found: found, err: err}
			return
		}
		data, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr == nil {
			readErr = closeErr
		}
		opened <- openResult{data: data, found: true, err: readErr}
	}()
	<-resolved

	openHoldsLock := !m.mu.TryLock()
	configured := make(chan error, 1)
	go func() { configured <- m.Configure(ctx, newRoot) }()
	<-configureReady
	if openHoldsLock {
		close(releaseOpen)
	} else {
		// The vulnerable implementation resolves before locking. Hold Open at
		// that boundary until Configure has swapped the source deterministically.
		m.mu.Unlock()
		if err := <-configured; err != nil {
			t.Fatal(err)
		}
		close(releaseOpen)
	}

	result := <-opened
	if result.err != nil || !result.found {
		t.Fatalf("open found=%v err=%v", result.found, result.err)
	}
	if string(result.data) != "old bytes" {
		t.Fatalf("old identity returned replacement bytes %q", result.data)
	}
	if openHoldsLock {
		if err := <-configured; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLateCanonicalUpsertFailurePublishesPartialMutation(t *testing.T) {
	candidate, _ := candidateMeta("partial.flac", "Partial", 10, 100)
	s, m, fake, _ := incrementalManager(t, candidate)
	meta := fake.metadata[candidate.NativeID]
	meta.TrackCredits = nil
	fake.metadata[candidate.NativeID] = meta
	bus := events.NewBus(s)
	m.SetBus(bus)
	sub, cancel := bus.Subscribe("", bus.LatestSeq(context.Background()))
	defer cancel()
	rescanAndWait(t, m)
	select {
	case event := <-sub:
		if event.Type != "library.changed" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("late UpsertTrack failure did not publish partial mutation")
	}
	changes, _, err := s.ChangesSince(context.Background(), 0, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range changes {
		found = found || change.Kind == "track"
	}
	if !found {
		t.Fatalf("partial track mutation not visible: %+v", changes)
	}
}
