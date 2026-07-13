package server

import (
	"context"
	"errors"
	"strings"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// ── converters ─────────────────────────────────────────────────

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func apiArtist(a store.ArtistRow) api.Artist {
	genres := a.Genres
	aliases := a.Aliases
	return api.Artist{
		ArtistId:      a.ArtistID,
		Name:          a.Name,
		ArtId:         optStr(a.ArtID),
		AlbumCount:    optInt(a.AlbumCount),
		Genres:        &genres,
		Aliases:       &aliases,
		MusicBrainzId: optStr(a.MusicBrainzID),
	}
}

func apiAlbum(a store.AlbumRow) api.Album {
	credits := apiCredits(a.ArtistCredits)
	return api.Album{
		AlbumId:                   a.AlbumID,
		Title:                     a.Title,
		PrimaryArtist:             api.ArtistReference{ArtistId: a.PrimaryArtist.ArtistID, Name: a.PrimaryArtist.Name},
		ArtistCredits:             credits,
		MusicBrainzReleaseId:      optStr(a.MusicBrainzReleaseID),
		MusicBrainzReleaseGroupId: optStr(a.MusicBrainzReleaseGroupID),
		Year:                      optInt(a.Year),
		ArtId:                     optStr(a.ArtID),
		TrackCount:                optInt(a.TrackCount),
		UpdatedAt:                 &a.UpdatedAt,
	}
}

func apiTrack(t store.TrackRow) api.Track {
	tr := api.Track{
		TrackId:                t.TrackID,
		Title:                  t.Title,
		Index:                  optInt(t.Index),
		DiscNumber:             optInt(t.Disc),
		PrimaryArtist:          api.ArtistReference{ArtistId: t.PrimaryArtist.ArtistID, Name: t.PrimaryArtist.Name},
		ArtistCredits:          apiCredits(t.ArtistCredits),
		MusicBrainzRecordingId: optStr(t.MusicBrainzRecordingID),
		AlbumId:                t.AlbumID,
		AlbumTitle:             t.AlbumTitle,
		DurationMs:             t.DurationMs,
		ArtId:                  optStr(t.ArtID),
		Media: api.MediaInfo{
			Container:   t.Container,
			AudioCodec:  t.Codec,
			BitrateKbps: optInt(t.BitrateKbps),
		},
	}
	if t.SizeBytes > 0 {
		tr.Media.SizeBytes = &t.SizeBytes
	}
	if t.Genre != "" {
		g := []string{t.Genre}
		tr.Genres = &g
	}
	return tr
}

func apiCredits(rows []store.ArtistCreditRow) []api.ArtistCredit {
	out := make([]api.ArtistCredit, 0, len(rows))
	for _, c := range rows {
		out = append(out, api.ArtistCredit{ArtistId: c.ArtistID, Name: c.Name, JoinPhrase: c.JoinPhrase})
	}
	return out
}

func renderCredits(credits []api.ArtistCredit) string {
	var b strings.Builder
	for _, c := range credits {
		b.WriteString(c.Name)
		b.WriteString(c.JoinPhrase)
	}
	return b.String()
}

func renderStoreCredits(credits []store.ArtistCreditRow) string {
	var b strings.Builder
	for _, c := range credits {
		b.WriteString(c.Name)
		b.WriteString(c.JoinPhrase)
	}
	return b.String()
}

func apiTracks(rows []store.TrackRow) []api.Track {
	out := make([]api.Track, 0, len(rows))
	for _, t := range rows {
		out = append(out, apiTrack(t))
	}
	return out
}

func pageParams(cursor *string, limit *int) (string, int) {
	c := ""
	if cursor != nil {
		c = *cursor
	}
	l := 200
	if limit != nil && *limit > 0 && *limit <= 1000 {
		l = *limit
	}
	return c, l
}

// ── library browse ─────────────────────────────────────────────

func (s *Server) GetLibrary(ctx context.Context, _ api.GetLibraryRequestObject) (api.GetLibraryResponseObject, error) {
	sum, err := s.st.GetLibrarySummary(ctx)
	if err != nil {
		return nil, err
	}
	libraryID, err := s.st.LibraryID(ctx)
	if err != nil {
		return nil, err
	}
	resp := api.GetLibrary200JSONResponse{
		LibraryId: libraryID,
		Title:     "Library",
		UpdatedAt: sum.UpdatedAt,
		Version:   sum.Version,
	}
	resp.Counts.Artists = &sum.Artists
	resp.Counts.Albums = &sum.Albums
	resp.Counts.Tracks = &sum.Tracks
	return resp, nil
}

// ListChanges returns the catalog delta since a client's known version. Missing
// rows become removed ids; changed rows are hydrated to the same shapes the list
// endpoints return. Bootstrap is via the paged lists; this is the incremental path.
func (s *Server) ListChanges(ctx context.Context, req api.ListChangesRequestObject) (api.ListChangesResponseObject, error) {
	var since int64
	if req.Params.Since != nil {
		since = *req.Params.Since
	}
	cur := ""
	if req.Params.Cursor != nil {
		cur = *req.Params.Cursor
	}
	limit := 200
	if l := req.Params.Limit; l != nil && int(*l) > 0 && int(*l) <= 1000 {
		limit = int(*l)
	}
	changes, next, err := s.st.ChangesSince(ctx, since, cur, limit)
	if err != nil {
		return nil, err
	}
	sum, err := s.st.GetLibrarySummary(ctx)
	if err != nil {
		return nil, err
	}
	resp := api.ListChanges200JSONResponse{
		Version:          sum.Version,
		Artists:          []api.Artist{},
		Albums:           []api.Album{},
		Tracks:           []api.Track{},
		RemovedArtistIds: []string{},
		RemovedAlbumIds:  []string{},
		RemovedTrackIds:  []string{},
	}
	if next != "" {
		resp.NextCursor = &next
	}
	for _, ch := range changes {
		if ch.Missing {
			switch ch.Kind {
			case "artist":
				resp.RemovedArtistIds = append(resp.RemovedArtistIds, ch.ID)
			case "album":
				resp.RemovedAlbumIds = append(resp.RemovedAlbumIds, ch.ID)
			case "track":
				resp.RemovedTrackIds = append(resp.RemovedTrackIds, ch.ID)
			}
			continue
		}
		switch ch.Kind {
		case "artist":
			if a, ok, err := s.st.GetArtist(ctx, ch.ID); err != nil {
				return nil, err
			} else if ok {
				resp.Artists = append(resp.Artists, apiArtist(a))
			}
		case "album":
			if a, ok, err := s.st.GetAlbum(ctx, ch.ID); err != nil {
				return nil, err
			} else if ok {
				resp.Albums = append(resp.Albums, apiAlbum(a))
			}
		case "track":
			if tr, ok, err := s.st.GetTrack(ctx, ch.ID); err != nil {
				return nil, err
			} else if ok {
				resp.Tracks = append(resp.Tracks, apiTrack(tr))
			}
		}
	}
	return resp, nil
}

func (s *Server) ListArtists(ctx context.Context, req api.ListArtistsRequestObject) (api.ListArtistsResponseObject, error) {
	cur, limit := pageParams(req.Params.Cursor, req.Params.Limit)
	sort := "title"
	if req.Params.Sort != nil {
		sort = string(*req.Params.Sort)
	}
	rows, next, err := s.st.ListArtists(ctx, sort, cur, limit)
	if err != nil {
		return nil, badRequestIfCursor(err)
	}
	resp := api.ListArtists200JSONResponse{Items: make([]api.Artist, 0, len(rows))}
	for _, a := range rows {
		resp.Items = append(resp.Items, apiArtist(a))
	}
	resp.NextCursor = optStr(next)
	return resp, nil
}

func (s *Server) GetArtist(ctx context.Context, req api.GetArtistRequestObject) (api.GetArtistResponseObject, error) {
	a, found, err := s.st.GetArtist(ctx, req.ArtistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.GetArtist404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	genres := a.Genres
	resp := api.GetArtist200JSONResponse{
		ArtistId:      a.ArtistID,
		Name:          a.Name,
		ArtId:         optStr(a.ArtID),
		AlbumCount:    optInt(a.AlbumCount),
		TrackCount:    optInt(a.TrackCount),
		Genres:        &genres,
		Aliases:       &a.Aliases,
		MusicBrainzId: optStr(a.MusicBrainzID),
	}
	ls, err := s.loveStateFor(ctx, a.ArtistID)
	if err != nil {
		return nil, err
	}
	resp.LoveState = ls
	return resp, nil
}

func (s *Server) ListArtistAlbums(ctx context.Context, req api.ListArtistAlbumsRequestObject) (api.ListArtistAlbumsResponseObject, error) {
	if _, found, err := s.st.GetArtist(ctx, req.ArtistId); err != nil {
		return nil, err
	} else if !found {
		return api.ListArtistAlbums404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	rows, err := s.st.ListArtistAlbums(ctx, req.ArtistId)
	if err != nil {
		return nil, err
	}
	out := make(api.ListArtistAlbums200JSONResponse, 0, len(rows))
	for _, a := range rows {
		out = append(out, apiAlbum(a))
	}
	return out, nil
}

func (s *Server) ListArtistTracks(ctx context.Context, req api.ListArtistTracksRequestObject) (api.ListArtistTracksResponseObject, error) {
	if _, found, err := s.st.GetArtist(ctx, req.ArtistId); err != nil {
		return nil, err
	} else if !found {
		return api.ListArtistTracks404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	rows, err := s.st.ListArtistTracks(ctx, req.ArtistId)
	if err != nil {
		return nil, err
	}
	tracks := apiTracks(rows)
	if err := s.decorateTracks(ctx, tracks); err != nil {
		return nil, err
	}
	return api.ListArtistTracks200JSONResponse(tracks), nil
}

func (s *Server) ListAlbums(ctx context.Context, req api.ListAlbumsRequestObject) (api.ListAlbumsResponseObject, error) {
	cur, limit := pageParams(req.Params.Cursor, req.Params.Limit)
	sort := "title"
	if req.Params.Sort != nil {
		sort = string(*req.Params.Sort)
	}
	rows, next, err := s.st.ListAlbums(ctx, sort, cur, limit)
	if err != nil {
		return nil, badRequestIfCursor(err)
	}
	resp := api.ListAlbums200JSONResponse{Items: make([]api.Album, 0, len(rows))}
	for _, a := range rows {
		resp.Items = append(resp.Items, apiAlbum(a))
	}
	resp.NextCursor = optStr(next)
	return resp, nil
}

func (s *Server) GetAlbum(ctx context.Context, req api.GetAlbumRequestObject) (api.GetAlbumResponseObject, error) {
	a, found, err := s.st.GetAlbum(ctx, req.AlbumId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.GetAlbum404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	out := apiAlbum(a)
	ls, err := s.loveStateFor(ctx, a.AlbumID)
	if err != nil {
		return nil, err
	}
	out.LoveState = ls
	return api.GetAlbum200JSONResponse(out), nil
}

func (s *Server) ListAlbumTracks(ctx context.Context, req api.ListAlbumTracksRequestObject) (api.ListAlbumTracksResponseObject, error) {
	if _, found, err := s.st.GetAlbum(ctx, req.AlbumId); err != nil {
		return nil, err
	} else if !found {
		return api.ListAlbumTracks404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	rows, err := s.st.ListAlbumTracks(ctx, req.AlbumId)
	if err != nil {
		return nil, err
	}
	tracks := apiTracks(rows)
	if err := s.decorateTracks(ctx, tracks); err != nil {
		return nil, err
	}
	return api.ListAlbumTracks200JSONResponse(tracks), nil
}

func (s *Server) ListTracks(ctx context.Context, req api.ListTracksRequestObject) (api.ListTracksResponseObject, error) {
	cur, limit := pageParams(req.Params.Cursor, req.Params.Limit)
	sort := "title"
	if req.Params.Sort != nil {
		sort = string(*req.Params.Sort)
	}
	rows, next, err := s.st.ListTracks(ctx, sort, cur, limit)
	if err != nil {
		return nil, badRequestIfCursor(err)
	}
	resp := api.ListTracks200JSONResponse{Items: apiTracks(rows)}
	if err := s.decorateTracks(ctx, resp.Items); err != nil {
		return nil, err
	}
	resp.NextCursor = optStr(next)
	return resp, nil
}

func (s *Server) GetTrack(ctx context.Context, req api.GetTrackRequestObject) (api.GetTrackResponseObject, error) {
	tr, found, err := s.st.GetTrack(ctx, req.TrackId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.GetTrack404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	one := []api.Track{apiTrack(tr)}
	if err := s.decorateTracks(ctx, one); err != nil {
		return nil, err
	}
	return api.GetTrack200JSONResponse(one[0]), nil
}

func (s *Server) ListGenres(ctx context.Context, _ api.ListGenresRequestObject) (api.ListGenresResponseObject, error) {
	genres, err := s.st.ListGenres(ctx)
	if err != nil {
		return nil, err
	}
	out := make(api.ListGenres200JSONResponse, 0, len(genres))
	for _, g := range genres {
		out = append(out, api.Genre{Name: g.Name, AlbumCount: g.AlbumCount, ArtId: optStr(g.ArtID)})
	}
	return out, nil
}

func (s *Server) ListGenreTracks(ctx context.Context, req api.ListGenreTracksRequestObject) (api.ListGenreTracksResponseObject, error) {
	rows, err := s.st.ListGenreTracks(ctx, req.Genre)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return api.ListGenreTracks404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	tracks := apiTracks(rows)
	if err := s.decorateTracks(ctx, tracks); err != nil {
		return nil, err
	}
	return api.ListGenreTracks200JSONResponse(tracks), nil
}

func (s *Server) Search(ctx context.Context, req api.SearchRequestObject) (api.SearchResponseObject, error) {
	if len(req.Params.Q) < 2 {
		return nil, badRequest("query_too_short")
	}
	want := map[string]bool{"artists": true, "albums": true, "tracks": true, "external": true}
	if req.Params.Types != nil && *req.Params.Types != "" {
		want = map[string]bool{}
		for _, t := range strings.Split(*req.Params.Types, ",") {
			want[strings.TrimSpace(t)] = true
		}
	}
	res, err := s.st.SearchLibrary(ctx, req.Params.Q)
	if err != nil {
		return nil, err
	}
	out := api.Search200JSONResponse{
		Artists:  []api.Artist{},
		Albums:   []api.Album{},
		Tracks:   []api.Track{},
		External: []api.SimilarArtist{}, // populated when a discovery integration exists (Arc E)
	}
	if want["artists"] {
		for _, a := range res.Artists {
			out.Artists = append(out.Artists, apiArtist(a))
		}
	}
	if want["albums"] {
		for _, a := range res.Albums {
			out.Albums = append(out.Albums, apiAlbum(a))
		}
	}
	if want["tracks"] {
		out.Tracks = apiTracks(res.Tracks)
		if err := s.decorateTracks(ctx, out.Tracks); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// badRequestIfCursor maps cursor decode failures to a 400 problem.
func badRequestIfCursor(err error) error {
	if err != nil && strings.Contains(err.Error(), "cursor") {
		return badRequest("invalid_cursor")
	}
	return err
}

// ── admin: filesystem source ───────────────────────────────────

func (s *Server) AdminGetFilesystemSource(ctx context.Context, _ api.AdminGetFilesystemSourceRequestObject) (api.AdminGetFilesystemSourceResponseObject, error) {
	st := s.lib.Status(ctx)
	resp := api.AdminGetFilesystemSource200JSONResponse{
		Configured: st.Configured,
		Scanning:   st.Scanning,
		Path:       optStr(st.Path),
		LastScanAt: st.LastScanAt,
	}
	resp.LastScanError = optStr(st.LastScanError)
	return resp, nil
}

func (s *Server) AdminSetFilesystemSource(ctx context.Context, req api.AdminSetFilesystemSourceRequestObject) (api.AdminSetFilesystemSourceResponseObject, error) {
	if err := s.lib.Configure(ctx, req.Body.Path); err != nil {
		return nil, badRequest("path_not_a_directory")
	}
	return api.AdminSetFilesystemSource204Response{}, nil
}

func (s *Server) AdminDeleteFilesystemSource(ctx context.Context, _ api.AdminDeleteFilesystemSourceRequestObject) (api.AdminDeleteFilesystemSourceResponseObject, error) {
	if err := s.lib.Unlink(ctx); err != nil {
		return nil, err
	}
	return api.AdminDeleteFilesystemSource204Response{}, nil
}

func (s *Server) AdminScanFilesystemSource(ctx context.Context, _ api.AdminScanFilesystemSourceRequestObject) (api.AdminScanFilesystemSourceResponseObject, error) {
	err := s.lib.Rescan(ctx)
	if errors.Is(err, library.ErrNotConfigured) {
		return api.AdminScanFilesystemSource409ApplicationProblemPlusJSONResponse(
			problem(409, "Conflict", "source_not_configured")), nil
	}
	if err != nil {
		return nil, err
	}
	return api.AdminScanFilesystemSource202Response{}, nil
}
