package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/source"
)

func TestArtEligibilityDailyBoundaryAndExplicitArtistExclusion(t *testing.T) {
	s := indexFixture(t)
	ctx := context.Background()
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
		NativeID: native, Title: title, Artist: artist, AlbumArtist: artist,
		Album: album, Genre: genre, Year: year, Index: idx,
		DurationMs: 2000, Container: "flac", Codec: "flac", BitrateKbps: 900,
		SizeBytes: 123456, Version: 1,
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
	if len(res.Artists) != 1 || len(res.Tracks) != 3 || len(res.Albums) != 0 {
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
