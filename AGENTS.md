# Cerberus — Agent Orientation

## What is this and why

Cerberus is an agent-first, single-binary Go control plane for managing local OS
processes, with a planned trajectory toward unified cloud infrastructure
orchestration. It exists so that humans and agents can build, deploy, supervise,
and inspect the dev servers, daemons, and background services that make up the
broader portfolio from one consistent surface (CLI, daemon, HTTP/socket API, and
MCP). Cerberus is itself the build/deploy authority for other local projects:
when an agent needs a project's binary built and its service (re)started, that
goes through Cerberus's v2 `resource` lane. The product is operationally
**v2-only** — `resources:` are the active model and legacy `services:` plus the
TUI are frozen.

## Golden rule: to update a running service, DEPLOY

A `run_from: artifact` service runs an **installed copy** under
`~/.cerberus/apps/<project>/<resource>/bin/`, not your repo binary. So:

- **Changed source and want it live → `cerberus resource deploy <id>`** (build + sync + activate). This is the ONLY thing that rebuilds and reinstalls.
- `go build` / `make build` / `go install` / `go test ./...` update or verify your **repo**, not the running service. They do **not** deploy anything.
- `reload` and any GUI/TUI "Restart" relaunch the **existing (maybe stale) artifact** — no rebuild.
- After acting, confirm with `cerberus resource status <id>` and obey its `recommended_next_step` (it reports `artifact_stale`). `mode: dev_session` resources have no staleness signal yet — restart the dev session yourself after a rebuild.

## Where to start

- **`cmd/cerberus/`** — main entry point: Cobra CLI, daemon mode, MCP adapter.
- **`cmd/cerberus-docker-plugin/`** — out-of-process Docker connector plugin.
- **`README.md`** — install, CLI quick reference, v2 resource model, port map.
- **`docs/adr/`** — accepted architecture decisions (start here for "why"):
  - `0001-local-runtime-backends.md` — dual local runtime backends.
  - `0002-resource-only-local-workload-model.md` — the v2-only cutover.
- **`docs/guides/`** — `setting-up-a-project-for-cerberus-v2.md` (repo-side rules
  for joining the v2 lane), `macos-beta-quickstart.md`,
  `local-runtime-troubleshooting.md`.
- **`docs/plans/`** — beta release plan/execution, daemon-management-v2,
  post-beta external connectors plugin plan.
- **`internal/`** — implementation: `domain/` (connector/provider interfaces),
  `config/`, `service/`, `daemon/`, `mcp/`, `connector/`, `pipeline/`,
  `pluginhost/`, `store/` (SQLite), `procscan/`, `tui/` (frozen).

## Key domain concepts

- **Resource** — the v2 unit of managed workload. Local workloads are
  `type: process`. Runtime policy is `mode: dev_session` (repo-local dev
  processes) or `mode: os_service` (native supervisor-managed background
  services; `launchd` on macOS).
- **Project** — a `ProjectDef` grouping related resources.
- **Pipeline** — a `PipelineDef` chaining operations.
- **Connector** — provider plugin (DigitalOcean, GitHub, Cloudflare, SSH,
  Namecheap, Forge, Docker, `local`). Interface lives in
  `internal/domain/connector.go`.
- **deploy vs apply vs reload** — `deploy` = build + sync artifact + activate;
  `apply` = converge/start an already-built resource; `reload` = restart the
  installed service without rebuilding or syncing.
- **Artifact** — for `run_from: artifact` services, runtime files installed
  under `~/.cerberus/apps/<project>/<resource>/...`. Cerberus records repo state
  at sync time and can warn when the installed artifact is older than the repo.
- **Config** — `~/.cerberus/config.yaml`, must declare `version: 2`.
- **Status detection (legacy lane)** — PID file primary, `lsof -ti :<port>`
  fallback. Never set `port: 0` (causes false-positive "running" — see
  `CLAUDE.md`); omit `port` for non-listening processes.

## Common operations

Inspect and manage v2 process resources:

```bash
cerberus resource list
cerberus resource status <resource-id>
cerberus resource inspect <resource-id>
cerberus resource doctor <resource-id>
cerberus resource logs <resource-id> --stream stderr --lines 100
```

Make a running service match the current source tree (build + sync + activate):

```bash
cerberus resource deploy <resource-id>
```

Start/converge an already-built resource, or restart without rebuilding:

```bash
cerberus resource apply <resource-id>
cerberus resource reload <resource-id>
```

Pause vs uninstall:

```bash
cerberus resource stop <resource-id>     # non-destructive pause
cerberus resource remove <resource-id>   # destructive: unload launch agent + remove artifact tree
```

Build Cerberus itself, bootstrap the daemon:

```bash
make build                 # go build -o cerberus ./cmd/cerberus
go install ./cmd/cerberus  # dev installs only; beta installs use ~/.cerberus/bin/cerberus
cerberus init              # create default config
cerberus install           # write the com.fragments-engine.cerberus launch agent
```

The Cerberus daemon itself is a v2 resource (`cerberus-daemon-service`, launchd
label `com.fragments-engine.cerberus`); manage it via the `resource` lane.

## Where to look for more

- **Architecture decisions** — `docs/adr/` (0001, 0002).
- **Roadmap** — `docs/plans/beta-release-plan.md` and
  `beta-release-execution.md` (current focus: Cerberus beta for real local use).
  Post-beta direction: `docs/plans/post-beta-external-connectors-plugin-plan.md`.
- **Release process** — `docs/release/beta-release-process.md`.
- **Portfolio context** — knowledge file
  `~/dev/agent-os/knowledge/projects/cerberus.md` (composition map, the missing
  reconciler-loop blocker, IaC driver decision).
- **Project SoT** — `.agent-ops/project.yaml`.
