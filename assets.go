// Package blitterserver embeds repo-level assets served by the daemon.
package blitterserver

import "embed"

// OpenAPISpec is the v1 API contract. It is the source of truth for the
// service surface; handlers are generated/written against it, never ahead
// of it.
//
//go:embed api/openapi.yaml
var OpenAPISpec []byte

// DocsAssets holds the vendored API documentation viewer (Scalar) and its
// host page, served under /docs/.
//
//go:embed web/docs
var DocsAssets embed.FS

// AdminSPA holds the built admin console (Svelte + DaisyUI), served under
// /admin/. dist/ is NOT committed: `make web` (or the release pipeline)
// builds it, and `make build` embeds it. A bare `go build` without the web
// build still compiles — the server then serves an honest "not built" page
// at /admin/ instead of a stale UI.
//
//go:embed all:web/admin/dist
var AdminSPA embed.FS
