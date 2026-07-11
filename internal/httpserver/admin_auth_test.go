package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
}

func TestAdminRealmRequiresSessionCookie(t *testing.T) {
	st := testStore(t)
	h := AdminAuth(st)(okHandler())

	for _, tc := range []struct{ method, path string }{
		{"GET", "/admin/api/state"}, {"DELETE", "/admin/api/session"},
		{"POST", "/admin/api/profiles"}, {"POST", "/admin/api/pair-codes"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 || !strings.Contains(rec.Header().Get("Content-Type"), "problem+json") {
			t.Errorf("%s %s without cookie: want 401 problem, got %d", tc.method, tc.path, rec.Code)
		}
	}

	// Bogus cookie is still 401.
	req := httptest.NewRequest("GET", "/admin/api/state", nil)
	req.AddCookie(&http.Cookie{Name: AdminCookieName, Value: "blt_bogus"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("bogus cookie: want 401, got %d", rec.Code)
	}
}

func TestAdminRealmPublicAndValidPaths(t *testing.T) {
	st := testStore(t)
	h := AdminAuth(st)(okHandler())

	// Setup + login are reachable without a session.
	for _, tc := range []struct{ method, path string }{
		{"POST", "/admin/api/setup"}, {"POST", "/admin/api/session"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != 200 {
			t.Errorf("%s %s: want passthrough 200, got %d", tc.method, tc.path, rec.Code)
		}
	}

	// Non-admin paths are not this middleware's business.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ping", nil))
	if rec.Code != 200 {
		t.Fatalf("non-admin path: want passthrough, got %d", rec.Code)
	}

	// A real session passes.
	raw, err := st.CreateAdminSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/admin/api/state", nil)
	req.AddCookie(&http.Cookie{Name: AdminCookieName, Value: raw})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("valid session: want 200, got %d", rec.Code)
	}
}

func TestBearerAuthSkipsAdminRealm(t *testing.T) {
	st := testStore(t)
	h := Auth(st)(okHandler())
	// No bearer token at all — Auth must leave admin paths to AdminAuth.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("Auth must pass admin paths through, got %d", rec.Code)
	}
}

func TestDeviceTokenPowersAreLimited(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	dev, _ := st.CreateDevice(ctx, "d", "ios")
	dtok, _ := st.CreateDeviceToken(ctx, dev)
	h := Auth(st)(okHandler())

	do := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+dtok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/profiles"}, {"POST", "/v1/profile-tokens"},
		{"GET", "/v1/me"}, {"GET", "/v1/status"}, {"GET", "/v1/capabilities"},
	} {
		if code := do(tc.method, tc.path); code != 200 {
			t.Errorf("device token on %s %s: want 200, got %d", tc.method, tc.path, code)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/artists"}, {"GET", "/v1/home"}, {"PUT", "/v1/loves/art_x"},
	} {
		if code := do(tc.method, tc.path); code != 403 {
			t.Errorf("device token on %s %s: want 403, got %d", tc.method, tc.path, code)
		}
	}
}

func TestAuthTouchesDeviceLastSeen(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	dev, _ := st.CreateDevice(ctx, "d", "ios")
	prf, _ := st.CreateProfile(ctx, "p")
	tok, _ := st.CreateProfileToken(ctx, dev, prf)

	h := Auth(st)(okHandler())
	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(httptest.NewRecorder(), req)

	d, _, _ := st.GetDevice(ctx, dev)
	if d.LastSeenAt == nil {
		t.Fatal("authenticated request must touch device last_seen_at")
	}
}

func TestRateLimitReturns429(t *testing.T) {
	limited := RateLimit(20, "POST /v1/pair", "POST /v1/pair/claim")(okHandler())
	var last int
	for i := 0; i < 40; i++ {
		req := httptest.NewRequest("POST", "/v1/pair", nil)
		req.RemoteAddr = "203.0.113.7:1234"
		rec := httptest.NewRecorder()
		limited.ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != 429 {
		t.Fatalf("hammering must hit 429, got %d", last)
	}
	// Other paths and other IPs are unaffected.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/pair", nil)
	req.RemoteAddr = "203.0.113.8:1234"
	limited.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("other IP must pass, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/v1/ping", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	limited.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("unlimited path must pass, got %d", rec.Code)
	}
}
