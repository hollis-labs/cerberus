# Changelog

All notable changes to Cerberus are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Cerberus is pre-1.0 beta software: minor bumps carry additive surface and may carry breaking changes, and there are no compatibility guarantees yet.

This file is backfilled from the git log on a good-faith basis, not exhaustively. It starts from the `v0.4.0-beta.1` release; the history before it (the service-manager TUI and first MCP server in March 2026, the v2 domain model and connectors, the daemon and hot-reload work) lives in the git log.

## [Unreleased]

### Added

- **`os_service` runs on Linux as a systemd user unit.** `supervisor: auto` (and `systemd_user`) now apply, reload, stop, remove, status, inspect and doctor through `systemctl --user`, with the launchd backend's guarantees: idempotent unit writes, restart only when something changed, and startup confirmed by a stable main PID. `resource doctor` warns when the user does not linger.
- **`cerberus install` / `uninstall` on Linux** write and remove the systemd user unit `com.hollis-labs.cerberus.service`, retiring a hand-written `cerberus.service` first. `cerberus daemon status` reports `origin: systemd`, and `daemon start|stop|restart` go through `systemctl --user` for a systemd-managed daemon. `--override-supervisor` is the new name for `--override-launchd`, which still works.
- **A relative `dir:` in a registered descriptor resolves against the descriptor's own directory**, so `dir: .` names the repo wherever it is checked out.

### Changed

- Adopt the published `libs/util`, `libs/ui-go` and `libs/plugin-mcp` modules; building now requires Go 1.26.8.
- Require protocol-2 plugin Init with a fresh host-issued incarnation, explicit no-reverse-authority grant set and separate writable data/cache roots. Protocol-1 plugins must migrate; Cerberus does not offer reverse host RPC or hooks.

- **A launchd plist or systemd unit whose resource sets no `PATH` gets the serving daemon's PATH** instead of the supervisor's minimal one. On macOS, an `os_service` resource without `PATH` in `env` reloads once on its next apply to pick it up.
- A resource build or install's `make` no longer inherits `MAKEFLAGS`/`MAKELEVEL` from an enclosing make, which added `Entering directory` lines to captured output.

- **The `github` connector is a plugin now** (breaking). The core is exactly `local`, `ssh` and `docker`; no provider SDK remains in the host binary.
- **Vercel deployments are the `vercel` plugin now** (breaking). The console's deploy-profile runner (`internal/infra`), its `/api/infra` and `/api/deployments` routes, the Deployments page and the `profile_save`/`profile_delete` console writes are removed. Profiles move to a file the plugin reads (`profiles_file` in `connector-config.yaml`); `vercel/token` and `vercel/scope` keep their names.
- **The console's credential editor lists what connectors declare.** `/api/credentials` and a Credentials page replace the hardcoded provider catalog; `provider_save` writes only a declared secret of an installed connector and refuses any other id, key or setting.

### Fixed

- A plugin secret declared without a description came back over the socket with its name redacted; a declared secret's `name`, `kind` and `env` are now names by schema.

## [0.5.0-beta.1] - 2026-09-30

A security-hardening release: the live-systems security model (policy, approvals, audit, redaction, plugin isolation) landed together with the extraction of provider connectors into plugins.

### Added

- **Policy engine** with postures, scoped enforcement, rate limits as a rule obligation, egress rules (pass, cap, mask or refuse labeled output, shadow mode first), and a policy report for the switch-on decision. `cerberus posture show|set|reset`.
- **Approvals.** Plan hashes and consume-by-approval-id; resource, pipeline and deploy actions bind to a plan (`resource plan`, `pipeline plan`, `connectors plan`); confirm on your own terminal or in the console; out-of-band approval with a passkey; grants (session and window approvals) and break glass; the agent's side of approvals over MCP, including URL elicitation.
- **Emergency brakes.** Lockdown and freeze, and a circuit breaker that suspends agent sessions.
- **Audit log** (append-only, hash-chained) covering the admin lane, runtime, pipelines, deploy profiles, the monitor and plugin lifecycle, with an `audit` CLI.
- **Request principals.** Every request carries a principal and the socket verifies its peer.
- **`cerberus-presence`**, a helper that requires the person at the Mac before a passkey enrollment, shipped in the release and reported in `cerberus status`.
- **`cerberus status`**, the operator's state at a glance, and target labels (`env`, `owner`, `admin`, `tags`).
- **`mcp-http` as an OAuth 2.1 resource server** with a built-in operator-issued token issuer (`mcp-http token issue|list|revoke`); the console signs in with a one-time link.
- **Secrets.** Vault references routed to secret-backend plugins, per-target credential bindings, `run-secrets` resolving vault references, and request-scoped value redaction so a resolved credential never reaches any output surface.
- **Plugins.** `cerberus connectors exec` runs any operation with typed arguments; plugin operations reach MCP as generated, default-deny tools; `connector-config.yaml`; capabilities a plugin declares instead of ambient access (`ssh_agent`, `docker_socket`); deadlines, output caps and process limits; output labels (`untrusted`, `personal`) carried into MCP results; install is an in-process TTY review and the bundle digest is checked on every load.
- Recursive SSH directory transfer.

### Changed

- **Provider connectors are plugins** (breaking): `cloudflare`, `digitalocean`, `namecheap` and `forge` moved out of the host, taking the stripped build from 62.8MB to 21.7MB.
- **The launchd label is now `com.hollis-labs.cerberus`**, with a migration from the earlier label on `cerberus install`.
- `--listen` takes `localhost` or a literal loopback IP only; `web` and `mcp-http` refuse non-loopback binds and DNS rebinding.
- Unmarked callers are treated as remote, so local writes need `--ack`; dry runs never execute and the acknowledgment gate fails closed.
- `mcp-http` requires authentication by default; `--no-auth` is allowed only on a single-account machine.

### Fixed

- Approvals show the command that will run, bind scope, expiry, channel and target, and a deploy builds the tree it was approved on.
- The audit log fails closed on a broken chain and survives an oversized line.
- Redaction no longer eats Cerberus's own guidance, connector and plugin ids, or protocol fields such as WebAuthn `allowCredentials`.
- The Docker connector's `stop` runs `compose stop`, not `compose down`; ad-hoc docker targets run only in the operator's shell.
- Plugins cannot claim a built-in connector id, and only the host's own apply records are trusted.
- Live port conflicts are checked before a launchd start.

## [0.4.0-beta.2] - 2026-09-16

### Added

- Infra admin control-plane lane: SSH file transfer, Docker fixes, and the plugin lane (#28).
- Host-side resolution of a plugin's declared secrets.
- `project.id` formalized as the portfolio slug, with `capabilities` and `links` in the registry; project props served from the runtime service.

### Changed

- Secret references resolve in the service's own process rather than being written into the launchd plist.

### Fixed

- Launchd recovery: startup-timeout evidence is preserved, removed jobs drain before bootstrap, bootstrap retries back off, and an unconfirmed artifact activation no longer triggers an automatic restart.
- Artifacts are replaced atomically and launchd startup is verified; builds lock the source tree through activation and honor pinned toolchains.
- The serving daemon refuses mutations targeting itself; dev sessions require launch ownership before stopping.
- Credentials are redacted across operator output boundaries; connector credential references resolve per operation.
- Duplicate local port assignments are rejected before activation; unknown config keys are surfaced at author time and tolerated at runtime.

## [0.4.0-beta.1] - 2026-05-25

### Added

- Beta readiness and the install pattern: Homebrew tap, checksummed release tarballs, `docs/install.md` and the beta release process.
- Opt-in resource registry: per-app `*.cerberus.yaml` descriptors with a resolver.
- Plugin host and daemon robustness; DigitalOcean droplets through the shared connector flow.
- Deploy surfaces build output and persists `build.log`; `install_after_build` chains `make install` after `make build`.
- MCP list-tool responses are budgeted.
- Web console moved to sysop-ui panel primitives and DataTable.

### Changed

- `cerberus.db` moved onto XDG path resolution.
- Artifact staleness is driven by source-binary content hash only.

[Unreleased]: https://github.com/hollis-labs/cerberus/compare/v0.5.0-beta.1...HEAD
[0.5.0-beta.1]: https://github.com/hollis-labs/cerberus/compare/v0.4.0-beta.2...v0.5.0-beta.1
[0.4.0-beta.2]: https://github.com/hollis-labs/cerberus/compare/v0.4.0-beta.1...v0.4.0-beta.2
[0.4.0-beta.1]: https://github.com/hollis-labs/cerberus/releases/tag/v0.4.0-beta.1
