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

## Flags in the table

| Flag | Meaning |
|---|---|
| `dirty` | Built from a work tree with uncommitted changes (`vcs.modified=true`, or a dirty script tree). |
| `unstamped` | A Go binary with no source identity: no VCS revision and no module version (`-buildvcs=false`, or built outside the repository). |
| `replaced-on-disk` | The running process's executable was replaced after it started. |
| `child-dirty:<name>`, `child-replaced:<name>` | The same, for a child executable. |
| `unreadable` | The executable could not be read (another user's process). |

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
