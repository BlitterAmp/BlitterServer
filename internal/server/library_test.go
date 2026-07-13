package server

import (
	"context"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/source"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// seedLibrary pushes a tiny library straight through the store (adapter
// parsing is covered elsewhere).
func seedLibrary(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	seq, err := st.NextScanSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []source.TrackMeta{
		{NativeID: "a/1.flac", Title: "One", PrimaryArtist: source.ArtistReference{Name: "Alpha"}, TrackCredits: []source.ArtistCredit{{Name: "Alpha", JoinPhrase: " feat. "}, {Name: "Guest"}}, AlbumCredits: []source.ArtistCredit{{Name: "Alpha"}}, RecordingMBID: "11111111-1111-1111-1111-111111111111", Album: "AA", Genre: "Rock", Year: 1990, Index: 1, DurationMs: 300000, Container: "flac", Codec: "flac", SizeBytes: 10, Version: 1},
		{NativeID: "a/2.flac", Title: "Two", PrimaryArtist: source.ArtistReference{Name: "Alpha"}, TrackCredits: []source.ArtistCredit{{Name: "Alpha"}}, AlbumCredits: []source.ArtistCredit{{Name: "Alpha"}}, Album: "AA", Genre: "Rock", Year: 1990, Index: 2, DurationMs: 300000, Container: "flac", Codec: "flac", SizeBytes: 10, Version: 1},
		{NativeID: "b/1.mp3", Title: "Three", PrimaryArtist: source.ArtistReference{Name: "Beta"}, TrackCredits: []source.ArtistCredit{{Name: "Beta"}}, AlbumCredits: []source.ArtistCredit{{Name: "Beta"}}, Album: "BB", Genre: "Jazz", Year: 2000, Index: 1, DurationMs: 180000, Container: "mp3", Codec: "mp3", SizeBytes: 10, Version: 1},
	} {
		if err := st.UpsertTrack(ctx, "filesystem", m, "", seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FinishScan(ctx, "filesystem", seq); err != nil {
		t.Fatal(err)
	}
}

func libSrv(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st := testStore(t)
	seedLibrary(t, st)
	mgr := library.NewManager(st, t.TempDir())
	return NewWithLibrary(st, mgr, "test"), st
}

func TestGetLibrarySummaryEndpoint(t *testing.T) {
	s, _ := libSrv(t)
	resp, err := s.GetLibrary(context.Background(), api.GetLibraryRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	lib := resp.(api.GetLibrary200JSONResponse)
	if lib.Counts.Tracks == nil || *lib.Counts.Tracks != 3 || lib.UpdatedAt == 0 {
		t.Fatalf("library: %+v", lib)
	}
}

func TestBrowseEndpoints(t *testing.T) {
	s, _ := libSrv(t)
	ctx := context.Background()

	arts, err := s.ListArtists(ctx, api.ListArtistsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	page := arts.(api.ListArtists200JSONResponse)
	if len(page.Items) != 2 || page.Items[0].Name != "Alpha" {
		t.Fatalf("artists: %+v", page)
	}
	alphaID := page.Items[0].ArtistId

	one, err := s.GetArtist(ctx, api.GetArtistRequestObject{ArtistId: alphaID})
	if err != nil {
		t.Fatal(err)
	}
	detail := one.(api.GetArtist200JSONResponse)
	if detail.Name != "Alpha" || detail.TrackCount == nil || *detail.TrackCount != 2 {
		t.Fatalf("artist detail: %+v", detail)
	}
	nf, _ := s.GetArtist(ctx, api.GetArtistRequestObject{ArtistId: "art_nope"})
	if _, is404 := nf.(api.GetArtist404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("unknown artist: want 404, got %T", nf)
	}

	albums, err := s.ListArtistAlbums(ctx, api.ListArtistAlbumsRequestObject{ArtistId: alphaID})
	if err != nil {
		t.Fatal(err)
	}
	albumList := albums.(api.ListArtistAlbums200JSONResponse)
	if len(albumList) != 1 || albumList[0].Title != "AA" {
		t.Fatalf("artist albums: %+v", albumList)
	}
	albumID := albumList[0].AlbumId

	tracks, err := s.ListAlbumTracks(ctx, api.ListAlbumTracksRequestObject{AlbumId: albumID})
	if err != nil {
		t.Fatal(err)
	}
	trackList := tracks.(api.ListAlbumTracks200JSONResponse)
	if len(trackList) != 2 || trackList[0].Title != "One" || trackList[0].Media.Container != "flac" {
		t.Fatalf("album tracks: %+v", trackList)
	}
	assertTrackIdentity(t, trackList[0])

	genres, err := s.ListGenres(ctx, api.ListGenresRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if g := genres.(api.ListGenres200JSONResponse); len(g) != 2 {
		t.Fatalf("genres: %+v", g)
	}

	res, err := s.Search(ctx, api.SearchRequestObject{Params: api.SearchParams{Q: "One"}})
	if err != nil {
		t.Fatal(err)
	}
	sr := res.(api.Search200JSONResponse)
	if len(sr.Tracks) != 1 || sr.Tracks[0].Title != "One" {
		t.Fatalf("search: %+v", sr)
	}
	assertTrackIdentity(t, sr.Tracks[0])
	if sr.External == nil || len(sr.External) != 0 {
		t.Fatalf("external must be an empty array, not null: %+v", sr.External)
	}
}

func assertTrackIdentity(t *testing.T, tr api.Track) {
	t.Helper()
	if len(tr.ArtistCredits) == 0 || tr.PrimaryArtist.ArtistId == "" || tr.PrimaryArtist.Name == "" {
		t.Fatalf("invalid track artist identity: %+v", tr)
	}
	if tr.Title == "One" && tr.MusicBrainzRecordingId == nil {
		t.Fatalf("recording identity lost: %+v", tr)
	}
}

func TestAlbumArtFallbacksReachTrackAndArtistAPI(t *testing.T) {
	s, st := libSrv(t)
	ctx := context.Background()
	albums, _, err := st.ListAlbums(ctx, "title", "", 1)
	if err != nil || len(albums) != 1 {
		t.Fatalf("list albums: %v %+v", err, albums)
	}
	album := albums[0]
	seq, err := st.NextScanSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTrack(ctx, "filesystem", source.TrackMeta{
		NativeID: "a/1.flac", Title: "One", PrimaryArtist: source.ArtistReference{Name: "Alpha"}, TrackCredits: []source.ArtistCredit{{Name: "Alpha"}}, AlbumCredits: []source.ArtistCredit{{Name: "Alpha"}}, Album: "AA",
		Genre: "Rock", Year: 1990, Index: 1, DurationMs: 300000, Container: "flac", Codec: "flac",
		SizeBytes: 10, Version: 1,
	}, "img_track", seq); err != nil {
		t.Fatal(err)
	}
	if applied, err := st.SetAlbumArt(ctx, album.AlbumID, "img_album", seq); err != nil || !applied {
		t.Fatalf("set album art: applied=%v err=%v", applied, err)
	}

	albumResp, err := s.GetAlbum(ctx, api.GetAlbumRequestObject{AlbumId: album.AlbumID})
	if err != nil {
		t.Fatal(err)
	}
	albumAPI := albumResp.(api.GetAlbum200JSONResponse)
	if albumAPI.ArtId == nil || *albumAPI.ArtId != "img_album" {
		t.Fatalf("album API art: %+v", albumAPI)
	}
	tracksResp, err := s.ListAlbumTracks(ctx, api.ListAlbumTracksRequestObject{AlbumId: album.AlbumID})
	if err != nil {
		t.Fatal(err)
	}
	tracks := tracksResp.(api.ListAlbumTracks200JSONResponse)
	if tracks[0].ArtId == nil || *tracks[0].ArtId != "img_track" || tracks[1].ArtId == nil || *tracks[1].ArtId != "img_album" {
		t.Fatalf("track API art precedence: %+v", tracks)
	}
	artistResp, err := s.GetArtist(ctx, api.GetArtistRequestObject{ArtistId: album.ArtistID})
	if err != nil {
		t.Fatal(err)
	}
	artist := artistResp.(api.GetArtist200JSONResponse)
	if artist.ArtId == nil || *artist.ArtId != "img_album" {
		t.Fatalf("artist API album fallback: %+v", artist)
	}

	if applied, err := st.SetArtistArt(ctx, album.ArtistID, "", "img_artist", seq+1); err != nil || !applied {
		t.Fatalf("set artist art: applied=%v err=%v", applied, err)
	}
	artistResp, err = s.GetArtist(ctx, api.GetArtistRequestObject{ArtistId: album.ArtistID})
	if err != nil {
		t.Fatal(err)
	}
	artist = artistResp.(api.GetArtist200JSONResponse)
	if artist.ArtId == nil || *artist.ArtId != "img_artist" {
		t.Fatalf("explicit artist API art: %+v", artist)
	}
}

func TestTracksPaginationEndpoint(t *testing.T) {
	s, _ := libSrv(t)
	ctx := context.Background()
	limit := 2
	first, err := s.ListTracks(ctx, api.ListTracksRequestObject{Params: api.ListTracksParams{Limit: &limit}})
	if err != nil {
		t.Fatal(err)
	}
	p1 := first.(api.ListTracks200JSONResponse)
	if len(p1.Items) != 2 || p1.NextCursor == nil {
		t.Fatalf("page 1: %+v", p1)
	}
	second, err := s.ListTracks(ctx, api.ListTracksRequestObject{
		Params: api.ListTracksParams{Limit: &limit, Cursor: p1.NextCursor}})
	if err != nil {
		t.Fatal(err)
	}
	p2 := second.(api.ListTracks200JSONResponse)
	if len(p2.Items) != 1 || p2.NextCursor != nil {
		t.Fatalf("page 2: %+v", p2)
	}
}

func TestStatusReflectsConfiguredSource(t *testing.T) {
	st := testStore(t)
	mgr := library.NewManager(st, t.TempDir())
	s := NewWithLibrary(st, mgr, "test")
	ctx := context.Background()

	resp, _ := s.GetStatus(ctx, api.GetStatusRequestObject{})
	if r := resp.(api.GetStatus200JSONResponse); r.Source.Kind != "none" {
		t.Fatalf("unconfigured: %+v", r.Source)
	}

	if err := mgr.Configure(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	resp, _ = s.GetStatus(ctx, api.GetStatusRequestObject{})
	r := resp.(api.GetStatus200JSONResponse)
	if r.Source.Kind != "filesystem" || !r.Source.Connected {
		t.Fatalf("configured: %+v", r.Source)
	}
}

func TestAdminFilesystemSourceEndpoints(t *testing.T) {
	st := testStore(t)
	mgr := library.NewManager(st, t.TempDir())
	s := NewWithLibrary(st, mgr, "test")
	ctx := context.Background()

	get, err := s.AdminGetFilesystemSource(ctx, api.AdminGetFilesystemSourceRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg := get.(api.AdminGetFilesystemSource200JSONResponse); cfg.Configured || cfg.Scanning {
		t.Fatalf("fresh: %+v", cfg)
	}

	scan, err := s.AdminScanFilesystemSource(ctx, api.AdminScanFilesystemSourceRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, is409 := scan.(api.AdminScanFilesystemSource409ApplicationProblemPlusJSONResponse); !is409 {
		t.Fatalf("scan unconfigured: want 409, got %T", scan)
	}

	if _, err := s.AdminSetFilesystemSource(ctx, api.AdminSetFilesystemSourceRequestObject{
		Body: &api.AdminSetFilesystemSourceJSONRequestBody{Path: "/definitely/not/here"}}); err == nil {
		t.Fatal("bad path must error (400)")
	}

	dir := t.TempDir()
	set, err := s.AdminSetFilesystemSource(ctx, api.AdminSetFilesystemSourceRequestObject{
		Body: &api.AdminSetFilesystemSourceJSONRequestBody{Path: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := set.(api.AdminSetFilesystemSource204Response); !is204 {
		t.Fatalf("set: want 204, got %T", set)
	}
	get, _ = s.AdminGetFilesystemSource(ctx, api.AdminGetFilesystemSourceRequestObject{})
	if cfg := get.(api.AdminGetFilesystemSource200JSONResponse); !cfg.Configured || cfg.Path == nil || *cfg.Path != dir {
		t.Fatalf("configured: %+v", cfg)
	}

	scan, err = s.AdminScanFilesystemSource(ctx, api.AdminScanFilesystemSourceRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, is202 := scan.(api.AdminScanFilesystemSource202Response); !is202 {
		t.Fatalf("scan: want 202, got %T", scan)
	}

	del, err := s.AdminDeleteFilesystemSource(ctx, api.AdminDeleteFilesystemSourceRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := del.(api.AdminDeleteFilesystemSource204Response); !is204 {
		t.Fatalf("delete: want 204, got %T", del)
	}
	get, _ = s.AdminGetFilesystemSource(ctx, api.AdminGetFilesystemSourceRequestObject{})
	if cfg := get.(api.AdminGetFilesystemSource200JSONResponse); cfg.Configured {
		t.Fatalf("after unlink: %+v", cfg)
	}
}
