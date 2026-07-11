package store

import (
	"context"
	"strings"
	"testing"
)

func TestAdminSessionLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	raw, err := s.CreateAdminSession(ctx)
	if err != nil || raw == "" {
		t.Fatalf("create session: %v %q", err, raw)
	}
	ok, err := s.ValidateAdminSession(ctx, raw)
	if err != nil || !ok {
		t.Fatalf("fresh session must validate: %v %v", ok, err)
	}
	if ok, _ := s.ValidateAdminSession(ctx, "blt_bogus"); ok {
		t.Fatal("unknown session must not validate")
	}
	if err := s.DeleteAdminSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ValidateAdminSession(ctx, raw); ok {
		t.Fatal("deleted session must not validate")
	}
}

func TestAdminSessionExpires(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	raw, err := s.CreateAdminSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE admin_sessions SET expires_at = '2001-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ValidateAdminSession(ctx, raw); ok {
		t.Fatal("expired session must not validate")
	}
}

func TestAdminSessionRawNeverStored(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	raw, _ := s.CreateAdminSession(ctx)
	var count int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM admin_sessions WHERE token_hash = ?`, raw).Scan(&count)
	if count != 0 || !strings.HasPrefix(raw, "blt_") {
		t.Fatalf("raw session token must be prefixed and stored hashed only (count=%d raw=%q)", count, raw)
	}
}
