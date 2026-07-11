package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BlitterAmp/Blittarr/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPublicPathsBypassAuth(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/ping"}, {"POST", "/v1/pair"}, {"GET", "/v1/pair/pair_abc123"},
		{"POST", "/v1/pair/claim"}, {"POST", "/admin/api/setup"}, {"POST", "/admin/api/session"},
		{"GET", "/docs/"}, {"GET", "/api/openapi.yaml"}, {"GET", "/"},
	} {
		if !isPublic(tc.method, tc.path) {
			t.Errorf("%s %s must be public", tc.method, tc.path)
		}
	}
	if isPublic("GET", "/v1/status") || isPublic("DELETE", "/admin/api/session") {
		t.Error("authed routes must not be public")
	}
}

func TestAuthRejectsMissingAndUnknownTokens(t *testing.T) {
	st := testStore(t)
	h := Auth(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, hdr := range []string{"", "Bearer nope"} {
		req := httptest.NewRequest("GET", "/v1/status", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 || !strings.Contains(rec.Header().Get("Content-Type"), "application/problem+json") {
			t.Fatalf("want 401 problem, got %d %s", rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestAuthResolvesIdentityIntoContext(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	dev, _ := st.CreateDevice(ctx, "d", "ios")
	prf, _ := st.CreateProfile(ctx, "p")
	tok, _ := st.CreateProfileToken(ctx, dev, prf)

	var got store.Identity
	h := Auth(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
	}))
	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.DeviceID != dev || got.ProfileID != prf {
		t.Fatalf("identity not in context: %+v", got)
	}
}

func TestRecoverTurnsPanicInto500Problem(t *testing.T) {
	h := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/status", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"status":500`) {
		t.Fatalf("want 500 problem, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequestLoggerInstallsContextLogger(t *testing.T) {
	var sawRequestID bool
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// logging.From must return a logger; we can't introspect attrs
		// directly, so assert the request id header contract instead.
		sawRequestID = w.Header().Get("X-Request-Id") != ""
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/ping", nil))
	if !sawRequestID {
		t.Fatal("request id must be set before handler runs")
	}
}
