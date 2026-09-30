# Security policy

## Supported versions

Cerberus is pre-1.0 beta software. Security fixes are made on `main` and the newest tagged beta release. Older betas do not receive backports. Upgrade notes are in [`CHANGELOG.md`](CHANGELOG.md).

## Report a vulnerability

Do not include an exploit, token, credential, audit log or other sensitive material in a public issue.

Use GitHub's private vulnerability-reporting flow when the repository's Security tab offers it. If it is unavailable, contact a repository maintainer privately through a contact channel published on the Hollis Labs organization or maintainer profile. Include:

- the affected version or commit and operating system
- which surface was involved (CLI, daemon socket, web console, `mcp`, `mcp-http`, a plugin)
- reproduction steps and the security impact
- whether credentials, hosts or remote systems could be reached or changed
- a safe way to contact you about coordination

Maintainers will acknowledge a private report, investigate it, and coordinate disclosure; response times are best effort during the beta.

## Deployment boundary

Cerberus is a control plane: its reach is whatever its operator configures, which may include production hosts, clusters and SaaS APIs. Treat the machine that runs it, and the `~/.cerberus/` directory, as sensitive.

- **Local daemon socket.** The daemon listens on a Unix socket and verifies the peer's account; a request carries a principal. Other local accounts are not trusted.
- **Web console.** Binds to loopback only, signs in with a one-time link, and approves console writes in the daemon with a passkey. It does not provide TLS.
- **`cerberus mcp`** (stdio) runs as the invoking user over the daemon socket. Use it for local clients.
- **`cerberus mcp-http`** serves the same tools over HTTP. A TCP connection cannot identify a local account, so it requires authentication: an OAuth 2.1 resource server with bearer tokens bound to its endpoint, on loopback too, and TLS off loopback. `--no-auth` is permitted only on loopback on a single-account machine. The easy way to reach it remotely is to keep it on loopback and use an SSH forward or a private network. See [`docs/mcp-http.md`](docs/mcp-http.md).
- **Acknowledgment is an intent gate, not a human one.** `--ack` records that the caller meant the operation; it does not prove a person approved it. Where a human must approve, use the approval and passkey flow described in [`docs/policy.md`](docs/policy.md) and [`docs/plans/live-systems-security-target.md`](docs/plans/live-systems-security-target.md).
- **Agents.** Agents that drive Cerberus over MCP act with the authority the operator grants. Tool results mark untrusted and personal fields, but an agent that reads untrusted text can be steered by it; scope tokens and policy accordingly, and use a read-only credential for any target you do not own.

## Secrets

Resources name credentials by reference (`keychain://`, `keyring://`, vault schemes through secret-backend plugins); they do not carry them. Secrets do not travel in the environment, and a plugin receives only the secrets it declared. Resolved credentials are registered with a per-request redaction scope and removed from every output surface, but redaction of text Cerberus did not compose (vendor, remote or child output) is best effort. See [`docs/secrets.md`](docs/secrets.md).

## Plugins

A plugin is a subprocess that runs with the operator's privileges on the host. Install is a TTY review of what the plugin declares, the bundle digest is checked on every load, and a plugin receives only the credentials and capabilities (`ssh_agent`, `docker_socket`) it declared and the host granted. This is not a sandbox: a plugin you install can do what your account can do. Install only plugins you have reviewed. See [`docs/plugins.md`](docs/plugins.md).

## Audit and data at rest

Administrative operations and plugin lifecycle events are written to an append-only, hash-chained audit log under `~/.cerberus/audit/`; Cerberus fails closed on a broken chain. State lives in local SQLite and YAML under `~/.cerberus/` and may name hosts, targets and credential references. There is no at-rest encryption beyond what the OS credential store provides for secrets. Protect the directory and any backups of it.

## Current security limitations

- macOS-first; on Linux the daemon and `os_service` resources run as systemd user units, which is newer and less exercised than launchd
- release binaries are unsigned betas
- no built-in TLS for the web console; `mcp-http` TLS is operator-configured
- plugins are not sandboxed
- acknowledgment is not human approval; policy enforcement and approvals are still being extended across resource mutators, pipelines and the audit CLI
- no at-rest encryption of Cerberus's own state
- pre-1.0 contracts

These are deployment constraints, not hidden roadmap promises. Operate within them or place Cerberus behind controls that provide the missing boundary.
