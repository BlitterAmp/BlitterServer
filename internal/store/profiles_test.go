package store

import (
	"context"
	"errors"
	"testing"
)

func str(s string) *string { return &s }

func TestProfileRecordCRUD(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	p, err := s.CreateProfileRecord(ctx, "Nathan", "1234", "#ff8800")
	if err != nil || p.Name != "Nathan" || !p.HasPin || p.AvatarColor != "#ff8800" || !p.ShareListening {
		t.Fatalf("create: %v %+v", err, p)
	}
	noPin, err := s.CreateProfileRecord(ctx, "Kid", "", "")
	if err != nil || noPin.HasPin {
		t.Fatalf("pinless create: %v %+v", err, noPin)
	}

	list, err := s.ListProfileRecords(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %+v", err, list)
	}

	got, found, err := s.GetProfileRecord(ctx, p.ProfileID)
	if err != nil || !found || got.Name != "Nathan" {
		t.Fatalf("get: %v %v %+v", err, found, got)
	}
	if _, found, _ := s.GetProfileRecord(ctx, "prf_nope"); found {
		t.Fatal("unknown profile must not be found")
	}

	// Rename + clear PIN.
	upd, err := s.UpdateProfile(ctx, p.ProfileID, ProfileUpdate{Name: str("Nate"), SetPin: true, Pin: ""})
	if err != nil || upd.Name != "Nate" || upd.HasPin {
		t.Fatalf("update: %v %+v", err, upd)
	}
	// Absent fields unchanged.
	upd, err = s.UpdateProfile(ctx, p.ProfileID, ProfileUpdate{AvatarColor: str("#00ff00")})
	if err != nil || upd.Name != "Nate" || upd.AvatarColor != "#00ff00" {
		t.Fatalf("partial update: %v %+v", err, upd)
	}
	if _, err := s.UpdateProfile(ctx, "prf_nope", ProfileUpdate{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updating unknown profile must be ErrNotFound, got %v", err)
	}

	if err := s.DeleteProfile(ctx, p.ProfileID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(ctx, p.ProfileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete must be ErrNotFound, got %v", err)
	}
}

func TestVerifyProfilePIN(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, _ := s.CreateProfileRecord(ctx, "N", "4321", "")
	free, _ := s.CreateProfileRecord(ctx, "F", "", "")

	ok, hasPin, err := s.VerifyProfilePIN(ctx, p.ProfileID, "4321")
	if err != nil || !ok || !hasPin {
		t.Fatalf("correct pin: %v %v %v", err, ok, hasPin)
	}
	ok, _, _ = s.VerifyProfilePIN(ctx, p.ProfileID, "0000")
	if ok {
		t.Fatal("wrong pin must not verify")
	}
	ok, hasPin, _ = s.VerifyProfilePIN(ctx, free.ProfileID, "")
	if !ok || hasPin {
		t.Fatalf("pinless profile must verify with empty pin: %v %v", ok, hasPin)
	}
	if _, _, err := s.VerifyProfilePIN(ctx, "prf_nope", "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown profile must be ErrNotFound, got %v", err)
	}
}

func TestDeleteProfileCascadesTokens(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, _ := s.CreateProfileRecord(ctx, "N", "", "")
	dev, _ := s.CreateDevice(ctx, "d", "ios")
	tok, _ := s.CreateProfileToken(ctx, dev, p.ProfileID)

	if err := s.DeleteProfile(ctx, p.ProfileID); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.ResolveToken(ctx, tok); found {
		t.Fatal("profile tokens must die with the profile")
	}
	// Device survives — it falls back to the profile picker.
	if _, found, _ := s.GetDevice(ctx, dev); !found {
		t.Fatal("device must survive profile deletion")
	}
}

func TestShareListeningRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	p, _ := s.CreateProfileRecord(ctx, "N", "", "")
	if err := s.SetShareListening(ctx, p.ProfileID, false); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetProfileRecord(ctx, p.ProfileID)
	if got.ShareListening {
		t.Fatal("shareListening=false must persist")
	}
}

func TestDevicesAndCounts(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	dev, _ := s.CreateDevice(ctx, "Nathan's iPhone", "ios")
	s.CreateProfileRecord(ctx, "N", "", "")
	s.StartPairing(ctx, "d", "ios", "")

	d, found, err := s.GetDevice(ctx, dev)
	if err != nil || !found || d.Name != "Nathan's iPhone" || d.Type != "ios" || d.PairedAt.IsZero() {
		t.Fatalf("get device: %v %v %+v", err, found, d)
	}
	if d.LastSeenAt != nil {
		t.Fatal("fresh device has no lastSeen")
	}
	if err := s.TouchDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	d, _, _ = s.GetDevice(ctx, dev)
	if d.LastSeenAt == nil {
		t.Fatal("touch must set lastSeen")
	}

	devices, err := s.ListDevices(ctx)
	if err != nil || len(devices) != 1 {
		t.Fatalf("list devices: %v %+v", err, devices)
	}

	profiles, devicesN, pairings, err := s.Counts(ctx)
	if err != nil || profiles != 1 || devicesN != 1 || pairings != 1 {
		t.Fatalf("counts: %v %d %d %d", err, profiles, devicesN, pairings)
	}
}
