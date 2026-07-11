package store

import (
	"context"
	"strings"
	"testing"
)

func TestTokenLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	dev, err := s.CreateDevice(ctx, "Nathan's iPhone", "ios")
	if err != nil || !strings.HasPrefix(dev, "dev_") {
		t.Fatalf("create device: %v %q", err, dev)
	}
	prf, err := s.CreateProfile(ctx, "Nathan")
	if err != nil || !strings.HasPrefix(prf, "prf_") {
		t.Fatalf("create profile: %v %q", err, prf)
	}

	dtok, err := s.CreateDeviceToken(ctx, dev)
	if err != nil || dtok == "" {
		t.Fatal(err)
	}
	ptok, err := s.CreateProfileToken(ctx, dev, prf)
	if err != nil {
		t.Fatal(err)
	}

	id, ok, err := s.ResolveToken(ctx, dtok)
	if err != nil || !ok || id.DeviceID != dev || id.ProfileID != "" {
		t.Fatalf("device token resolve: %v %v %+v", err, ok, id)
	}
	id, ok, _ = s.ResolveToken(ctx, ptok)
	if !ok || id.DeviceID != dev || id.ProfileID != prf {
		t.Fatalf("profile token resolve: %+v", id)
	}
	if _, ok, _ := s.ResolveToken(ctx, "garbage"); ok {
		t.Fatal("unknown token must not resolve")
	}
}

func TestDeviceDeleteCascadesTokens(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	dev, _ := s.CreateDevice(ctx, "d", "ios")
	prf, _ := s.CreateProfile(ctx, "p")
	dtok, _ := s.CreateDeviceToken(ctx, dev)
	ptok, _ := s.CreateProfileToken(ctx, dev, prf)

	if err := s.DeleteDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ResolveToken(ctx, dtok); ok {
		t.Fatal("device token must die with device")
	}
	if _, ok, _ := s.ResolveToken(ctx, ptok); ok {
		t.Fatal("profile token must die with device (contract: revoking a device kills its profile tokens)")
	}
}

func TestRawTokensNeverStored(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	dev, _ := s.CreateDevice(ctx, "d", "ios")
	raw, _ := s.CreateDeviceToken(ctx, dev)
	var count int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM device_tokens WHERE token_hash = ?`, raw).Scan(&count)
	if count != 0 {
		t.Fatal("raw token found in db — must store hashes only")
	}
}
