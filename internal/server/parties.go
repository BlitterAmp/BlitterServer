package server

import (
	"context"
	"errors"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/party"
)

func apiPartyState(s party.State) api.PartyState {
	out := api.PartyState{Paused: s.Paused, PositionMs: s.PositionMs, AsOf: s.AsOf}
	if s.TrackID != "" {
		out.TrackId = &s.TrackID
	}
	return out
}

func apiParty(p party.Party) api.Party {
	out := api.Party{
		PartyId:       p.PartyID,
		HostProfileId: p.HostProfileID,
		State:         apiPartyState(p.State),
		CreatedAt:     p.CreatedAt,
	}
	out.Name = optStr(p.Name)
	out.Members = make([]struct {
		Name      string `json:"name"`
		ProfileId string `json:"profileId"`
	}, 0, len(p.Members))
	for _, m := range p.Members {
		out.Members = append(out.Members, struct {
			Name      string `json:"name"`
			ProfileId string `json:"profileId"`
		}{Name: m.Name, ProfileId: m.ProfileID})
	}
	out.Queue = make([]api.PartyQueueItem, 0, len(p.Queue))
	for _, item := range p.Queue {
		tr := apiTrack(item.Track)
		out.Queue = append(out.Queue, api.PartyQueueItem{
			ItemId: item.ItemID, AddedByProfileId: item.AddedBy,
			TrackId: tr.TrackId, Title: tr.Title, Index: tr.Index, DiscNumber: tr.DiscNumber,
			PrimaryArtist: tr.PrimaryArtist, ArtistCredits: tr.ArtistCredits,
			AlbumId: tr.AlbumId, AlbumTitle: tr.AlbumTitle,
			DurationMs: tr.DurationMs, ArtId: tr.ArtId, Genres: tr.Genres, Media: tr.Media,
			MusicBrainzRecordingId: tr.MusicBrainzRecordingId,
		})
	}
	return out
}

func partyNotFound[T any](wrap func(api.NotFoundApplicationProblemPlusJSONResponse) T) T {
	return wrap(notFoundProblem())
}

func (s *Server) ListParties(ctx context.Context, _ api.ListPartiesRequestObject) (api.ListPartiesResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	parties := s.pty.ListFor(ctx, prf)
	out := make(api.ListParties200JSONResponse, 0, len(parties))
	for _, p := range parties {
		out = append(out, apiParty(p))
	}
	return out, nil
}

func (s *Server) CreateParty(ctx context.Context, req api.CreatePartyRequestObject) (api.CreatePartyResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	name := ""
	if req.Body != nil && req.Body.Name != nil {
		name = *req.Body.Name
	}
	p, err := s.pty.Create(ctx, prf, name)
	if err != nil {
		return nil, err
	}
	return api.CreateParty201JSONResponse(apiParty(p)), nil
}

func (s *Server) GetParty(ctx context.Context, req api.GetPartyRequestObject) (api.GetPartyResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.pty.Get(ctx, req.PartyId, prf)
	if errors.Is(err, party.ErrNotFound) {
		return api.GetParty404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetParty200JSONResponse(apiParty(p)), nil
}

func (s *Server) EndParty(ctx context.Context, req api.EndPartyRequestObject) (api.EndPartyResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	err = s.pty.End(ctx, req.PartyId, prf)
	switch {
	case errors.Is(err, party.ErrHostOnly):
		return api.EndParty403ApplicationProblemPlusJSONResponse{
			ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(forbidden("host_only"))}, nil
	case errors.Is(err, party.ErrNotFound):
		return api.EndParty404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	case err != nil:
		return nil, err
	}
	return api.EndParty204Response{}, nil
}

func (s *Server) InviteToParty(ctx context.Context, req api.InviteToPartyRequestObject) (api.InviteToPartyResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	err = s.pty.Invite(ctx, req.PartyId, prf, req.Body.ProfileIds)
	if errors.Is(err, party.ErrNotFound) {
		return api.InviteToParty404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.InviteToParty204Response{}, nil
}

func (s *Server) JoinParty(ctx context.Context, req api.JoinPartyRequestObject) (api.JoinPartyResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.pty.Join(ctx, req.PartyId, prf)
	if errors.Is(err, party.ErrNotFound) {
		return api.JoinParty404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.JoinParty200JSONResponse(apiParty(p)), nil
}

func (s *Server) LeaveParty(ctx context.Context, req api.LeavePartyRequestObject) (api.LeavePartyResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	err = s.pty.Leave(ctx, req.PartyId, prf)
	if errors.Is(err, party.ErrNotFound) {
		return api.LeaveParty404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.LeaveParty204Response{}, nil
}

func (s *Server) AppendPartyQueue(ctx context.Context, req api.AppendPartyQueueRequestObject) (api.AppendPartyQueueResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	err = s.pty.AppendQueue(ctx, req.PartyId, prf, req.Body.TrackIds)
	if errors.Is(err, party.ErrNotFound) {
		return api.AppendPartyQueue404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.AppendPartyQueue204Response{}, nil
}

func (s *Server) PartyTransport(ctx context.Context, req api.PartyTransportRequestObject) (api.PartyTransportResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body.Action == "seek" && req.Body.PositionMs == nil {
		return nil, badRequest("seek_requires_position")
	}
	positionMs := 0
	if req.Body.PositionMs != nil {
		positionMs = *req.Body.PositionMs
	}
	state, err := s.pty.Transport(ctx, req.PartyId, prf, string(req.Body.Action), positionMs)
	switch {
	case errors.Is(err, party.ErrHostOnly):
		return api.PartyTransport403ApplicationProblemPlusJSONResponse{
			ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(forbidden("host_only"))}, nil
	case errors.Is(err, party.ErrNotFound):
		return api.PartyTransport404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	case err != nil:
		return nil, err
	}
	return api.PartyTransport200JSONResponse(apiPartyState(state)), nil
}

func (s *Server) KickFromParty(ctx context.Context, req api.KickFromPartyRequestObject) (api.KickFromPartyResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	err = s.pty.Kick(ctx, req.PartyId, prf, req.Body.ProfileId)
	switch {
	case errors.Is(err, party.ErrHostOnly):
		return api.KickFromParty403ApplicationProblemPlusJSONResponse{
			ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(forbidden("host_only"))}, nil
	case errors.Is(err, party.ErrNotFound):
		return api.KickFromParty404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	case err != nil:
		return nil, err
	}
	return api.KickFromParty204Response{}, nil
}
