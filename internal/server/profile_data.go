package server

import (
	"context"
	"errors"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/lastfm"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

const lovedTracksTitle = "Loved Tracks"

// ── converters ─────────────────────────────────────────────────

func apiPlaylist(p store.PlaylistRow) api.Playlist {
	out := api.Playlist{
		PlaylistId: p.PlaylistID,
		Title:      p.Title,
		TrackCount: p.TrackCount,
		Origin:     api.PlaylistOrigin(p.Origin),
		Visibility: api.PlaylistVisibility(p.Visibility),
	}
	out.DurationMs = optInt(p.DurationMs)
	out.ArtId = optStr(p.ArtID)
	out.OwnerProfileId = optStr(p.OwnerProfileID)
	out.OwnerName = optStr(p.OwnerName)
	out.UpdatedAt = &p.UpdatedAt
	return out
}

func apiLoveRecord(l store.LoveRow) api.LoveRecord {
	return api.LoveRecord{
		LoveId:     l.LoveID,
		Ref:        l.Ref,
		Kind:       api.LoveRecordKind(l.Kind),
		State:      api.LoveState(l.State),
		Name:       l.Name,
		ArtistName: optStr(l.ArtistName),
		Owned:      l.Owned,
		UpdatedAt:  l.UpdatedAt,
	}
}

func apiRecommendation(r store.RecommendationRow) api.Recommendation {
	return api.Recommendation{
		RecommendationId: r.RecommendationID,
		FromProfileId:    r.FromProfileID,
		FromProfileName:  r.FromProfileName,
		ToProfileId:      r.ToProfileID,
		Ref:              r.Ref,
		Kind:             api.RecommendationKind(r.Kind),
		Name:             r.Name,
		ArtistName:       optStr(r.ArtistName),
		Note:             optStr(r.Note),
		Seen:             r.Seen,
		CreatedAt:        r.CreatedAt,
	}
}

// ── decoration: per-profile loveState + ratings on entities ───

// decorateTracks stamps the calling profile's loveState/userRating10 onto
// track payloads. Callers without a profile identity get bare entities.
func (s *Server) decorateTracks(ctx context.Context, tracks []api.Track) error {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || id.ProfileID == "" || len(tracks) == 0 {
		return nil
	}
	refs := make([]string, len(tracks))
	for i, t := range tracks {
		refs[i] = t.TrackId
	}
	states, err := s.st.GetLoveStates(ctx, id.ProfileID, refs)
	if err != nil {
		return err
	}
	ratings, err := s.st.GetRatings(ctx, id.ProfileID, refs)
	if err != nil {
		return err
	}
	for i := range tracks {
		if st := states[tracks[i].TrackId]; st != "" {
			ls := api.LoveState(st)
			tracks[i].LoveState = &ls
		}
		if r := ratings[tracks[i].TrackId]; r > 0 {
			rr := r
			tracks[i].UserRating10 = &rr
		}
	}
	return nil
}

func (s *Server) loveStateFor(ctx context.Context, ref string) (*api.LoveState, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || id.ProfileID == "" {
		return nil, nil
	}
	states, err := s.st.GetLoveStates(ctx, id.ProfileID, []string{ref})
	if err != nil {
		return nil, err
	}
	if st := states[ref]; st != "" {
		ls := api.LoveState(st)
		return &ls, nil
	}
	return nil, nil
}

// ── playlists ──────────────────────────────────────────────────

// canSee: private playlists exist only for their owner.
func canSeePlaylist(p store.PlaylistRow, profileID string) bool {
	return p.OwnerProfileID == profileID || p.Visibility == "shared" ||
		p.Visibility == "collaborative" || p.Origin == "source"
}

func (s *Server) publishPlaylistChanged(ctx context.Context, p store.PlaylistRow) {
	scope := p.OwnerProfileID
	if p.Visibility == "shared" || p.Visibility == "collaborative" {
		scope = "" // household-visible playlists notify everyone
	}
	if err := s.bus.Publish(ctx, "playlist.changed", scope, map[string]any{"playlistId": p.PlaylistID}); err != nil {
		logging.From(ctx).Warn("publish playlist.changed", "err", err)
	}
}

func (s *Server) ListPlaylists(ctx context.Context, _ api.ListPlaylistsRequestObject) (api.ListPlaylistsResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.ListPlaylists(ctx, prf)
	if err != nil {
		return nil, err
	}
	out := make(api.ListPlaylists200JSONResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, apiPlaylist(p))
	}
	return out, nil
}

func (s *Server) CreatePlaylist(ctx context.Context, req api.CreatePlaylistRequestObject) (api.CreatePlaylistResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body.Title == "" {
		return nil, badRequest("title_required")
	}
	visibility := "private"
	if req.Body.Visibility != nil {
		visibility = string(*req.Body.Visibility)
	}
	var trackIDs []string
	if req.Body.TrackIds != nil {
		trackIDs = *req.Body.TrackIds
	}
	p, err := s.st.CreatePlaylist(ctx, prf, req.Body.Title, visibility, trackIDs)
	if errors.Is(err, store.ErrNotFound) {
		return nil, badRequest("unknown_track")
	}
	if err != nil {
		return nil, err
	}
	s.publishPlaylistChanged(ctx, p)
	return api.CreatePlaylist201JSONResponse(apiPlaylist(p)), nil
}

// loadPlaylistFor fetches a playlist enforcing visibility; found=false means
// "404 to this caller".
func (s *Server) loadPlaylistFor(ctx context.Context, playlistID string) (store.PlaylistRow, string, bool, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return store.PlaylistRow{}, "", false, err
	}
	p, found, err := s.st.GetPlaylist(ctx, playlistID)
	if err != nil {
		return store.PlaylistRow{}, prf, false, err
	}
	if !found || !canSeePlaylist(p, prf) {
		return store.PlaylistRow{}, prf, false, nil
	}
	return p, prf, true, nil
}

func (s *Server) GetPlaylist(ctx context.Context, req api.GetPlaylistRequestObject) (api.GetPlaylistResponseObject, error) {
	p, _, found, err := s.loadPlaylistFor(ctx, req.PlaylistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.GetPlaylist404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	return api.GetPlaylist200JSONResponse(apiPlaylist(p)), nil
}

func forbidden(code string) api.Problem { return problem(403, "Forbidden", code) }

func (s *Server) UpdatePlaylist(ctx context.Context, req api.UpdatePlaylistRequestObject) (api.UpdatePlaylistResponseObject, error) {
	p, prf, found, err := s.loadPlaylistFor(ctx, req.PlaylistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.UpdatePlaylist404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if p.OwnerProfileID != prf {
		return api.UpdatePlaylist403ApplicationProblemPlusJSONResponse{
			ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(forbidden("owner_only"))}, nil
	}
	var visibility *string
	if req.Body.Visibility != nil {
		v := string(*req.Body.Visibility)
		visibility = &v
	}
	updated, err := s.st.UpdatePlaylist(ctx, req.PlaylistId, req.Body.Title, visibility)
	if err != nil {
		return nil, err
	}
	s.publishPlaylistChanged(ctx, updated)
	return api.UpdatePlaylist200JSONResponse(apiPlaylist(updated)), nil
}

func (s *Server) DeletePlaylist(ctx context.Context, req api.DeletePlaylistRequestObject) (api.DeletePlaylistResponseObject, error) {
	p, prf, found, err := s.loadPlaylistFor(ctx, req.PlaylistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.DeletePlaylist404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if p.OwnerProfileID != prf {
		return api.DeletePlaylist403ApplicationProblemPlusJSONResponse{
			ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(forbidden("owner_only"))}, nil
	}
	if err := s.st.DeletePlaylist(ctx, req.PlaylistId); err != nil {
		return nil, err
	}
	s.publishPlaylistChanged(ctx, p)
	return api.DeletePlaylist204Response{}, nil
}

func (s *Server) ListPlaylistTracks(ctx context.Context, req api.ListPlaylistTracksRequestObject) (api.ListPlaylistTracksResponseObject, error) {
	_, _, found, err := s.loadPlaylistFor(ctx, req.PlaylistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.ListPlaylistTracks404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	cur, limit := pageParams(req.Params.Cursor, req.Params.Limit)
	items, next, err := s.st.ListPlaylistItems(ctx, req.PlaylistId, cur, limit)
	if err != nil {
		return nil, badRequestIfCursor(err)
	}
	tracks := make([]api.Track, len(items))
	for i, item := range items {
		tracks[i] = apiTrack(item.Track)
	}
	if err := s.decorateTracks(ctx, tracks); err != nil {
		return nil, err
	}
	resp := api.ListPlaylistTracks200JSONResponse{Items: make([]api.PlaylistTrack, len(items))}
	for i, item := range items {
		t := tracks[i]
		resp.Items[i] = api.PlaylistTrack{
			ItemId: item.ItemID, TrackId: t.TrackId, Title: t.Title,
			Index: t.Index, DiscNumber: t.DiscNumber,
			PrimaryArtist: t.PrimaryArtist, ArtistCredits: t.ArtistCredits,
			AlbumId: t.AlbumId, AlbumTitle: t.AlbumTitle,
			DurationMs: t.DurationMs, ArtId: t.ArtId, Genres: t.Genres,
			Media: t.Media, LoveState: t.LoveState, UserRating10: t.UserRating10,
			MusicBrainzRecordingId: t.MusicBrainzRecordingId,
		}
	}
	resp.NextCursor = optStr(next)
	return resp, nil
}

func (s *Server) AppendPlaylistTracks(ctx context.Context, req api.AppendPlaylistTracksRequestObject) (api.AppendPlaylistTracksResponseObject, error) {
	p, prf, found, err := s.loadPlaylistFor(ctx, req.PlaylistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.AppendPlaylistTracks404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if p.OwnerProfileID != prf && p.Visibility != "collaborative" {
		return api.AppendPlaylistTracks403ApplicationProblemPlusJSONResponse{
			ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(forbidden("not_collaborative"))}, nil
	}
	if err := s.st.AppendPlaylistTracks(ctx, req.PlaylistId, req.Body.TrackIds); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, badRequest("unknown_track")
		}
		return nil, err
	}
	s.publishPlaylistChanged(ctx, p)
	return api.AppendPlaylistTracks204Response{}, nil
}

func (s *Server) RemovePlaylistTrack(ctx context.Context, req api.RemovePlaylistTrackRequestObject) (api.RemovePlaylistTrackResponseObject, error) {
	p, prf, found, err := s.loadPlaylistFor(ctx, req.PlaylistId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.RemovePlaylistTrack404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if p.OwnerProfileID != prf && p.Visibility != "collaborative" {
		return api.RemovePlaylistTrack403ApplicationProblemPlusJSONResponse{
			ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(forbidden("not_collaborative"))}, nil
	}
	err = s.st.RemovePlaylistItem(ctx, req.PlaylistId, req.ItemId)
	if errors.Is(err, store.ErrNotFound) {
		return api.RemovePlaylistTrack404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	s.publishPlaylistChanged(ctx, p)
	return api.RemovePlaylistTrack204Response{}, nil
}

// ── loves ──────────────────────────────────────────────────────

// syncLovedTracksPlaylist maintains the profile's auto playlist as a side
// effect of track loves (spec-mandated).
func (s *Server) syncLovedTracksPlaylist(ctx context.Context, prf, trackID, state string) error {
	lists, err := s.st.ListPlaylists(ctx, prf)
	if err != nil {
		return err
	}
	var loved *store.PlaylistRow
	for i := range lists {
		if lists[i].OwnerProfileID == prf && lists[i].Title == lovedTracksTitle {
			loved = &lists[i]
			break
		}
	}
	if state == "loved" {
		if loved == nil {
			p, err := s.st.CreatePlaylist(ctx, prf, lovedTracksTitle, "private", nil)
			if err != nil {
				return err
			}
			loved = &p
		}
		items, _, err := s.st.ListPlaylistItems(ctx, loved.PlaylistID, "", 10000)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Track.TrackID == trackID {
				return nil // already there
			}
		}
		if err := s.st.AppendPlaylistTracks(ctx, loved.PlaylistID, []string{trackID}); err != nil {
			return err
		}
		s.publishPlaylistChanged(ctx, *loved)
		return nil
	}
	// Any non-loved state removes it from the auto playlist.
	if loved == nil {
		return nil
	}
	items, _, err := s.st.ListPlaylistItems(ctx, loved.PlaylistID, "", 10000)
	if err != nil {
		return err
	}
	changed := false
	for _, item := range items {
		if item.Track.TrackID == trackID {
			if err := s.st.RemovePlaylistItem(ctx, loved.PlaylistID, item.ItemID); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		s.publishPlaylistChanged(ctx, *loved)
	}
	return nil
}

func (s *Server) SetLove(ctx context.Context, req api.SetLoveRequestObject) (api.SetLoveResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := s.st.SetLove(ctx, prf, req.Ref, string(req.Body.State))
	if errors.Is(err, store.ErrNotFound) {
		return api.SetLove404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.Kind == "track" {
		if err := s.syncLovedTracksPlaylist(ctx, prf, req.Ref, rec.State); err != nil {
			return nil, err
		}
		// Local love is authoritative. Last.fm failure is deliberately best-effort
		// and never rolls back the user's local action.
		if conn, ok, e := s.st.GetLastfmConnection(ctx, prf); e == nil && ok {
			if tr, found, e := s.st.GetTrack(ctx, req.Ref); e == nil && found {
				client, configured, _ := s.lastfmClient(ctx)
				if configured {
					if e := client.Love(ctx, conn.SessionKey, lastfm.Track{Artist: renderStoreCredits(tr.ArtistCredits), Title: tr.Title, Album: tr.AlbumTitle}, rec.State == "loved"); e != nil {
						logging.From(ctx).Warn("last.fm love relay failed")
						if providerKind(e) == lastfm.ErrorInvalidSession {
							_, _ = s.st.DeleteLastfmData(ctx, prf)
						}
					}
				}
			}
		}
	}
	out := apiLoveRecord(rec)
	if err := s.bus.Publish(ctx, "love.updated", prf, out); err != nil {
		logging.From(ctx).Warn("publish love.updated", "err", err)
	}
	return api.SetLove200JSONResponse(out), nil
}

func (s *Server) ListLoves(ctx context.Context, req api.ListLovesRequestObject) (api.ListLovesResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	cur, limit := pageParams(req.Params.Cursor, req.Params.Limit)
	kind, state := "", ""
	if req.Params.Kind != nil {
		kind = string(*req.Params.Kind)
	}
	if req.Params.State != nil {
		state = string(*req.Params.State)
	}
	rows, next, err := s.st.ListLoves(ctx, prf, kind, state, cur, limit)
	if err != nil {
		return nil, badRequestIfCursor(err)
	}
	resp := api.ListLoves200JSONResponse{Items: make([]api.LoveRecord, 0, len(rows))}
	for _, l := range rows {
		resp.Items = append(resp.Items, apiLoveRecord(l))
	}
	resp.NextCursor = optStr(next)
	return resp, nil
}

// ── ratings ────────────────────────────────────────────────────

func (s *Server) SetRating(ctx context.Context, req api.SetRatingRequestObject) (api.SetRatingResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	// PUT-replace semantics: absent and null both clear.
	if req.Body.Rating10 == nil {
		if err := s.st.ClearRating(ctx, prf, req.Body.ItemId); err != nil {
			return nil, err
		}
		return api.SetRating204Response{}, nil
	}
	if *req.Body.Rating10 < 0 || *req.Body.Rating10 > 10 {
		return nil, badRequest("rating_out_of_range")
	}
	err = s.st.SetRating(ctx, prf, string(req.Body.ItemType), req.Body.ItemId, *req.Body.Rating10)
	if errors.Is(err, store.ErrNotFound) {
		return api.SetRating404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.SetRating204Response{}, nil
}

// ── playback + presence + taste ────────────────────────────────

func (s *Server) ReportPlaybackEvents(ctx context.Context, req api.ReportPlaybackEventsRequestObject) (api.ReportPlaybackEventsResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	if id.ProfileID == "" {
		return nil, &api.StatusError{Status: 403, Code: "profile_token_required", Title: "Forbidden"}
	}
	if req.Body == nil || len(req.Body.Events) > 500 {
		return nil, badRequest("playback_batch_too_large")
	}
	records := make([]store.PlaybackEventRecord, 0, len(req.Body.Events))
	for _, e := range req.Body.Events {
		rec := store.PlaybackEventRecord{
			EventID: e.EventId, Type: string(e.Type), TrackID: e.TrackId, At: e.At,
		}
		if e.PlaySessionId != nil {
			rec.PlaySessionID = *e.PlaySessionId
		}
		if e.PositionSec != nil {
			v := float64(*e.PositionSec)
			rec.PositionSec = &v
		}
		records = append(records, rec)
	}
	accepted, ids, err := s.st.IngestPlaybackEventsDetailed(ctx, id.ProfileID, id.DeviceID, records)
	if err != nil {
		return nil, err
	}
	_ = ids // accepted ids are persisted; the worker scans bounded durable work.
	if accepted > 0 && s.lastfmWorker != nil {
		s.lastfmWorker.notify()
	}
	if accepted > 0 {
		if p, found, err := s.st.GetProfileRecord(ctx, id.ProfileID); err == nil && found && p.ShareListening {
			for _, entry := range s.presenceEntries(ctx) {
				if entry.ProfileId == id.ProfileID {
					if err := s.bus.Publish(ctx, "presence.updated", "", entry); err != nil {
						logging.From(ctx).Warn("publish presence.updated", "err", err)
					}
				}
			}
		}
		if err := s.bus.Publish(ctx, "taste.updated", id.ProfileID, map[string]any{}); err != nil {
			logging.From(ctx).Warn("publish taste.updated", "err", err)
		}
	}
	return api.ReportPlaybackEvents202Response{}, nil
}

func providerKind(err error) lastfm.ErrorKind {
	if err == nil {
		return ""
	}
	var pe *lastfm.ProviderError
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return lastfm.ErrorPermanent
}

func (s *Server) presenceEntries(ctx context.Context) []api.PresenceEntry {
	rows, err := s.st.ListPresence(ctx)
	if err != nil {
		logging.From(ctx).Warn("presence", "err", err)
		return nil
	}
	out := make([]api.PresenceEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, api.PresenceEntry{
			ProfileId:   r.ProfileID,
			ProfileName: r.ProfileName,
			AvatarColor: optStr(r.AvatarColor),
			Track:       apiTrack(r.Track),
			At:          r.At,
		})
	}
	return out
}

func (s *Server) GetPresence(ctx context.Context, _ api.GetPresenceRequestObject) (api.GetPresenceResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	return api.GetPresence200JSONResponse(s.presenceEntries(ctx)), nil
}

func (s *Server) GetTasteSnapshot(ctx context.Context, _ api.GetTasteSnapshotRequestObject) (api.GetTasteSnapshotResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.st.TasteSnapshot(ctx, prf)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	resp := api.GetTasteSnapshot200JSONResponse{ComputedAt: &now}
	resp.Artists = make([]struct {
		Affinity float32 `json:"affinity"`
		Name     string  `json:"name"`
	}, len(snap.Artists))
	for i, a := range snap.Artists {
		resp.Artists[i].Name = a.Name
		resp.Artists[i].Affinity = float32(a.Affinity)
	}
	resp.Tracks = make([]struct {
		Key          string     `json:"key"`
		LastPlayedAt *time.Time `json:"lastPlayedAt,omitempty"`
		Plays        int        `json:"plays"`
		Skips        *int       `json:"skips,omitempty"`
	}, len(snap.Tracks))
	for i, t := range snap.Tracks {
		resp.Tracks[i].Key = t.Key
		resp.Tracks[i].Plays = t.Plays
		skips := t.Skips
		resp.Tracks[i].Skips = &skips
		resp.Tracks[i].LastPlayedAt = t.LastPlayedAt
	}
	return resp, nil
}

// ── recommendations ────────────────────────────────────────────

func (s *Server) CreateRecommendation(ctx context.Context, req api.CreateRecommendationRequestObject) (api.CreateRecommendationResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	note := ""
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	rec, err := s.st.CreateRecommendation(ctx, prf, req.Body.ToProfileId, req.Body.Ref, note)
	if errors.Is(err, store.ErrNotFound) {
		return api.CreateRecommendation404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	out := apiRecommendation(rec)
	if err := s.bus.Publish(ctx, "recommendation.received", rec.ToProfileID, out); err != nil {
		logging.From(ctx).Warn("publish recommendation.received", "err", err)
	}
	return api.CreateRecommendation201JSONResponse(out), nil
}

func (s *Server) ListRecommendations(ctx context.Context, req api.ListRecommendationsRequestObject) (api.ListRecommendationsResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	cur, limit := pageParams(req.Params.Cursor, req.Params.Limit)
	unseen := req.Params.Unseen != nil && *req.Params.Unseen
	rows, next, err := s.st.ListRecommendations(ctx, prf, unseen, cur, limit)
	if err != nil {
		return nil, badRequestIfCursor(err)
	}
	resp := api.ListRecommendations200JSONResponse{Items: make([]api.Recommendation, 0, len(rows))}
	for _, r := range rows {
		resp.Items = append(resp.Items, apiRecommendation(r))
	}
	resp.NextCursor = optStr(next)
	return resp, nil
}

func (s *Server) MarkRecommendationSeen(ctx context.Context, req api.MarkRecommendationSeenRequestObject) (api.MarkRecommendationSeenResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	err = s.st.MarkRecommendationSeen(ctx, prf, req.RecommendationId)
	if errors.Is(err, store.ErrNotFound) {
		return api.MarkRecommendationSeen404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.MarkRecommendationSeen204Response{}, nil
}
