# BlitterServer — Project Instructions

The self-hosted backend service for **BlitterAmp** (the player app at `BlitterAmp/BlitterAmp`; both grew out of `matjam/musex` — its CLAUDE.md holds the accumulated platform history). BlitterServer is a **middleman**: the apps talk ONLY to BlitterServer; BlitterServer owns the music source (Plex first, Jellyfin/Navidrome later), server-side transcoding, Lidarr acquisition, last.fm, and cross-device taste/love/playback/social state.

## Where the design lives (POLICY — decided 2026-07-10)

**This repo carries only the current, accurate implementation and API documentation.** All architecture design, specs, plans, decision history, and session notes live in the AgentOS vault: `~/Documents/Vaults/AgentOS/Apps/BlitterAmp/` (`BlitterServer Architecture.md`, `BlitterServer v1 Session-2 Design.md`, `Decisions.md`). Read the vault before proposing changes; write design/decision updates THERE, not here. Do not add design docs, specs, or plans to this repo.

## Non-negotiables

- **Contract-first.** `api/openapi.yaml` is the source of truth. Iterate the spec BEFORE writing handlers; server stubs and the apps' TS client are generated from it. A behavior change that isn't in the spec is a bug.
- **TDD, no exceptions (user directive 2026-07-10).** Tests first for every endpoint and feature, then implementation: contract tests (generated client → running server), Go table tests against fake ports (`MusicSource`/Lidarr/last.fm) written before handlers, real-ffmpeg fixture tests (generated sine fixtures), env-gated live-Plex smoke (`BLITTER_PLEX_E2E`).
- **Go**, single static binary, embedded web admin assets (Svelte + DaisyUI SPA, node-built, `go:embed`). Single-tenant self-host (hosted/multi-tenant mode is explicitly shelved — don't build for it, don't preclude it: keep auth a real layer).
- **Hexagonal.** The music source is a port (`MusicSource`); Plex is an adapter. Same for acquisition (Lidarr), scrobbling/discovery (last.fm), transcoding (ffmpeg). Domain logic never imports an adapter.
- **Deploy targets:** manual from source (`go build`), prebuilt tarballs, Docker. ffmpeg is invoked as an external binary (system dep on bare metal, bundled in the Docker image) — never assume a GUI host.
- No silently swallowed errors. 401s from Plex surface as source-disconnected, never retry-loops.

## Git workflow

- Remote: `git@github.com:BlitterAmp/BlitterServer.git`. Feature branches + PRs; conventional-commit PR titles (squash-merge, same regime as musex).
- `git add -A` always; push after every commit; full test/lint bar locally before push.
