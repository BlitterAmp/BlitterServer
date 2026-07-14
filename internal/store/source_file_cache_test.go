package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/pressly/goose/v3"
)

func TestMigration0015SourceFileCacheUpDownReUp(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migration.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	assertSourceFileCacheSchema(t, db)
	if err := goose.DownToContext(ctx, db, "migrations", 14); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM source_file_cache`); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("migration down retained cache table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT value FROM settings WHERE key='filesystem_source_generation'`); err != nil {
		t.Fatal(err)
	}
	var settings int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE key IN ('filesystem_source_id','filesystem_source_generation')`).Scan(&settings); err != nil || settings != 0 {
		t.Fatalf("migration down retained source settings: count=%d err=%v", settings, err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 15); err != nil {
		t.Fatal(err)
	}
	assertSourceFileCacheSchema(t, db)
}

func assertSourceFileCacheSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO source_file_cache
		(source_instance_id,source_kind,native_id,size_bytes,mtime_ns,parser_version,parsed_meta_json,art_pending)
		VALUES ('src_one','filesystem','song.flac',12,34,1,'{}',1)`); err != nil {
		t.Fatalf("insert cache row: %v", err)
	}
	var generation string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='filesystem_source_generation'`).Scan(&generation); err != nil || generation != "0" {
		t.Fatalf("source generation=%q err=%v", generation, err)
	}
}

func completeTrackMeta() source.TrackMeta {
	return source.TrackMeta{
		NativeID: "artist/album/song.flac", Title: "Song",
		PrimaryArtist: source.ArtistReference{Name: "Album Artist", MBID: "primary-mbid"},
		TrackCredits: []source.ArtistCredit{
			{Name: "Lead", JoinPhrase: " feat. ", MBID: "lead-mbid"},
			{Name: "Guest", MBID: "guest-mbid"},
		},
		AlbumCredits: []source.ArtistCredit{{Name: "Album Artist", JoinPhrase: " & ", MBID: "album-mbid"}},
		Album:        "Album", RecordingMBID: "recording-mbid", ReleaseMBID: "release-mbid",
		ReleaseGroupMBID: "release-group-mbid", Genre: "Electronic", Year: 2026, Index: 7, Disc: 2,
		DurationMs: 234567, Container: "flac", Codec: "flac", BitrateKbps: 987,
		SizeBytes: 123456789, Version: 99887766, ArtHash: "art-hash",
	}
}

func TestSourceFileCacheRoundTripsCompleteTrackMeta(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	candidate := source.TrackCandidate{NativeID: "artist/album/song.flac", SizeBytes: 123456789, MtimeNS: 456789123}
	want := completeTrackMeta()
	if err := s.PutSourceFileCache(ctx, "src_one", "filesystem", 3, candidate, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSourceFileCache(ctx, "src_one", "filesystem", 3)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := got[candidate.NativeID]
	if !ok || entry.Candidate != candidate || !reflect.DeepEqual(entry.Meta, want) {
		t.Fatalf("cache round trip: ok=%v got=%+v want=%+v", ok, entry, want)
	}
}

func TestSourceFileCacheRoundTripsPendingArtState(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	candidate := source.TrackCandidate{NativeID: "pending.flac", SizeBytes: 12, MtimeNS: 34}
	meta := completeTrackMeta()
	meta.NativeID = candidate.NativeID
	if err := s.PutSourceFileCacheState(ctx, "src_one", "filesystem", 2, candidate, meta, true); err != nil {
		t.Fatal(err)
	}
	cache, err := s.LoadSourceFileCache(ctx, "src_one", "filesystem", 2)
	if err != nil || !cache[candidate.NativeID].ArtPending {
		t.Fatalf("pending cache=%+v err=%v", cache, err)
	}
	if err := s.PutSourceFileCacheState(ctx, "src_one", "filesystem", 2, candidate, meta, false); err != nil {
		t.Fatal(err)
	}
	cache, err = s.LoadSourceFileCache(ctx, "src_one", "filesystem", 2)
	if err != nil || cache[candidate.NativeID].ArtPending {
		t.Fatalf("completed cache=%+v err=%v", cache, err)
	}
}

func TestUpsertArtErrorDoesNotExposeStoragePath(t *testing.T) {
	s := open(t)
	parent := t.TempDir()
	artDir := filepath.Join(parent, "private-art-dir")
	if err := os.WriteFile(artDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.UpsertArt(context.Background(), "hash", "image/jpeg", []byte("data"), artDir)
	var storageErr *ArtStorageError
	if err == nil || !errors.As(err, &storageErr) || strings.Contains(err.Error(), parent) {
		t.Fatalf("path-bearing art error: %v", err)
	}
}

func TestSourceFileCacheIsolatesSourceAndParserAndSkipsMalformedJSON(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	candidate := source.TrackCandidate{NativeID: "song.flac", SizeBytes: 10, MtimeNS: 20}
	if err := s.PutSourceFileCache(ctx, "src_one", "filesystem", 1, candidate, completeTrackMeta()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sourceID, kind string
		parser               int
	}{
		{name: "source", sourceID: "src_two", kind: "filesystem", parser: 1},
		{name: "kind", sourceID: "src_one", kind: "plex", parser: 1},
		{name: "parser", sourceID: "src_one", kind: "filesystem", parser: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.LoadSourceFileCache(ctx, tc.sourceID, tc.kind, tc.parser)
			if err != nil || len(got) != 0 {
				t.Fatalf("isolated load=%+v err=%v", got, err)
			}
		})
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE source_file_cache SET parsed_meta_json='{' WHERE source_instance_id='src_one'`); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSourceFileCache(ctx, "src_one", "filesystem", 1)
	if err != nil || len(got) != 0 {
		t.Fatalf("malformed JSON must be a miss: got=%+v err=%v", got, err)
	}
}

func TestFilesystemSourceInstanceRetainsNormalizedRootAndChangesGeneration(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	root := t.TempDir()
	first, changed, err := s.ConfigureFilesystemSource(ctx, filepath.Join(root, "."))
	if err != nil || !changed || first.ID == "" || first.Generation == 0 {
		t.Fatalf("first configure: %+v changed=%v err=%v", first, changed, err)
	}
	again, changed, err := s.ConfigureFilesystemSource(ctx, root)
	if err != nil || changed || again != first {
		t.Fatalf("same normalized root: first=%+v again=%+v changed=%v err=%v", first, again, changed, err)
	}
	other, changed, err := s.ConfigureFilesystemSource(ctx, t.TempDir())
	if err != nil || !changed || other.ID == first.ID || other.Generation <= first.Generation {
		t.Fatalf("different root: first=%+v other=%+v changed=%v err=%v", first, other, changed, err)
	}
}

func TestChangingRootDeletesObsoleteSourceCache(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	first, _, err := s.ConfigureFilesystemSource(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	candidate := source.TrackCandidate{NativeID: "private/old.flac", SizeBytes: 10, MtimeNS: 20}
	meta := completeTrackMeta()
	meta.NativeID = candidate.NativeID
	if err := s.PutSourceFileCache(ctx, first.ID, "filesystem", 1, candidate, meta); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ConfigureFilesystemSource(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_file_cache WHERE source_kind='filesystem'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("obsolete cache rows=%d err=%v", rows, err)
	}
}

func TestFinalizeSourceScanMarksEveryEncounteredExistingTrackSeen(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	initialSeq, _ := s.NextScanSeq(ctx)
	track := meta("unstable.flac", "Stable Canonical", "Artist", "Album", "", 2026, 1)
	if err := s.UpsertTrack(ctx, "filesystem", track, "", initialSeq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", initialSeq); err != nil {
		t.Fatal(err)
	}
	var trackID string
	var originalChange int64
	if err := s.db.QueryRowContext(ctx, `SELECT track_id,change_seq FROM tracks WHERE native_id=?`, track.NativeID).Scan(&trackID, &originalChange); err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	if err := s.FinalizeSourceScan(ctx, "filesystem", seq, "src_one", map[string]struct{}{track.NativeID: {}}, map[string]struct{}{track.NativeID: {}}); err != nil {
		t.Fatal(err)
	}
	var seen, change int64
	var missing int
	if err := s.db.QueryRowContext(ctx, `SELECT seen_seq,change_seq,missing FROM tracks WHERE track_id=?`, trackID).Scan(&seen, &change, &missing); err != nil {
		t.Fatal(err)
	}
	if seen != seq || change != originalChange || missing != 0 {
		t.Fatalf("encountered track seen=%d change=%d missing=%d", seen, change, missing)
	}
}

func TestFinalizeSourceScanRestorationConvergesTrackAlbumAndArtist(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	initialSeq, _ := s.NextScanSeq(ctx)
	track := meta("restored.flac", "Restored", "Artist", "Album", "", 2026, 1)
	if err := s.UpsertTrack(ctx, "filesystem", track, "", initialSeq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", initialSeq); err != nil {
		t.Fatal(err)
	}
	missingSeq, _ := s.NextScanSeq(ctx)
	if err := s.FinishScan(ctx, "filesystem", missingSeq); err != nil {
		t.Fatal(err)
	}
	restoreSeq, _ := s.NextScanSeq(ctx)
	if err := s.FinalizeSourceScan(ctx, "filesystem", restoreSeq, "src_one", map[string]struct{}{track.NativeID: {}}, map[string]struct{}{track.NativeID: {}}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tracks", "albums", "artists"} {
		var missing int
		var changeSeq int64
		if err := s.db.QueryRowContext(ctx, `SELECT missing,change_seq FROM `+table+` LIMIT 1`).Scan(&missing, &changeSeq); err != nil {
			t.Fatal(err)
		}
		if missing != 0 || changeSeq != restoreSeq {
			t.Fatalf("%s restoration missing=%d change_seq=%d want=%d", table, missing, changeSeq, restoreSeq)
		}
	}
}

func TestFinalizeSourceScanRollsBackSeenRestorationAndPruning(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	initialSeq, _ := s.NextScanSeq(ctx)
	track := meta("restore.flac", "Restore", "Artist", "Album", "", 2026, 1)
	if err := s.UpsertTrack(ctx, "filesystem", track, "", initialSeq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", initialSeq); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tracks SET missing=1 WHERE native_id=?`, track.NativeID); err != nil {
		t.Fatal(err)
	}
	candidate := source.TrackCandidate{NativeID: "stale.flac", SizeBytes: 1, MtimeNS: 2}
	cacheMeta := completeTrackMeta()
	cacheMeta.NativeID = candidate.NativeID
	if err := s.PutSourceFileCache(ctx, "src_one", "filesystem", 1, candidate, cacheMeta); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_scan_finish BEFORE UPDATE ON albums BEGIN SELECT RAISE(FAIL, 'synthetic finalization failure'); END`); err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	err := s.FinalizeSourceScan(ctx, "filesystem", seq, "src_one", map[string]struct{}{track.NativeID: {}}, map[string]struct{}{track.NativeID: {}})
	if err == nil {
		t.Fatal("finalization unexpectedly succeeded")
	}
	var missing int
	var seen int64
	if err := s.db.QueryRowContext(ctx, `SELECT missing,seen_seq FROM tracks WHERE native_id=?`, track.NativeID).Scan(&missing, &seen); err != nil {
		t.Fatal(err)
	}
	if missing != 1 || seen != initialSeq {
		t.Fatalf("failed finalization leaked restoration: missing=%d seen=%d", missing, seen)
	}
	var cacheRows int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_file_cache WHERE native_id='stale.flac'`).Scan(&cacheRows); err != nil || cacheRows != 1 {
		t.Fatalf("failed finalization leaked pruning: rows=%d err=%v", cacheRows, err)
	}
}

func TestUnlinkAtomicallyMarksLibraryMissingAndClearsFilesystemCache(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	instance, _, err := s.ConfigureFilesystemSource(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	track := meta("song.flac", "Song", "Artist", "Album", "", 2026, 1)
	if err := s.UpsertTrack(ctx, "filesystem", track, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	candidate := source.TrackCandidate{NativeID: track.NativeID, SizeBytes: track.SizeBytes, MtimeNS: 99}
	if err := s.PutSourceFileCache(ctx, instance.ID, "filesystem", 1, candidate, track); err != nil {
		t.Fatal(err)
	}
	generation, err := s.UnlinkFilesystemSource(ctx)
	if err != nil || generation <= instance.Generation {
		t.Fatalf("unlink generation=%d err=%v", generation, err)
	}
	var missingTracks, missingAlbums, missingArtists, cacheRows int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tracks WHERE missing=1`).Scan(&missingTracks); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM albums WHERE missing=1`).Scan(&missingAlbums); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artists WHERE missing=1`).Scan(&missingArtists); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_file_cache WHERE source_kind='filesystem'`).Scan(&cacheRows); err != nil {
		t.Fatal(err)
	}
	if missingTracks != 1 || missingAlbums != 1 || missingArtists != 1 || cacheRows != 0 {
		t.Fatalf("unlink tracks=%d albums=%d artists=%d cache=%d", missingTracks, missingAlbums, missingArtists, cacheRows)
	}
	changes, _, err := s.ChangesSince(ctx, seq, "", 10)
	if err != nil || len(changes) < 3 {
		t.Fatalf("unlink removals=%+v err=%v", changes, err)
	}
}

func TestUnlinkFailureRollsBackConfigurationCacheAndRemovals(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	root := t.TempDir()
	instance, _, err := s.ConfigureFilesystemSource(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	track := meta("song.flac", "Song", "Artist", "Album", "", 2026, 1)
	if err := s.UpsertTrack(ctx, "filesystem", track, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	candidate := source.TrackCandidate{NativeID: track.NativeID, SizeBytes: track.SizeBytes, MtimeNS: 99}
	if err := s.PutSourceFileCache(ctx, instance.ID, "filesystem", 1, candidate, track); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_unlink_finish BEFORE UPDATE ON albums BEGIN SELECT RAISE(FAIL, 'synthetic unlink failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlinkFilesystemSource(ctx); err == nil {
		t.Fatal("unlink unexpectedly succeeded")
	}
	path, configured, err := s.GetSetting(ctx, "filesystem_path")
	if err != nil || !configured || path != root {
		t.Fatalf("configuration rolled forward: path=%q configured=%v err=%v", path, configured, err)
	}
	var cacheRows, missing int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_file_cache`).Scan(&cacheRows); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT missing FROM tracks WHERE native_id=?`, track.NativeID).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if cacheRows != 1 || missing != 0 {
		t.Fatalf("unlink rollback cache=%d missing=%d", cacheRows, missing)
	}
}

func TestMarkTracksSeenRestoresMissingWithDeltaWithoutBumpingPresentTrack(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	track := meta("song.flac", "Song", "Artist", "Album", "", 2026, 1)
	if err := s.UpsertTrack(ctx, "filesystem", track, "", seq); err != nil {
		t.Fatal(err)
	}
	var originalChange int64
	if err := s.db.QueryRowContext(ctx, `SELECT change_seq FROM tracks WHERE native_id=?`, track.NativeID).Scan(&originalChange); err != nil {
		t.Fatal(err)
	}
	seenSeq, _ := s.NextScanSeq(ctx)
	marked, err := s.MarkTracksSeen(ctx, "filesystem", []string{track.NativeID}, seenSeq)
	if err != nil || marked != 1 {
		t.Fatalf("mark present: marked=%d err=%v", marked, err)
	}
	var changeSeq, actualSeen int64
	if err := s.db.QueryRowContext(ctx, `SELECT change_seq,seen_seq FROM tracks WHERE native_id=?`, track.NativeID).Scan(&changeSeq, &actualSeen); err != nil {
		t.Fatal(err)
	}
	if changeSeq != originalChange || actualSeen != seenSeq {
		t.Fatalf("no-op hit changed delta: change=%d want=%d seen=%d want=%d", changeSeq, originalChange, actualSeen, seenSeq)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tracks SET missing=1 WHERE native_id=?`, track.NativeID); err != nil {
		t.Fatal(err)
	}
	restoreSeq, _ := s.NextScanSeq(ctx)
	marked, err = s.MarkTracksSeen(ctx, "filesystem", []string{track.NativeID, "absent.flac"}, restoreSeq)
	if err != nil || marked != 1 {
		t.Fatalf("restore: marked=%d err=%v", marked, err)
	}
	var missing int
	if err := s.db.QueryRowContext(ctx, `SELECT missing,change_seq FROM tracks WHERE native_id=?`, track.NativeID).Scan(&missing, &changeSeq); err != nil {
		t.Fatal(err)
	}
	if missing != 0 || changeSeq != restoreSeq {
		t.Fatalf("restored track missing=%d change_seq=%d want=%d", missing, changeSeq, restoreSeq)
	}
	changes, _, err := s.ChangesSince(ctx, seenSeq, "", 10)
	if err != nil || len(changes) != 1 || changes[0].Kind != "track" || changes[0].Missing || changes[0].ChangeSeq != restoreSeq {
		t.Fatalf("restoration delta=%+v err=%v", changes, err)
	}
}
