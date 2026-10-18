# Changelog

Outpost is new software being built in the open. There are no tagged releases
or compatibility guarantees yet.

## Unreleased

### Added

- Explicit optional plugins with isolated admission/health failures, authenticated
  unavailable responses, and aggregate readiness at `/api/readyz`.
- Shared per-machine host for independent subprocess plugins, with scoped
  controller pairing, immediate revocation and plugin health supervision.
- Public usage, security and contribution documentation under the MIT license.
- Linux and macOS CI with race tests, vet, lint and vulnerability scanning.

- `scripts/activate-bundle.py`: switch a staged bundle in with a backup, a
  settle check and automatic rollback of the inventory on failure.

### Changed

- Bundle activation now requires all configured plugins healthy in the supervised
  host, even optional plugins. Hosts without the readiness endpoint are refused.
- Move the independent Go module to `github.com/hollis-labs/tools/outpost`,
  preserving source history and adopting the shared repository license.
- Clarify the operator's responsibility for encrypted transport and trusted
  plugins; the listener validates address ranges, not interface ownership.

### Fixed

- `scripts/stage-machine.py` wrote the macOS launchd label into the Cerberus
  plugin's protected units on every platform; the plugin rejects it on Linux
  and the host then refused to start. Names are now platform-specific.
- Reject trailing configuration data instead of silently ignoring it.
- Refuse using the pairing store itself as the credential output file.
