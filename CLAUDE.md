# BlitterServer — Project Instructions

The self-hosted backend service for **BlitterAmp** (the player app at `BlitterAmp/BlitterAmp`; both grew out of `matjam/musex` — its CLAUDE.md holds the accumulated platform history). BlitterServer is a **middleman**: the apps talk ONLY to BlitterServer. It owns the library index (canonical type-prefixed ids over SQLite), music sources behind a port (**filesystem-first** — files in a directory are a complete source, metadata enriched from public sources + last.fm; Plex/Jellyfin/Navidrome are optional integrations, never dependencies), server-side transcoding (ffmpeg), acquisition (Lidarr), last.fm relay, and cross-device taste/love/playback/social state. It deploys standalone (NAS/server) or bundled into the desktop app as a managed engine.

## Where the design lives (POLICY — decided 2026-07-10)

**This repo carries only the current, accurate implementation and API documentation.** All architecture design, specs, plans, decision history, and session notes live in the AgentOS vault: `~/Documents/Vaults/AgentOS/Apps/BlitterAmp/` (`BlitterServer Architecture.md`, `Blittarr v1 Session-2 Design.md` (historical name), `Decisions.md`). Read the vault before proposing changes; write design/decision updates THERE, not here. Do not add design docs, specs, or plans to this repo.

## Commands

- `make check` — gofmt + vet + `go test ./...`. Must pass before every push.
- `make build` — static binary at `dist/blitterserver` (version injected via ldflags). `make run` for dev.
- `make generate` — regenerate `internal/api/api.gen.go` from `api/openapi.yaml` (oapi-codegen v2, pinned by the go.mod `tool` directive). Generated code is **committed**; CI fails if regeneration produces a diff.
- `make lint-api gen-check` — redocly spec lint + TS/Go codegen smoke checks (pinned npx versions).

## Layout

- `cmd/blitterserver` — main: config → logging → store → httpserver, graceful shutdown.
- `internal/api` — generated strict server + client (`api.gen.go`), plus the hand-maintained `Unimplemented` base: every contract op returns `ErrNotImplemented`, mapped to a 501 Problem. A new spec operation breaks compilation here until implemented or consciously 501'd — that is the drift gate.
- `internal/server` — implements `api.StrictServerInterface`; embeds `Unimplemented`, overrides only what's real.
- `internal/httpserver` — middleware chain (request-id logging, panic recovery, bearer auth), `application/problem+json` writer, handler assembly, embedded docs viewer at `/docs/`.
- `internal/store` — SQLite (modernc, WAL) + goose migrations: settings, profiles, devices, hashed tokens. No SQL leaves this package.
- `internal/config` — bootstrap config, precedence flags > env (`BLITTER_*`) > file > defaults. Runtime settings live in SQLite behind the admin API.
- `internal/logging` (slog + rotating file), `internal/transcode` (ffmpeg detection; Transcoder port later).

## Non-negotiables

- **Contract-first.** `api/openapi.yaml` is the source of truth. Iterate the spec BEFORE writing handlers; server stubs and the apps' TS client are generated from it. A behavior change that isn't in the spec is a bug.
- **Backward-compatible 1.x API.** BlitterAmp desktop relies on the server `1.x` contract. Preserve compatibility for
  all `1.x` releases and run `make compat-api` for contract changes. Do not implement, merge, version, or publish a
  breaking API or major release without the user's explicit approval; report the incompatibility and affected clients
  instead.
- **TDD, no exceptions (user directive 2026-07-10).** Tests first for every endpoint and feature, then implementation: contract tests (generated client → running server), Go table tests against fake ports (`MusicSource`/Lidarr/last.fm) written before handlers, real-ffmpeg fixture tests (generated sine fixtures), env-gated live-source smoke tests.
- **Go**, single static binary, embedded web admin assets (Svelte + DaisyUI SPA, node-built, `go:embed`). Single-tenant self-host (hosted/multi-tenant mode is explicitly shelved — don't build for it, don't preclude it: keep auth a real layer).
- **Hexagonal.** Music sources (filesystem, Plex, …) are adapters behind the `MusicSource` port. Same for acquisition (Lidarr), scrobbling/discovery (last.fm), transcoding (ffmpeg). Domain logic never imports an adapter.
- **Deploy targets:** manual from source (`go build`), prebuilt tarballs, Docker. ffmpeg is invoked as an external binary (system dep on bare metal, bundled in the Docker image) — never assume a GUI host.
- No silently swallowed errors. Source 401s surface as source-disconnected, never retry-loops.

## Gotchas

- log/slog ONLY; request-scoped loggers travel in context (`logging.From(ctx)`). Never log raw tokens or request bodies.
- Bearer tokens persist as SHA-256 hex only — raw values never touch the database. Ids are prefixed (`dev_`, `prf_`, …; registry in the spec's `info.description`).
- Regenerate only via `make generate`; never hand-edit `api.gen.go`. When the spec adds operations, extend `internal/api/unimplemented.go` to match.

## Git workflow

- Remote: `git@github.com:BlitterAmp/BlitterServer.git`. Feature branches + PRs; conventional-commit PR titles (squash-merge, same regime as musex).
- `git add -A` always; push after every commit; full test/lint bar locally before push.
