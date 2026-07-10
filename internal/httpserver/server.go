// Package httpserver assembles Blittarr's HTTP surface. During the API
// design phase it serves only the contract and its documentation viewer;
// /v1 handlers arrive with the skeleton arc, generated from the contract.
package httpserver

import (
	"io/fs"
	"net/http"

	blittarr "github.com/BlitterAmp/Blittarr"
)

// New returns the Blittarr HTTP server bound to addr.
func New(addr string) *http.Server {
	return &http.Server{Addr: addr, Handler: Handler()}
}

// Handler builds the root mux. Split from New so tests can drive it with
// httptest without binding a socket.
func Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(blittarr.OpenAPISpec)
	})

	docs, err := fs.Sub(blittarr.DocsAssets, "web/docs")
	if err != nil {
		// Embedded path is fixed at compile time; failure here is a build
		// defect, not a runtime condition.
		panic(err)
	}
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServerFS(docs)))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusTemporaryRedirect)
	})

	return mux
}
