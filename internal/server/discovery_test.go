package server

import (
	"context"
	"testing"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// discoverySrv seeds plays/loves so mixes materialize.
func discoverySrv(t *testing.T) (*Server, context.Context, context.Context) {
	t.Helper()
	s, st, _, ctx1, ctx2 := dataSrv(t)
	id, _ := auth.IdentityFrom(ctx1)
	tr := firstTrack(t, st)
	st.IngestPlaybackEvents(context.Background(), id.ProfileID, "d", []store.PlaybackEventRecord{
		{EventID: "x1", Type: "ended", TrackID: tr.TrackID, At: time.Now().UTC()},
	})
	st.SetRating(context.Background(), id.ProfileID, "track", tr.TrackID, 9)
	return s, ctx1, ctx2
}

func TestHomeMixesRadio(t *testing.T) {
	s, ctx1, _ := discoverySrv(t)

	home, err := s.GetHome(ctx1, api.GetHomeRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	rails := home.(api.GetHome200JSONResponse)
	if len(rails.Rails) == 0 {
		t.Fatalf("home must have rails: %+v", rails)
	}

	mixes, err := s.ListMixes(ctx1, api.ListMixesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	list := mixes.(api.ListMixes200JSONResponse)
	if len(list) == 0 {
		t.Fatalf("mixes: %+v", list)
	}

	tracks, err := s.ListMixTracks(ctx1, api.ListMixTracksRequestObject{MixId: list[0].MixId})
	if err != nil {
		t.Fatal(err)
	}
	if got := tracks.(api.ListMixTracks200JSONResponse); len(got) == 0 {
		t.Fatalf("mix tracks: %+v", got)
	}
	nf, _ := s.ListMixTracks(ctx1, api.ListMixTracksRequestObject{MixId: "bogus"})
	if _, is404 := nf.(api.ListMixTracks404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("unknown mix: want 404, got %T", nf)
	}

	radio, err := s.GetRadioNext(ctx1, api.GetRadioNextRequestObject{
		Body: &api.GetRadioNextJSONRequestBody{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := radio.(api.GetRadioNext200JSONResponse); len(got) == 0 {
		t.Fatalf("radio: %+v", got)
	}
}

func TestDiscoveryHonestAbsence(t *testing.T) {
	s, ctx1, _ := discoverySrv(t)

	disc, err := s.GetMyDiscover(ctx1, api.GetMyDiscoverRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if got := disc.(api.GetMyDiscover200JSONResponse); len(got) != 0 {
		t.Fatalf("no provider means empty discover: %+v", got)
	}

	tr := firstTrackFromCtx(t, s, ctx1)
	similar, err := s.ListSimilarArtists(ctx1, api.ListSimilarArtistsRequestObject{ArtistId: tr.ArtistId})
	if err != nil {
		t.Fatal(err)
	}
	if got := similar.(api.ListSimilarArtists200JSONResponse); len(got) != 0 {
		t.Fatalf("no discovery integration means empty similar: %+v", got)
	}
	nf, _ := s.ListSimilarArtists(ctx1, api.ListSimilarArtistsRequestObject{ArtistId: "art_nope"})
	if _, is404 := nf.(api.ListSimilarArtists404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("unknown artist: want 404, got %T", nf)
	}

	ext, _ := s.GetExternalArtist(ctx1, api.GetExternalArtistRequestObject{Name: "Anyone"})
	if _, is404 := ext.(api.GetExternalArtist404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("external artist without discovery: want 404, got %T", ext)
	}

	act, err := s.GetAcquisitionActivity(ctx1, api.GetAcquisitionActivityRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	a := act.(api.GetAcquisitionActivity200JSONResponse)
	if a.Queue == nil || a.Wanted == nil || len(a.Queue) != 0 || len(a.Wanted) != 0 {
		t.Fatalf("no acquirer means empty arrays: %+v", a)
	}
}

func TestPartyEndpoints(t *testing.T) {
	s, ctx1, ctx2 := discoverySrv(t)
	p2, _ := auth.IdentityFrom(ctx2)

	created, err := s.CreateParty(ctx1, api.CreatePartyRequestObject{
		Body: &api.CreatePartyJSONRequestBody{Name: str("Disco")}})
	if err != nil {
		t.Fatal(err)
	}
	pty := created.(api.CreateParty201JSONResponse)
	if pty.Name == nil || *pty.Name != "Disco" || len(pty.Members) != 1 {
		t.Fatalf("create: %+v", pty)
	}

	inv, err := s.InviteToParty(ctx1, api.InviteToPartyRequestObject{PartyId: pty.PartyId,
		Body: &api.InviteToPartyJSONRequestBody{ProfileIds: []string{p2.ProfileID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := inv.(api.InviteToParty204Response); !is204 {
		t.Fatalf("invite: %T", inv)
	}
	joined, err := s.JoinParty(ctx2, api.JoinPartyRequestObject{PartyId: pty.PartyId})
	if err != nil {
		t.Fatal(err)
	}
	if got := joined.(api.JoinParty200JSONResponse); len(got.Members) != 2 {
		t.Fatalf("join: %+v", got)
	}

	tr := firstTrackFromCtx(t, s, ctx1)
	q, err := s.AppendPartyQueue(ctx2, api.AppendPartyQueueRequestObject{PartyId: pty.PartyId,
		Body: &api.AppendPartyQueueJSONRequestBody{TrackIds: []string{tr.TrackId}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := q.(api.AppendPartyQueue204Response); !is204 {
		t.Fatalf("queue: %T", q)
	}

	// Guest transport is 403; host play starts the timeline.
	denied, _ := s.PartyTransport(ctx2, api.PartyTransportRequestObject{PartyId: pty.PartyId,
		Body: &api.PartyTransportJSONRequestBody{Action: "play"}})
	if _, is403 := denied.(api.PartyTransport403ApplicationProblemPlusJSONResponse); !is403 {
		t.Fatalf("guest transport: %T", denied)
	}
	played, err := s.PartyTransport(ctx1, api.PartyTransportRequestObject{PartyId: pty.PartyId,
		Body: &api.PartyTransportJSONRequestBody{Action: "play"}})
	if err != nil {
		t.Fatal(err)
	}
	state := played.(api.PartyTransport200JSONResponse)
	if state.Paused || state.TrackId == nil || *state.TrackId != tr.TrackId {
		t.Fatalf("play: %+v", state)
	}

	lists, err := s.ListParties(ctx2, api.ListPartiesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if got := lists.(api.ListParties200JSONResponse); len(got) != 1 || len(got[0].Queue) != 1 {
		t.Fatalf("list: %+v", got)
	}

	ended, err := s.EndParty(ctx1, api.EndPartyRequestObject{PartyId: pty.PartyId})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := ended.(api.EndParty204Response); !is204 {
		t.Fatalf("end: %T", ended)
	}
	nf, _ := s.GetParty(ctx1, api.GetPartyRequestObject{PartyId: pty.PartyId})
	if _, is404 := nf.(api.GetParty404ApplicationProblemPlusJSONResponse); !is404 {
		t.Fatalf("ended party: %T", nf)
	}
}

func TestIntegrationConfigEndpoints(t *testing.T) {
	s, _, _ := discoverySrv(t)
	ctx := context.Background()

	// Lidarr: unconfigured → configured (secrets write-only) → test fails fast → delete.
	get, err := s.AdminGetLidarr(ctx, api.AdminGetLidarrRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg := get.(api.AdminGetLidarr200JSONResponse); cfg.Configured {
		t.Fatalf("fresh lidarr: %+v", cfg)
	}
	set, err := s.AdminSetLidarr(ctx, api.AdminSetLidarrRequestObject{
		Body: &api.AdminSetLidarrJSONRequestBody{BaseUrl: "http://127.0.0.1:1", ApiKey: "sekrit"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := set.(api.AdminSetLidarr204Response); !is204 {
		t.Fatalf("set lidarr: %T", set)
	}
	get, _ = s.AdminGetLidarr(ctx, api.AdminGetLidarrRequestObject{})
	cfg := get.(api.AdminGetLidarr200JSONResponse)
	if !cfg.Configured || cfg.ApiKeySet == nil || !*cfg.ApiKeySet || cfg.BaseUrl == nil {
		t.Fatalf("configured lidarr: %+v", cfg)
	}
	test, err := s.AdminTestLidarr(ctx, api.AdminTestLidarrRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if r := test.(api.AdminTestLidarr200JSONResponse); r.Ok || r.Error == nil {
		t.Fatalf("unreachable lidarr must test false: %+v", r)
	}
	del, _ := s.AdminDeleteLidarr(ctx, api.AdminDeleteLidarrRequestObject{})
	if _, is204 := del.(api.AdminDeleteLidarr204Response); !is204 {
		t.Fatalf("delete lidarr: %T", del)
	}

	// last.fm: credentials stored write-only; capabilities flip.
	lfGet, _ := s.AdminGetLastfm(ctx, api.AdminGetLastfmRequestObject{})
	if cfg := lfGet.(api.AdminGetLastfm200JSONResponse); cfg.Configured {
		t.Fatalf("fresh lastfm: %+v", cfg)
	}
	lfSet, err := s.AdminSetLastfm(ctx, api.AdminSetLastfmRequestObject{
		Body: &api.AdminSetLastfmJSONRequestBody{ApiKey: "k", SharedSecret: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, is204 := lfSet.(api.AdminSetLastfm204Response); !is204 {
		t.Fatalf("set lastfm: %T", lfSet)
	}
	lfGet, _ = s.AdminGetLastfm(ctx, api.AdminGetLastfmRequestObject{})
	if cfg := lfGet.(api.AdminGetLastfm200JSONResponse); !cfg.Configured {
		t.Fatalf("configured lastfm: %+v", cfg)
	}
	caps, _ := s.GetCapabilities(ctx, api.GetCapabilitiesRequestObject{})
	if c := caps.(api.GetCapabilities200JSONResponse); !c.Lastfm {
		t.Fatalf("capabilities must reflect lastfm creds: %+v", c)
	}
	lfDel, _ := s.AdminDeleteLastfm(ctx, api.AdminDeleteLastfmRequestObject{})
	if _, is204 := lfDel.(api.AdminDeleteLastfm204Response); !is204 {
		t.Fatalf("delete lastfm: %T", lfDel)
	}
}

// firstTrackFromCtx fetches a decorated track via the API for id reuse.
func firstTrackFromCtx(t *testing.T, s *Server, ctx context.Context) api.Track {
	t.Helper()
	resp, err := s.ListTracks(ctx, api.ListTracksRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	page := resp.(api.ListTracks200JSONResponse)
	if len(page.Items) == 0 {
		t.Fatal("no tracks")
	}
	return page.Items[0]
}
