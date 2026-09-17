# Infra Admin Control Plane

Direction for making Cerberus the single place we administer the
infrastructure we actually run — local dev services, the Adtran work host, and
the Azure dev box — from the CLI, the console, and agents.

Status: agreed direction, execution started 2026-09-16.

## What this is, and what it is not

This is about **imperative administration**: ssh, file transfer, deploys,
settings changes, creating and tearing down hosts, reading state. Run a verb,
get a result.

It is **not** about supervision. Cerberus already has a supervision lane —
`resources:` with `auto_restart`, health probes, launchd, artifact staleness —
and that lane is for long-lived local workloads. A container on a remote host
does not belong in it. We are not trying to make Cerberus watch remote
containers.

That distinction decides the architecture, so it is worth being blunt about:
**the work is breadth of verbs, not new architecture.**

## The lane we build in

`internal/cerbapi/external_connector_service.go` is already the right home. It
is imperative, stateless, resolved per call, and it already carries the
furniture an admin tool needs:

| Capability | Where |
|---|---|
| `Destructive: true` → operator ack required | `requireAcknowledgment` |
| `SupportsDry: true` → `--dry-run` preview | `dryRunPreview` |
| Progress + message notifications | `gmcp.NotifyProgress` / `NotifyMessage` |
| Secret redaction on error paths | `redact.Text` |
| Per-call connector resolution | `registry.Resolve` |

A verb added here is a CLI command and an HTTP API operation at once. The MCP
tool is **not** free for a built-in connector: plugin connectors get generated
names via `plugin.ToolNameForOperation`, but the built-ins have hand-written
tools in `internal/mcp/tools_<connector>.go` that must also be registered in
both `cmd_mcp.go` and `cmd_daemon.go`. Budget four touch points per verb —
operation definition, service dispatch, CLI command, MCP tool — plus a
`decodeConnectorPayload` case if the operation returns a typed DTO rather than
a string, or the CLI gets `unexpected result type json.RawMessage` over the
socket.

**Explicitly deferred:** letting `type: container` through
`resource_runtime_service.go`. That service is hardcoded to local/process in
roughly ten places. Changing it buys supervision we do not want. A
container/docker resource currently *validates clean* and then fails on every
runtime operation — see Open Questions for what to do about that trap.

## What we manage

**muctlvaig.corp.adtran.com** — Adtran work host, reachable only on VPN. Runs
the ContextForge MCP gateway (`contextforge-gateway:4444` on the
`agent-gateway-net` docker network), `workday-mcp`, `ess-agent-workday`,
`ess-teams-bot`, and the Nanite demo. Two nginx layers: host nginx routes by
hostname, the `nginx-router` container routes by path.

**Azure dev box** — pending provisioning. Development projects, testing,
experiments. Docker will run here too.

**Local** — the dev services in `~/.cerberus/config.yaml`, plus Docker Desktop.

## The capability spec already exists

`~/Projects/tools` is the specification for this work. It is ~70 shell scripts
that already do the job, written against the real systems, with the failure
modes documented in-line. We are not designing from scratch; we are promoting
proven behaviour into typed connector operations.

What it establishes, and what it maps to:

| `tools/` behaviour | Becomes |
|---|---|
| `rsh` — run a command on the host | `ssh.exec` (exists) |
| `rput` / `scp` — push a file | `ssh.put` (done) |
| `rsudo` — run as root, needs a tty | `ssh.exec` with elevation — missing |
| `ssh_open` ControlMaster multiplexing | Native: one `*ssh.Client`, many sessions |
| `docker load` / `docker ps` on the host | Remote docker via `DOCKER_HOST` (done) |
| `30-configure.sh`, `35-settings.sh` — API writes | App-specific connectors |
| ContextForge gateway/server/tool admin | ContextForge connector |
| `probe-*` — read-only, never prints a secret | Non-destructive operations |
| `out/<date>/<script>-<time>.log` run logs | Operation result capture |

Two conventions from `tools/` worth carrying over verbatim, because they are
good and they are already habits: **`probe-*` never changes anything and never
prints a secret value** (names only), and **anything destructive asks first.**
The second already exists as `Destructive` + `--ack`.

### One correction to `tools/`

`tools/README.md` says SSH to muctlvaig is password auth. That is stale — key
auth works now, verified 2026-09-16:

```
$ cerberus ssh status muctlvaig
{"reachable": true, "latency": 1836625167, "os": "Linux"}
```

This matters because the whole ControlMaster/`ControlPersist=15m` apparatus in
`lib/common.sh` exists to avoid repeated password prompts. With key auth and a
Go SSH client holding one `*ssh.Client`, multiplexing is native and that
complexity disappears rather than being ported.

## Libraries

Use a library rather than writing one, in every case where a good one exists.

| Need | Library | Note |
|---|---|---|
| File transfer | `github.com/pkg/sftp` v1.13.11 | Rides the existing `golang.org/x/crypto/ssh` client |
| SSH transport | `golang.org/x/crypto/ssh` | Already a direct dependency |
| Local port forward | `golang.org/x/crypto/ssh` native | `client.Dial` + a local listener; no new dep |
| ContextForge | `github.com/leefowlercu/go-contextforge` v0.9.0 | Community, not IBM — see below |
| Azure | `github.com/Azure/azure-sdk-for-go` | Mirrors how `godo` is used for DigitalOcean |
| Docker | docker CLI, as today | `DOCKER_HOST=ssh://` gets remote for free |

### On the ContextForge SDK

IBM's [mcp-context-forge](https://github.com/IBM/mcp-context-forge) is
Python/FastAPI and ships no official Go client.
[`leefowlercu/go-contextforge`](https://github.com/leefowlercu/go-contextforge)
is a community SDK at v0.9.0 (14 releases) covering exactly the admin surface
we need: tools, resources, **gateways** (the upstream registrations — per our
own notes the only place auth can be set), virtual servers, prompts, and A2A
agents. Bearer JWT auth, which matches what our probes found: CF accepts
`Authorization: Bearer <JWT>` only; `X-API-Key` and a raw token both 401.

Pin it, and put it behind the same `Backend` interface the DigitalOcean and
Docker connectors already use. That is what makes a v0.x dependency cheap: if
it churns or goes unmaintained we swap the backend implementation and the
operation, CLI and MCP surface does not move. CF also serves `/openapi.json`,
so a generated client is the fallback. Do not hand-roll one.

CF is only reachable through the tunnel at `127.0.0.1:14444`, or on the box, so
the connector needs a configurable base URL and depends on the tunnel being up.

## Core or plugin

Cerberus ships as a single installed binary. What is in that binary is the
primitives the control plane is built on — `local`, `ssh`, `docker`, `github`.
Provider integrations that are optional per user, carry a vendor SDK, and ship
on someone else's schedule are plugins: `cloudflare`, `digitalocean`, `forge`,
`namecheap`, and the new ContextForge and Azure connectors.

Our plugins live in `hollis-labs/cerberus-plugins`, one directory each. Third-
party plugins are standalone repos. The full table, the migration order, and why
the four existing provider connectors stay compiled for now are in
`docs/plans/connector-work-packages.md`.

The plugin lane is real and working — install, load, execute and uninstall were
verified end to end against the generated Docker plugin prototype on
2026-09-16 — and writing a plugin outside this repo is now possible: the
authoring contract lives in `pkg/plugin` (WP-2, 2026-09-16). A plugin module
imports `pkg/connector`, `pkg/resource`, `pkg/plugin` and
`plugin-sdk/subprocess`; the host half — manager, installer, trust policy,
subprocess launcher — stays in `internal/pluginhost` and stays unreachable.

## Current verb inventory

| Connector | Operations |
|---|---|
| cloudflare | zones, DNS CRUD |
| digitalocean | droplet list/get/create/start/stop/destroy |
| forge | servers, sites, deploy_site, exec_site_command, deployment script get/update |
| github | status, releases, workflow runs |
| namecheap | domain list/status, DNS |
| ssh | status, exec, **put**, **get**, stop |
| docker | ps, logs, start, stop, destroy |

Gaps: no ContextForge, no Azure, no recursive transfer, no privilege
elevation. Remote docker landed 2026-09-17 as host selection on the existing
docker operations rather than as new verbs.

Work is broken into agent-sized packages in
`docs/plans/connector-work-packages.md`.

## Work items

### 1. Unbreak the Docker connector — DONE 2026-09-16

Every Docker call through Cerberus failed:

```
$ cerberus docker ps
Error: daemon: docker list_containers: connector_unavailable:
       docker connector: docker CLI not found in PATH
```

Three compounding causes:

1. **The daemon's PATH.** `~/Library/LaunchAgents/com.fragments-engine.cerberus.plist`
   declares no `EnvironmentVariables`, so launchd gives the daemon
   `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. Docker Desktop's CLI is at
   `/usr/local/bin/docker`. Confirmed with `ps eww`.
2. **Eager registration caches the failure.** `internal/app/app.go` registers
   Docker with `dockerconn.New()` at boot and stores the error via
   `RegisterUnavailable`. Docker is the only connector that is both eager and
   fallible — the other five use `RegisterFactory`, and SSH is eager but cannot
   fail to construct. The failure is then cached for the daemon's lifetime.
3. **The LIVE column hides it.** `cerberus connectors list` reports
   `docker … LIVE: yes`, because that column is computed in the *CLI process's*
   registry (`cmd_connectors.go`), which has the user's full shell PATH, and is
   merged over the daemon's answer. The one command you would use to check
   reports healthy while every real call fails.

Fix, in order of what actually matters:

- **`DetectDocker()` gains fallback search paths** — `/usr/local/bin/docker`,
  `/opt/homebrew/bin/docker`, `~/.docker/bin/docker` — plus an optional
  configured override. This is the fix that works on every machine with no
  per-machine plist editing.
- **Convert Docker to `RegisterFactory`** so resolution reruns per call. Docker
  Desktop being off becomes a per-call error an agent can read and act on,
  instead of a permanent startup condition, and recovery needs no daemon
  restart.
- **Make the LIVE column reflect the daemon.** Not optional cleanup: `Configured()`
  returns true when *either* an instance or a factory is registered, so the
  factory change alone makes LIVE unconditionally true. It must ship together.
- Check `AvailabilityError("docker")`, which returns nil once the `unavailable`
  entry is gone.

Note that `RegisterFactory` **alone does not fix this bug** — `exec.LookPath`
still reads the daemon's minimal PATH and still fails. The fallback paths are
the actual fix; the factory change is what makes it recoverable.

**Verified after the change**, with the daemon still on launchd's minimal PATH
(`ps eww` confirms `PATH=/usr/bin:/bin:/usr/sbin:/sbin`), so this is the real
condition and not a repaired environment:

```
$ cerberus docker ps
ID            NAME                  IMAGE             STATUS               PORTS
5c3cde0809e2  mtbf-monitor-caddy-1  caddy:2           Up 7 days            443/tcp (+3)
d478e203778f  mtbf-monitor-app-1    mtbf-monitor-app  Up 7 days (healthy)  8000/tcp
```

The MCP tool `cerberus_docker_ps` returns the same. `LIVE` is now computed by
the daemon via `Registry.Probe`, so it reports what the process that runs the
operation can actually construct:

```
docker        container   yes
ssh           server      yes
digitalocean  server      no     # truthful: no API token, ops fail credential_missing
```

`TestDetectDockerFindsBinaryWithEmptyPATH` is the regression test — it clears
`PATH` entirely and asserts discovery still succeeds.

### 2. SSH file transfer — DONE 2026-09-16

`ssh put` and `ssh get` over `github.com/pkg/sftp` v1.13.11, reusing the
existing `APIBackend` SSH client — the SFTP subsystem opens as another channel
on the connection, so a transfer costs no second handshake.

`put` is `Destructive` + `SupportsDry` (it overwrites a file on a real host);
`get` is read-only and deliberately needs no ack, or the gate stops meaning
anything.

Both write to a temporary name and rename into place, so an interrupted
transfer leaves the previous file intact rather than a truncated one — which is
the whole point when the target is a compose file or an env file something is
about to read. `put` preserves the local mode, so an uploaded script stays
executable.

Verified end to end against muctlvaig:

```
$ cerberus ssh put muctlvaig ./probe.txt /home/cburks/cerberus-probe.txt --dry-run
{ "summary": "Would upload a local file over SFTP, replacing the remote file if it exists.",
  "input": { "bytes": 52, "mode": "-rw-r--r--" }, ... }

$ cerberus ssh put muctlvaig ./probe.txt /home/cburks/cerberus-probe.txt --ack
./probe.txt → /home/cburks/cerberus-probe.txt (52 bytes)

$ cerberus ssh get muctlvaig /home/cburks/cerberus-probe.txt ./roundtrip.txt
# diff against the original: identical; remote mode preserved as -rw-r--r--
```

MCP tools `cerberus_ssh_put` and `cerberus_ssh_get` are registered alongside.

This replaces `rput`/`scp` in `tools/lib/common.sh`. Still missing for a full
`tools/` port: recursive directory transfer, and the `rsudo` elevation model
(see Open Questions).

### 3. Remote Docker — DONE 2026-09-17

`DOCKER_HOST` / `docker context` support on the Docker connector, so the same
operations target a remote daemon. Host-agnostic: one implementation serves
both the Azure box and muctlvaig.

Shipped as `docker.Target`, resolved per call from the operation's config:
`--host`/`-H` and `--context` on every `cerberus docker` command,
`docker_host`/`docker_context` on the MCP tools. Host and context are refused
together rather than resolved, because the CLI resolves that conflict silently
in favor of `--context`.

The non-obvious half was the error text. The docker CLI reports **every**
`ssh://` failure as `Cannot connect to the Docker daemon at
http://docker.example.com` — a placeholder host, identical whichever machine was
asked for, blaming a daemon that is usually running fine — and surfaces the real
cause only at `--log-level debug`. The connector turns debug logging on for
`ssh://` targets alone and recovers the cause from it, so a permission failure
now names the host, the cause and the `docker` group. Full detail and the
verification runs are in WP-3 of `docs/plans/connector-work-packages.md`.

**Constraint on muctlvaig:** `cburks` is not in the `docker` group there —
verified 2026-09-16:

```
$ cerberus ssh exec muctlvaig --ack -- 'id; docker ps'
uid=12989(cburks) gid=11000(hsv-all) groups=11000(hsv-all),20922(muctlvaig)
permission denied while trying to connect to the docker API at unix:///var/run/docker.sock
```

So remote docker there needs either group membership (see Open Questions) or
sudo. Until then, introspect ContextForge over HTTP rather than via `docker ps`
— which is what `tools/` already does.

**On the Azure box: add the user to the `docker` group at provisioning time.**
It is greenfield, so this is free there, and it is what makes remote Docker work
without sudo.

### 4. ContextForge connector

On `go-contextforge`, behind a `Backend` interface, JWT from the existing
secret provider (`keychain://`), configurable base URL.

### 5. Azure connector

On the DigitalOcean shape: `Backend` interface, SDK, secret provider, typed
operations. Worth it primarily for VM start/stop — deallocated Azure VMs stop
billing compute, so this is real cost control, not just convenience.

### 6. Tunnels as managed resources — DONE 2026-09-16

`tunnel-muctlvaig` was running as a detached `ssh -N -f` started outside
Cerberus, so `resource list` showed blank status while both forwards were
demonstrably open. The manual process was killed and the resource started with
`cerberus resource apply tunnel-muctlvaig`; it now reports `running`, and CF
(`:14444/health`) and Nanite (`:18095/api/health`) both answer 200 through it.

Note the dependency this creates: the CF connector in item 4 is unreachable
whenever this tunnel is down, and the tunnel is deliberately `auto_start: false`
/ `auto_restart: false` because a Cerberus-started `ssh` has no TTY and a
restart loop against corporate auth risks an account lockout. A CF operation
failing with a connection refused on 14444 means "start the tunnel", not "CF is
down" — worth surfacing in the connector's error text.

Two other SSH processes to muctlvaig are unrelated and were left alone: an
interactive session, and the `ControlPersist` sftp master that `tools/` opens.

## Open Questions

### ~~Team impact of the Docker connector change~~ — RESOLVED 2026-09-16

Decided and approved by the owner: ship it. Cerberus has no users outside this
machine yet — it is being prepared to share, not already shared — so there is no
installed base to coordinate with and no scripts in the wild to break. Landing a
semantic change *before* anyone depends on it is the cheapest moment to do it.

Recorded because the behaviour is non-obvious, not because it still needs a
decision:

- **`connectors list` `"live"` means "constructible right now", not
  "registered".** Connectors with no credentials configured correctly report
  `no`. User-facing docs should say "usable now", never "installed".
- **Connector errors surface per call rather than at daemon startup**, so a
  connector that becomes available later works without a daemon restart.

General principle worth keeping while the app is pre-share: prefer the correct
semantic now over a compatible one, and spend the freedom deliberately before it
expires.

### Should `cburks` be added to the `docker` group on muctlvaig?

It would unlock remote Docker administration there without sudo, which is a
meaningful capability gain. It is also a privilege escalation on a corporate
host and is not ours to grant. Needs whoever administers that box.

### What to do about container resources validating clean but failing at runtime

A `type: container` / `connector: docker` resource passes `cerberus validate`
today and then fails on every runtime operation. Options: reject it in registry
validation with a message pointing at the connector operations, or warn. Leaning
reject — a clean validation that cannot run is the worst of both.

### Elevation model for `ssh.exec`

`tools/` uses `rsudo` with `-t` because sudo on muctlvaig requires a tty, and it
quotes each argument because `"sudo $*"` silently mangled a display name
containing a space. Whatever we build needs a deliberate answer for privilege
elevation, not an accidental one.
