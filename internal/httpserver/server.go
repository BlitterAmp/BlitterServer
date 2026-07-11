// Package httpserver assembles Blittarr's HTTP surface: middleware, the
// generated contract handler, and the docs viewer.
package httpserver

import (
	"errors"
	"io/fs"
	"net/http"

	blittarr "github.com/BlitterAmp/Blittarr"
	"github.com/BlitterAmp/Blittarr/internal/api"
	"github.com/BlitterAmp/Blittarr/internal/logging"
	"github.com/BlitterAmp/Blittarr/internal/server"
	"github.com/BlitterAmp/Blittarr/internal/store"
)

// New returns the Blittarr HTTP server bound to addr.
func New(addr string, st *store.Store, version string) *http.Server {
	return &http.Server{Addr: addr, Handler: Handler(st, version)}
}

// Handler builds the full stack; split from New so tests can drive it with
// httptest without binding a socket.
func Handler(st *store.Store, version string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(blittarr.OpenAPISpec)
	})
	docs, err := fs.Sub(blittarr.DocsAssets, "web/docs")
	if err != nil {
		panic(err) // embedded path is fixed at compile time
	}
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServerFS(docs)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusTemporaryRedirect)
	})

	strict := api.NewStrictHandlerWithOptions(server.New(st, version), nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteProblem(w, http.StatusBadRequest, "Bad Request", "bad_request")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, api.ErrNotImplemented) {
				WriteProblem(w, http.StatusNotImplemented, "Not Implemented", "not_implemented")
				return
			}
			logging.From(r.Context()).Error("handler error", "err", err)
			WriteProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal")
		},
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: mux})

	return RequestLogger(Recover(Auth(st)(mux)))
}
