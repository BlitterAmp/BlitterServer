package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/api"
)

// Integration settings keys. Secrets are write-only through the admin API —
// reads report set-flags only. The live adapters (acquisition side effects,
// scrobbling, discovery providers) ship in a later arc; storing config and
// testing connectivity is real behavior today.
const (
	settingLidarrBaseURL      = "lidarr_base_url"
	settingLidarrAPIKey       = "lidarr_api_key"
	settingLidarrAllowSelfSig = "lidarr_allow_self_signed"
	settingLastfmAPIKey       = "lastfm_api_key"
	settingLastfmSharedSecret = "lastfm_shared_secret"
	settingFanartAPIKey       = "fanart_api_key"
)

func (s *Server) lidarrConfigured(ctx context.Context) (baseURL, apiKey string, err error) {
	baseURL, _, err = s.st.GetSetting(ctx, settingLidarrBaseURL)
	if err != nil {
		return "", "", err
	}
	apiKey, _, err = s.st.GetSetting(ctx, settingLidarrAPIKey)
	return baseURL, apiKey, err
}

func (s *Server) lastfmConfigured(ctx context.Context) (bool, error) {
	key, _, err := s.st.GetSetting(ctx, settingLastfmAPIKey)
	if err != nil {
		return false, err
	}
	secret, _, err := s.st.GetSetting(ctx, settingLastfmSharedSecret)
	return key != "" && secret != "", err
}

// ── Lidarr ─────────────────────────────────────────────────────

func (s *Server) AdminGetLidarr(ctx context.Context, _ api.AdminGetLidarrRequestObject) (api.AdminGetLidarrResponseObject, error) {
	baseURL, apiKey, err := s.lidarrConfigured(ctx)
	if err != nil {
		return nil, err
	}
	resp := api.AdminGetLidarr200JSONResponse{Configured: baseURL != "" && apiKey != ""}
	if baseURL != "" {
		resp.BaseUrl = &baseURL
	}
	keySet := apiKey != ""
	resp.ApiKeySet = &keySet
	if v, _, _ := s.st.GetSetting(ctx, settingLidarrAllowSelfSig); v == "true" {
		t := true
		resp.AllowSelfSigned = &t
	}
	return resp, nil
}

func (s *Server) AdminSetLidarr(ctx context.Context, req api.AdminSetLidarrRequestObject) (api.AdminSetLidarrResponseObject, error) {
	u, err := url.Parse(req.Body.BaseUrl)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, badRequest("base_url_must_be_absolute")
	}
	if req.Body.ApiKey == "" {
		return nil, badRequest("api_key_required")
	}
	if err := s.st.SetSetting(ctx, settingLidarrBaseURL, strings.TrimSuffix(req.Body.BaseUrl, "/")); err != nil {
		return nil, err
	}
	if err := s.st.SetSetting(ctx, settingLidarrAPIKey, req.Body.ApiKey); err != nil {
		return nil, err
	}
	allow := "false"
	if req.Body.AllowSelfSigned != nil && *req.Body.AllowSelfSigned {
		allow = "true"
	}
	if err := s.st.SetSetting(ctx, settingLidarrAllowSelfSig, allow); err != nil {
		return nil, err
	}
	return api.AdminSetLidarr204Response{}, nil
}

func (s *Server) AdminDeleteLidarr(ctx context.Context, _ api.AdminDeleteLidarrRequestObject) (api.AdminDeleteLidarrResponseObject, error) {
	for _, key := range []string{settingLidarrBaseURL, settingLidarrAPIKey, settingLidarrAllowSelfSig} {
		if err := s.st.SetSetting(ctx, key, ""); err != nil {
			return nil, err
		}
	}
	return api.AdminDeleteLidarr204Response{}, nil
}

// AdminTestLidarr probes the configured instance's system status for real.
func (s *Server) AdminTestLidarr(ctx context.Context, _ api.AdminTestLidarrRequestObject) (api.AdminTestLidarrResponseObject, error) {
	baseURL, apiKey, err := s.lidarrConfigured(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(msg string) api.AdminTestLidarrResponseObject {
		return api.AdminTestLidarr200JSONResponse{Ok: false, Error: &msg}
	}
	if baseURL == "" || apiKey == "" {
		return fail("lidarr is not configured"), nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, "GET", baseURL+"/api/v1/system/status", nil)
	if err != nil {
		return fail(err.Error()), nil
	}
	httpReq.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fail(err.Error()), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Sprintf("lidarr answered %d", resp.StatusCode)), nil
	}
	var status struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fail("unparseable system status"), nil
	}
	out := api.AdminTestLidarr200JSONResponse{Ok: true}
	if status.Version != "" {
		out.Version = &status.Version
	}
	return out, nil
}

// ── last.fm ────────────────────────────────────────────────────

func (s *Server) AdminGetLastfm(ctx context.Context, _ api.AdminGetLastfmRequestObject) (api.AdminGetLastfmResponseObject, error) {
	configured, err := s.lastfmConfigured(ctx)
	if err != nil {
		return nil, err
	}
	count, err := s.st.CountLastfmProfiles(ctx)
	if err != nil {
		return nil, err
	}
	return api.AdminGetLastfm200JSONResponse{Configured: configured, ConnectedProfiles: &count}, nil
}

func (s *Server) AdminSetLastfm(ctx context.Context, req api.AdminSetLastfmRequestObject) (api.AdminSetLastfmResponseObject, error) {
	if req.Body.ApiKey == "" || req.Body.SharedSecret == "" {
		return nil, badRequest("api_key_and_secret_required")
	}
	if err := s.st.SetLastfmCredentials(ctx, req.Body.ApiKey, req.Body.SharedSecret); err != nil {
		return nil, err
	}
	s.lib.TriggerAlbumEnrichment()
	return api.AdminSetLastfm204Response{}, nil
}

func (s *Server) AdminDeleteLastfm(ctx context.Context, _ api.AdminDeleteLastfmRequestObject) (api.AdminDeleteLastfmResponseObject, error) {
	for _, key := range []string{settingLastfmAPIKey, settingLastfmSharedSecret} {
		if err := s.st.SetSetting(ctx, key, ""); err != nil {
			return nil, err
		}
	}
	if err := s.st.DeleteAllLastfmData(ctx); err != nil {
		return nil, err
	}
	return api.AdminDeleteLastfm204Response{}, nil
}

func (s *Server) AdminGetFanart(ctx context.Context, _ api.AdminGetFanartRequestObject) (api.AdminGetFanartResponseObject, error) {
	key, _, err := s.st.GetSetting(ctx, settingFanartAPIKey)
	if err != nil {
		return nil, err
	}
	return api.AdminGetFanart200JSONResponse{Configured: key != ""}, nil
}

func (s *Server) AdminSetFanart(ctx context.Context, req api.AdminSetFanartRequestObject) (api.AdminSetFanartResponseObject, error) {
	if req.Body.ApiKey == "" {
		return nil, badRequest("api_key_required")
	}
	if err := s.st.SetSetting(ctx, settingFanartAPIKey, req.Body.ApiKey); err != nil {
		return nil, err
	}
	s.lib.TriggerArtistEnrichment()
	return api.AdminSetFanart204Response{}, nil
}

func (s *Server) AdminDeleteFanart(ctx context.Context, _ api.AdminDeleteFanartRequestObject) (api.AdminDeleteFanartResponseObject, error) {
	if err := s.st.SetSetting(ctx, settingFanartAPIKey, ""); err != nil {
		return nil, err
	}
	return api.AdminDeleteFanart204Response{}, nil
}
