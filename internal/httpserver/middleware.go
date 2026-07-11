package httpserver

import (
	"context"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/BlitterAmp/Blittarr/internal/logging"
	"github.com/BlitterAmp/Blittarr/internal/store"
)

type identityKey struct{}

// IdentityFrom returns the bearer identity Auth resolved for this request.
func IdentityFrom(ctx context.Context) (store.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(store.Identity)
	return id, ok
}

// isPublic lists the routes reachable without credentials: the contract's
// six security:[] operations plus the docs viewer and spec.
func isPublic(method, path string) bool {
	switch {
	case method == "GET" && path == "/v1/ping",
		method == "POST" && path == "/v1/pair",
		method == "POST" && path == "/v1/pair/claim",
		method == "GET" && strings.HasPrefix(path, "/v1/pair/"),
		method == "POST" && path == "/admin/api/setup",
		method == "POST" && path == "/admin/api/session",
		method == "GET" && path == "/",
		method == "GET" && path == "/api/openapi.yaml",
		method == "GET" && strings.HasPrefix(path, "/docs/"):
		return true
	}
	return false
}

// Auth resolves the bearer token to an identity, or 401s. Public routes and
// the admin realm's cookie-session enforcement (spec 2) pass through.
func Auth(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
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
			ctx := context.WithValue(r.Context(), identityKey{}, id)
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
