package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

func applyCanonicalRelease(t *testing.T, s *Store, album MusicBrainzAlbum, artistName, artistMBID string, credits []source.ArtistCredit) int64 {
	t.Helper()
	seq, err := s.NextScanSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release := CanonicalRelease{ReleaseID: "release-" + album.AlbumID, ReleaseGroupID: "group-" + album.AlbumID, AlbumCredits: []source.ArtistCredit{{Name: artistName, MBID: artistMBID}}}
	for _, track := range album.Tracks {
		release.Tracks = append(release.Tracks, CanonicalTrack{Disc: track.Disc, Index: track.Index, Title: track.Title, DurationMs: track.DurationMs, RecordingID: "recording-" + track.TrackID, Credits: credits})
	}
	if _, err := s.ApplyMusicBrainzRelease(context.Background(), album, release, seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

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

func TestApplyMusicBrainzReleaseAdoptsPrimaryOwnerAndIsIdempotent(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	ownerID := album.PrimaryArtist.ArtistID
	seq := applyCanonicalRelease(t, s, album, "Canonical Artist", "mbid-owner", []source.ArtistCredit{{Name: "Canonical Artist", MBID: "mbid-owner"}})
	artist, found, err := s.GetArtist(ctx, ownerID)
	if err != nil || !found || artist.MusicBrainzID != "mbid-owner" || artist.Name != "Canonical Artist" {
		t.Fatalf("owner adoption: found=%v err=%v artist=%+v", found, err, artist)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artists`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("artist count=%d err=%v", count, err)
	}
	var aliases int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_aliases WHERE artist_id=? AND name='Local Artist'`, ownerID).Scan(&aliases); err != nil || aliases != 1 {
		t.Fatalf("aliases=%d err=%v", aliases, err)
	}
	var before int64
	if err := s.db.QueryRowContext(ctx, `SELECT change_seq FROM artists WHERE artist_id=?`, ownerID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	changed, err := s.ApplyMusicBrainzRelease(ctx, album, CanonicalRelease{ReleaseID: "release-" + album.AlbumID, ReleaseGroupID: "group-" + album.AlbumID, AlbumCredits: []source.ArtistCredit{{Name: "Canonical Artist", MBID: "mbid-owner"}}, Tracks: []CanonicalTrack{{Disc: 1, Index: 1, Title: "First", DurationMs: 180000, RecordingID: "recording-" + album.Tracks[0].TrackID, Credits: []source.ArtistCredit{{Name: "Canonical Artist", MBID: "mbid-owner"}}}}}, seq+1)
	if err != nil || changed {
		t.Fatalf("second apply changed=%v err=%v", changed, err)
	}
	var after int64
	_ = s.db.QueryRowContext(ctx, `SELECT change_seq FROM artists WHERE artist_id=?`, ownerID).Scan(&after)
	if after != before {
		t.Fatalf("change_seq churned: %d -> %d", before, after)
	}
}

func TestApplyMusicBrainzReleaseGuestAdoptionRules(t *testing.T) {
	for _, tc := range []struct {
		name, guest string
		rows        int
		wantAdopt   bool
	}{{"unique exact", "Guest", 1, true}, {"ambiguous exact", "Guest", 2, false}, {"no exact match", "Canonical Guest", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, album := musicBrainzAlbumFixture(t)
			ctx := context.Background()
			var ids []string
			for i := 0; i < tc.rows; i++ {
				id := NewID("art")
				name := "Guest"
				if tc.name == "no exact match" {
					name = "Local Guest"
				}
				if _, err := s.db.ExecContext(ctx, `INSERT INTO artists(artist_id,name,created_at,change_seq) VALUES(?,?,?,?)`, id, name, time.Now().Unix(), album.Version); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			applyCanonicalRelease(t, s, album, "Owner", "mbid-owner", []source.ArtistCredit{{Name: "Owner", MBID: "mbid-owner", JoinPhrase: " feat. "}, {Name: tc.guest, MBID: "mbid-guest"}})
			var got string
			if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='mbid-guest'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			adopted := len(ids) > 0 && got == ids[0]
			if adopted != tc.wantAdopt {
				t.Fatalf("guest id=%s candidates=%v adopted=%v", got, ids, adopted)
			}
		})
	}
}

func TestCanonicalConvergenceMarksDrainedArtistMissingAndRemoved(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	for i, name := range []string{"Jean Michel Jarre", "Jean-Michel Jarre"} {
		m := meta(name, "Track", name, "Album "+name, "", 2000+i, 1)
		if err := s.UpsertTrack(ctx, "filesystem", m, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	albums, _ := s.DueMusicBrainzAlbums(ctx, time.Now(), 10)
	firstOwner, secondOwner := albums[0].PrimaryArtist.ArtistID, albums[1].PrimaryArtist.ArtistID
	firstSeq := applyCanonicalRelease(t, s, albums[0], "Jean-Michel Jarre", "jarre-mbid", []source.ArtistCredit{{Name: "Jean-Michel Jarre", MBID: "jarre-mbid"}})
	var survivingBefore int64
	if err := s.db.QueryRowContext(ctx, `SELECT change_seq FROM artists WHERE artist_id=?`, firstOwner).Scan(&survivingBefore); err != nil {
		t.Fatal(err)
	}
	secondSeq := applyCanonicalRelease(t, s, albums[1], "Jean-Michel Jarre", "jarre-mbid", []source.ArtistCredit{{Name: "Jean-Michel Jarre", MBID: "jarre-mbid"}})
	artists, _, _ := s.ListArtists(ctx, "title", "", 10)
	if len(artists) != 1 || artists[0].ArtistID != firstOwner || artists[0].AlbumCount != 2 {
		t.Fatalf("artists=%+v owners=%s/%s", artists, firstOwner, secondOwner)
	}
	var missing int
	if err := s.db.QueryRowContext(ctx, `SELECT missing FROM artists WHERE artist_id=?`, secondOwner).Scan(&missing); err != nil || missing != 1 {
		t.Fatalf("drained missing=%d err=%v", missing, err)
	}
	changes, _, err := s.ChangesSince(ctx, firstSeq, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	foundRemoved, foundSurviving := false, false
	for _, change := range changes {
		foundRemoved = foundRemoved || change.ID == secondOwner && change.Missing && change.ChangeSeq == secondSeq
		foundSurviving = foundSurviving || change.ID == firstOwner && !change.Missing && change.ChangeSeq == secondSeq
	}
	if !foundRemoved || !foundSurviving {
		t.Fatalf("convergence changes removed=%v surviving=%v: %+v", foundRemoved, foundSurviving, changes)
	}
	if survivingBefore >= secondSeq {
		t.Fatalf("surviving artist did not advance: before=%d convergence=%d", survivingBefore, secondSeq)
	}
	changed, err := s.ApplyMusicBrainzRelease(ctx, albums[1], CanonicalRelease{ReleaseID: "release-" + albums[1].AlbumID, ReleaseGroupID: "group-" + albums[1].AlbumID, AlbumCredits: []source.ArtistCredit{{Name: "Jean-Michel Jarre", MBID: "jarre-mbid"}}, Tracks: []CanonicalTrack{{Disc: albums[1].Tracks[0].Disc, Index: albums[1].Tracks[0].Index, Title: albums[1].Tracks[0].Title, DurationMs: albums[1].Tracks[0].DurationMs, RecordingID: "recording-" + albums[1].Tracks[0].TrackID, Credits: []source.ArtistCredit{{Name: "Jean-Michel Jarre", MBID: "jarre-mbid"}}}}}, secondSeq+1)
	if err != nil || changed {
		t.Fatalf("identical reapply changed=%v err=%v", changed, err)
	}
	var survivingAfter int64
	if err := s.db.QueryRowContext(ctx, `SELECT change_seq FROM artists WHERE artist_id=?`, firstOwner).Scan(&survivingAfter); err != nil {
		t.Fatal(err)
	}
	if survivingAfter != secondSeq {
		t.Fatalf("identical reapply churned surviving artist: %d -> %d", secondSeq, survivingAfter)
	}
}

func TestCombinedNameDissolvesAndGuestsRemainSearchableOnly(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	combinedID := album.PrimaryArtist.ArtistID
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET name='X with Y' WHERE artist_id=?`, combinedID); err != nil {
		t.Fatal(err)
	}
	primaryID := NewID("art")
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artists(artist_id,name,musicbrainz_id,created_at,change_seq) VALUES(?,?,?,?,?)`, primaryID, "X", "x-mbid", time.Now().Unix(), album.Version); err != nil {
		t.Fatal(err)
	}
	applyCanonicalRelease(t, s, album, "X", "x-mbid", []source.ArtistCredit{{Name: "X", MBID: "x-mbid", JoinPhrase: " with "}, {Name: "Y", MBID: "y-mbid"}})
	artists, _, _ := s.ListArtists(ctx, "title", "", 10)
	if len(artists) != 1 || artists[0].ArtistID != primaryID {
		t.Fatalf("browse artists=%+v", artists)
	}
	var guestID string
	if err := s.db.QueryRowContext(ctx, `SELECT artist_id FROM artists WHERE musicbrainz_id='y-mbid'`).Scan(&guestID); err != nil {
		t.Fatal(err)
	}
	if guest, found, err := s.GetArtist(ctx, guestID); err != nil || !found || guest.Name != "Y" {
		t.Fatalf("guest detail: found=%v err=%v guest=%+v", found, err, guest)
	}
	search, err := s.SearchLibrary(ctx, "Y")
	foundGuest := false
	for _, artist := range search.Artists {
		foundGuest = foundGuest || artist.ArtistID == guestID
	}
	if err != nil || !foundGuest {
		t.Fatalf("guest search=%+v err=%v", search.Artists, err)
	}
	needs, err := s.ArtistsNeedingArt(ctx, 10)
	if err != nil || len(needs) != 1 || needs[0].Name != "X" {
		t.Fatalf("art needs=%+v err=%v", needs, err)
	}
	summary, err := s.GetLibrarySummary(ctx)
	if err != nil || summary.Artists != len(artists) {
		t.Fatalf("summary=%+v browse=%d err=%v", summary, len(artists), err)
	}
	var missing sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT missing FROM artists WHERE artist_id=?`, combinedID).Scan(&missing); err != nil || !missing.Valid || missing.Int64 != 1 {
		t.Fatalf("combined row missing=%v err=%v", missing, err)
	}
}

// Edition-ambiguous albums still yield unambiguous artist/release-group
// identity when every candidate agrees; consensus application must keep the
// match reviewable and must not collide on the unique release-id column.
func TestApplyConsensusKeepsAmbiguousStateAndNullReleaseIDs(t *testing.T) {
	s, first := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	// Re-upsert the fixture's track too: FinishScan marks unseen tracks missing.
	one := source.TrackMeta{NativeID: "one.flac", Title: "First", Album: "Local Album", Year: 2020, Index: 1, Disc: 1, DurationMs: 180000, PrimaryArtist: source.ArtistReference{Name: "Local Artist"}, AlbumCredits: []source.ArtistCredit{{Name: "Local Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Local Artist"}}, Container: "flac", Codec: "flac", Version: 1}
	meta := source.TrackMeta{NativeID: "two.flac", Title: "Second", Album: "Other Album", Year: 2021, Index: 1, Disc: 1, DurationMs: 170000, PrimaryArtist: source.ArtistReference{Name: "Other Artist"}, AlbumCredits: []source.ArtistCredit{{Name: "Other Artist"}}, TrackCredits: []source.ArtistCredit{{Name: "Other Artist"}}, Container: "flac", Codec: "flac", Version: 1}
	for _, m := range []source.TrackMeta{one, meta} {
		if err := s.UpsertTrack(ctx, "filesystem", m, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueMusicBrainzAlbums(ctx, time.Now(), 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due=%v err=%v", due, err)
	}
	next := time.Now().Add(30 * 24 * time.Hour)
	for i, album := range due {
		credits := []source.ArtistCredit{{Name: album.PrimaryArtist.Name, MBID: fmt.Sprintf("mbid-consensus-%d", i)}}
		release := CanonicalRelease{ReleaseGroupID: fmt.Sprintf("rg-consensus-%d", i), AlbumCredits: credits}
		applySeq, _ := s.NextScanSeq(ctx)
		changed, err := s.ApplyMusicBrainzConsensus(ctx, album, release, applySeq, MusicBrainzCandidate{ReleaseID: "cand", Title: album.Title}, nil, next)
		if err != nil {
			t.Fatalf("consensus %d: %v", i, err)
		}
		if !changed {
			t.Fatalf("consensus %d reported no change", i)
		}
	}
	_ = first
	rows, err := s.db.QueryContext(ctx, `SELECT al.album_id, al.musicbrainz_release_id IS NULL, COALESCE(al.musicbrainz_release_group_id,''), m.state, a.musicbrainz_id IS NOT NULL FROM albums al JOIN album_musicbrainz_matches m ON m.album_id=al.album_id JOIN artists a ON a.artist_id=al.artist_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var albumID, rg, state string
		var nullRelease, artistIdentified bool
		if err := rows.Scan(&albumID, &nullRelease, &rg, &state, &artistIdentified); err != nil {
			t.Fatal(err)
		}
		count++
		if !nullRelease || rg == "" || state != "ambiguous" || !artistIdentified {
			t.Fatalf("album %s: nullRelease=%v rg=%q state=%q artistIdentified=%v", albumID, nullRelease, rg, state, artistIdentified)
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 consensus albums, got %d", count)
	}
}

// A later consensus without release-group agreement must not erase an
// earlier consensus release group.
func TestApplyConsensusPreservesExistingReleaseGroup(t *testing.T) {
	s, album := musicBrainzAlbumFixture(t)
	ctx := context.Background()
	next := time.Now().Add(30 * 24 * time.Hour)
	credits := []source.ArtistCredit{{Name: album.PrimaryArtist.Name, MBID: "mbid-keep"}}
	applySeq, _ := s.NextScanSeq(ctx)
	if _, err := s.ApplyMusicBrainzConsensus(ctx, album, CanonicalRelease{ReleaseGroupID: "rg-keep", AlbumCredits: credits}, applySeq, MusicBrainzCandidate{}, nil, next); err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.DueMusicBrainzAlbumsPage(ctx, time.Now().Add(31*24*time.Hour), -1, "", 10)
	if err != nil || len(refreshed) != 1 {
		t.Fatalf("refreshed=%v err=%v", refreshed, err)
	}
	applySeq, _ = s.NextScanSeq(ctx)
	if _, err := s.ApplyMusicBrainzConsensus(ctx, refreshed[0], CanonicalRelease{AlbumCredits: credits}, applySeq, MusicBrainzCandidate{}, nil, next); err != nil {
		t.Fatal(err)
	}
	var rg string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(musicbrainz_release_group_id,'') FROM albums WHERE album_id=?`, album.AlbumID).Scan(&rg); err != nil {
		t.Fatal(err)
	}
	if rg != "rg-keep" {
		t.Fatalf("release group erased: %q", rg)
	}
}
