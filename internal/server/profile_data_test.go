package server

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/artifacts"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// dataSrv: server over a seeded library with two profiles and a live bus.
func dataSrv(t *testing.T) (*Server, *store.Store, *events.Bus, context.Context, context.Context) {
	t.Helper()
	st := testStore(t)
	seedLibrary(t, st) // 3 tracks: One/Two (Alpha/AA), Three (Beta/BB)
	bus := events.NewBus(st)
	lib := library.NewManager(st, t.TempDir())
	s := NewFull(st, lib, bus, artifacts.NewManager(st, lib, bus, t.TempDir()), "test")
	ctx := context.Background()
	dev, _ := st.CreateDevice(ctx, "d", "ios")
	p1, _ := st.CreateProfileRecord(ctx, "Nathan", "", "")
	p2, _ := st.CreateProfileRecord(ctx, "Kid", "", "")
	ctx1 := auth.WithIdentity(ctx, auth.Identity{DeviceID: dev, ProfileID: p1.ProfileID})
	ctx2 := auth.WithIdentity(ctx, auth.Identity{DeviceID: dev, ProfileID: p2.ProfileID})
	return s, st, bus, ctx1, ctx2
}

func firstTrack(t *testing.T, st *store.Store) store.TrackRow {
	t.Helper()
	tracks, _, err := st.ListTracks(context.Background(), "title", "", 1)
	if err != nil || len(tracks) == 0 {
		t.Fatal(err)
	}
	return tracks[0]
}

func TestPlaylistPermissions(t *testing.T) {
	s, st, _, ctx1, ctx2 := dataSrv(t)
	tr := firstTrack(t, st)

	created, err := s.CreatePlaylist(ctx1, api.CreatePlaylistRequestObject{
		Body: &api.CreatePlaylistJSONRequestBody{Title: "Mine", TrackIds: &[]string{tr.TrackID}}})
	if err != nil {
		t.Fatal(err)
	}
	pl := created.(api.CreatePlaylist201JSONResponse)
	if pl.Origin != "blitterserver" || pl.Visibility != "private" || pl.TrackCount != 1 {
		t.Fatalf("create: %+v", pl)
	}

	// Private: invisible to the other profile (list and direct get).
	other, _ := s.ListPlaylists(ctx2, api.ListPlaylistsRequestObject{})
	if got := other.(api.ListPlaylists200JSONResponse); len(got) != 0 {
		t.Fatalf("private leaked: %+v", got)
	}
	hidden, _ := s.GetPlaylist(ctx2, api.GetPlaylistRequestObject{PlaylistId: pl.PlaylistId})
	if _, is404 := hidden.(api.GetPlaylist404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("private direct get by other: want 404, got %T", hidden)
	}

	// Non-owner cannot mutate even when shared.
	s.UpdatePlaylist(ctx1, api.UpdatePlaylistRequestObject{PlaylistId: pl.PlaylistId,
		Body: &api.UpdatePlaylistJSONRequestBody{Visibility: (*api.UpdatePlaylistJSONBodyVisibility)(str("shared"))}})
	denied, _ := s.UpdatePlaylist(ctx2, api.UpdatePlaylistRequestObject{PlaylistId: pl.PlaylistId,
		Body: &api.UpdatePlaylistJSONRequestBody{Title: str("Stolen")}})
	if _, is403 := denied.(api.UpdatePlaylist403ApplicationProblemPlusJSONResponse); !is403 {
		t.Fatalf("non-owner rename: want 403, got %T", denied)
	}
	deniedDel, _ := s.DeletePlaylist(ctx2, api.DeletePlaylistRequestObject{PlaylistId: pl.PlaylistId})
	if _, is403 := deniedDel.(api.DeletePlaylist403ApplicationProblemPlusJSONResponse); !is403 {
		t.Fatalf("non-owner delete: want 403, got %T", deniedDel)
	}
	deniedAppend, _ := s.AppendPlaylistTracks(ctx2, api.AppendPlaylistTracksRequestObject{PlaylistId: pl.PlaylistId,
		Body: &api.AppendPlaylistTracksJSONRequestBody{TrackIds: []string{tr.TrackID}}})
	if _, is403 := deniedAppend.(api.AppendPlaylistTracks403ApplicationProblemPlusJSONResponse); !is403 {
		t.Fatalf("append to shared by non-owner: want 403, got %T", deniedAppend)
	}

	// Collaborative: household may append and remove, still not rename.
	s.UpdatePlaylist(ctx1, api.UpdatePlaylistRequestObject{PlaylistId: pl.PlaylistId,
		Body: &api.UpdatePlaylistJSONRequestBody{Visibility: (*api.UpdatePlaylistJSONBodyVisibility)(str("collaborative"))}})
	okAppend, err := s.AppendPlaylistTracks(ctx2, api.AppendPlaylistTracksRequestObject{PlaylistId: pl.PlaylistId,
		Body: &api.AppendPlaylistTracksJSONRequestBody{TrackIds: []string{tr.TrackID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := okAppend.(api.AppendPlaylistTracks204Response); !is204 {
		t.Fatalf("collaborative append: want 204, got %T", okAppend)
	}

	items, err := s.ListPlaylistTracks(ctx2, api.ListPlaylistTracksRequestObject{PlaylistId: pl.PlaylistId})
	if err != nil {
		t.Fatal(err)
	}
	page := items.(api.ListPlaylistTracks200JSONResponse)
	if len(page.Items) != 2 || page.Items[0].ItemId == "" {
		t.Fatalf("items: %+v", page)
	}
	removed, err := s.RemovePlaylistTrack(ctx2, api.RemovePlaylistTrackRequestObject{
		PlaylistId: pl.PlaylistId, ItemId: page.Items[0].ItemId})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := removed.(api.RemovePlaylistTrack204Response); !is204 {
		t.Fatalf("collaborative remove: want 204, got %T", removed)
	}

	// Owner deletes.
	okDel, _ := s.DeletePlaylist(ctx1, api.DeletePlaylistRequestObject{PlaylistId: pl.PlaylistId})
	if _, is204 := okDel.(api.DeletePlaylist204Response); !is204 {
		t.Fatalf("owner delete: want 204, got %T", okDel)
	}
}

func TestSetLoveEndpointMaintainsLovedTracks(t *testing.T) {
	s, st, _, ctx1, _ := dataSrv(t)
	tr := firstTrack(t, st)

	resp, err := s.SetLove(ctx1, api.SetLoveRequestObject{Ref: tr.TrackID,
		Body: &api.SetLoveJSONRequestBody{State: "loved"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := resp.(api.SetLove200JSONResponse)
	if rec.State != "loved" || rec.Kind != "track" || !rec.Owned {
		t.Fatalf("love: %+v", rec)
	}

	// Loved Tracks auto-playlist exists and contains the track.
	lists, _ := s.ListPlaylists(ctx1, api.ListPlaylistsRequestObject{})
	var loved *api.Playlist
	for _, p := range lists.(api.ListPlaylists200JSONResponse) {
		if p.Title == "Loved Tracks" {
			loved = &p
		}
	}
	if loved == nil || loved.TrackCount != 1 {
		t.Fatalf("loved tracks playlist: %+v", loved)
	}

	// Unloving removes it from the auto playlist.
	if _, err := s.SetLove(ctx1, api.SetLoveRequestObject{Ref: tr.TrackID,
		Body: &api.SetLoveJSONRequestBody{State: "neutral"}}); err != nil {
		t.Fatal(err)
	}
	lists, _ = s.ListPlaylists(ctx1, api.ListPlaylistsRequestObject{})
	for _, p := range lists.(api.ListPlaylists200JSONResponse) {
		if p.Title == "Loved Tracks" && p.TrackCount != 0 {
			t.Fatalf("unlove must empty the auto playlist: %+v", p)
		}
	}

	nf, _ := s.SetLove(ctx1, api.SetLoveRequestObject{Ref: "trk_nope",
		Body: &api.SetLoveJSONRequestBody{State: "loved"}})
	if _, is404 := nf.(api.SetLove404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("unknown ref: want 404, got %T", nf)
	}
}

func TestBrowseDecoration(t *testing.T) {
	s, st, _, ctx1, ctx2 := dataSrv(t)
	tr := firstTrack(t, st)

	s.SetLove(ctx1, api.SetLoveRequestObject{Ref: tr.TrackID, Body: &api.SetLoveJSONRequestBody{State: "loved"}})
	s.SetRating(ctx1, api.SetRatingRequestObject{Body: &api.SetRatingJSONRequestBody{
		ItemType: "track", ItemId: tr.TrackID, Rating10: ptrInt(8)}})

	got, _ := s.GetTrack(ctx1, api.GetTrackRequestObject{TrackId: tr.TrackID})
	dec := got.(api.GetTrack200JSONResponse)
	if dec.LoveState == nil || *dec.LoveState != "loved" || dec.UserRating10 == nil || *dec.UserRating10 != 8 {
		t.Fatalf("decoration for owner profile: %+v", dec)
	}
	// The other profile sees neutral/unrated.
	got2, _ := s.GetTrack(ctx2, api.GetTrackRequestObject{TrackId: tr.TrackID})
	dec2 := got2.(api.GetTrack200JSONResponse)
	if dec2.LoveState != nil || dec2.UserRating10 != nil {
		t.Fatalf("cross-profile decoration leak: %+v", dec2)
	}
}

func TestPlaybackAndPresenceEndpoints(t *testing.T) {
	s, st, _, ctx1, ctx2 := dataSrv(t)
	tr := firstTrack(t, st)

	resp, err := s.ReportPlaybackEvents(ctx1, api.ReportPlaybackEventsRequestObject{
		Body: &api.ReportPlaybackEventsJSONRequestBody{Events: []api.PlaybackEvent{
			{EventId: "e1", Type: "started", TrackId: tr.TrackID, At: time.Now()},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is202 := resp.(api.ReportPlaybackEvents202Response); !is202 {
		t.Fatalf("report: want 202, got %T", resp)
	}

	pres, err := s.GetPresence(ctx2, api.GetPresenceRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	entries := pres.(api.GetPresence200JSONResponse)
	if len(entries) != 1 || entries[0].ProfileName != "Nathan" || entries[0].Track.TrackId != tr.TrackID {
		t.Fatalf("presence: %+v", entries)
	}

	s.ReportPlaybackEvents(ctx1, api.ReportPlaybackEventsRequestObject{
		Body: &api.ReportPlaybackEventsJSONRequestBody{Events: []api.PlaybackEvent{
			{EventId: "e2", Type: "ended", TrackId: tr.TrackID, At: time.Now()},
		}}})
	snap, err := s.GetTasteSnapshot(ctx1, api.GetTasteSnapshotRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	ts := snap.(api.GetTasteSnapshot200JSONResponse)
	if len(ts.Tracks) != 1 || ts.Tracks[0].Plays != 1 {
		t.Fatalf("taste: %+v", ts)
	}
}

func TestRecommendationEndpoints(t *testing.T) {
	s, st, _, ctx1, ctx2 := dataSrv(t)
	tr := firstTrack(t, st)
	p2, _ := auth.IdentityFrom(ctx2)

	created, err := s.CreateRecommendation(ctx1, api.CreateRecommendationRequestObject{
		Body: &api.CreateRecommendationJSONRequestBody{ToProfileId: p2.ProfileID, Ref: tr.AlbumID, Note: str("listen!")}})
	if err != nil {
		t.Fatal(err)
	}
	rec := created.(api.CreateRecommendation201JSONResponse)
	if rec.Kind != "album" || rec.ToProfileId != p2.ProfileID {
		t.Fatalf("create: %+v", rec)
	}

	inbox, err := s.ListRecommendations(ctx2, api.ListRecommendationsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	page := inbox.(api.ListRecommendations200JSONResponse)
	if len(page.Items) != 1 || page.Items[0].Seen {
		t.Fatalf("inbox: %+v", page)
	}

	seen, err := s.MarkRecommendationSeen(ctx2, api.MarkRecommendationSeenRequestObject{
		RecommendationId: rec.RecommendationId})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := seen.(api.MarkRecommendationSeen204Response); !is204 {
		t.Fatalf("seen: want 204, got %T", seen)
	}
	// Someone else's recommendation is 404 to mark.
	nf, _ := s.MarkRecommendationSeen(ctx1, api.MarkRecommendationSeenRequestObject{
		RecommendationId: rec.RecommendationId})
	if _, is404 := nf.(api.MarkRecommendationSeen404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("cross-profile seen: want 404, got %T", nf)
	}
}

func ptrInt(v int) *int { return &v }
