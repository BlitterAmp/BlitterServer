# BlitterServer

[![CI](https://github.com/BlitterAmp/BlitterServer/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/BlitterAmp/BlitterServer/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BlitterAmp/BlitterServer?display_name=tag&sort=semver)](https://github.com/BlitterAmp/BlitterServer/releases/latest)

> Development database reset: the MusicBrainz identity/structured-credit schema rewrites the pre-user library
> baseline. Existing development data directories created before this schema must have `blitterserver.db` removed
> explicitly and then be rescanned; startup never deletes a database automatically. This loses all local development
> state, including profiles, playlists, loves, history, and opaque entity IDs.

Filesystem metadata extraction reads Picard/MusicBrainz IDs supported by `dhowden/tag`. The dependency collapses
repeated Vorbis values in some files, so this phase safely preserves one unsplit scalar credit for those files rather
than guessing by splitting artist text. Richer repeated-value parsing is deferred. Tracks also remain source-file
records in this phase, so multiple files or encodings may share a recording MBID; a later canonical recording/source
mapping phase will normalize them.

MusicBrainz identity resolution runs at album granularity on startup and in randomized 4-8 hour serialized enrichment
passes. One process-wide client sends a contactable versioned User-Agent, spaces every request start (including retries)
by at least 1.1 seconds, honors provider retry deadlines globally, and durably caches direct and search responses.
Embedded release IDs are looked up directly; otherwise release candidates are scored from title, credited artists,
date, track layout, and available durations. Only a high-confidence candidate with a clear runner-up margin changes
canonical identity. Ambiguous and unmatched evidence remains in SQLite without changing the library. Matched albums are
eligible after 7 days, transient failures after 24 hours, and unmatched albums after 30 days. A serialized batch emits
at most one `library.changed`, and only if identity, credits, recordings, or artwork actually changed.

Tags remain the primary metadata source, but a consistent filesystem layout gives the resolver additional evidence when
album titles or disc numbers are malformed or missing:

```text
Artist/Album Title (YEAR)/track.flac
Artist/Album Title (YEAR)/CD 01/track.flac
Artist/Album Title (YEAR)/CD 02/track.flac
```

The year suffix is optional. Numbered `CD`, `Disc`, and `Disk` directories are recognized. BlitterServer uses this only
when all tracks agree on the album directory and normal tag-based lookup fails; it does not rewrite the file tags.

The self-hosted backend for [BlitterAmp](https://github.com/BlitterAmp/BlitterAmp). BlitterServer is the engine of your music world: it indexes your library, streams and transcodes your files, keeps taste and playback state consistent across your devices, and gives the BlitterAmp apps exactly one API to talk to — this one.

It is **filesystem-first**: a directory of music files is a complete source, with metadata enriched from public sources and last.fm. Media servers (Plex, and later Jellyfin/Navidrome) are optional integrations, not dependencies. It runs standalone on a NAS or server, and is designed to also ship embedded inside the BlitterAmp desktop app as a managed engine.

Single-tenant and self-hosted, but multi-user: one household, several profiles, each with their own taste.

## Status

**Functional server.** The contract in [`api/openapi.yaml`](api/openapi.yaml) is enforced by generated strict handlers and a CI drift gate. The server includes:

- Filesystem library scanning, canonical artist/album/track ids, search and browse, range streaming, artwork, and ffmpeg-backed transcodes/downloads.
- Household profiles and devices, PIN pairing, per-profile taste and playback state, playlists, recommendations, presence, listen parties, radio, and SSE updates.
- MusicBrainz, Cover Art Archive, fanart.tv, and last.fm enrichment; personal last.fm authorization, scrobbling, loves, and discovery.
- An embedded admin console for setup, sources, profiles, devices, and integrations, plus the admin API used by BlitterAmp's bundled engine.

The four Plex administration operations remain honest `501 Not Implemented` responses. Lidarr can be configured and tested, but acquisition actions are not yet connected to loves.

## Quick start

Requires Go 1.26+ and node (for the embedded admin console; `make build` handles it). `ffmpeg` on `PATH` is optional and only affects reported transcode capabilities. A bare `go build ./cmd/blitterserver` works without node but yields a binary whose `/admin/` page explains the console wasn't built — release builds always include it.

```sh
make build
./dist/blitterserver
```

Then open `http://127.0.0.1:8484/admin/` for the admin console (first run walks you through creating the admin password) and `http://127.0.0.1:8484/docs/` for the interactive API reference. Logs go to stdout and to a rotating file under the data directory.

### Docker Compose

The production image is published as [`matjam/blitterserver`](https://hub.docker.com/r/matjam/blitterserver) for
`linux/amd64` and `linux/arm64`. It includes ffmpeg and the embedded admin console, and runs as non-root UID/GID
`10001`.

Save the repository's [`compose.yaml`](compose.yaml), then set the required values and start it:

```sh
export BLITTER_ADMIN_PASSWORD='choose-a-password-of-at-least-10-characters'
export MUSIC_DIR='/absolute/path/to/your/music'
docker compose up -d
```

The compose file is ready to copy as-is:

```yaml
services:
  blitterserver:
    image: matjam/blitterserver:${BLITTERSERVER_TAG:-latest}
    restart: unless-stopped
    ports:
      - "8484:8484"
    environment:
      BLITTER_ADMIN_PASSWORD: ${BLITTER_ADMIN_PASSWORD:?Set BLITTER_ADMIN_PASSWORD to at least 10 characters}
      BLITTER_MUSIC_DIR: /music
    volumes:
      - blitterserver-data:/data
      - ${MUSIC_DIR:?Set MUSIC_DIR to the host music directory}:/music:ro
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://127.0.0.1:8484/v1/ping"]
      interval: 30s
      timeout: 5s
      start_period: 15s
      retries: 3
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true

volumes:
  blitterserver-data:
```

`BLITTER_ADMIN_PASSWORD` must be at least 10 characters. It initializes the administrator only when the data volume
has no existing admin; later container starts never replace the stored password. Likewise, `BLITTER_MUSIC_DIR`
configures `/music` only when no source exists and never replaces an existing source. Invalid values fail a fresh
startup with a clear error, and the password is never printed.

The named volume contains the SQLite database, logs, artwork, and caches. Docker creates it with the image's
UID/GID `10001`; if you replace it with a host bind mount, make that directory writable by `10001:10001`. The music
mount is read-only.

BlitterServer serves plain HTTP by default. Do not expose port 8484 directly to the public internet; put it behind a
TLS-terminating reverse proxy or access it through a private network/tailnet.

### Configuration

Precedence, highest to lowest: **flags > environment > config file > defaults**.

| Flag | Env var | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `BLITTER_CONFIG` | (none) | Path to a `blitterserver.yaml` config file |
| `--listen` | `BLITTER_LISTEN` | `127.0.0.1:8484` | Address to listen on |
| `--data-dir` | `BLITTER_DATA_DIR` | `$XDG_DATA_HOME/blitterserver`, else `~/.local/share/blitterserver` | State directory (SQLite database, logs, caches) |
| `--log-level` | `BLITTER_LOG_LEVEL` | `info` | `debug`\|`info`\|`warn`\|`error` |
| (file only) | `BLITTER_LOG_FORMAT` | `text` | `text`\|`json` |
| (none) | `BLITTER_ADMIN_PASSWORD` | (none) | First-start-only admin password (minimum 10 characters) |
| (none) | `BLITTER_MUSIC_DIR` | (none) | First-start-only filesystem source path |

Example config file:

```yaml
listen: "0.0.0.0:8484"
data_dir: /var/lib/blitterserver
log:
  level: info
  format: text
  file:
    enabled: true        # rotating file under <data-dir>/logs/
    max_size_mb: 10
    max_backups: 5
    max_age_days: 30
    compress: true
```

### Releases

[GitHub releases](https://github.com/BlitterAmp/BlitterServer/releases/latest) provide standalone archives for Linux
and macOS on amd64 and arm64, plus Windows amd64. Each archive includes the server, README, and license; verify downloads
against the attached `SHA256SUMS`. Release binaries include the admin console and report their exact version through
`/v1/ping`. `ffmpeg` remains an optional external runtime dependency for archive/source installs and is bundled in
the Docker image.

The server's distribution version is independent from the OpenAPI contract version. BlitterAmp desktop releases pin and
build an exact published BlitterServer tag rather than consuming the server's moving `main` branch.

The release workflow also publishes the version without the `v` prefix and `latest` to Docker Hub, for example
`matjam/blitterserver:1.0.3` and `matjam/blitterserver:latest`. It also synchronizes this README to the Docker Hub
overview after the image is published.

## API

The contract in [`api/openapi.yaml`](api/openapi.yaml) is the source of truth — the server implements it, never the other way around. The running server serves the spec at `/api/openapi.yaml` and a documentation viewer at `/docs/`. Errors are `application/problem+json`. Ids are opaque and type-prefixed (`art_`, `alb_`, `trk_`, …); list endpoints use cursor pagination.

## Development

```sh
make check       # gofmt + go vet + go test ./...
make generate    # regenerate internal/api from the spec (oapi-codegen, pinned in go.mod)
make lint-api    # redocly lint of the OpenAPI spec
make gen-check   # verify the spec survives TS + Go client codegen
make run         # go run ./cmd/blitterserver
make web         # build the admin console (node) — make build runs it for you
```

The generated server (`internal/api/api.gen.go`) is committed; CI regenerates and fails on any diff, so spec and server cannot drift. Handlers implement the generated strict interface and embed an `Unimplemented` base — adding an operation to the spec breaks the build until it is implemented or consciously 501'd.

Layout: `cmd/blitterserver` wires `internal/config` → `internal/logging` → `internal/store` (SQLite + goose migrations) → `internal/httpserver` (middleware + generated handler); `internal/server` holds endpoint implementations, with adapters under packages such as `internal/lastfm`, `internal/source`, and `internal/transcode`.

Development is contract-first and test-driven: spec changes land before handlers, and tests land before implementations — including contract tests that drive a real server through the generated client.

## License

MIT
