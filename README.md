# Blittarr

The self-hosted backend for [BlitterAmp](https://github.com/BlitterAmp/BlitterAmp) — a middleman service that sits between the BlitterAmp apps and your music world. Blittarr connects to your Plex server (more sources later), transcodes server-side, drives acquisition through your Lidarr, relays last.fm, and keeps taste/love state consistent across your devices. The apps speak one API: this one.

**Status: skeleton foundation.** The contract lives in [`api/openapi.yaml`](api/openapi.yaml); the server is generated from it with a CI drift gate, and currently serves real `ping`/`status`/`capabilities` plus 501s for the rest of the surface.

Planned deployment: build from source (Go), prebuilt tarballs, or Docker. Single-tenant, self-hosted.

## Running

```sh
make build
./dist/blittarr
```

The API contract is served at `http://<listen-addr>/docs/`.

### Configuration

Precedence, highest to lowest: **flags > environment > config file > defaults**.

| Flag | Env var | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `BLITTARR_CONFIG` | (none) | Path to a `blittarr.yaml` config file |
| `--listen` | `BLITTARR_LISTEN` | `127.0.0.1:8484` | Address to listen on |
| `--data-dir` | `BLITTARR_DATA_DIR` | `$XDG_DATA_HOME/blittarr`, else `~/.local/share/blittarr` | State directory (sqlite database, logs, caches) |
| `--log-level` | `BLITTARR_LOG_LEVEL` | `info` | `debug`\|`info`\|`warn`\|`error` |
| (file only) | `BLITTARR_LOG_FORMAT` | `text` | `text`\|`json` |

Logs are written to stdout and, by default, to a rotating file at `<data-dir>/logs/blittarr.log`.

## License

MIT
