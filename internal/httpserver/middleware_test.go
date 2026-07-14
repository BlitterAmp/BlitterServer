package httpserver

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
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
		{"POST", "/v1/pair/claim"},
		// The last.fm browser callback carries its state in the path.
		{"GET", "/v1/lastfm/callback/c3RhdGU"},
		{"GET", "/docs/"}, {"GET", "/api/openapi.yaml"}, {"GET", "/"},
	} {
		if !isPublic(tc.method, tc.path) {
			t.Errorf("%s %s must be public", tc.method, tc.path)
		}
	}
	// Admin-realm paths are AdminAuth's business, never bearer-public.
	if isPublic("GET", "/v1/status") || isPublic("POST", "/admin/api/setup") {
		t.Error("authed/admin routes must not be bearer-public")
	}
	// The bare collection path (no state) is not a route and stays gated.
	if isPublic("GET", "/v1/lastfm/callback") || isPublic("GET", "/v1/lastfm/callbackevil") {
		t.Error("only state-bearing callback paths are public")
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

	var got auth.Identity
	h := Auth(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = auth.IdentityFrom(r.Context())
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
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	var sawRequestID bool
	h := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequestID = w.Header().Get("X-Request-Id") != ""
		logging.From(r.Context()).Info("probe")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/ping", nil))
	if !sawRequestID {
		t.Fatal("request id must be set before handler runs")
	}
	out := buf.String()
	if !strings.Contains(out, "request_id=") {
		t.Fatalf("context logger missing request_id attr: %q", out)
	}
	if !strings.Contains(out, "probe") {
		t.Fatalf("handler's context logger did not reach the buffer: %q", out)
	}
}

func TestRequestLoggerLevelsByStatus(t *testing.T) {
	for _, tc := range []struct {
		status      int
		wantVisible bool
		wantLevel   string
	}{
		{status: 204, wantVisible: false},
		{status: 302, wantVisible: false},
		{status: 404, wantVisible: true, wantLevel: "INFO"},
		{status: 500, wantVisible: true, wantLevel: "ERROR"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			req := httptest.NewRequest("GET", "/routine", nil)
			req = req.WithContext(logging.With(req.Context(), logger))
			RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			})).ServeHTTP(httptest.NewRecorder(), req)
			out := buf.String()
			if strings.Contains(out, "msg=request") != tc.wantVisible {
				t.Fatalf("status %d visibility=%v log=%q", tc.status, tc.wantVisible, out)
			}
			if tc.wantVisible && (!strings.Contains(out, "level="+tc.wantLevel) || !strings.Contains(out, "request_id=") || !strings.Contains(out, "status=")) {
				t.Fatalf("status %d fields/level log=%q", tc.status, out)
			}
		})
	}
}
