package server

import (
	"context"

	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// problem builds the contract's Problem shape for typed error responses.
func problem(status int, title, code string) api.Problem {
	return api.Problem{Status: status, Title: title, Code: &code}
}

func notFoundProblem() api.NotFoundApplicationProblemPlusJSONResponse {
	return api.NotFoundApplicationProblemPlusJSONResponse(problem(404, "Not Found", "not_found"))
}

// identity returns the request identity or a 401 StatusError; the auth
// middleware makes absence a programming error, not a user condition.
func identity(ctx context.Context) (auth.Identity, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return auth.Identity{}, &api.StatusError{Status: 401, Code: "missing_identity", Title: "Unauthorized"}
	}
	return id, nil
}

// profileID returns the profile-scoped identity's profile, mirroring the
// middleware's device-token power check.
func profileID(ctx context.Context) (string, error) {
	id, err := identity(ctx)
	if err != nil {
		return "", err
	}
	if id.ProfileID == "" {
		return "", &api.StatusError{Status: 403, Code: "profile_token_required", Title: "Forbidden"}
	}
	return id.ProfileID, nil
}

func apiProfile(p store.ProfileRecord) api.Profile {
	out := api.Profile{ProfileId: p.ProfileID, Name: p.Name, HasPin: p.HasPin}
	if p.AvatarColor != "" {
		out.AvatarColor = &p.AvatarColor
	}
	return out
}

func apiDevice(d store.DeviceRecord) api.Device {
	out := api.Device{
		DeviceId: d.DeviceID,
		Name:     d.Name,
		Type:     api.DeviceType(d.Type),
		PairedAt: d.PairedAt,
	}
	out.LastSeenAt = d.LastSeenAt
	return out
}
