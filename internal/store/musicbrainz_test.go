package store

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

func musicBrainzAlbumFixture(t *testing.T) (*Store, MusicBrainzAlbum) {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	meta := source.TrackMeta{NativeID: "one.flac", Title: "First", Album: "Local Album", Year: 2020, Index: 1, Disc: 1, DurationMs: 180000, PrimaryArtist: source.ArtistReference{Name: "Local Artist"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Artist"}}, Container: "flac", Codec: "flac", Version: 1}
	if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	albums, err := s.DueMusicBrainzAlbums(ctx, time.Now(), 1)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums=%v err=%v", albums, err)
	}
	return s, albums[0]
}

func TestApplyMusicBrainzReleaseIsTransactionalAndPreservesJoinPhrases(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	release := CanonicalRelease{ReleaseID: "release-id", ReleaseGroupID: "group-id", AlbumCredits: []source.ArtistCredit{{Name: "Canonical One", MBID: "artist-one", JoinPhrase: " & "}, {Name: "Canonical Two", MBID: "artist-two"}}, Tracks: []CanonicalTrack{{Disc: 1, Index: 1, Title: "First", DurationMs: 181000, RecordingID: "recording-id", Credits: []source.ArtistCredit{{Name: "Canonical One", MBID: "artist-one", JoinPhrase: " feat. "}, {Name: "Guest", MBID: "artist-guest"}}}}}
	changed, err := s.ApplyMusicBrainzRelease(ctx, album, release, seq)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, found, err := s.GetAlbum(ctx, album.AlbumID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if got.MusicBrainzReleaseID != "release-id" || got.PrimaryArtist.Name != "Canonical One" || len(got.ArtistCredits) != 2 || got.ArtistCredits[0].JoinPhrase != " & " {
		t.Fatalf("album=%+v credits=%+v", got, got.ArtistCredits)
	}
	tracks, err := s.ListAlbumTracks(ctx, album.AlbumID)
	if err != nil {
		t.Fatal(err)
	}
	if tracks[0].MusicBrainzRecordingID != "recording-id" || len(tracks[0].ArtistCredits) != 2 || tracks[0].ArtistCredits[0].JoinPhrase != " feat. " {
		t.Fatalf("track=%+v", tracks[0])
	}
}

func TestAmbiguousResultDoesNotMutateCanonicalIdentity(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	candidate := MusicBrainzCandidate{ReleaseID: "candidate", Score: 75}
	if err := s.RecordMusicBrainzResult(ctx, album.AlbumID, "ambiguous", candidate, []MusicBrainzCandidate{candidate}, time.Now().Add(30*24*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetAlbum(ctx, album.AlbumID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MusicBrainzReleaseID != "" {
		t.Fatalf("ambiguous candidate mutated release id: %q", got.MusicBrainzReleaseID)
	}
}

func TestMusicBrainzEligibilityUsesPersistedDeadline(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	next := time.Now().Add(7 * 24 * time.Hour)
	selected := MusicBrainzCandidate{ReleaseID: "matched", Score: 99}
	if err := s.RecordMusicBrainzResult(ctx, album.AlbumID, "matched", selected, nil, next, ""); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueMusicBrainzAlbums(ctx, next.Add(-time.Second), 10); err != nil || len(due) != 0 {
		t.Fatalf("early due=%d err=%v", len(due), err)
	}
	if due, err := s.DueMusicBrainzAlbums(ctx, next.Add(time.Second), 10); err != nil || len(due) != 1 {
		t.Fatalf("overdue=%d err=%v", len(due), err)
	}
}

func TestApplyMusicBrainzMatchRollsBackCanonicalChangesWhenStateWriteFails(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_mb_state BEFORE INSERT ON album_musicbrainz_matches BEGIN SELECT RAISE(ABORT, 'state write failed'); END`); err != nil {
		t.Fatal(err)
	}
	seq, _ := s.NextScanSeq(ctx)
	release := CanonicalRelease{ReleaseID: "must-rollback", ReleaseGroupID: "group", AlbumCredits: []source.ArtistCredit{{Name: "Changed", MBID: "changed"}}}
	if _, err := s.ApplyMusicBrainzMatch(ctx, album, release, seq, MusicBrainzCandidate{ReleaseID: release.ReleaseID}, nil, time.Now()); err == nil {
		t.Fatal("expected state write failure")
	}
	got, _, err := s.GetAlbum(ctx, album.AlbumID)
	if err != nil || got.MusicBrainzReleaseID != "" || got.PrimaryArtist.Name != "Local Artist" {
		t.Fatalf("canonical mutation escaped rollback: album=%+v err=%v", got, err)
	}
}

func TestUntaggedRescansRetainResolverAssignedIdentity(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	release := CanonicalRelease{ReleaseID: "resolved-release", ReleaseGroupID: "resolved-group", AlbumCredits: []source.ArtistCredit{{Name: "Canonical Artist", MBID: "canonical-artist", JoinPhrase: " & "}, {Name: "Canonical Guest", MBID: "canonical-guest"}}, Tracks: []CanonicalTrack{{Disc: 1, Index: 1, Title: "First", DurationMs: 180000, RecordingID: "resolved-recording", Credits: []source.ArtistCredit{{Name: "Canonical Artist", MBID: "canonical-artist", JoinPhrase: " feat. "}, {Name: "Track Guest", MBID: "track-guest"}}}}}
	if _, err := s.ApplyMusicBrainzMatch(ctx, album, release, seq, MusicBrainzCandidate{ReleaseID: release.ReleaseID}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	meta := source.TrackMeta{NativeID: "one.flac", Title: "First", Album: "Local Album", Year: 2020, Index: 1, Disc: 1, DurationMs: 180000, PrimaryArtist: source.ArtistReference{Name: "Local Artist"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Artist"}}, Container: "flac", Codec: "flac", Version: 1}
	for range 2 {
		seq, _ = s.NextScanSeq(ctx)
		if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
			t.Fatal(err)
		}
	}
	tracks, _, err := s.ListTracks(ctx, "title", "", 10)
	if err != nil || len(tracks) != 1 || tracks[0].AlbumID != album.AlbumID {
		t.Fatalf("tracks=%+v err=%v", tracks, err)
	}
	gotAlbum, _, err := s.GetAlbum(ctx, album.AlbumID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAlbum.MusicBrainzReleaseID != "resolved-release" || gotAlbum.MusicBrainzReleaseGroupID != "resolved-group" || gotAlbum.PrimaryArtist.Name != "Canonical Artist" || len(gotAlbum.ArtistCredits) != 2 || gotAlbum.ArtistCredits[0].JoinPhrase != " & " {
		t.Fatalf("resolver album identity was overwritten: %+v credits=%+v", gotAlbum, gotAlbum.ArtistCredits)
	}
	if tracks[0].MusicBrainzRecordingID != "resolved-recording" || tracks[0].ArtistName != "Canonical Artist" || len(tracks[0].ArtistCredits) != 2 || tracks[0].ArtistCredits[0].JoinPhrase != " feat. " {
		t.Fatalf("resolver track identity was overwritten: %+v", tracks[0])
	}
	meta.ReleaseMBID = "tagged-release"
	meta.ReleaseGroupMBID = "tagged-group"
	meta.RecordingMBID = "tagged-recording"
	meta.PrimaryArtist = source.ArtistReference{Name: "Tagged Artist", MBID: "tagged-artist"}
	meta.AlbumCredits = []source.ArtistCredit{{Name: "Tagged Artist", MBID: "tagged-artist"}}
	meta.TrackCredits = []source.ArtistCredit{{Name: "Tagged Artist", MBID: "tagged-artist"}}
	seq, _ = s.NextScanSeq(ctx)
	if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	gotAlbum, _, _ = s.GetAlbum(ctx, album.AlbumID)
	tracks, _ = s.ListAlbumTracks(ctx, album.AlbumID)
	if gotAlbum.MusicBrainzReleaseID != "tagged-release" || gotAlbum.PrimaryArtist.Name != "Tagged Artist" || len(tracks) != 1 || tracks[0].MusicBrainzRecordingID != "tagged-recording" || tracks[0].ArtistName != "Tagged Artist" {
		t.Fatalf("authoritative tags did not replace resolver identity: album=%+v tracks=%+v", gotAlbum, tracks)
	}
	var albums int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM albums`).Scan(&albums); err != nil || albums != 1 {
		t.Fatalf("album count=%d err=%v", albums, err)
	}
}

func TestApplyMusicBrainzMatchRejectsStaleSnapshot(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	meta := source.TrackMeta{NativeID: "one.flac", Title: "Changed while resolving", Album: "Local Album", Year: 2020, Index: 1, Disc: 1, DurationMs: 180000, PrimaryArtist: source.ArtistReference{Name: "Local Artist"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Artist"}}, Container: "flac", Codec: "flac", Version: 2}
	if err := s.UpsertTrack(ctx, "filesystem", meta, "", seq); err != nil {
		t.Fatal(err)
	}
	release := CanonicalRelease{ReleaseID: "stale-release", ReleaseGroupID: "stale-group", AlbumCredits: []source.ArtistCredit{{Name: "Wrong", MBID: "wrong"}}}
	changed, err := s.ApplyMusicBrainzMatch(ctx, album, release, seq, MusicBrainzCandidate{ReleaseID: release.ReleaseID}, nil, time.Now())
	if err != nil || changed {
		t.Fatalf("stale apply changed=%v err=%v", changed, err)
	}
	got, _, _ := s.GetAlbum(ctx, album.AlbumID)
	if got.MusicBrainzReleaseID != "" {
		t.Fatalf("stale resolver result applied: %+v", got)
	}
	var matched int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM album_musicbrainz_matches WHERE album_id=? AND state='matched'`, album.AlbumID).Scan(&matched); err != nil {
		t.Fatal(err)
	}
	if matched != 0 {
		t.Fatal("stale resolver result was marked matched")
	}
}
