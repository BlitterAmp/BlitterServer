package store

import (
	"context"

	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/BlitterAmp/BlitterServer/internal/auth"
)

// HashToken maps a raw bearer value to its storage form. Raw tokens are
// never persisted.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func newRawToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "blt_" + hex.EncodeToString(b)
}

func (s *Store) CreateDevice(ctx context.Context, name, typ string) (string, error) {
	id := NewID("dev")
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO devices (device_id, name, type) VALUES (?, ?, ?)`, id, name, typ)
	return id, err
}

func (s *Store) CreateProfile(ctx context.Context, name string) (string, error) {
	id := NewID("prf")
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO profiles (profile_id, name) VALUES (?, ?)`, id, name)
	return id, err
}

func (s *Store) CreateDeviceToken(ctx context.Context, deviceID string) (string, error) {
	raw := newRawToken()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO device_tokens (token_hash, device_id) VALUES (?, ?)`, HashToken(raw), deviceID)
	return raw, err
}

func (s *Store) CreateProfileToken(ctx context.Context, deviceID, profileID string) (string, error) {
	raw := newRawToken()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO profile_tokens (token_hash, device_id, profile_id) VALUES (?, ?, ?)`,
		HashToken(raw), deviceID, profileID)
	return raw, err
}

// ResolveToken looks a raw bearer value up in both token tables.
func (s *Store) ResolveToken(ctx context.Context, raw string) (auth.Identity, bool, error) {
	h := HashToken(raw)
	var id auth.Identity
	err := s.db.QueryRowContext(ctx,
		`SELECT device_id, profile_id FROM profile_tokens WHERE token_hash = ?`, h).
		Scan(&id.DeviceID, &id.ProfileID)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return auth.Identity{}, false, err
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT device_id FROM device_tokens WHERE token_hash = ?`, h).Scan(&id.DeviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Identity{}, false, nil
	}
	if err != nil {
		return auth.Identity{}, false, err
	}
	return id, true, nil
}

func (s *Store) DeleteDevice(ctx context.Context, deviceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE device_id = ?`, deviceID)
	return err
}
