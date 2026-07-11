package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/BlitterAmp/BlitterServer/internal/auth"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// The session endpoints live outside the generated strict handler because
// they own Set-Cookie, which strict response objects cannot express.

func adminSessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     AdminCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func handleAdminLogin(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
			WriteProblem(w, http.StatusBadRequest, "Bad Request", "bad_request")
			return
		}
		hash, ok, err := st.GetSetting(r.Context(), "admin_password_hash")
		if err != nil {
			logging.From(r.Context()).Error("admin login", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		}
		if !ok {
			WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "setup_incomplete")
			return
		}
		match, err := auth.VerifyPassword(body.Password, hash)
		if err != nil {
			logging.From(r.Context()).Error("admin login verify", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		}
		if !match {
			WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "wrong_password")
			return
		}
		raw, err := st.CreateAdminSession(r.Context())
		if err != nil {
			logging.From(r.Context()).Error("admin session create", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			return
		}
		http.SetCookie(w, adminSessionCookie(raw, 7*24*60*60))
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleAdminLogout(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// AdminAuth already validated the cookie; delete the session and
		// expire the cookie client-side.
		if c, err := r.Cookie(AdminCookieName); err == nil {
			if err := st.DeleteAdminSession(r.Context(), c.Value); err != nil {
				logging.From(r.Context()).Error("admin session delete", "err", err)
				WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
				return
			}
		}
		http.SetCookie(w, adminSessionCookie("", -1))
		w.WriteHeader(http.StatusNoContent)
	}
}
