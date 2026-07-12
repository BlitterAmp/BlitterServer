# BlitterServer

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

### Configuration

Precedence, highest to lowest: **flags > environment > config file > defaults**.

| Flag | Env var | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `BLITTER_CONFIG` | (none) | Path to a `blitterserver.yaml` config file |
| `--listen` | `BLITTER_LISTEN` | `127.0.0.1:8484` | Address to listen on |
| `--data-dir` | `BLITTER_DATA_DIR` | `$XDG_DATA_HOME/blitterserver`, else `~/.local/share/blitterserver` | State directory (SQLite database, logs, caches) |
| `--log-level` | `BLITTER_LOG_LEVEL` | `info` | `debug`\|`info`\|`warn`\|`error` |
| (file only) | `BLITTER_LOG_FORMAT` | `text` | `text`\|`json` |

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

Planned deployment options: build from source, prebuilt tarballs, and Docker (with ffmpeg bundled).

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
