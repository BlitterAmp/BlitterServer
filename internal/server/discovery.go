package server

import (
	"context"
	"errors"

	"github.com/BlitterAmp/BlitterServer/internal/api"
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
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	return api.GetMyDiscover200JSONResponse{}, nil
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
	return api.ListSimilarArtists200JSONResponse{}, nil
}

// GetExternalArtist needs a discovery integration to say anything useful.
func (s *Server) GetExternalArtist(ctx context.Context, _ api.GetExternalArtistRequestObject) (api.GetExternalArtistResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	return api.GetExternalArtist404ApplicationProblemPlusJSONResponse{
		NotFoundApplicationProblemPlusJSONResponse: api.NotFoundApplicationProblemPlusJSONResponse(
			problem(404, "Not Found", "discovery_not_configured"))}, nil
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
