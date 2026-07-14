package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

func TestArtEligibilityDailyBoundaryAndExplicitArtistExclusion(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	// Fanart selection requires MBIDs; identify the fixture artists.
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id='mbid-' || artist_id`); err != nil {
		t.Fatal(err)
	}
	artists, err := s.ArtistsNeedingArt(ctx, 10)
	if err != nil || len(artists) == 0 {
		t.Fatalf("initial artists: %v %v", artists, err)
	}
	explicitID := artists[0].ArtistID
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET art_id='img_explicit', art_tried=1, art_tried_at=? WHERE artist_id=?`, time.Now().Add(-25*time.Hour).Unix(), explicitID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET art_tried=1, art_tried_at=? WHERE artist_id<>?`, time.Now().Add(-24*time.Hour).Unix(), explicitID); err != nil {
		t.Fatal(err)
	}
	artists, err = s.ArtistsNeedingArt(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, artist := range artists {
		if artist.ArtistID == explicitID {
			t.Fatal("artist with explicit art remained eligible")
		}
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET art_tried_at=? WHERE art_id IS NULL`, time.Now().Add(-24*time.Hour-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	artists, err = s.ArtistsNeedingArt(ctx, 10)
	if err != nil || len(artists) == 0 {
		t.Fatalf("overdue fallback-only artists: %v %v", artists, err)
	}
}

func TestArtRetryUpgradeAndExternalArtistRace(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	// Fanart selection requires MBIDs; identify the fixture artists.
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id='mbid-' || artist_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET art_tried=1,art_tried_at=0; UPDATE artists SET art_tried=1,art_tried_at=0`); err != nil {
		t.Fatal(err)
	}
	albums, err := s.AlbumsNeedingArt(ctx, 10)
	if err != nil || len(albums) == 0 {
		t.Fatalf("album retry: %d %v", len(albums), err)
	}
	artists, err := s.ArtistsNeedingArt(ctx, 10)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artist retry: %d %v", len(artists), err)
	}
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, owned, e := s.ResolveArtistName(ctx, "Synthetic External")
			if e != nil {
				t.Errorf("resolve: %v", e)
			}
			if owned {
				t.Error("external marked owned")
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("race minted multiple ids: %v", ids)
		}
	}
}

func TestNextScanSeqIsAtomic(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const calls = 20
	seqs := make(chan int64, calls)
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seq, err := s.NextScanSeq(ctx)
			seqs <- seq
			errs <- err
		}()
	}
	wg.Wait()
	close(seqs)
	close(errs)
	seen := make(map[int64]bool, calls)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for seq := range seqs {
		if seq < 1 || seq > calls || seen[seq] {
			t.Fatalf("non-atomic sequence %d, seen=%v", seq, seen)
		}
		seen[seq] = true
	}
}

func TestExternalArtDoesNotOverwriteNewerAttachedArt(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	// Fanart selection requires MBIDs; identify the fixture artists.
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id='mbid-' || artist_id`); err != nil {
		t.Fatal(err)
	}
	albums, _ := s.AlbumsNeedingArt(ctx, 1)
	artists, _ := s.ArtistsNeedingArt(ctx, 1)
	oldArtistArt := artists[0].ArtID
	newID, err := s.UpsertArt(ctx, "newer", "image/jpeg", []byte("newer"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	externalID, err := s.UpsertArt(ctx, "external", "image/jpeg", []byte("external"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET art_id=? WHERE album_id=?`, newID, albums[0].AlbumID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET art_id=? WHERE artist_id=?`, newID, artists[0].ArtistID); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.SetAlbumArt(ctx, albums[0].AlbumID, externalID, 99); err != nil || applied {
		t.Fatalf("stale album art applied=%v err=%v", applied, err)
	}
	if applied, err := s.SetArtistArt(ctx, artists[0].ArtistID, oldArtistArt, externalID, 99); err != nil || applied {
		t.Fatalf("stale artist art applied=%v err=%v", applied, err)
	}
}

func TestSetAlbumArtFallsBackToTracksAndArtistWithoutOverridingExplicitArt(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	albums, _, err := s.ListAlbums(ctx, "title", "", 10)
	if err != nil {
		t.Fatalf("list albums: %v %+v", err, albums)
	}
	var album AlbumRow
	for _, candidate := range albums {
		if candidate.Title == "First Album" {
			album = candidate
			break
		}
	}
	if album.AlbumID == "" {
		t.Fatalf("First Album missing: %+v", albums)
	}
	tracks, err := s.ListAlbumTracks(ctx, album.AlbumID)
	if err != nil || len(tracks) < 2 {
		t.Fatalf("list album tracks: %v %+v", err, tracks)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tracks SET art_id = ? WHERE track_id = ?`, "img_track", tracks[0].TrackID); err != nil {
		t.Fatal(err)
	}

	applied, err := s.SetAlbumArt(ctx, album.AlbumID, "img_album", 99)
	if err != nil || !applied {
		t.Fatalf("set album art: applied=%v err=%v", applied, err)
	}
	tracks, err = s.ListAlbumTracks(ctx, album.AlbumID)
	if err != nil {
		t.Fatal(err)
	}
	if tracks[0].ArtID != "img_track" || tracks[1].ArtID != "img_album" {
		t.Fatalf("track art precedence: %+v", tracks)
	}
	artist, found, err := s.GetArtist(ctx, album.ArtistID)
	if err != nil || !found || artist.ArtID != "img_album" {
		t.Fatalf("artist album fallback: found=%v err=%v artist=%+v", found, err, artist)
	}

	if applied, err := s.SetArtistArt(ctx, album.ArtistID, "", "img_artist", 100); err != nil || !applied {
		t.Fatalf("set artist art: applied=%v err=%v", applied, err)
	}
	artist, found, err = s.GetArtist(ctx, album.ArtistID)
	if err != nil || !found || artist.ArtID != "img_artist" {
		t.Fatalf("explicit artist art: found=%v err=%v artist=%+v", found, err, artist)
	}
}

func TestArtistExternalArtUpgradesUnchangedAlbumFallback(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	// Fanart selection requires MBIDs; identify the fixture artists.
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET musicbrainz_id='mbid-' || artist_id`); err != nil {
		t.Fatal(err)
	}
	artist := func() ArtistArtNeed {
		rows, _ := s.ArtistsNeedingArt(ctx, 1)
		return rows[0]
	}()
	externalID, err := s.UpsertArt(ctx, "artist-external", "image/jpeg", []byte("external"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	applied, err := s.SetArtistArt(ctx, artist.ArtistID, artist.ArtID, externalID, 99)
	if err != nil || !applied {
		t.Fatalf("fallback upgrade applied=%v err=%v", applied, err)
	}
}

func meta(native, title, artist, album, genre string, year, idx int) source.TrackMeta {
	return source.TrackMeta{
		NativeID: native, Title: title, PrimaryArtist: source.ArtistReference{Name: artist}, TrackCredits: []source.ArtistCredit{{Name: artist}}, AlbumCredits: []source.ArtistCredit{{Name: artist}},
		Album: album, Genre: genre, Year: year, Index: idx,
		DurationMs: 2000, Container: "flac", Codec: "flac", BitrateKbps: 900,
		SizeBytes: 123456, Version: 1,
	}
}

func TestMusicBrainzIdentityAndStructuredCredits(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const primaryMBID = "11111111-1111-1111-1111-111111111111"
	const guestMBID = "22222222-2222-2222-2222-222222222222"
	seq, _ := s.NextScanSeq(ctx)
	credits := []source.ArtistCredit{{Name: "Jean-Michel Jarre", MBID: primaryMBID, JoinPhrase: " feat. "}, {Name: "Guest", MBID: guestMBID}}
	first := source.TrackMeta{NativeID: "one.flac", Title: "One", Album: "Release", PrimaryArtist: source.ArtistReference{Name: "Jean-Michel Jarre", MBID: primaryMBID}, TrackCredits: credits, AlbumCredits: credits, RecordingMBID: "33333333-3333-3333-3333-333333333333", ReleaseMBID: "44444444-4444-4444-4444-444444444444", ReleaseGroupMBID: "55555555-5555-5555-5555-555555555555", Container: "flac", Codec: "flac"}
	if err := s.UpsertTrack(ctx, "filesystem", first, "", seq); err != nil {
		t.Fatal(err)
	}
	alias := first
	alias.NativeID = "two.flac"
	alias.Title = "Two"
	alias.Album = "Other Release"
	alias.PrimaryArtist.Name = "Jean Michel Jarre"
	alias.TrackCredits = []source.ArtistCredit{{Name: "Jean Michel Jarre", MBID: primaryMBID}}
	alias.AlbumCredits = []source.ArtistCredit{{Name: "Jean Michel Jarre", MBID: primaryMBID}}
	alias.RecordingMBID = "99999999-9999-9999-9999-999999999999"
	alias.ReleaseMBID = "66666666-6666-6666-6666-666666666666"
	if err := s.UpsertTrack(ctx, "filesystem", alias, "", seq); err != nil {
		t.Fatal(err)
	}
	homonym := alias
	homonym.NativeID = "three.flac"
	homonym.PrimaryArtist.MBID = "77777777-7777-7777-7777-777777777777"
	homonym.TrackCredits[0].MBID = homonym.PrimaryArtist.MBID
	homonym.AlbumCredits[0].MBID = homonym.PrimaryArtist.MBID
	homonym.RecordingMBID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	homonym.ReleaseMBID = "88888888-8888-8888-8888-888888888888"
	if err := s.UpsertTrack(ctx, "filesystem", homonym, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}

	artists, _, err := s.ListArtists(ctx, "title", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 2 {
		t.Fatalf("artists=%d want primary and homonym", len(artists))
	}
	var primary ArtistRow
	for _, a := range artists {
		if a.MusicBrainzID == primaryMBID {
			primary = a
		}
	}
	if primary.ArtistID == "" || len(primary.Aliases) != 1 || primary.Aliases[0] != "Jean Michel Jarre" {
		t.Fatalf("primary identity: %+v", primary)
	}
	search, err := s.SearchLibrary(ctx, "Guest")
	if err != nil || len(search.Artists) != 0 || len(search.Tracks) != 1 {
		t.Fatalf("guest search: %v artists=%+v tracks=%+v", err, search.Artists, search.Tracks)
	}
	guestID := search.Tracks[0].ArtistCredits[1].ArtistID
	guest, found, err := s.GetArtist(ctx, guestID)
	if err != nil || found {
		t.Fatalf("credit-only artist became a resource: found=%v err=%v artist=%+v", found, err, guest)
	}
	tracks, err := s.ListArtistTracks(ctx, guestID)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("featured membership: %v %+v", err, tracks)
	}
	if len(tracks[0].ArtistCredits) != 2 || tracks[0].ArtistCredits[0].JoinPhrase != " feat. " {
		t.Fatalf("credits: %+v", tracks[0].ArtistCredits)
	}
	if tracks[0].MusicBrainzRecordingID != first.RecordingMBID {
		t.Fatalf("recording MBID: %+v", tracks[0])
	}
}

func TestAlbumIdentitySeparatesTaggedReleasesAndUntaggedFallback(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)

	first := meta("first.flac", "First", "Artist", "Shared Title", "", 2000, 1)
	first.ReleaseMBID = "11111111-1111-1111-1111-111111111111"
	second := meta("second.flac", "Second", "Artist", "Shared Title", "", 2000, 1)
	second.ReleaseMBID = "22222222-2222-2222-2222-222222222222"
	untagged := meta("untagged.flac", "Untagged", "Artist", "Shared Title", "", 2000, 1)
	untaggedAgain := meta("untagged-again.flac", "Untagged Again", "Artist", "Shared Title", "", 2000, 2)
	for _, track := range []source.TrackMeta{untagged, first, second, untaggedAgain} {
		if err := s.UpsertTrack(ctx, "filesystem", track, "", seq); err != nil {
			t.Fatal(err)
		}
	}

	var albums int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT album_id) FROM tracks WHERE source_kind = 'filesystem'`).Scan(&albums); err != nil {
		t.Fatal(err)
	}
	if albums != 3 {
		t.Fatalf("distinct albums=%d want 3", albums)
	}
	var untaggedTracks int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks JOIN albums USING (album_id) WHERE tracks.native_id LIKE 'untagged%' AND albums.musicbrainz_release_id IS NULL`).Scan(&untaggedTracks); err != nil {
		t.Fatal(err)
	}
	if untaggedTracks != 2 {
		t.Fatalf("untagged tracks on fallback album=%d want 2", untaggedTracks)
	}
}

func TestRecordingMBIDAllowsMultipleSourceTracks(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	const recordingMBID = "33333333-3333-3333-3333-333333333333"

	first := meta("lossless.flac", "Song", "Artist", "Album", "", 2000, 1)
	first.RecordingMBID = recordingMBID
	second := meta("portable.m4a", "Song", "Artist", "Album", "", 2000, 1)
	second.RecordingMBID = recordingMBID
	second.Container = "m4a"
	second.Codec = "aac"
	for _, track := range []source.TrackMeta{first, second} {
		if err := s.UpsertTrack(ctx, "filesystem", track, "", seq); err != nil {
			t.Fatal(err)
		}
	}

	var tracks int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE musicbrainz_recording_id = ?`, recordingMBID).Scan(&tracks); err != nil {
		t.Fatal(err)
	}
	if tracks != 2 {
		t.Fatalf("tracks=%d want 2", tracks)
	}
}

func TestRescanPromotesSourceLinkedFallbackIdentity(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	initial := meta("song.flac", "Song", "Local Artist", "Local Album", "", 2000, 1)
	seq, _ := s.NextScanSeq(ctx)
	if err := s.UpsertTrack(ctx, "filesystem", initial, "", seq); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.ListTracks(ctx, "title", "", 10)
	artistID, albumID := before[0].ArtistID, before[0].AlbumID

	tagged := initial
	tagged.PrimaryArtist.MBID = "11111111-1111-1111-1111-111111111111"
	tagged.TrackCredits[0].MBID = tagged.PrimaryArtist.MBID
	tagged.AlbumCredits[0].MBID = tagged.PrimaryArtist.MBID
	tagged.ReleaseMBID = "22222222-2222-2222-2222-222222222222"
	seq, _ = s.NextScanSeq(ctx)
	if err := s.UpsertTrack(ctx, "filesystem", tagged, "", seq); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.ListTracks(ctx, "title", "", 10)
	if after[0].ArtistID != artistID || after[0].AlbumID != albumID {
		t.Fatalf("opaque ids changed on promotion: before=%s/%s after=%s/%s", artistID, albumID, after[0].ArtistID, after[0].AlbumID)
	}
	artist, found, err := s.GetArtist(ctx, artistID)
	if err != nil || !found || artist.MusicBrainzID != tagged.PrimaryArtist.MBID {
		t.Fatalf("artist promotion: found=%v err=%v artist=%+v", found, err, artist)
	}
	album, found, err := s.GetAlbum(ctx, albumID)
	if err != nil || !found || album.MusicBrainzReleaseID != tagged.ReleaseMBID {
		t.Fatalf("album promotion: found=%v err=%v album=%+v", found, err, album)
	}
}

func TestRescanUsesExistingCanonicalIdentityOnMBIDConflict(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	const artistMBID = "11111111-1111-1111-1111-111111111111"
	const releaseMBID = "22222222-2222-2222-2222-222222222222"
	seq, _ := s.NextScanSeq(ctx)
	canonical := meta("canonical.flac", "Canonical", "Canonical Artist", "Canonical Album", "", 2000, 1)
	canonical.PrimaryArtist.MBID, canonical.TrackCredits[0].MBID, canonical.AlbumCredits[0].MBID = artistMBID, artistMBID, artistMBID
	canonical.ReleaseMBID = releaseMBID
	if err := s.UpsertTrack(ctx, "filesystem", canonical, "", seq); err != nil {
		t.Fatal(err)
	}
	fallback := meta("fallback.flac", "Fallback", "Local Artist", "Local Album", "", 2000, 1)
	if err := s.UpsertTrack(ctx, "filesystem", fallback, "", seq); err != nil {
		t.Fatal(err)
	}
	tracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	var canonicalArtistID, canonicalAlbumID, fallbackArtistID, fallbackAlbumID string
	for _, tr := range tracks {
		if tr.Title == "Canonical" {
			canonicalArtistID, canonicalAlbumID = tr.ArtistID, tr.AlbumID
		} else {
			fallbackArtistID, fallbackAlbumID = tr.ArtistID, tr.AlbumID
		}
	}
	fallback.PrimaryArtist.MBID, fallback.TrackCredits[0].MBID, fallback.AlbumCredits[0].MBID = artistMBID, artistMBID, artistMBID
	fallback.ReleaseMBID = releaseMBID
	seq, _ = s.NextScanSeq(ctx)
	if err := s.UpsertTrack(ctx, "filesystem", fallback, "", seq); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetTrack(ctx, tracks[1].TrackID)
	if err != nil || !found {
		t.Fatalf("fallback track: found=%v err=%v", found, err)
	}
	if got.ArtistID != canonicalArtistID || got.AlbumID != canonicalAlbumID {
		t.Fatalf("conflict did not use canonical rows: %+v", got)
	}
	var fallbackArtistMBID, fallbackAlbumMBID *string
	if err := s.db.QueryRowContext(ctx, `SELECT musicbrainz_id FROM artists WHERE artist_id=?`, fallbackArtistID).Scan(&fallbackArtistMBID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT musicbrainz_release_id FROM albums WHERE album_id=?`, fallbackAlbumID).Scan(&fallbackAlbumMBID); err != nil {
		t.Fatal(err)
	}
	if fallbackArtistMBID != nil || fallbackAlbumMBID != nil {
		t.Fatalf("conflicting fallback rows were destructively promoted: artist=%v album=%v", fallbackArtistMBID, fallbackAlbumMBID)
	}
}

func TestFinishScanRemovesCreditOnlyCollaboratorFromArtistCatalog(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	track := meta("eminem/the-monster.flac", "The Monster", "Eminem", "The Marshall Mathers LP2", "", 2013, 1)
	track.TrackCredits = []source.ArtistCredit{{Name: "Eminem feat. Rihanna"}}
	track.AlbumCredits = []source.ArtistCredit{{Name: "Eminem"}}
	if err := s.UpsertTrack(ctx, "filesystem", track, "", seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	artists, _, err := s.ListArtists(ctx, "title", "", 10)
	if err != nil || len(artists) != 1 || artists[0].Name != "Eminem" {
		t.Fatalf("catalog artists: err=%v artists=%+v", err, artists)
	}
	for _, artist := range artists {
		if _, found, err := s.GetArtist(ctx, artist.ArtistID); err != nil || !found {
			t.Fatalf("artist %q is not resolvable: found=%v err=%v", artist.Name, found, err)
		}
	}
	needsArt, err := s.ArtistsNeedingArt(ctx, 10)
	if err != nil || len(needsArt) != 1 || needsArt[0].Name != "Eminem" {
		t.Fatalf("only album owners should be enriched: err=%v artists=%+v", err, needsArt)
	}
	result, err := s.SearchLibrary(ctx, "Rihanna")
	if err != nil || len(result.Artists) != 0 || len(result.Tracks) != 1 {
		t.Fatalf("credited performer should resolve only through tracks: err=%v result=%+v", err, result)
	}
	changes, _, err := s.ChangesSince(ctx, 0, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	creditID := result.Tracks[0].ArtistCredits[0].ArtistID
	removed := false
	for _, change := range changes {
		removed = removed || change.Kind == "artist" && change.ID == creditID && change.Missing
	}
	if !removed {
		t.Fatalf("credit-only artist did not produce a removal: %+v", changes)
	}
}

func TestAlbumOwnershipUsesAlbumCreditsWhileTrackBrowseUsesCredits(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seq, _ := s.NextScanSeq(ctx)
	for i, track := range []source.TrackMeta{
		{
			NativeID: "compilation/one.flac", Title: "One", Album: "Shared Album",
			PrimaryArtist: source.ArtistReference{Name: "Guest One"},
			AlbumCredits:  []source.ArtistCredit{{Name: "Album Owner"}},
			TrackCredits:  []source.ArtistCredit{{Name: "Guest One"}},
			Container:     "flac", Codec: "flac", Version: 1,
		},
		{
			NativeID: "compilation/two.flac", Title: "Two", Album: "Shared Album",
			PrimaryArtist: source.ArtistReference{Name: "Guest Two"},
			AlbumCredits:  []source.ArtistCredit{{Name: "Album Owner"}},
			TrackCredits:  []source.ArtistCredit{{Name: "Guest Two"}},
			Container:     "flac", Codec: "flac", Version: 1,
		},
	} {
		track.Index = i + 1
		if err := s.UpsertTrack(ctx, "filesystem", track, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}

	albums, _, err := s.ListAlbums(ctx, "title", "", 10)
	if err != nil || len(albums) != 1 || albums[0].ArtistName != "Album Owner" {
		t.Fatalf("albums=%+v err=%v", albums, err)
	}
	artists, _, err := s.ListArtists(ctx, "title", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]ArtistRow, len(artists))
	for _, artist := range artists {
		byName[artist.Name] = artist
	}
	if len(artists) != 1 {
		t.Fatalf("only the album owner should be top-level: %+v", artists)
	}
	owner := byName["Album Owner"]
	if owner.AlbumCount != 1 {
		t.Fatalf("owner=%+v", owner)
	}
	owned, err := s.ListArtistAlbums(ctx, owner.ArtistID)
	if err != nil || len(owned) != 1 || owned[0].AlbumID != albums[0].AlbumID {
		t.Fatalf("owned albums=%+v err=%v", owned, err)
	}
	search, err := s.SearchLibrary(ctx, "Guest One")
	if err != nil || len(search.Artists) != 0 || len(search.Tracks) != 1 {
		t.Fatalf("credited guest search=%+v err=%v", search, err)
	}
	guestID := search.Tracks[0].ArtistCredits[0].ArtistID
	guest, found, err := s.GetArtist(ctx, guestID)
	if err != nil || found {
		t.Fatalf("credit-only guest became a resource: guest=%+v found=%v err=%v", guest, found, err)
	}
	guestAlbums, err := s.ListArtistAlbums(ctx, guestID)
	if err != nil || len(guestAlbums) != 0 {
		t.Fatalf("guest albums=%+v err=%v", guestAlbums, err)
	}
	guestTracks, err := s.ListArtistTracks(ctx, guestID)
	if err != nil || len(guestTracks) != 1 || guestTracks[0].Title != "One" {
		t.Fatalf("guest tracks=%+v err=%v", guestTracks, err)
	}
}

// indexFixture syncs a small two-artist library and returns the store.
func indexFixture(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	ctx := context.Background()
	seq, err := s.NextScanSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []source.TrackMeta{
		meta("a1/al1/t1.flac", "Alpha One", "The Alphas", "First Album", "Rock", 1994, 1),
		meta("a1/al1/t2.flac", "Alpha Two", "The Alphas", "First Album", "Rock", 1994, 2),
		meta("a1/al2/t1.flac", "Alpha Three", "The Alphas", "Second Album", "Electronic", 2001, 1),
		meta("a2/al1/t1.flac", "Beta One", "Betamax", "Beta Album", "Jazz", 1987, 1),
	} {
		if err := s.UpsertTrack(ctx, "filesystem", m, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSyncMintsStableIDs(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()

	artists, _, err := s.ListArtists(ctx, "title", "", 100)
	if err != nil || len(artists) != 2 {
		t.Fatalf("artists: %v %+v", err, artists)
	}
	if artists[0].Name != "Betamax" || artists[1].Name != "The Alphas" {
		t.Fatalf("sorted by name: %+v", artists) // "Betamax" < "The Alphas"
	}
	alphaID := artists[1].ArtistID

	// Re-scan: same content, ids must not change.
	seq, _ := s.NextScanSeq(ctx)
	if err := s.UpsertTrack(ctx, "filesystem",
		meta("a1/al1/t1.flac", "Alpha One", "The Alphas", "First Album", "Rock", 1994, 1), "", seq); err != nil {
		t.Fatal(err)
	}
	s.FinishScan(ctx, "filesystem", seq)

	again, _, _ := s.ListArtists(ctx, "title", "", 100)
	var alphaAgain ArtistRow
	for _, a := range again {
		if a.Name == "The Alphas" {
			alphaAgain = a
		}
	}
	if alphaAgain.ArtistID != alphaID {
		t.Fatalf("artist id must be stable across scans: %q vs %q", alphaID, alphaAgain.ArtistID)
	}

	// Tracks not seen in the latest scan are missing, never deleted.
	tr, _, err := s.ListTracks(ctx, "title", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 1 || tr[0].Title != "Alpha One" {
		t.Fatalf("non-missing tracks after partial rescan: %+v", tr)
	}
	lib, err := s.GetLibrarySummary(ctx)
	if err != nil || lib.Tracks != 1 || lib.Artists != 1 || lib.Albums != 1 {
		t.Fatalf("summary counts must exclude missing: %v %+v", err, lib)
	}
	if lib.UpdatedAt == 0 {
		t.Fatal("library updatedAt must be set by FinishScan")
	}
}

func TestBrowseQueries(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()

	artists, _, _ := s.ListArtists(ctx, "title", "", 100)
	var alpha ArtistRow
	for _, a := range artists {
		if a.Name == "The Alphas" {
			alpha = a
		}
	}
	if alpha.AlbumCount != 2 {
		t.Fatalf("alpha albums: %+v", alpha)
	}
	got, found, err := s.GetArtist(ctx, alpha.ArtistID)
	if err != nil || !found || got.Name != "The Alphas" || got.TrackCount != 3 {
		t.Fatalf("get artist: %v %v %+v", err, found, got)
	}
	if len(got.Genres) == 0 {
		t.Fatalf("artist genres must aggregate from tracks: %+v", got)
	}
	if _, found, _ := s.GetArtist(ctx, "art_nope"); found {
		t.Fatal("unknown artist")
	}

	albums, err := s.ListArtistAlbums(ctx, alpha.ArtistID)
	if err != nil || len(albums) != 2 || albums[0].ArtistName != "The Alphas" {
		t.Fatalf("artist albums: %v %+v", err, albums)
	}
	album := albums[0]
	tracks, err := s.ListAlbumTracks(ctx, album.AlbumID)
	if err != nil || len(tracks) == 0 || tracks[0].AlbumTitle != album.Title {
		t.Fatalf("album tracks: %v %+v", err, tracks)
	}
	if tracks[0].Index != 1 {
		t.Fatalf("tracks ordered by disc/index: %+v", tracks)
	}

	atr, err := s.ListArtistTracks(ctx, alpha.ArtistID)
	if err != nil || len(atr) != 3 {
		t.Fatalf("artist tracks: %v %d", err, len(atr))
	}

	genres, err := s.ListGenres(ctx)
	if err != nil || len(genres) != 3 {
		t.Fatalf("genres: %v %+v", err, genres)
	}
	gt, err := s.ListGenreTracks(ctx, "rock")
	if err != nil || len(gt) != 2 {
		t.Fatalf("genre tracks (case-insensitive): %v %d", err, len(gt))
	}
}

func TestCursorPaginationWalksEverything(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()

	var all []TrackRow
	cursor := ""
	pages := 0
	for {
		page, next, err := s.ListTracks(ctx, "title", cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		pages++
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("cursor loop")
		}
	}
	if len(all) != 4 || pages < 2 {
		t.Fatalf("pagination must walk all 4 tracks over ≥2 pages: %d tracks %d pages", len(all), pages)
	}
	seen := map[string]bool{}
	for _, tr := range all {
		if seen[tr.TrackID] {
			t.Fatalf("duplicate row across pages: %s", tr.TrackID)
		}
		seen[tr.TrackID] = true
	}

	if _, _, err := s.ListTracks(ctx, "title", "not-base64!", 3); err == nil {
		t.Fatal("garbage cursor must error")
	}
}

func TestSearchLibrary(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	res, err := s.SearchLibrary(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Artists) != 1 || len(res.Tracks) != 3 || len(res.Albums) != 2 {
		t.Fatalf("search: %+v", res)
	}
	res, _ = s.SearchLibrary(ctx, "album")
	if len(res.Albums) != 3 {
		t.Fatalf("album search: %+v", res)
	}
}

func TestArtStorageAndPropagation(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	artDir := filepath.Join(t.TempDir(), "art")

	artID, err := s.UpsertArt(ctx, "deadbeef01", "image/png", []byte("PNGDATA"), artDir)
	if err != nil || artID == "" {
		t.Fatalf("upsert art: %v %q", err, artID)
	}
	dup, err := s.UpsertArt(ctx, "deadbeef01", "image/png", []byte("PNGDATA"), artDir)
	if err != nil || dup != artID {
		t.Fatalf("same hash must dedupe: %v %q vs %q", err, dup, artID)
	}

	seq, _ := s.NextScanSeq(ctx)
	m := meta("x/t1.flac", "T", "A", "Al", "Rock", 2000, 1)
	if err := s.UpsertTrack(ctx, "filesystem", m, artID, seq); err != nil {
		t.Fatal(err)
	}
	s.FinishScan(ctx, "filesystem", seq)

	// Art propagates track → album → artist.
	tracks, _, _ := s.ListTracks(ctx, "title", "", 10)
	if tracks[0].ArtID != artID {
		t.Fatalf("track art: %+v", tracks[0])
	}
	albums, _, _ := s.ListAlbums(ctx, "title", "", 10)
	if albums[0].ArtID != artID {
		t.Fatalf("album art must inherit: %+v", albums[0])
	}
	artists, _, _ := s.ListArtists(ctx, "title", "", 10)
	if artists[0].ArtID != artID {
		t.Fatalf("artist art must inherit: %+v", artists[0])
	}

	path, mime, found, err := s.GetArt(ctx, artID)
	if err != nil || !found || mime != "image/png" || path == "" {
		t.Fatalf("get art: %v %v %q %q", err, found, path, mime)
	}
}

func TestUpsertArtDoesNotRewriteExistingBlobAfterDatabaseReset(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	artDir := filepath.Join(dataDir, "art")
	s, err := Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertArt(ctx, "survivor", "image/png", []byte("PNGDATA"), artDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(artDir, "survivor")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Remove(filepath.Join(dataDir, "blitterserver.db")); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	time.Sleep(10 * time.Millisecond)
	if _, err := s.UpsertArt(ctx, "survivor", "image/png", []byte("PNGDATA"), artDir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("blob was rewritten: before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

func TestResolveTrackNative(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	tracks, _, _ := s.ListTracks(ctx, "title", "", 1)
	kind, native, found, err := s.ResolveTrackNative(ctx, tracks[0].TrackID)
	if err != nil || !found || kind != "filesystem" || native == "" {
		t.Fatalf("resolve: %v %v %q %q", err, found, kind, native)
	}
	if _, _, found, _ := s.ResolveTrackNative(ctx, "trk_nope"); found {
		t.Fatal("unknown track must not resolve")
	}
}

// Incremental scans skip probing unchanged files: the store supplies known
// versions and marks skipped files seen so FinishScan keeps them alive.
func TestKnownVersionsAndMarkSeenSurviveFinishScan(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
	known, err := s.KnownTrackVersions(ctx, "filesystem")
	if err != nil || len(known) == 0 {
		t.Fatalf("known=%v err=%v", known, err)
	}
	seq, _ := s.NextScanSeq(ctx)
	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	if err := s.MarkTracksSeen(ctx, "filesystem", ids, seq); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tracks WHERE missing=1`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("unchanged tracks marked missing: %d", missing)
	}
}
