# Setting Up A Project For Cerberus V2

This guide defines the expected repo shape for projects Cerberus manages through the v2 local runtime lane.

The short version:

- durable APIs and daemons should use `resource` entries, not legacy `service` entries
- frontends, Vite servers, Wails dev flows, and other interactive dev loops should usually stay on `dev_session`
- every project should separate `dev`, `uat`, and `release` ownership clearly
- `os_service` resources must have a deterministic repo-local runtime story
- Cerberus should not have to guess which binary on `PATH` is the real one

## Choose The Right Runtime Mode

Use `dev_session` when the process is primarily for local iteration:

- `vite`
- `npm run dev`
- `wails dev`
- `go run`
- file-watcher driven preview servers

Use `os_service` when the process is meant to be a durable background service:

- HTTP API
- MCP server
- scheduler
- daemon
- long-running worker

## Separate Dev, UAT, And Release Ownership

Do not let one port or one process pretend to serve every audience.

The preferred pattern is:

- `dev`: repo-local iteration flows on `dev_session`
- `uat`: the shared background runtime agents and operators test against on `os_service`
- `release`: the promoted binary installed into a user-owned or system-owned location, also on `os_service`

Practical consequences:

- dev frontends should point at dev backends by default, not the UAT port
- UAT ports should have one active Cerberus-managed owner
- release installs should not depend on whichever dev server happened to be left running

## Separate Backend Services From Frontend Dev Servers

Do not force a project’s frontend dev workflow into the durable service lane.

A common good split is:

- backend API or daemon for iteration: `dev_session`
- backend API or daemon for shared testing: `os_service`
- frontend dev server: `dev_session`

This keeps Cerberus from conflating production-like background services with interactive local development.

## Artifact-Backed Services Need Repo-Local Outputs

If a resource uses:

```yaml
mode: os_service
run_from: artifact
```

then the repo must produce a deterministic filesystem artifact in the workspace.

Good examples:

- `./bin/hadrond`
- `./contextd`
- `./nanite`
- `./clockwork`

Bad examples:

- `contextd` from `PATH`
- `nanite` from `~/go/bin`
- wrapper scripts that only work because a global install happens to exist

Cerberus syncs artifacts from the workspace into:

```text
~/.cerberus/apps/<project>/<resource>/...
```

If the workspace does not contain the real artifact, artifact mode is the wrong choice.

## Workspace-Backed Services Are Allowed

Use:

```yaml
run_from: workspace
```

when the service is intentionally launched from the repo and there is no clean artifact-install story yet.

This is acceptable for:

- Python apps launched through a repo-local script
- projects where the durable runtime is still workspace-native

It is a compromise, not the ideal end state. Prefer artifact-backed services when the repo can support them cleanly.

For promoted release installs, artifact mode is still the preferred shape; the only difference is where the artifact originates. The release binary may come from a user-owned bin directory or a system-installed location rather than a workspace dev build, but Cerberus should still treat it as an explicit artifact, not a guessed PATH lookup.

## Preferred Build Contract

Every durable backend project should have a clear production build contract from the repo root.

Preferred shape:

1. one command Cerberus can run from the repo root
2. the command succeeds without relying on unrelated sibling repos
3. the command produces the runtime artifact at a stable workspace path
4. the runtime command uses that artifact directly

Examples:

```make
build:
	go build -o ./bin/myd ./cmd/myd
```

or

```make
build:
	cd frontend && npm run build
	go build -o ./my-api ./cmd/my-api
```

## Avoid PATH-Only Production Deployments

Do not make Cerberus depend on:

- `go install` into `~/go/bin`
- ad hoc symlinks
- a shell profile that modifies `PATH`
- a hand-installed global binary with unclear provenance

Those patterns are exactly what create stale-binary and wrong-binary failures.

If a project still uses `go install`, treat that as legacy and move toward a repo-local artifact output.

## Wrapper Scripts Need Clear Ownership

Wrapper scripts are acceptable only when they add real value:

- env normalization
- argument shaping
- startup sequencing
- service-specific bootstrap

They should not exist only to hide a `PATH` dependency.

If a wrapper remains in the runtime path, it should point at a deterministic repo-local artifact or otherwise have a documented reason to stay workspace-backed.

## Keep Build Dependencies Self-Contained

A repo-local production build should not fail because of an undeclared or surprising external dependency.

Examples of problems to avoid:

- `go.mod replace ../some-sibling-repo` without a documented bootstrap requirement
- frontend build steps that assume missing generated files
- production builds that only succeed after manually running unrelated dev commands

If a project genuinely requires a bootstrap step, make it explicit and repeatable.

## Env And Data Rules

Encode runtime env intentionally in Cerberus config:

- `env_file` for repo-managed secrets/config
- explicit `env` for required runtime values
- user-owned data roots when the service should write outside the repo

Examples:

```yaml
env_file: .env
env:
  CONTEXTD_ROOT: ~/.conduit
```

For v2 local process resources, Cerberus now expands `~` in relevant local paths
and env values before launchd sees them. Even so, absolute paths are still the
preferred config shape for durable services because they are easier to inspect
and less surprising to operators.

## Health Endpoints Matter

Durable backend services should expose a stable health signal when practical.

Examples:

- `/health`
- `/ready`
- `/v1/health/readiness`
- an explicit command health check if HTTP is not appropriate

Health endpoints make status, diagnosis, and future rollout automation much more reliable.

## Recommended Cerberus Resource Shape

Example artifact-backed API:

```yaml
resources:
  - id: my-api-service
    name: "My API Service"
    project: my-project
    type: process
    connector: local
    tags: [api, go, launchd, v2]
    config:
      dir: /absolute/path/to/repo
      command: ["./bin/myd", "serve", "--port", "8080"]
      build: ["make", "build"]
      url: http://127.0.0.1:8080
      port: 8080
      mode: os_service
      supervisor: launchd
      run_from: artifact
```

Example workspace-backed Python service:

```yaml
resources:
  - id: my-python-service
    name: "My Python Service"
    project: my-project
    type: process
    connector: local
    tags: [api, python, launchd, v2]
    config:
      dir: /absolute/path/to/repo
      command: ["./bin/my-service", "serve", "--port", "8096"]
      url: http://127.0.0.1:8096
      port: 8096
      mode: os_service
      supervisor: launchd
      run_from: workspace
      env:
        PYTHONUNBUFFERED: "1"
```

## Migration Checklist

Before migrating a project from legacy `services:` to v2 `resources:`:

1. Decide whether the backend belongs on `dev_session` or `os_service`.
2. Ensure the repo has a deterministic production build path.
3. Ensure the runtime command uses a real filesystem path, not a PATH-only binary.
4. Build locally from the repo root.
5. If artifact-backed, verify the artifact exists in the repo after build.
6. Add the v2 `resource` entry.
7. Run `cerberus resource sync <id>` if artifact-backed.
8. Run `cerberus resource apply <id>`.
9. Verify with:
   - `cerberus resource status <id>`
   - `cerberus resource inspect <id>`
   - `cerberus resource doctor <id>`
10. Only then remove the legacy `service` entry.

## What We Learned From Early Migrations

The first wave surfaced the failure modes to design against:

- stale binaries from `go install` and PATH drift
- services restarting from the wrong binary after rebuilds
- repo builds that depend on undeclared sibling paths
- UI builds blocking backend artifact creation
- duplicated runtime ownership when v1 and v2 lanes both point at the same port

The correct pattern is:

- repo-local build output
- explicit runtime mode
- explicit run source
- explicit `dev` vs `uat` vs `release` ownership
- Cerberus-owned home expansion instead of trusting launchd or the shell
- one active owner per port
- supervisor-managed status for durable services

For artifact-backed resources with a `build:` command, Cerberus now also records Git repo state when the artifact is synced. That lets status warn when the current repo commit or worktree no longer matches the installed UAT or release artifact, even if nobody rebuilt the binary yet.
