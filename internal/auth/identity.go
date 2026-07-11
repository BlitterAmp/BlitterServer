package auth

import "context"

// Identity is a resolved bearer token. Empty ProfileID means a device token
// (whose only contract powers are listing profiles and minting profile tokens).
type Identity struct {
	DeviceID  string
	ProfileID string
}

type identityKey struct{}

// WithIdentity returns ctx carrying the resolved identity.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity the auth middleware resolved for this
// request, if any.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
