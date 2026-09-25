# Local Runtime Troubleshooting

Use this guide when a v2 local `process` resource does not build, start, reload, report status, or answer through the daemon/MCP socket.

Start with the highest-signal commands:

```bash
cerberus resource status <resource-id>
cerberus resource doctor <resource-id>
cerberus resource inspect <resource-id>
cerberus resource logs <resource-id> --stream stderr --lines 100
```

## Daemon And Socket

Symptoms:

- CLI or MCP calls fail because the daemon is unreachable.
- The daemon appears stale after editing `~/.cerberus/config.yaml`.
- MCP tools do not see the resource you just added.

Checks:

```bash
cerberus validate
cerberus daemon restart
cerberus resource list
```

Recovery:

- Use `cerberus daemon restart` when the daemon is running but stale or wedged.
- Use `cerberus install` when the launch agent or socket bootstrap path is broken.
- Use direct CLI resource commands as a fallback; they attempt the socket path first and fall back to local execution when the daemon is unreachable.
- Confirm you are editing the live config at `~/.cerberus/config.yaml`, or pass `--config /path/to/config.yaml` consistently.

## launchd Services

Symptoms:

- `status` shows stopped, failed, throttled, or ambiguous launchd state.
- `apply` succeeds but the process exits immediately.
- `reload` does not appear to start the service.

Checks:

```bash
cerberus resource inspect <resource-id>
cerberus resource doctor <resource-id>
cerberus resource logs <resource-id> --stream stdout --lines 100
cerberus resource logs <resource-id> --stream stderr --lines 100
```

Recovery:

- Use `doctor` to check plist paths, launchd state, artifact state, and log paths.
- Use `inspect` to find the launchd label, plist path, install root, current working directory, and stdout/stderr logs.
- Use `deploy` after source changes. `apply` does not run the build command.
- Use `reload` only when the installed artifact and service definition are already correct.
- Use `stop` when you want to stop runtime execution without deleting installed artifact or service state.
- Use `remove` only when you want to unload the launch agent and delete installed runtime artifacts.

## Artifact Drift

Symptoms:

- `status` or `list` reports a stale artifact.
- A service runs an older binary than the current source tree.
- The repo changed after the installed artifact was synced.

Recovery:

- If source changed and the resource has `build_strategy:`, run `cerberus resource deploy <resource-id> --ack`.
- If the workspace artifact is already correct and you only need to refresh the install layout, run `cerberus resource sync <resource-id> --ack`, then `cerberus resource apply <resource-id> --ack` when ready to activate it.
- If the service should restart with the already-installed artifact, run `cerberus resource reload <resource-id> --ack`.
- Avoid PATH-only commands for artifact-backed services. `command[0]` should be a filesystem path such as `./bin/my-api`, not `my-api`.

## Missing Or Wrong Artifacts

Symptoms:

- `deploy` fails during build.
- `apply` or `sync` cannot find the configured command artifact.
- launchd starts a wrapper that points at the wrong binary.

Checks:

```bash
cerberus resource inspect <resource-id>
cd /absolute/path/from/inspect
<run the configured build_strategy>
ls -l <artifact path from inspect>
```

Recovery:

- Make the repo build contract deterministic from the repo root.
- Ensure `build_strategy:` produces the same path used by `command[0]`.
- Prefer repo-local outputs such as `./bin/my-api` over `~/go/bin/my-api`.
- If a wrapper script is required, keep it repo-owned and make it point at a deterministic artifact.

## Runtime And Port Conflicts

Symptoms:

- A dev server fails because the port is already in use.
- `status` points at a process that is not the intended resource.
- A frontend is testing against the wrong backend.

Recovery:

- Give `dev`, `uat`, and release-style resources separate ports.
- Use tags and IDs that encode ownership, such as `my-api-dev`, `my-api-uat`, and `my-api-release`.
- Keep Vite, Wails, `go run`, and watcher loops on `mode: dev_session`.
- Keep shared background APIs, daemons, schedulers, and MCP servers on `mode: os_service`.

## Command Selection

Use the verb that matches the operator intent:

| Intent | Command |
| --- | --- |
| Make running service match current source | `cerberus resource deploy <id> --ack` |
| Start or converge from an already-built artifact | `cerberus resource apply <id> --ack` |
| Restart the current installed service only | `cerberus resource reload <id> --ack` |
| Copy artifact without touching runtime backend | `cerberus resource sync <id> --ack` |
| Stop without deleting install state | `cerberus resource stop <id> --ack` |
| Read live state and next action | `cerberus resource status <id>` |
| Diagnose install/runtime problems | `cerberus resource doctor <id>` |
| Find paths, labels, logs, and command details | `cerberus resource inspect <id>` |
| Uninstall runtime state | `cerberus resource remove <id> --ack` |

Use `stop` for non-destructive stop/pause intent. Do not treat `remove` as a synonym for stop.
