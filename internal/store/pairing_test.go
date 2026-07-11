package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPairingApprovalFlow(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	p, err := s.StartPairing(ctx, "Nathan's iPhone", "ios", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.PairingID, "pair_") || len(p.Code) != 6 || p.Status != "pending" {
		t.Fatalf("bad pairing: %+v", p)
	}
	if !p.ExpiresAt.After(time.Now()) {
		t.Fatal("pairing must expire in the future")
	}

	// Nothing to deliver while pending.
	if _, _, ok, err := s.DeliverPairingToken(ctx, p.PairingID); err != nil || ok {
		t.Fatalf("pending pairing must not deliver: %v %v", ok, err)
	}

	if err := s.ApprovePairing(ctx, p.PairingID); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetPairing(ctx, p.PairingID)
	if err != nil || !found || got.Status != "approved" {
		t.Fatalf("approved pairing state: %v %v %+v", err, found, got)
	}

	token, deviceID, ok, err := s.DeliverPairingToken(ctx, p.PairingID)
	if err != nil || !ok || token == "" || !strings.HasPrefix(deviceID, "dev_") {
		t.Fatalf("delivery: %v %v %q %q", err, ok, token, deviceID)
	}
	// Token works and is device-scoped.
	id, found, err := s.ResolveToken(ctx, token)
	if err != nil || !found || id.DeviceID != deviceID || id.ProfileID != "" {
		t.Fatalf("delivered token must resolve to the device: %v %v %+v", err, found, id)
	}
	// Exactly once.
	if _, _, ok, _ := s.DeliverPairingToken(ctx, p.PairingID); ok {
		t.Fatal("token must be delivered exactly once")
	}
}

func TestPairingDenyAndNotFound(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, _ := s.StartPairing(ctx, "d", "ios", "")
	if err := s.DenyPairing(ctx, p.PairingID); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetPairing(ctx, p.PairingID)
	if got.Status != "denied" {
		t.Fatalf("want denied, got %q", got.Status)
	}
	if err := s.ApprovePairing(ctx, "pair_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approving unknown pairing must be ErrNotFound, got %v", err)
	}
	if err := s.ApprovePairing(ctx, p.PairingID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approving a denied pairing must be ErrNotFound, got %v", err)
	}
	if _, found, _ := s.GetPairing(ctx, "pair_nope"); found {
		t.Fatal("unknown pairing must not be found")
	}
}

func TestPairingExpiryReportedAndExcluded(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, _ := s.StartPairing(ctx, "d", "ios", "")
	fresh, _ := s.StartPairing(ctx, "e", "desktop", "")
	if _, err := s.db.ExecContext(ctx,
		`UPDATE pairings SET expires_at = '2001-01-01T00:00:00Z' WHERE pairing_id = ?`, p.PairingID); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetPairing(ctx, p.PairingID)
	if err != nil || !found || got.Status != "expired" {
		t.Fatalf("stale pending pairing must read expired: %v %v %+v", err, found, got)
	}
	if err := s.ApprovePairing(ctx, p.PairingID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired pairing must not be approvable")
	}
	pending, err := s.ListPendingPairings(ctx)
	if err != nil || len(pending) != 1 || pending[0].PairingID != fresh.PairingID {
		t.Fatalf("pending list must exclude expired: %v %+v", err, pending)
	}
}

func TestPairCodeClaimOnce(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	code, expiresAt, err := s.CreatePairCode(ctx)
	if err != nil || len(code) != 6 || !expiresAt.After(time.Now()) {
		t.Fatalf("mint: %v %q %v", err, code, expiresAt)
	}
	token, deviceID, err := s.ClaimPairCode(ctx, code, "Nathan's iPhone", "ios")
	if err != nil || token == "" || !strings.HasPrefix(deviceID, "dev_") {
		t.Fatalf("claim: %v %q %q", err, token, deviceID)
	}
	if id, found, _ := s.ResolveToken(ctx, token); !found || id.DeviceID != deviceID {
		t.Fatal("claimed token must resolve")
	}
	if _, _, err := s.ClaimPairCode(ctx, code, "again", "ios"); !errors.Is(err, ErrGone) {
		t.Fatalf("second claim must be ErrGone, got %v", err)
	}
	if _, _, err := s.ClaimPairCode(ctx, "ZZZZZZ", "d", "ios"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code must be ErrNotFound, got %v", err)
	}
}

func TestPairCodeExpiryIsGone(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	code, _, _ := s.CreatePairCode(ctx)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE pair_codes SET expires_at = '2001-01-01T00:00:00Z' WHERE code = ?`, code); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimPairCode(ctx, code, "d", "ios"); !errors.Is(err, ErrGone) {
		t.Fatalf("expired code must be ErrGone, got %v", err)
	}
}
