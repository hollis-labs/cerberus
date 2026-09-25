---
id: "CERB-CAP-202"
class: "capability"
name: "Docker connector"
summary: "Runs container and Compose operations against the local Docker daemon or a declared docker resource's target; ad-hoc targets run only in the operator's shell."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.9
confidence_label: "ps and logs verified live through the daemon; target rules, stop/destroy argv and schemas re-read on main after P0 (#48 to #54); destroy unrun"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/docker/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "compose"
  - "connector"
  - "container"
  - "docker"
  - "launchd-path"
  - "per-call-resolution"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "One of the two connectors the control plane is built on"
  - type: "depends_on"
    target: "CERB-CAP-201"
    note: "Remote targets ride the ssh:// transport"
  - type: "relates_to"
    target: "CERB-DEC-296"
    note: "Per-call resolution is the fix for the boot-time caching outage"
  - type: "blocks"
    target: "CERB-GAP-271"
    note: "destroy has no dry-run preview; its dry run is refused, not executed, since PR #49"
  - type: "blocks"
    target: "CERB-GAP-272"
    note: "destroy has no CLI verb or MCP tool"
  - type: "relates_to"
    target: "CERB-DEC-812"
    note: "down maps to stop, and stop is compose stop"
  - type: "relates_to"
    target: "CERB-DEC-813"
    note: "remote surfaces take a declared resource, not an ad-hoc target"
  - type: "blocks"
    target: "CERB-GAP-847"
    note: "the docker allow-list sits at the socket and web, not in Execute"
---

# Docker connector

> Runs container and Compose operations against the local Docker daemon or a declared docker resource's target; ad-hoc targets run only in the operator's shell.

The Docker connector is the audit's canonical example, because it is the one
that was dead for weeks while `cerberus connectors list` called it healthy. The
original failure had two halves. It resolved the `docker` binary once at
registration and cached the result for the daemon's lifetime; and the daemon is
started by launchd, which hands it `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, so
Docker Desktop's CLI at `/usr/local/bin/docker` was invisible to it even though
it resolved fine in an interactive shell. Starting Docker Desktop could not
recover it. Correcting the PATH could not recover it. Only a daemon restart
could.

Both halves are fixed, and the fixes are verifiable rather than inferable.

**Resolution is per call.** `internal/app/app.go` registers docker with
`RegisterFactory`, not `Register`, with the reason in a comment beside it. The
factory calls `DetectDocker()` on every operation and every liveness probe.
`DetectDocker` honours a `CERBERUS_DOCKER_PATH` override, then `PATH`, then five
compiled-in fallback locations, and there is a test that it finds the binary
with `PATH` emptied.

**Liveness now comes from the daemon, not the CLI.** `cerberus connectors list`
asks the daemon for the live set and only falls back to its own view if the
daemon cannot answer, with a comment naming the exact failure it prevents: "This
process has the user's shell PATH and credentials; the daemon has launchd's.
Reporting the CLI's view here is how a connector could read LIVE=yes while every
call against it failed." Verified: with the daemon running (socket ready, pid
8188), `connectors list` reports `docker live=yes`, and `cerberus docker ps`
and `cerberus docker logs <container> --lines 3` both return real output.
That `yes` is the daemon's answer under launchd's PATH, which means the fallback
list is what is carrying it.

`live` can still overstate, in one specific way: `DetectDocker` stats an
executable file and nothing more. It does not ask whether the daemon is
reachable. So `docker live=yes` with Docker Desktop stopped is a truthful answer
to a narrower question than an operator reads it as.

Host selection is the other thing worth knowing. `Target` is per operation, read
from the operation's config as either a `host` (a `DOCKER_HOST` value) or a
`context`, never both — the CLI resolves that conflict silently in favour of
`--context`, verified against docker 24.0.2, and for `destroy` running against a
daemon the caller did not ask for is the worst possible place to be wrong.
`WithTarget` returns a copy so no call can mutate state another call observes.
The connector also does real work to make remote failures readable: every
`ssh://` transport failure is reported by the CLI as "Cannot connect to the
Docker daemon at http://docker.example.com", a placeholder that is identical
whichever host was asked for, so the connector enables debug logging only for
ssh:// targets, extracts the child process's own error, and names the real
target itself.

**Where a target may come from changed in P0.** Before PR #52, the socket, the
web API and MCP accepted `host`, `context` and `compose_file` on any operation,
and MCP offered `docker_host` and `docker_context` to agents. An arbitrary
compose file chooses images, commands and host bind mounts, so this amounted to
code execution with host filesystem access through `cerberus_docker_up`. Now
those surfaces take an allow-list built from the connector's own key table
(`internal/connector/docker/config_keys.go`): `resource`, the container keys and
`id`/`lines`. `RefuseAdHocDockerTarget` refuses every other key by name,
including every compose-file alias. A declared resource's `host`, `context` and
`compose_file` come from its declaration and still apply, resolved by whoever
runs the call (CERB-DEC-813). On the CLI, `--host`, `--context` and `-f` force
the call in-process, where they still work. `docker ps` with no flags goes to
the local daemon as before. PR #54 made every docker operation schema advertise
exactly the allow-list.

**Stop no longer removes.** Before PR #49, `Stop` and `Destroy` both ran
`docker compose down` for a compose resource, so the un-acked `stop` was a
teardown. `Stop` now runs `docker compose stop` through `Backend.ComposeStop`,
and `Destroy` keeps `compose down` (or `docker rm`) with `--ack`. `cerberus
docker down` and `cerberus_docker_down` still invoke `stop`, and now stop
without removing anything (CERB-DEC-812).

The gap in the operation set is at the other end. `destroy` is declared,
destructive and ack-gated, and has no dry-run preview, no CLI command and no MCP
tool. Its dry run is refused as `preview_unsupported` rather than executed, and
it is reachable only through the generic connector route (CERB-GAP-271,
CERB-GAP-272).

## Owns

- Container list, start, stop, remove and logs via the docker CLI
- Compose up, stop, down and ps against a declared compose file
- Docker target selection from a declared resource (DOCKER_HOST, a docker context, or neither), or ad-hoc in the operator's shell
- Refusing ad-hoc target keys on the socket and web, from its own key table
- Locating the docker binary per call, including fallbacks for a launchd PATH
- Recovering the real failure cause the CLI hides behind a placeholder hostname

## Does not own

- Container supervision. Nothing here restarts or health-probes a container — a container resource is a named handle, not a workload
- Building images. CanBuild is declared but no build operation is exposed
- Docker itself, or whether its daemon is running
- Creating containers. CanCreate is false; Compose files declare what exists
