# Blittarr — Project Instructions

The self-hosted backend service for **BlitterAmp** (the player app at `BlitterAmp/BlitterAmp`; both grew out of `matjam/musex` — its CLAUDE.md holds the accumulated platform history). Blittarr is a **middleman**: the apps talk ONLY to Blittarr; Blittarr owns the music source (Plex first, Jellyfin/Navidrome later), server-side transcoding, Lidarr acquisition, last.fm, and cross-device taste/follow/playback state.

## Non-negotiables

- **Contract-first.** `api/openapi.yaml` is the source of truth. Iterate the spec BEFORE writing handlers; server stubs and the apps' TS client are generated from it. A behavior change that isn't in the spec is a bug.
- **Go**, single static binary, embedded web admin assets. Single-tenant self-host (hosted/multi-tenant mode is explicitly shelved — don't build for it, don't preclude it: keep auth a real layer).
- **Hexagonal.** The music source is a port (`MusicSource`); Plex is an adapter. Same for acquisition (Lidarr) and scrobbling (last.fm). Domain logic never imports an adapter.
- **Deploy targets:** manual from source (`go build`), prebuilt tarballs, Docker. ffmpeg is invoked as an external binary (system dep on bare metal, bundled in the Docker image) — never assume a GUI host.
- No silently swallowed errors. 401s from Plex surface as source-disconnected, never retry-loops.

## Git workflow

- Remote: `git@github.com:BlitterAmp/Blittarr.git`. Feature branches + PRs once main has history; conventional-commit PR titles (squash-merge, same regime as musex).
- `git add -A` always; push after every commit; full test/lint bar locally before push.

## Key decisions (2026-07-10, brainstormed in musex session)

- **Auth:** PIN pairing → long-lived per-device bearer tokens; per-device revocation. Admin realm is separate (cookie session, password set at first run).
- **Admin:** embedded web UI (setup wizard, Plex PIN link, integrations, device/pairing management).
- **Events:** WebSocket (`GET /v1/ws`), typed JSON envelope — protocol catalog in `docs/architecture.md`.
- **Downloads are artifacts:** clients never transcode. `POST /v1/artifacts` requests a (track, format, bitrate) artifact; Blittarr transcodes/caches; client downloads a complete file with exact `Content-Length` (feeds BlitterAmp iOS's native background engine + size-verify). Rationale: the on-device AAC pipeline failed in the field (1000 songs = 90GB) and Plex's transcode-session reaping made device-driven HLS stitching unworkable in background.
- **Follow verb:** follow(artist) = Lidarr ensure + monitor new releases + back-catalog search; unfollow stops watching, keeps media. iOS ships Follow-only UI (App Store 5.2.3 posture — no acquisition surface on the phone).
