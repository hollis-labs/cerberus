# Fleet provenance

`cerberus fleet probe` answers, for one host, *what is running and what was it built
from*. It is the data layer for fleet inventory: run it on every host and you can tell
which commit each service runs, whether that build came from a clean tree, and whether
a process is still running a binary that has since been replaced on disk.

The probe is read-only. It never reads a process environment, a config file's values
or a credential. Provenance comes from executables (Go build info, ELF headers,
sha256), `/proc` metadata (executable link, parent, command line, working directory),
`systemctl show` properties and `docker inspect` image metadata. Git remote URLs are
reported with any embedded credentials removed.

## Usage

```
cerberus fleet probe                 # table
cerberus fleet probe --json          # cerberus.fleet.probe.v1
cerberus fleet probe --bin-dir /opt/tools/bin --bin-dir ~/.local/bin
cerberus fleet probe --no-health     # skip GETs against declared health URLs
cerberus fleet probe --no-registry   # do not attribute services to resources
```

The probe needs no Cerberus registry: on a host without one it reports services and
binaries without resource attribution. It is a single static binary, so it can be
copied to a host and run there.

| Flag / variable | Default | Meaning |
|---|---|---|
| `--bin-dir` | `~/.local/bin` | Installed-binary directories to report (repeatable). |
| `CERBERUS_FLEET_BIN_DIRS` | unset | Colon-separated default for `--bin-dir`. |
| `--system-units` | `true` | Include system-manager services whose executable is not part of the OS. |
| `--no-health` | `false` | Skip health GETs (3 s timeout each). |
| `--no-registry` | `false` | Skip resource attribution. |

Digests are cached by file identity (device, inode, size, mtime) in
`$XDG_CACHE_HOME/cerberus/fleet-probe-hashes.json` (mode 0600), so repeat probes do not
re-hash unchanged files.

## What it reports

**Services.** Every running systemd user service (session plumbing such as `dbus` is
skipped), system services whose executable lives outside the OS directories, and every
running container.

- For a unit, the main process's executable read through `/proc/<pid>/exe`, so a
  replaced or deleted file is still the one identified. `replaced_on_disk` is set when
  the file at that path is no longer the one the process runs: a restart is pending.
- For an interpreter (`python3`, `node`, `bash`, …) the artifact is the script on its
  command line, with the git state of the script's work tree.
- `children` lists distinct executables below the main process, such as stdio MCP
  servers a daemon spawned. Shells, sandbox wrappers and OS tools are skipped.
- For a container, the image reference, image ID, repo digests, OCI version/revision
  labels and the compose project.
- With a registry, `resource_id` and `project` attribute the service to a Cerberus
  resource: by the resource's `service_name` or `container`, by the generated unit name
  `com.hollis-labs.cerberus.[<project>.]<id>`, or by name. `health` is a GET against the
  resource's `health_check.url`, else its `url`.

**Binaries.** Every executable in the installed-binary directories. Copies an operator
keeps beside a tool (`tool.prev-20261030`, `tool.before-x`, `tool.bak`) are skipped.

### Why a build is `unstamped`

Go records the VCS revision only when the module sits in a repository whose `.git` is
a **directory**. A `git worktree` has a `.git` *file*, so `go build` in a worktree
produces a binary with no `vcs.*` settings at all, silently, even with
`-buildvcs=true`. To ship a stamped build from a worktree's commit, build from a clone
instead (`git clone --shared <repo> <dir> && git -C <dir> checkout <commit>`). Run any
step that rewrites tracked files (`npm install` updating a lockfile) and restore them
before `go build`, or the build is stamped `vcs.modified=true`.

## Flags in the table

| Flag | Meaning |
|---|---|
| `dirty` | Built from a work tree with uncommitted changes (`vcs.modified=true`, or a dirty script tree). |
| `unstamped` | A Go binary with no source identity: no VCS revision and no module version (`-buildvcs=false`, or built outside the repository). |
| `replaced-on-disk` | The running process's executable was replaced after it started. |
| `child-dirty:<name>`, `child-replaced:<name>` | The same, for a child executable. |
| `unreadable` | The executable could not be read (another user's process). |

## Inventory across the fleet

```
cerberus fleet stage-probe --ack            # once, and after upgrading cerberus
cerberus fleet inventory                    # every host in hosts.yaml
cerberus fleet inventory --role worker --issues --binaries
cerberus fleet inventory --service torque   # one service everywhere
cerberus fleet inventory --json             # cerberus.fleet.inventory.v1
```

`fleet inventory` probes every host in `hosts.yaml` (or `--host`, `--role`) and
prints a host summary, one row per service (and binary, with `--binaries`), and the
executables that differ across hosts:

| Column | Meaning |
|---|---|
| `BUILD` | Module and revision (and release version), image reference, or script and revision. |
| `FLAGS` | `dirty`, `unstamped`, `replaced-on-disk`, as for the probe. |
| `BEHIND` | Commits on the repository's default branch that the build lacks, with the first merged pull requests it lacks (`3 (#321 #320 #319)`). `-` when unknown. |
| `DRIFT` | The same executable (name and module) at different builds on two or more hosts. |

The local host is probed in process, with resource attribution and health. Other
hosts are probed over their `reach.ssh` with a fixed command that runs the staged
probe; a host without one reports `no_probe` with the command to stage it, and a
host with no ssh reach reports `no_reach`. Hosts are probed six at a time with a
90-second limit each. Results are cached for a minute (`--refresh` probes again).

`BEHIND` is computed on the controller from local checkouts: Go modules found under
`CERBERUS_FLEET_REPO_ROOTS` (colon-separated; default `~/dev`), compared with each
checkout's last fetch of `origin`'s default branch. The probe never contacts a git
remote, so fetch your checkouts for current numbers. A revision missing from the
local checkout is reported with a note rather than a count.

**Per-host binary directories.** A host's `fleet.bin_dirs` label in `hosts.yaml`
(colon-separated) names the installed-binary directories reported there; without
it the probe's default (`~/.local/bin`) applies. On the local host,
`CERBERUS_FLEET_BIN_DIRS` is the fallback.

**Staging.** `fleet stage-probe --ack` copies the running cerberus binary to each
selected host's `~/.local/share/cerberus/fleet-probe/cerberus` over its ssh reach,
verifies the upload's sha256 and moves it into place. Hosts of another OS or
architecture and hosts already at this build are skipped. It is an audited write
(`host/fleet_stage_probe`); the inventory itself is a read (`host/fleet_inventory`).

**MCP and the daemon.** The daemon serves the inventory at `GET /v1/fleet/inventory`
(`hosts`, `role`, `refresh` query parameters) and as the `cerberus_fleet_inventory`
tool. The CLI reads through the daemon when it serves the route and falls back to
running the inventory in process.

## Audit

```
cerberus fleet audit                      # findings, newest-first marks
cerberus fleet audit --new                # only what is new since the last run
cerberus fleet audit --json               # cerberus.fleet.audit.v1
cerberus fleet audit --notify-command 'mail -s "$FLEET_AUDIT_SUBJECT" ops@example.com'
```

`fleet audit` takes a fresh inventory and reports findings, most severe first:

| Kind | Severity | Finding |
|---|---|---|
| `dirty_build` | high (service), low (binary) | Built from a tree with uncommitted changes; a script service running from a dirty work tree is medium. |
| `unstamped` | medium | A running Go service with no VCS revision. |
| `replaced_on_disk` | medium | A process running a binary replaced after it started: a restart is pending. |
| `module_mismatch` | medium | A resource with a `build_strategy` runs a build of a different Go module than its source dir declares. |
| `stale_descriptor` | medium | A `*.cerberus.yaml` under the descriptor dirs that the registry index does not load; edits to it change nothing. |
| `not_running` | medium | An `auto_start` resource with no running unit or container. |
| `host_unreachable` / `no_probe` | medium / low | A host the inventory could not probe. |
| `behind` | low, medium past 5× | More than `--behind-threshold` (default 20) commits behind the default branch; installed binaries use twice the threshold. |
| `drift` | low | The same executable at different builds across hosts. |

Each finding has an ID that stays the same while the problem does (same host,
subject and build). The last run's IDs are kept in `--state-dir` (default
`~/.cerberus/fleet-audit/state.json`), so each run marks findings `new` and lists
the IDs `resolved` since; every run's report is kept in `reports/` (the last 30).
`--notify-command` runs only when there is something new, with `sh -c`, the
summary on stdin and the subject in `FLEET_AUDIT_SUBJECT`, so a daily timer stays
quiet while nothing changes. Descriptor directories default to `~/.config`, or the
colon-separated `CERBERUS_FLEET_DESCRIPTOR_DIRS`.

Cerberus's scheduler does not run commands, so schedule the audit with the host's
own scheduler, for example a systemd user timer:

```ini
# ~/.config/systemd/user/cerberus-fleet-audit.service
[Service]
Type=oneshot
Environment=CERBERUS_FLEET_REPO_ROOTS=%h/src
ExecStart=%h/.local/bin/cerberus fleet audit --new --notify-command 'your-notifier'

# ~/.config/systemd/user/cerberus-fleet-audit.timer
[Timer]
OnCalendar=*-*-* 08:30:00
Persistent=true
[Install]
WantedBy=timers.target
```

## Schema `cerberus.fleet.probe.v1`

```jsonc
{
  "schema": "cerberus.fleet.probe.v1",
  "host": "string",                 // os.Hostname()
  "probed_at": "RFC 3339",
  "prober": GoBuild,                // the cerberus build that produced the report
  "duration": "string",
  "warnings": ["string"],
  "services": [{
    "kind": "systemd-user | systemd-system | container",
    "name": "unit name without .service, or container name",
    "resource_id": "string?", "project": "string?",
    "pid": 0, "started_at": "RFC 3339?",
    "artifact": Artifact,           // units
    "children": [Artifact],         // units; each carries "pids"
    "image": {"ref", "id", "digests": [], "created", "version", "revision", "source", "state", "health"},
    "compose": {"project", "service", "working_dir"},
    "health": {"url", "status", "latency_ms", "error"}
  }],
  "binaries": [Artifact]
}

Artifact = {
  "kind": "go | elf | script | other",
  "path": "string", "resolved_path": "string?",
  "sha256": "hex?", "size": 0, "mtime": "RFC 3339?",
  "replaced_on_disk": false,
  "pids": [0],
  "go": GoBuild?,
  "script": {"interpreter", "path", "sha256", "git": {"root", "remote", "branch", "revision", "dirty"}}?,
  "release": {"app", "version", "dir"}?,   // path matches <...>/releases/<app>/<version>/<file>
  "error": "string?"
}

GoBuild = {"go_version", "path", "module", "module_version", "vcs", "revision", "time", "modified"}
```

Fields marked `?` are omitted when empty. Additive changes keep the `v1` suffix; a
change that removes or redefines a field bumps it.
