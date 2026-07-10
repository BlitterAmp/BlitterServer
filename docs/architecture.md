# Blittarr Architecture

**Status:** v1 design, 2026-07-10. Companion to `api/openapi.yaml` (the contract — iterate there first).

## What Blittarr is

A single-tenant, self-hosted Go service that sits between the BlitterAmp apps (iOS, desktop) and everything else. The apps speak one API — Blittarr's — and Blittarr owns:

- **Source** — the user's music library. Plex is the v1 adapter behind a `MusicSource` port; Jellyfin/Navidrome are future adapters. Source credentials never reach the apps.
- **Transcoding** — server-side ffmpeg. Live streams direct-play where possible; downloads are pre-transcoded **artifacts** with exact sizes.
- **Acquisition** — Lidarr, driven by the Follow verb. The apps never talk to Lidarr.
- **last.fm** — scrobbling (from playback events), similar artists, artist info, radio seeds.
- **Taste + follows + played-state** — one server-side profile instead of per-device silos.

```
BlitterAmp iOS ──┐                          ┌── Plex (MusicSource port)
BlitterAmp desktop ─┤── Blittarr /v1 (HTTP+WS) ──┤── Lidarr (Acquirer port)
web admin ───────┘        │                  ├── last.fm (Scrobbler/Discovery port)
                          └ ffmpeg, SQLite,  └── ffmpeg (Transcoder port)
                            artifact cache
```

## Decisions of record

| Decision | Choice | Why |
|---|---|---|
| Language / shape | Go, single static binary, embedded admin assets | Deploy story (tarball = one file), owner fluency, long-running daemon fit |
| Tenancy | Single-tenant self-host; hosted mode shelved | Credential custody + acquisition liability of hosting for others |
| Users | Household **profiles** (Netflix-style): passwordless, optional PIN, admin-created in the web admin | A family shares the instance but not playlists/taste; full accounts are ceremony a household doesn't need |
| User data | **Blittarr-native** (SQLite): playlists, ratings, taste, follows, history are per-profile in Blittarr, NOT written to Plex | Source-agnostic (survives a Jellyfin move), works for members without Plex accounts; source playlists surface read-only |
| Token model | Pairing mints a **device token** (can only list profiles + mint profile tokens); all real calls use **profile tokens** | In-app profile switching = swap cached tokens; offline queues keep correct attribution; admin revokes at device level |
| API style | Contract-first OpenAPI 3.0 (`api/openapi.yaml`), generated Go stubs + TS client | Prevents Go↔TS model drift; spec is the review artifact |
| App auth | PIN pairing → per-device long-lived bearer tokens | Plex-familiar UX, per-device revocation |
| Admin auth | Separate realm: password (set at first run) + cookie session | Admin powers ≠ device powers |
| Events | WebSocket `/v1/ws`, typed JSON envelope | Live download/acquisition progress; bidirectional headroom for future remote-control |
| Downloads | Artifact model (request → transcode/cache → fetch complete file) | Exact Content-Length enables iOS background URLSession + size verification; kills on-device transcoding |
| Live playback | `/v1/stream/{trackId}` direct-play proxy with Range; optional short-lived **stream grants** for header-less players | Keep tokens out of URLs by default |
| Art | `/v1/art/{artId}?w=&h=` with server-side resize + cache | Phones stop downloading full-size art |
| Persistence | SQLite (devices, pairings, follows, taste, artifact index, play history) + on-disk artifact/art caches + a YAML config file | Zero external services |

## Security model

- Device tokens: `Authorization: Bearer <token>`, minted only via admin-approved pairing, revocable per device, never expire by default (revocation is the mechanism). A device token's only powers are `GET /v1/profiles` and `POST /v1/profile-tokens`; revoking a device kills all profile tokens minted through it.
- Profile tokens: what apps actually call with. Per (device, profile); switching profiles in-app uses a different cached token — no admin involvement after pairing. Profile PINs are a household courtesy (kids vs parents), not a security boundary.
- Admin session: cookie, password-authenticated; all `/admin/api/*` and pairing approval live here. The web admin is same-origin with the API.
- Streams/art accept the bearer header. Where a player can't send headers, the app exchanges the track for a **stream grant** — a short-lived signed URL (`POST /v1/stream-grants`). No long-lived secrets in URLs, ever.
- Blittarr holds the Plex token, Lidarr API key, last.fm secrets in its own store; they are write-only through the admin API (read back as `{set: true}`).
- TLS: Blittarr serves plain HTTP on loopback/LAN by default; remote exposure is the operator's reverse-proxy/tailnet problem (documented, not solved here). This is the same trust posture as the *arr family.

## WebSocket protocol (`GET /v1/ws`)

Authenticated like any endpoint (bearer header on the upgrade request). Envelope:

```json
{ "type": "artifact.updated", "at": "2026-07-10T03:21:00Z", "data": { ... } }
```

Server→client event types (v1):

| type | data | fired when |
|---|---|---|
| `library.changed` | `{libraryId, updatedAt}` | source scan detected (Plex ws/poll) — clients refetch. Instance-wide |
| `artifact.updated` | `Artifact` | transcode queued/progress/ready/failed. Instance-wide |
| `acquisition.updated` | `AcquisitionActivity` summary | Lidarr queue/wanted changed. Instance-wide |
| `follow.updated` | `FollowRecord` | follow state change (incl. acquisition landing). Profile-scoped |
| `playlist.changed` | `{playlistId}` | playlist mutated (from any of the profile's devices). Profile-scoped |
| `taste.updated` | `{}` | profile recomputed — home rails may change. Profile-scoped |

Connections authenticate with a profile token; instance-wide events go to every
connection, profile-scoped events only to that profile's connections.

Client→server: none in v1 (reserved for future remote-control). Unknown types MUST be ignored by clients.

## Subsystem notes

- **Playback events** (`POST /v1/playback/events`, batched): the single ingestion point for played/skipped/progress. Feeds taste profile, last.fm now-playing/scrobble (with the existing scrobble-gate rules ported from musex core), and recently-played. Batching makes the apps' offline queues trivial.
- **Artifacts:** requested per track with `{format, bitrateKbps}`; deduped by (trackId, format, bitrate, sourceVersion). LRU cache with a configurable byte budget; `ready` artifacts report exact `bytes`. Album/artist sync = the app requests a batch and downloads as each flips `ready` (WS `artifact.updated`).
- **Follows:** `PUT /v1/follows` is idempotent, per profile. Artist follow = Lidarr ensure + `monitorNewItems: "all"` + back-catalog search (musex `#3597` re-assert workaround carries over). Watching is instance-level: Blittarr watches while ANY profile follows; the last unfollow stops watching. Unfollow never removes media. Album/track follows are per-profile favorites (no acquisition).
- **Profiles + per-profile data:** playlists/ratings/taste/follows/history are Blittarr-native SQLite rows keyed by profile. Plex playlists surface read-only (`origin: source`) to all profiles. last.fm: instance API credentials (admin) + per-profile user sessions (`/v1/me/lastfm`) — each member scrobbles to their own account. Deleting a profile deletes its data; its devices fall back to the profile picker.
- **Home/discovery is server-computed:** `/v1/home` returns rails (mixes, playlists, recently played/added, discoveries); mixes/for-you/smart playlists are Blittarr's port of the musex core logic, computed against the server-side taste profile. The TS implementations in `@musex/core` are the reference for the Go ports (translate their test suites).
- **Source freshness:** Plex adapter keeps the musex approach — PMS websocket notifications + poll fallback → `library.changed` fan-out; Blittarr's own list cache invalidates exactly.

## Deploy

- **Source:** `go build ./cmd/blittarr` (ffmpeg on PATH required for transcode features; absence degrades to original-format artifacts with a surfaced warning).
- **Tarball:** per-platform archive with the binary + LICENSE + sample config; systemd unit example in `deploy/`.
- **Docker:** image bundles ffmpeg; volume for `/data` (SQLite + caches + config).

## Roadmap (each arc = its own spec→plan cycle)

1. **Contract** — iterate `api/openapi.yaml` (this arc), generate Go server stubs + TS client skeleton.
2. **Skeleton** — binary, config, SQLite, admin setup + pairing, Plex link, `/v1/status`+`/v1/capabilities`, deploy pipeline (docker/tarball/source, CI).
3. **Browse + streaming** — library endpoints, art resize cache, stream proxy + grants; BlitterAmp mobile migrates browse.
4. **Artifacts** — ffmpeg pipeline, artifact cache, WS progress; mobile downloads switch to artifacts (device transcode machinery deleted).
5. **Follows + acquisition + last.fm + taste** — Lidarr/last.fm adapters, playback-events ingestion, server-computed home; iOS Follow UI ships.
6. **Desktop migration** — desktop consumes Blittarr; its main-process Plex/cache/lidarr plumbing retires.
