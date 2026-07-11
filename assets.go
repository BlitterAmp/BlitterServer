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

// AdminSPA holds the built admin console (Svelte + DaisyUI, built with
// `make web`, dist committed so `go build` never needs node), served under
// /admin/.
//
//go:embed web/admin/dist
var AdminSPA embed.FS
