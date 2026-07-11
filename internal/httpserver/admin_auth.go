package httpserver

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// AdminCookieName matches the contract's adminSession security scheme.
const AdminCookieName = "blitter_admin"

// isAdminPublic lists the two admin operations reachable without a session:
// first-run setup and login itself.
func isAdminPublic(method, path string) bool {
	return method == "POST" && (path == "/admin/api/setup" || path == "/admin/api/session")
}

// AdminAuth gates the /admin/api/* realm behind the session cookie. Non-admin
// paths pass through untouched — bearer auth is a separate realm.
func AdminAuth(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/admin/api/") || isAdminPublic(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			c, err := r.Cookie(AdminCookieName)
			if err != nil || c.Value == "" {
				WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "admin_session_required")
				return
			}
			ok, err := st.ValidateAdminSession(r.Context(), c.Value)
			if err != nil {
				logging.From(r.Context()).Error("admin session lookup", "err", err)
				WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
				return
			}
			if !ok {
				WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "invalid_session")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit applies a per-IP sliding-window cap to the listed "METHOD /path"
// routes (the contract's 429s on pairing endpoints). Window is one minute.
func RateLimit(perMinute int, routes ...string) func(http.Handler) http.Handler {
	limited := make(map[string]bool, len(routes))
	for _, r := range routes {
		limited[r] = true
	}
	var (
		mu   sync.Mutex
		hits = make(map[string][]time.Time)
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limited[r.Method+" "+r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			ip := r.RemoteAddr
			if i := strings.LastIndex(ip, ":"); i >= 0 {
				ip = ip[:i]
			}
			now := time.Now()
			cutoff := now.Add(-time.Minute)
			mu.Lock()
			kept := hits[ip][:0]
			for _, t := range hits[ip] {
				if t.After(cutoff) {
					kept = append(kept, t)
				}
			}
			allowed := len(kept) < perMinute
			if allowed {
				kept = append(kept, now)
			}
			hits[ip] = kept
			if len(hits) > 10000 { // household-scale safety valve
				hits = map[string][]time.Time{ip: kept}
			}
			mu.Unlock()
			if !allowed {
				WriteProblem(w, http.StatusTooManyRequests, "Too Many Requests", "rate_limited")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
