package server

import (
	"context"
	"fmt"

	"github.com/BlitterAmp/BlitterServer/internal/api"
)

func (s *Server) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	dev, found, err := s.st.GetDevice(ctx, id.DeviceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("identity device %s vanished", id.DeviceID)
	}
	me := api.GetMe200JSONResponse{Device: apiDevice(dev)}
	if id.ProfileID != "" {
		p, found, err := s.st.GetProfileRecord(ctx, id.ProfileID)
		if err != nil {
			return nil, err
		}
		if found {
			ap := apiProfile(p)
			me.Profile = &ap
		}
	}
	return me, nil
}

func (s *Server) ListProfiles(ctx context.Context, _ api.ListProfilesRequestObject) (api.ListProfilesResponseObject, error) {
	records, err := s.st.ListProfileRecords(ctx)
	if err != nil {
		return nil, err
	}
	out := make(api.ListProfiles200JSONResponse, 0, len(records))
	for _, p := range records {
		out = append(out, apiProfile(p))
	}
	return out, nil
}

func (s *Server) CreateProfileToken(ctx context.Context, req api.CreateProfileTokenRequestObject) (api.CreateProfileTokenResponseObject, error) {
	id, err := identity(ctx)
	if err != nil {
		return nil, err
	}
	p, found, err := s.st.GetProfileRecord(ctx, req.Body.ProfileId)
	if err != nil {
		return nil, err
	}
	if !found {
		return api.CreateProfileToken404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	pin := ""
	if req.Body.Pin != nil {
		pin = *req.Body.Pin
	}
	ok, hasPin, err := s.st.VerifyProfilePIN(ctx, p.ProfileID, pin)
	if err != nil {
		return nil, err
	}
	if !ok {
		code := "wrong_pin"
		if hasPin && pin == "" {
			code = "pin_required"
		}
		return api.CreateProfileToken403ApplicationProblemPlusJSONResponse(problem(403, "Forbidden", code)), nil
	}
	token, err := s.st.CreateProfileToken(ctx, id.DeviceID, p.ProfileID)
	if err != nil {
		return nil, err
	}
	return api.CreateProfileToken201JSONResponse{Token: token, Profile: apiProfile(p)}, nil
}

func (s *Server) GetMySettings(ctx context.Context, _ api.GetMySettingsRequestObject) (api.GetMySettingsResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	p, found, err := s.st.GetProfileRecord(ctx, prf)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("identity profile %s vanished", prf)
	}
	return api.GetMySettings200JSONResponse{ShareListening: p.ShareListening}, nil
}

func (s *Server) UpdateMySettings(ctx context.Context, req api.UpdateMySettingsRequestObject) (api.UpdateMySettingsResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body.ShareListening != nil {
		if err := s.st.SetShareListening(ctx, prf, *req.Body.ShareListening); err != nil {
			return nil, err
		}
	}
	p, found, err := s.st.GetProfileRecord(ctx, prf)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("identity profile %s vanished", prf)
	}
	return api.UpdateMySettings200JSONResponse{ShareListening: p.ShareListening}, nil
}

// last.fm state is honestly absent until the integration arc ships an
// adapter: no instance credentials, so nothing is available or connected.

func (s *Server) GetMyLastfm(ctx context.Context, _ api.GetMyLastfmRequestObject) (api.GetMyLastfmResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	return api.GetMyLastfm200JSONResponse{Available: false, Connected: false}, nil
}

func (s *Server) ConnectMyLastfm(ctx context.Context, _ api.ConnectMyLastfmRequestObject) (api.ConnectMyLastfmResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	return api.ConnectMyLastfm409ApplicationProblemPlusJSONResponse(
		problem(409, "Conflict", "lastfm_not_configured")), nil
}

func (s *Server) DisconnectMyLastfm(ctx context.Context, _ api.DisconnectMyLastfmRequestObject) (api.DisconnectMyLastfmResponseObject, error) {
	if _, err := profileID(ctx); err != nil {
		return nil, err
	}
	return api.DisconnectMyLastfm204Response{}, nil
}
