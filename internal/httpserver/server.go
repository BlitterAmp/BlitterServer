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
	"github.com/BlitterAmp/BlitterServer/internal/artifacts"
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
	adminSPA, err := fs.Sub(blitterserver.AdminSPA, "web/admin/dist")
	if err != nil {
		panic(err) // embedded path is fixed at compile time
	}
	if _, err := fs.Stat(adminSPA, "index.html"); err == nil {
		mux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServerFS(adminSPA)))
	} else {
		// Binary was built without the web assets (bare `go build`).
		mux.HandleFunc("GET /admin/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`<!doctype html><title>BlitterServer</title><body style="font-family:system-ui;max-width:40rem;margin:4rem auto"><h1>Admin console not built</h1><p>This binary was compiled without the web assets. Build them with <code>make web</code> and rebuild, or use a release build. The API itself is fully functional.</p></body>`))
		})
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})

	bus := events.NewBus(st)
	artMgr := artifacts.NewManager(st, mgr, bus, dataDir)
	artMgr.Start()
	strict := api.NewStrictHandlerWithOptions(server.NewFull(st, mgr, bus, artMgr, version), nil, api.StrictHTTPServerOptions{
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
	download := handleDownloadArtifact(st, artMgr)
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
		case strings.HasPrefix(r.URL.Path, "/v1/artifacts/") && strings.HasSuffix(r.URL.Path, "/file") && r.Method == http.MethodGet:
			download(w, r)
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
