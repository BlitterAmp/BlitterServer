# Blittarr

The self-hosted backend for [BlitterAmp](https://github.com/BlitterAmp/BlitterAmp) — a middleman service that sits between the BlitterAmp apps and your music world. Blittarr connects to your Plex server (more sources later), transcodes server-side, drives acquisition through your Lidarr, relays last.fm, and keeps taste/follow state consistent across your devices. The apps speak one API: this one.

**Status: API design.** The contract lives in [`api/openapi.yaml`](api/openapi.yaml) and is being iterated before any service code is written. Architecture and decisions of record: [`docs/architecture.md`](docs/architecture.md).

Planned deployment: build from source (Go), prebuilt tarballs, or Docker. Single-tenant, self-hosted.

## License

MIT
