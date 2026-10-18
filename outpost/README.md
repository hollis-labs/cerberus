# Outpost

A small per-machine host for trusted subprocess plugins. Outpost starts the
configured binaries, routes authenticated HTTP requests to them and exits when
a plugin becomes unhealthy so the machine's supervisor can restart it.

> **Pre-release.** Outpost is new and being built in the open. This documentation
> describes what exists today; interfaces and behavior can change without notice,
> and there are no compatibility guarantees or tagged releases yet.

Outpost builds on the public [app-host](https://github.com/hollis-labs/libs/tree/main/app/app-host),
[plugin-mcp](https://github.com/hollis-labs/libs/tree/main/mcp/plugin-mcp) and
[auth](https://github.com/hollis-labs/libs/tree/main/security/auth) libraries.
[Tether](https://github.com/hollis-labs/tether) and
[Cerberus](https://github.com/hollis-labs/cerberus) provide optional peer plugins
for sessions/mail and native service operations. Their binaries are installed
separately; Outpost imports neither application.

## Install

Requires Go 1.26.9 or later. Linux and macOS are the intended host platforms.

```sh
go install github.com/hollis-labs/tools/outpost/cmd/outpost@latest
outpost version
```

From this module's directory, `make build` creates `bin/outpost`. Source builds
report `dev` unless you supply `make build VERSION=<version>`. Use
`go version -m /path/to/outpost` to inspect embedded Go build information.

## Configure

The host reads an operator-owned JSON file. Replace the example paths with
absolute paths to your installed plugin binaries and private state directories:

```json
{
  "address": "127.0.0.1:8977",
  "state_dir": "/absolute/outpost/state",
  "credentials_file": "/absolute/outpost/pairings.json",
  "plugins": [
    {
      "id": "tether",
      "command": "/absolute/releases/tether-worker",
      "args": ["plugin"],
      "dir": "/absolute/worker",
      "env": ["HOME=/absolute/home", "PATH=/usr/local/bin:/usr/bin:/bin"],
      "config": {"worker_root": "/absolute/existing-worker-state"}
    },
    {
      "id": "cerberus",
      "command": "/absolute/releases/cerberus-outpost",
      "dir": "/absolute/home",
      "env": ["HOME=/absolute/home", "PATH=/usr/local/bin:/usr/bin:/bin"],
      "config": {"protected_units": "outpost.service"}
    }
  ]
}
```

Paths must be absolute. Each plugin gets exactly its declared environment,
configuration, data directory, and cache directory. The Tether plugin retains
the existing enrollment, device token, mail registrations, provider commands,
and shim state at `worker_root`.

## Pair and run

Flags must come before the subcommand. The local operator pairs a controller
to one plugin:

```sh
outpost --config /absolute/outpost.json --id controller \
  --plugin cerberus --output /private/controller.token pair
outpost --config /absolute/outpost.json run
outpost --config /absolute/outpost.json --id controller revoke
```

Only a credential hash is stored in `pairings.json`. Deliver the new private
token file to the controller over the existing authenticated deployment
channel. Revocation takes effect on the next request. Credentials are not
printed, placed in child environments, or forwarded to plugins.

Authenticated requests use `/api/plugins/{id}/…` and require scope
`outpost.{id}`. `/healthz` is an unauthenticated readiness endpoint. Plain HTTP
binds only to literal loopback or Tailscale-range addresses. The host cannot
verify interface ownership or encryption; configure Tailscale correctly or
use an HTTPS proxy with a loopback backend. See [SECURITY.md](SECURITY.md). The plugin inventory starts completely before HTTP admission.
A failed plugin makes the host exit so its native supervisor can restart it.

The Tether worker binary supports `plugin` as well as its existing enrollment,
mail, shim, and relay commands. Cerberus's `cerberus-outpost` binary implements
the fixed `probe`, `units`, `logs`, `start`, `stop`, and `restart` protocol.
Cerberus performs planning, approval, auditing, and output redaction; its plugin
executes native user-service commands and refuses mutations of the control
plane carrying its request.

## Deployment

For deployment, stage immutable plugin binaries and switch the inventory to
their exact paths. Preserve the prior inventory and native service definition
for rollback. Stop the standalone worker before starting Outpost, since both
claim the same mail socket and enrollment. On Linux use `KillMode=process`;
on macOS avoid switching while sessions are live. SSH is the recovery and
self-upgrade route. The host can be supervised by an existing Cerberus resource
or by systemd/launchd on machines without a local Cerberus controller.


`scripts/stage-machine.py` is an optional migration helper for operators who
already have an enrolled Tether worker. It requires Python 3 and a trusted bundle
containing `outpost`, `tether-worker`, `cerberus-outpost` and `SHA256SUMS` in
`<sha256>  <filename>` format. Checksums detect corruption; obtain the bundle and
manifest through your authenticated deployment channel. Run `python3
scripts/stage-machine.py --help` for options. It writes inventory and supervisor
configuration but does not stop, start or enroll services. Existing files receive
one `.before-outpost` backup; keep your own backups for later migrations. The
Cerberus plugin's `protected_units` are written for the platform: the systemd
unit names on Linux, the launchd label on macOS.

`scripts/activate-bundle.py --bundle <dir>` switches an already staged bundle
in. It verifies the checksums, backs up `outpost.json`, repoints every plugin
command, restarts the supervisor and requires the host to stay running for a
settle period (`--settle`, default 15 seconds; optionally also `--health-command`
exiting 0). If anything fails it restores the previous inventory and restarts
the supervisor again, exiting non-zero. It restarts every plugin, so check the
machine's live sessions first. Tests: `python3 -m unittest discover -s scripts/tests`.

## Current limits

- Plugins share the operating-system user's authority and must be trusted.
- Pairing grants a whole plugin, not individual routes or operations.
- HTTP request bodies are buffered and limited to 4 MiB. Streaming/SSE is not
  supported by this route adapter.
- Plugin health is checked every five seconds with a three-second timeout per
  plugin. A failed plugin restarts the whole inventory through the supervisor.
- Native supervision and deployment are operator-owned. On macOS, live-session
  survival across a host migration has not been established; migrate while idle.

## Development and license

See [CONTRIBUTING.md](CONTRIBUTING.md), [CHANGELOG.md](CHANGELOG.md) and
[SECURITY.md](SECURITY.md). Licensed under the MIT license; see the repository's
LICENSE file.
