package server

import (
	"context"
	"errors"
	"strings"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/lastfm"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func apiMix(m store.MixInfo) api.Mix {
	out := api.Mix{MixId: m.MixID, Kind: api.MixKind(m.Kind), Title: m.Title}
	out.TrackCount = optInt(m.TrackCount)
	if len(m.CollageArtIDs) > 0 {
		ids := m.CollageArtIDs
		out.CollageArtIds = &ids
	}
	return out
}

func (s *Server) GetHome(ctx context.Context, _ api.GetHomeRequestObject) (api.GetHomeResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	var resp api.GetHome200JSONResponse
	type rail = struct {
		Albums    *[]api.Album           `json:"albums,omitempty"`
		Kind      api.HomeRailsRailsKind `json:"kind"`
		Mixes     *[]api.Mix             `json:"mixes,omitempty"`
		Playlists *[]api.Playlist        `json:"playlists,omitempty"`
		Title     string                 `json:"title"`
		Tracks    *[]api.Track           `json:"tracks,omitempty"`
	}

	if mixes, err := s.st.AvailableMixes(ctx, prf); err != nil {
		return nil, err
	} else if len(mixes) > 0 {
		items := make([]api.Mix, 0, len(mixes))
		for _, m := range mixes {
			items = append(items, apiMix(m))
		}
		resp.Rails = append(resp.Rails, rail{Kind: "mixes", Title: "Made For You", Mixes: &items})
	}

	if lists, err := s.st.ListPlaylists(ctx, prf); err != nil {
		return nil, err
	} else if len(lists) > 0 {
		items := make([]api.Playlist, 0, len(lists))
		for _, p := range lists {
			items = append(items, apiPlaylist(p))
		}
		resp.Rails = append(resp.Rails, rail{Kind: "playlists", Title: "Playlists", Playlists: &items})
	}

	if recent, err := s.st.RecentlyPlayedTracks(ctx, prf, 20); err != nil {
		return nil, err
	} else if len(recent) > 0 {
		items := apiTracks(recent)
		if err := s.decorateTracks(ctx, items); err != nil {
			return nil, err
		}
		resp.Rails = append(resp.Rails, rail{Kind: "recentlyPlayed", Title: "Recently Played", Tracks: &items})
	}

	if added, err := s.st.RecentlyAddedAlbums(ctx, 20); err != nil {
		return nil, err
	} else if len(added) > 0 {
		items := make([]api.Album, 0, len(added))
		for _, a := range added {
			items = append(items, apiAlbum(a))
		}
		resp.Rails = append(resp.Rails, rail{Kind: "recentlyAdded", Title: "Recently Added", Albums: &items})
	}
	if resp.Rails == nil {
		resp.Rails = []rail{}
	}
	return resp, nil
}

func (s *Server) ListMixes(ctx context.Context, _ api.ListMixesRequestObject) (api.ListMixesResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	mixes, err := s.st.AvailableMixes(ctx, prf)
	if err != nil {
		return nil, err
	}
	out := make(api.ListMixes200JSONResponse, 0, len(mixes))
	for _, m := range mixes {
		out = append(out, apiMix(m))
	}
	return out, nil
}

func (s *Server) ListMixTracks(ctx context.Context, req api.ListMixTracksRequestObject) (api.ListMixTracksResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.MixTracks(ctx, prf, req.MixId)
	if errors.Is(err, store.ErrNotFound) {
		return api.ListMixTracks404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	tracks := apiTracks(rows)
	if err := s.decorateTracks(ctx, tracks); err != nil {
		return nil, err
	}
	return api.ListMixTracks200JSONResponse(tracks), nil
}

func (s *Server) GetRadioNext(ctx context.Context, req api.GetRadioNextRequestObject) (api.GetRadioNextResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	count := 10
	var seedArtists, exclude []string
	if req.Body != nil {
		if req.Body.Count != nil && *req.Body.Count > 0 && *req.Body.Count <= 50 {
			count = *req.Body.Count
		}
		if req.Body.SeedArtistIds != nil {
			seedArtists = *req.Body.SeedArtistIds
		}
		if req.Body.SeedTrackIds != nil {
			for _, trackID := range *req.Body.SeedTrackIds {
				if tr, found, err := s.st.GetTrack(ctx, trackID); err != nil {
					return nil, err
				} else if found {
					seedArtists = append(seedArtists, tr.ArtistID)
				}
			}
		}
		if req.Body.ExcludeTrackIds != nil {
			exclude = *req.Body.ExcludeTrackIds
		}
	}
	rows, err := s.st.RadioNext(ctx, prf, seedArtists, exclude, count)
	if err != nil {
		return nil, err
	}
	tracks := apiTracks(rows)
	if err := s.decorateTracks(ctx, tracks); err != nil {
		return nil, err
	}
	return api.GetRadioNext200JSONResponse(tracks), nil
}

// GetMyDiscover is honest about the missing provider: empty list, exactly as
// the contract specifies for capabilities.discovery=false.
func (s *Server) GetMyDiscover(ctx context.Context, _ api.GetMyDiscoverRequestObject) (api.GetMyDiscoverResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	conn, ok, err := s.st.GetLastfmConnection(ctx, prf)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.GetMyDiscover200JSONResponse{}, nil
	}
	client, ok, err := s.lastfmClient(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.GetMyDiscover200JSONResponse{}, nil
	}
	top, err := client.TopArtists(ctx, conn.Username, 8)
	if err != nil {
		return api.GetMyDiscover502ApplicationProblemPlusJSONResponse(problem(502, "Bad Gateway", "discovery_provider_unavailable")), nil
	}
	seen := map[string]bool{}
	out := api.GetMyDiscover200JSONResponse{}
	for _, seed := range top {
		similar, e := client.Similar(ctx, seed.Name, 5)
		if e != nil {
			return api.GetMyDiscover502ApplicationProblemPlusJSONResponse(problem(502, "Bad Gateway", "discovery_provider_unavailable")), nil
		}
		for _, a := range similar {
			key := strings.ToLower(a.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			id, owned, e := s.st.ResolveArtistName(ctx, a.Name)
			if e != nil {
				return nil, e
			}
			states, e := s.st.GetLoveStates(ctx, prf, []string{id})
			if e != nil {
				return nil, e
			}
			if states[id] == "not_for_me" {
				continue
			}
			reason := "Because you listen to " + seed.Name
			kind := api.DiscoverItemKindArtist
			out = append(out, api.DiscoverItem{Kind: kind, Name: a.Name, Owned: owned, Ref: &id, Reason: &reason})
			if len(out) >= 20 {
				return out, nil
			}
		}
	}
	return out, nil
}

func (s *Server) lastfmClient(ctx context.Context) (lastfmProvider, bool, error) {
	key, _, err := s.st.GetSetting(ctx, settingLastfmAPIKey)
	if err != nil {
		return nil, false, err
	}
	secret, _, err := s.st.GetSetting(ctx, settingLastfmSharedSecret)
	if err != nil {
		return nil, false, err
	}
	if key == "" || secret == "" {
		return nil, false, nil
	}
	return s.lastfmFactory(key, secret), true, nil
}

// ListSimilarArtists returns an empty set until a discovery integration
// (last.fm adapter arc) exists; unknown artists still 404.
func (s *Server) ListSimilarArtists(ctx context.Context, req api.ListSimilarArtistsRequestObject) (api.ListSimilarArtistsResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	_, found, err := s.st.GetArtist(ctx, req.ArtistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.ListSimilarArtists404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	client, ok, err := s.lastfmClient(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.ListSimilarArtists200JSONResponse{}, nil
	}
	artist, _, _ := s.st.GetArtist(ctx, req.ArtistId)
	items, err := client.Similar(ctx, artist.Name, 50)
	if err != nil {
		return api.ListSimilarArtists502ApplicationProblemPlusJSONResponse(problem(502, "Bad Gateway", "discovery_provider_unavailable")), nil
	}
	out := api.ListSimilarArtists200JSONResponse{}
	for _, item := range items {
		id, owned, e := s.st.ResolveArtistName(ctx, item.Name)
		if e != nil {
			return nil, e
		}
		match := float32(item.Match)
		out = append(out, api.SimilarArtist{Name: item.Name, ArtistId: id, Owned: owned, Match: &match})
	}
	return out, nil
}

// GetExternalArtist needs a discovery integration to say anything useful.
func (s *Server) GetExternalArtist(ctx context.Context, req api.GetExternalArtistRequestObject) (api.GetExternalArtistResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	client, ok, err := s.lastfmClient(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return api.GetExternalArtist404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	info, err := client.ArtistInfo(ctx, req.Name)
	if err != nil {
		if providerKind(err) == lastfm.ErrorNotFound {
			return api.GetExternalArtist404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
		}
		return api.GetExternalArtist502ApplicationProblemPlusJSONResponse(problem(502, "Bad Gateway", "discovery_provider_unavailable")), nil
	}
	if info.Name == "" {
		return api.GetExternalArtist404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	id, owned, err := s.st.ResolveArtistName(ctx, info.Name)
	if err != nil {
		return nil, err
	}
	bio := info.Bio
	out := api.GetExternalArtist200JSONResponse{Name: info.Name, ArtistId: id, Owned: owned, Bio: &bio}
	similar, err := client.Similar(ctx, info.Name, 20)
	if err != nil {
		return api.GetExternalArtist502ApplicationProblemPlusJSONResponse(problem(502, "Bad Gateway", "discovery_provider_unavailable")), nil
	}
	similarOut := []api.SimilarArtist{}
	for _, a := range similar {
		sid, sowned, e := s.st.ResolveArtistName(ctx, a.Name)
		if e != nil {
			return nil, e
		}
		m := float32(a.Match)
		similarOut = append(similarOut, api.SimilarArtist{Name: a.Name, ArtistId: sid, Owned: sowned, Match: &m})
	}
	out.Similar = &similarOut
	discography := []struct {
		AlbumId *string                            `json:"albumId,omitempty"`
		State   api.ExternalArtistDiscographyState `json:"state"`
		Title   string                             `json:"title"`
		Year    *int                               `json:"year,omitempty"`
	}{}
	out.Discography = &discography
	return out, nil
}

// GetAcquisitionActivity is empty until an acquirer adapter exists.
func (s *Server) GetAcquisitionActivity(ctx context.Context, _ api.GetAcquisitionActivityRequestObject) (api.GetAcquisitionActivityResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	var resp api.GetAcquisitionActivity200JSONResponse
	resp.Queue = []struct {
		ArtistName string  `json:"artistName"`
		Progress   float32 `json:"progress"`
		Title      string  `json:"title"`
	}{}
	resp.Wanted = []struct {
		ArtistName string `json:"artistName"`
		Title      string `json:"title"`
	}{}
	return resp, nil
}
