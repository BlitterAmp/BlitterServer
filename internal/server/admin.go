package server

import (
	"context"
	"errors"
	"net/url"
	"strconv"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// Settings keys owned by the admin surface.
const (
	settingAdminPasswordHash = "admin_password_hash"
	settingCanonicalURL      = "canonical_url"
	settingTranscodeFormat   = "transcode_default_format"
	settingTranscodeBitrate  = "transcode_default_bitrate_kbps"
	settingArtifactCacheMax  = "artifact_cache_max_bytes"
)

const defaultArtifactCacheMaxBytes = int64(10) << 30 // 10 GiB

func badRequest(code string) error {
	return &api.StatusError{Status: 400, Code: code, Title: "Bad Request"}
}

func (s *Server) AdminSetup(ctx context.Context, req api.AdminSetupRequestObject) (api.AdminSetupResponseObject, error) {
	if len(req.Body.Password) < 10 {
		return nil, badRequest("password_too_short")
	}
	done, err := s.st.SetupComplete(ctx)
	if err != nil {
		return nil, err
	}
	if done {
		return api.AdminSetup409ApplicationProblemPlusJSONResponse(
			problem(409, "Conflict", "setup_already_complete")), nil
	}
	hash, err := auth.HashPassword(req.Body.Password)
	if err != nil {
		return nil, err
	}
	if err := s.st.SetSetting(ctx, settingAdminPasswordHash, hash); err != nil {
		return nil, err
	}
	return api.AdminSetup204Response{}, nil
}

func (s *Server) AdminGetState(ctx context.Context, _ api.AdminGetStateRequestObject) (api.AdminGetStateResponseObject, error) {
	done, err := s.st.SetupComplete(ctx)
	if err != nil {
		return nil, err
	}
	profiles, devices, pendingPairings, err := s.st.Counts(ctx)
	if err != nil {
		return nil, err
	}
	canonicalURL, _, err := s.st.GetSetting(ctx, settingCanonicalURL)
	if err != nil {
		return nil, err
	}

	var state api.AdminGetState200JSONResponse
	state.SetupComplete = done
	state.ProfileCount = profiles
	state.DeviceCount = devices
	state.PendingPairings = pendingPairings
	urlSet := canonicalURL != ""
	state.CanonicalUrlSet = &urlSet
	f := false
	state.Source.Linked = &f
	state.Source.LibrarySelected = &f
	state.Source.Connected = &f
	lidarr := api.AdminStateIntegrationsLidarr("absent")
	if baseURL, apiKey, err := s.lidarrConfigured(ctx); err != nil {
		return nil, err
	} else if baseURL != "" && apiKey != "" {
		lidarr = api.AdminStateIntegrationsLidarr("configured")
	}
	lastfm := api.AdminStateIntegrationsLastfm("absent")
	if configured, err := s.lastfmConfigured(ctx); err != nil {
		return nil, err
	} else if configured {
		lastfm = api.AdminStateIntegrationsLastfm("configured")
	}
	state.Integrations.Lidarr = &lidarr
	state.Integrations.Lastfm = &lastfm
	return state, nil
}

// ── profiles ───────────────────────────────────────────────────

func validPIN(pin string) bool {
	if len(pin) < 4 || len(pin) > 8 {
		return false
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *Server) AdminListProfiles(ctx context.Context, _ api.AdminListProfilesRequestObject) (api.AdminListProfilesResponseObject, error) {
	records, err := s.st.ListProfileRecords(ctx)
	if err != nil {
		return nil, err
	}
	out := make(api.AdminListProfiles200JSONResponse, 0, len(records))
	for _, p := range records {
		out = append(out, apiProfile(p))
	}
	return out, nil
}

func (s *Server) AdminCreateProfile(ctx context.Context, req api.AdminCreateProfileRequestObject) (api.AdminCreateProfileResponseObject, error) {
	if req.Body.Name == "" {
		return nil, badRequest("name_required")
	}
	pin := ""
	if req.Body.Pin != nil && *req.Body.Pin != "" {
		pin = *req.Body.Pin
		if !validPIN(pin) {
			return nil, badRequest("pin_must_be_4_to_8_digits")
		}
	}
	color := ""
	if req.Body.AvatarColor != nil {
		color = *req.Body.AvatarColor
	}
	p, err := s.st.CreateProfileRecord(ctx, req.Body.Name, pin, color)
	if err != nil {
		return nil, err
	}
	return api.AdminCreateProfile201JSONResponse(apiProfile(p)), nil
}

func (s *Server) AdminUpdateProfile(ctx context.Context, req api.AdminUpdateProfileRequestObject) (api.AdminUpdateProfileResponseObject, error) {
	var upd store.ProfileUpdate
	upd.Name = req.Body.Name
	upd.AvatarColor = req.Body.AvatarColor
	if req.Body.Pin.IsSpecified() {
		upd.SetPin = true
		if !req.Body.Pin.IsNull() {
			pin, err := req.Body.Pin.Get()
			if err != nil {
				return nil, err
			}
			if pin != "" && !validPIN(pin) {
				return nil, badRequest("pin_must_be_4_to_8_digits")
			}
			upd.Pin = pin
		}
	}
	p, err := s.st.UpdateProfile(ctx, req.ProfileId, upd)
	if errors.Is(err, store.ErrNotFound) {
		return api.AdminUpdateProfile404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.AdminUpdateProfile200JSONResponse(apiProfile(p)), nil
}

func (s *Server) AdminDeleteProfile(ctx context.Context, req api.AdminDeleteProfileRequestObject) (api.AdminDeleteProfileResponseObject, error) {
	err := s.st.DeleteProfile(ctx, req.ProfileId)
	if errors.Is(err, store.ErrNotFound) {
		return api.AdminDeleteProfile404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.AdminDeleteProfile204Response{}, nil
}

// ── pairings ───────────────────────────────────────────────────

func (s *Server) AdminListPairings(ctx context.Context, _ api.AdminListPairingsRequestObject) (api.AdminListPairingsResponseObject, error) {
	pending, err := s.st.ListPendingPairings(ctx)
	if err != nil {
		return nil, err
	}
	out := make(api.AdminListPairings200JSONResponse, 0, len(pending))
	for _, p := range pending {
		out = append(out, api.PairingRequest{
			PairingId:   p.PairingID,
			Code:        p.Code,
			DeviceName:  p.DeviceName,
			DeviceType:  p.DeviceType,
			RequestedAt: p.RequestedAt,
			ExpiresAt:   p.ExpiresAt,
		})
	}
	return out, nil
}

func (s *Server) AdminApprovePairing(ctx context.Context, req api.AdminApprovePairingRequestObject) (api.AdminApprovePairingResponseObject, error) {
	err := s.st.ApprovePairing(ctx, req.PairingId)
	if errors.Is(err, store.ErrNotFound) {
		return api.AdminApprovePairing404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.AdminApprovePairing204Response{}, nil
}

func (s *Server) AdminDenyPairing(ctx context.Context, req api.AdminDenyPairingRequestObject) (api.AdminDenyPairingResponseObject, error) {
	err := s.st.DenyPairing(ctx, req.PairingId)
	if errors.Is(err, store.ErrNotFound) {
		return api.AdminDenyPairing404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.AdminDenyPairing204Response{}, nil
}

func (s *Server) AdminCreatePairCode(ctx context.Context, _ api.AdminCreatePairCodeRequestObject) (api.AdminCreatePairCodeResponseObject, error) {
	canonicalURL, _, err := s.st.GetSetting(ctx, settingCanonicalURL)
	if err != nil {
		return nil, err
	}
	if canonicalURL == "" {
		return api.AdminCreatePairCode409ApplicationProblemPlusJSONResponse(
			problem(409, "Conflict", "canonical_url_not_configured")), nil
	}
	code, expiresAt, err := s.st.CreatePairCode(ctx)
	if err != nil {
		return nil, err
	}
	return api.AdminCreatePairCode201JSONResponse{
		Code:         code,
		ExpiresAt:    expiresAt,
		CanonicalUrl: canonicalURL,
		QrPayload:    "blitteramp://pair?server=" + url.QueryEscape(canonicalURL) + "&code=" + code,
	}, nil
}

// ── devices ────────────────────────────────────────────────────

func (s *Server) AdminListDevices(ctx context.Context, _ api.AdminListDevicesRequestObject) (api.AdminListDevicesResponseObject, error) {
	devices, err := s.st.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make(api.AdminListDevices200JSONResponse, 0, len(devices))
	for _, d := range devices {
		out = append(out, apiDevice(d))
	}
	return out, nil
}

func (s *Server) AdminRevokeDevice(ctx context.Context, req api.AdminRevokeDeviceRequestObject) (api.AdminRevokeDeviceResponseObject, error) {
	err := s.st.DeleteDevice(ctx, req.DeviceId)
	if errors.Is(err, store.ErrNotFound) {
		return api.AdminRevokeDevice404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: notFoundProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.AdminRevokeDevice204Response{}, nil
}

// ── settings ───────────────────────────────────────────────────

func (s *Server) AdminGetServerSettings(ctx context.Context, _ api.AdminGetServerSettingsRequestObject) (api.AdminGetServerSettingsResponseObject, error) {
	canonicalURL, _, err := s.st.GetSetting(ctx, settingCanonicalURL)
	if err != nil {
		return nil, err
	}
	var resp api.AdminGetServerSettings200JSONResponse
	if canonicalURL != "" {
		resp.CanonicalUrl = &canonicalURL
	}
	return resp, nil
}

func (s *Server) AdminSetServerSettings(ctx context.Context, req api.AdminSetServerSettingsRequestObject) (api.AdminSetServerSettingsResponseObject, error) {
	value := ""
	if req.Body.CanonicalUrl != nil {
		value = *req.Body.CanonicalUrl
		u, err := url.Parse(value)
		if err != nil || (value != "" && (u.Scheme == "" || u.Host == "")) {
			return nil, badRequest("canonical_url_must_be_absolute")
		}
	}
	if err := s.st.SetSetting(ctx, settingCanonicalURL, value); err != nil {
		return nil, err
	}
	return api.AdminSetServerSettings204Response{}, nil
}

func (s *Server) transcodeSettings(ctx context.Context) (api.TranscodeSettings, error) {
	out := api.TranscodeSettings{
		DefaultFormat:         "original",
		DefaultBitrateKbps:    256,
		ArtifactCacheMaxBytes: defaultArtifactCacheMaxBytes,
	}
	if v, ok, err := s.st.GetSetting(ctx, settingTranscodeFormat); err != nil {
		return out, err
	} else if ok {
		out.DefaultFormat = api.TranscodeSettingsDefaultFormat(v)
	}
	if v, ok, err := s.st.GetSetting(ctx, settingTranscodeBitrate); err != nil {
		return out, err
	} else if ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return out, err
		}
		out.DefaultBitrateKbps = api.TranscodeSettingsDefaultBitrateKbps(n)
	}
	if v, ok, err := s.st.GetSetting(ctx, settingArtifactCacheMax); err != nil {
		return out, err
	} else if ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return out, err
		}
		out.ArtifactCacheMaxBytes = n
	}
	return out, nil
}

func (s *Server) AdminGetTranscodeSettings(ctx context.Context, _ api.AdminGetTranscodeSettingsRequestObject) (api.AdminGetTranscodeSettingsResponseObject, error) {
	ts, err := s.transcodeSettings(ctx)
	if err != nil {
		return nil, err
	}
	return api.AdminGetTranscodeSettings200JSONResponse(ts), nil
}

func (s *Server) AdminSetTranscodeSettings(ctx context.Context, req api.AdminSetTranscodeSettingsRequestObject) (api.AdminSetTranscodeSettingsResponseObject, error) {
	switch req.Body.DefaultFormat {
	case "original", "aac":
	default:
		return nil, badRequest("format_must_be_original_or_aac")
	}
	switch req.Body.DefaultBitrateKbps {
	case 128, 192, 256, 320:
	default:
		return nil, badRequest("bitrate_must_be_128_192_256_or_320")
	}
	if req.Body.ArtifactCacheMaxBytes <= 0 {
		return nil, badRequest("cache_budget_must_be_positive")
	}
	if err := s.st.SetSetting(ctx, settingTranscodeFormat, string(req.Body.DefaultFormat)); err != nil {
		return nil, err
	}
	if err := s.st.SetSetting(ctx, settingTranscodeBitrate, strconv.Itoa(int(req.Body.DefaultBitrateKbps))); err != nil {
		return nil, err
	}
	if err := s.st.SetSetting(ctx, settingArtifactCacheMax, strconv.FormatInt(req.Body.ArtifactCacheMaxBytes, 10)); err != nil {
		return nil, err
	}
	return api.AdminSetTranscodeSettings204Response{}, nil
}
