package server

import (
	"context"
	"errors"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func (s *Server) StartPairing(ctx context.Context, req api.StartPairingRequestObject) (api.StartPairingResponseObject, error) {
	appVersion := ""
	if req.Body.AppVersion != nil {
		appVersion = *req.Body.AppVersion
	}
	p, err := s.st.StartPairing(ctx, req.Body.DeviceName, string(req.Body.DeviceType), appVersion)
	if err != nil {
		return nil, err
	}
	return api.StartPairing201JSONResponse{
		PairingId: p.PairingID, Code: p.Code, ExpiresAt: p.ExpiresAt}, nil
}

func (s *Server) GetPairing(ctx context.Context, req api.GetPairingRequestObject) (api.GetPairingResponseObject, error) {
	p, found, err := s.st.GetPairing(ctx, req.PairingId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.GetPairing404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	resp := api.GetPairing200JSONResponse{Status: api.GetPairing200JSONResponseBodyStatus(p.Status)}
	if p.Status == "approved" {
		token, deviceID, delivered, err := s.st.DeliverPairingToken(ctx, req.PairingId)
		if err != nil {
			return nil, err
		}
		if delivered {
			resp.Token = &token
			resp.DeviceId = &deviceID
		}
	}
	return resp, nil
}

func (s *Server) ClaimPairCode(ctx context.Context, req api.ClaimPairCodeRequestObject) (api.ClaimPairCodeResponseObject, error) {
	token, deviceID, err := s.st.ClaimPairCode(ctx, req.Body.Code, req.Body.DeviceName, string(req.Body.DeviceType))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.ClaimPairCode404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	case errors.Is(err, store.ErrGone):
		return api.ClaimPairCode410ApplicationProblemPlusJSONResponse(
			problem(410, "Gone", "code_expired_or_used")), nil
	case err != nil:
		return nil, err
	}
	return api.ClaimPairCode201JSONResponse{Token: token, DeviceId: deviceID}, nil
}
