// Package httpserver assembles BlitterServer's HTTP surface: middleware, the
// generated contract handler, and the docs viewer.
package httpserver

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	blitterserver "github.com/BlitterAmp/BlitterServer"
	"github.com/BlitterAmp/BlitterServer/internal/api"
	"github.com/BlitterAmp/BlitterServer/internal/events"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/server"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// New returns the BlitterServer HTTP server bound to addr.
func New(addr string, st *store.Store, mgr *library.Manager, dataDir, version string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Handler(st, mgr, dataDir, version),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// Handler builds the full stack; split from New so tests can drive it with
// httptest without binding a socket.
func Handler(st *store.Store, mgr *library.Manager, dataDir, version string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(blitterserver.OpenAPISpec)
	})
	docs, err := fs.Sub(blitterserver.DocsAssets, "web/docs")
	if err != nil {
		panic(err) // embedded path is fixed at compile time
	}
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServerFS(docs)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusTemporaryRedirect)
	})

	bus := events.NewBus(st)
	strict := api.NewStrictHandlerWithOptions(server.NewFull(st, mgr, bus, version), nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteProblem(w, http.StatusBadRequest, "Bad Request", "bad_request")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			var se *api.StatusError
			switch {
			case errors.Is(err, api.ErrNotImplemented):
				WriteProblem(w, http.StatusNotImplemented, "Not Implemented", "not_implemented")
			case errors.As(err, &se):
				WriteProblem(w, se.Status, se.Title, se.Code)
			case errors.Is(err, store.ErrNotFound):
				WriteProblem(w, http.StatusNotFound, "Not Found", "not_found")
			case errors.Is(err, store.ErrGone):
				WriteProblem(w, http.StatusGone, "Gone", "expired_or_used")
			default:
				logging.From(r.Context()).Error("handler error", "err", err)
				WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
			}
		},
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: mux})

	// Overlay: endpoints that can't ride the strict handler — cookie-setting
	// session ops, raw byte streams with Range, and grant URLs built from
	// request context.
	login := handleAdminLogin(st)
	logout := handleAdminLogout(st)
	stream := handleStreamTrack(st, mgr)
	sse := handleStreamEvents(bus)
	art := handleGetArt(st, dataDir)
	grants := handleCreateStreamGrant(st)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/admin/api/session" && r.Method == http.MethodPost:
			login(w, r)
		case r.URL.Path == "/admin/api/session" && r.Method == http.MethodDelete:
			logout(w, r)
		case r.URL.Path == "/v1/events" && r.Method == http.MethodGet:
			sse(w, r)
		case strings.HasPrefix(r.URL.Path, "/v1/stream/") && r.Method == http.MethodGet:
			stream(w, r)
		case strings.HasPrefix(r.URL.Path, "/v1/art/") && r.Method == http.MethodGet:
			art(w, r)
		case r.URL.Path == "/v1/stream-grants" && r.Method == http.MethodPost:
			grants(w, r)
		default:
			mux.ServeHTTP(w, r)
		}
	})

	limited := RateLimit(30, "POST /v1/pair", "POST /v1/pair/claim")(root)
	return RequestLogger(Recover(AdminAuth(st)(Auth(st)(limited))))
}
