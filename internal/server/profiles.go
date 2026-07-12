package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/store"
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

// Per-profile last.fm connections need the callback flow (adapter arc);
// availability honestly reflects whether instance credentials exist.

func (s *Server) GetMyLastfm(ctx context.Context, _ api.GetMyLastfmRequestObject) (api.GetMyLastfmResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	available, err := s.lastfmConfigured(ctx)
	if err != nil {
		return nil, err
	}
	conn, connected, err := s.st.GetLastfmConnection(ctx, prf)
	if err != nil {
		return nil, err
	}
	resp := api.GetMyLastfm200JSONResponse{Available: available, Connected: connected}
	if connected {
		resp.Username = &conn.Username
	}
	return resp, nil
}

func (s *Server) ConnectMyLastfm(ctx context.Context, _ api.ConnectMyLastfmRequestObject) (api.ConnectMyLastfmResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	key, _, err := s.st.GetSetting(ctx, settingLastfmAPIKey)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return api.ConnectMyLastfm409ApplicationProblemPlusJSONResponse(
			problem(409, "Conflict", "lastfm_not_configured")), nil
	}
	canonical, _, err := s.st.GetSetting(ctx, settingCanonicalURL)
	if err != nil {
		return nil, err
	}
	if canonical == "" {
		return api.ConnectMyLastfm409ApplicationProblemPlusJSONResponse(problem(409, "Conflict", "canonical_url_required")), nil
	}
	base, err := url.Parse(canonical)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && !(base.Scheme == "http" && isLoopbackHost(base.Hostname()))) {
		return api.ConnectMyLastfm409ApplicationProblemPlusJSONResponse(problem(409, "Conflict", "secure_canonical_url_required")), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	if err := s.st.CreateLastfmAttempt(ctx, prf, state, time.Now().Add(15*time.Minute)); err != nil {
		return nil, err
	}
	base.Path = "/v1/lastfm/callback"
	base.RawQuery = url.Values{"state": {state}}.Encode()
	base.Fragment = ""
	callback := base.String()
	return api.ConnectMyLastfm201JSONResponse{Url: "https://www.last.fm/api/auth/?api_key=" + url.QueryEscape(key) + "&cb=" + url.QueryEscape(callback)}, nil
}

func (s *Server) DisconnectMyLastfm(ctx context.Context, _ api.DisconnectMyLastfmRequestObject) (api.DisconnectMyLastfmResponseObject, error) {
	prf, err := profileID(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.st.DeleteLastfmData(ctx, prf); err != nil {
		return nil, err
	}
	return api.DisconnectMyLastfm204Response{}, nil
}

func (s *Server) CompleteLastfmAuth(ctx context.Context, req api.CompleteLastfmAuthRequestObject) (api.CompleteLastfmAuthResponseObject, error) {
	cache, csp, referrer := "no-store", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'", "no-referrer"
	fail := func() api.CompleteLastfmAuthResponseObject {
		body := "<!doctype html><title>Last.fm connection failed</title><p>This authorization is invalid or expired. Return to BlitterAmp and try again.</p>"
		return api.CompleteLastfmAuth400TexthtmlResponse{Body: strings.NewReader(body), ContentLength: int64(len(body)), Headers: api.CompleteLastfmAuth400ResponseHeaders{CacheControl: &cache, ContentSecurityPolicy: &csp, ReferrerPolicy: &referrer}}
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{32}$`).MatchString(req.Params.Token) {
		return fail(), nil
	}
	claimRaw := make([]byte, 16)
	if _, err := rand.Read(claimRaw); err != nil {
		return nil, err
	}
	claimID := base64.RawURLEncoding.EncodeToString(claimRaw)
	prf, err := s.st.ClaimLastfmAttempt(ctx, req.Params.State, claimID)
	if err != nil {
		return fail(), nil
	}
	completed := false
	defer func() {
		if !completed {
			_ = s.st.ReleaseLastfmAttempt(context.Background(), req.Params.State, claimID)
		}
	}()
	client, ok, err := s.lastfmClient(ctx)
	if err != nil || !ok {
		return fail(), nil
	}
	session, err := client.Exchange(ctx, req.Params.Token)
	if err != nil || session.Key == "" || session.Username == "" {
		return fail(), nil
	}
	if err := s.st.CompleteLastfmAttempt(ctx, req.Params.State, claimID, prf, store.LastfmConnection{Username: session.Username, SessionKey: session.Key}); err != nil {
		return nil, err
	}
	completed = true
	body := "<!doctype html><title>Last.fm connected</title><p>Connected. You can close this window and return to BlitterAmp.</p>"
	return api.CompleteLastfmAuth200TexthtmlResponse{Body: strings.NewReader(body), ContentLength: int64(len(body)), Headers: api.CompleteLastfmAuth200ResponseHeaders{CacheControl: &cache, ContentSecurityPolicy: &csp, ReferrerPolicy: &referrer}}, nil
}

func (s *Server) AdminAnonymizeProfileData(ctx context.Context, req api.AdminAnonymizeProfileDataRequestObject) (api.AdminAnonymizeProfileDataResponseObject, error) {
	if _, found, err := s.st.GetProfileRecord(ctx, req.ProfileId); err != nil {
		return nil, err
	} else if !found {
		return api.AdminAnonymizeProfileData404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	actual, err := s.st.LastfmDataCategories(ctx, req.ProfileId)
	if err != nil {
		return nil, err
	}
	categories := make([]api.AdminAnonymizeProfileData200JSONResponseBodyCategories, len(actual))
	for i, c := range actual {
		categories[i] = api.AdminAnonymizeProfileData200JSONResponseBodyCategories(c)
	}
	if !req.Body.DryRun {
		deleted, err := s.st.DeleteLastfmData(ctx, req.ProfileId)
		if err != nil {
			return nil, err
		}
		categories = make([]api.AdminAnonymizeProfileData200JSONResponseBodyCategories, len(deleted))
		for i, c := range deleted {
			categories[i] = api.AdminAnonymizeProfileData200JSONResponseBodyCategories(c)
		}
	}
	return api.AdminAnonymizeProfileData200JSONResponse{DryRun: req.Body.DryRun, Categories: categories}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
