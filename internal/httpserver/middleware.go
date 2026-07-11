package httpserver

import (
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// isPublic lists the routes reachable without credentials: the contract's
// security:[] bearer-realm operations plus the docs viewer and spec. The
// /admin/api realm is not listed — AdminAuth owns it entirely.
func isPublic(method, path string) bool {
	switch {
	case method == "GET" && path == "/v1/ping",
		method == "POST" && path == "/v1/pair",
		method == "POST" && path == "/v1/pair/claim",
		method == "GET" && strings.HasPrefix(path, "/v1/pair/"),
		method == "GET" && path == "/",
		method == "GET" && path == "/api/openapi.yaml",
		method == "GET" && strings.HasPrefix(path, "/docs/"):
		return true
	}
	return false
}

// deviceTokenAllowed whitelists what a device-scoped token may call:
// identity, the profile picker, token exchange, and system state. Everything
// else needs a profile token (architecture: device tokens have two powers).
func deviceTokenAllowed(method, path string) bool {
	switch {
	case method == "GET" && path == "/v1/me",
		method == "GET" && path == "/v1/profiles",
		method == "POST" && path == "/v1/profile-tokens",
		method == "GET" && path == "/v1/status",
		method == "GET" && path == "/v1/capabilities":
		return true
	}
	return false
}

// Auth resolves the bearer token to an identity, or 401s. Public routes pass
// through, as does the whole /admin/api realm (cookie-gated by AdminAuth).
func Auth(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r.Method, r.URL.Path) || strings.HasPrefix(r.URL.Path, "/admin/api/") {
				next.ServeHTTP(w, r)
				return
			}
			// A valid signed grant substitutes for the bearer on stream GETs
			// only (players that cannot send headers).
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/stream/") &&
				r.URL.Query().Get("grant") != "" {
				if validStreamGrant(r, st) {
					next.ServeHTTP(w, r)
				} else {
					WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid_grant")
				}
				return
			}
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "missing_token")
				return
			}
			id, found, err := st.ResolveToken(r.Context(), raw)
			if err != nil {
				logging.From(r.Context()).Error("token lookup", "err", err)
				WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
				return
			}
			if !found {
				WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid_token")
				return
			}
			if id.ProfileID == "" && !deviceTokenAllowed(r.Method, r.URL.Path) {
				WriteProblem(w, http.StatusForbidden, "Forbidden", "profile_token_required")
				return
			}
			if err := st.TouchDevice(r.Context(), id.DeviceID); err != nil {
				logging.From(r.Context()).Warn("touch device", "err", err)
			}
			ctx := auth.WithIdentity(r.Context(), id)
			l := logging.From(ctx).With("device_id", id.DeviceID)
			if id.ProfileID != "" {
				l = l.With("profile_id", id.ProfileID)
			}
			next.ServeHTTP(w, r.WithContext(logging.With(ctx, l)))
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer
// (Flusher etc. — required before the SSE arc).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// RequestLogger creates the request-scoped context logger and emits one
// summary line per request.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := store.NewID("req")
		w.Header().Set("X-Request-Id", reqID)
		l := logging.From(r.Context()).With(
			"request_id", reqID, "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r.WithContext(logging.With(r.Context(), l)))
		l.Info("request", "status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

// Recover converts handler panics into logged 500 Problems.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logging.From(r.Context()).Error("panic", "value", v, "stack", string(debug.Stack()))
				WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
