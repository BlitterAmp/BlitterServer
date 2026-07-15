# BlitterServer Agent Instructions

Read and follow `CLAUDE.md` and the AgentOS vault instructions before changing this repository.

## API Compatibility

- BlitterAmp desktop depends on the BlitterServer `1.x` API for the foreseeable future. Every `1.x` server release must
  remain backward compatible with existing desktop clients.
- Prefer additive contract evolution: add optional fields, operations, and enum values without removing or redefining
  existing behavior. Preserve existing authentication, error, pagination, and resource semantics.
- Do not ship a backward-incompatible API or a new major version without the user's explicit approval. A conventional
  commit marker, Release Please proposal, or technically passing build is not approval.
- When requested work would break compatibility, stop before implementing or releasing it and identify the incompatible
  behavior, affected clients, migration options, and required major release for the user.
- Never merge a major-version release PR, add a `Release-As` major footer, or publish a major release unless the user has
  explicitly approved that specific break and release.
- Run `make compat-api` for contract changes. CI compares `api/openapi.yaml` with the latest published `v1` contract and
  rejects breaking changes.
