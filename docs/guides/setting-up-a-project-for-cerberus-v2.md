# Setting Up A Project For Cerberus V2

This guide defines the expected repo shape for projects Cerberus manages through the v2 local runtime lane.

The short version:

- durable APIs and daemons should use `resource` entries, not legacy `service` entries
- frontends, Vite servers, Wails dev flows, and other interactive dev loops should usually stay on `dev_session`
- every project should separate `dev`, `uat`, and `release` ownership clearly
- `os_service` resources must have a deterministic repo-local runtime story
- Cerberus should not have to guess which binary on `PATH` is the real one

## Keep The Config In Its Owning Repo

Each project owns its `.cerberus.yaml` in its repo, conventionally at the repo
root as `<project>.cerberus.yaml`. Cerberus records that file's path; it does
not keep a separate copy of the definition. The project root derives from the
config directory.

Commit the config on `main` before registering its absolute repo path. A config
that exists only on a feature branch disappears when the working tree changes
branches. Keep the file on any active branch that will remain checked out.

`~/.cerberus/projects/` is retired, including the equivalent `projects/`
directory beside a custom Cerberus registry. Registration and validation reject
those paths, including symlink aliases; registry health identifies an old
pointer with instructions to relocate it. Register each repo-owned file with
`cerberus register <repo>/<project>.cerberus.yaml`.

The old `config migrate` command and console migration action no longer create
central copies. Registration updates discovery only; it does not build, deploy
or restart a resource.

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

Recommended naming:

- resource IDs should encode audience: `my-api-dev`, `my-api-uat`, `my-api-release`
- tags should include the audience: `dev`, `uat`, or `release`
- durable services should also tag runtime shape: `launchd`, `artifact`, `workspace`, `api`, `daemon`, or `mcp`
- ports should be unique per audience; do not reuse the dev port for UAT
- data roots should be audience-specific, such as `~/.my-project/dev`, `~/.my-project/uat`, and `~/.my-project/release`

This keeps operator intent clear in `resource list`, MCP filters, logs, and future automation.

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
- `./tesseract`
- `./nanite`
- `./clockwork`

Bad examples:

- `tesseract` from `PATH`
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

Keep data roots separate by audience:

```yaml
env:
  MY_API_ENV: uat
  MY_API_DATA_DIR: ~/.my-api/uat
```

Do not point `dev`, `uat`, and release-style resources at the same mutable data directory unless that sharing is the explicit test scenario.

## Health Endpoints Matter

Durable backend services should expose a stable health signal when practical.

Examples:

- `/health`
- `/ready`
- `/v1/health/readiness`
- an explicit command health check if HTTP is not appropriate

Health endpoints make status, diagnosis, and future rollout automation much more reliable.

## The Project Block

Every project config declares exactly one project. Beyond a name, it
carries the slug other systems join on and two portable props the local
control plane reads:

```yaml
kind: cerberus-project/v1
project:
  id: my-project
  name: "My Project"
  description: What this project is.
  capabilities: [go, launchd, mcp]
  links:
    - { kind: repo,     target: "git@github.com:hollis-labs/my-project.git" }
    - { kind: docs,     target: ./docs }
    - { kind: owned_by, target: "org:hollis-labs" }
```

### `project.id` is the portfolio-wide slug

It is not a local label. The same string is already:

- the Cerberus registry key (`owner`),
- the Tesseract memory namespace segment — `user/<user>/project/<slug>/memory/<type>`,
- the agent-setup project-template basename — `templates/projects/<slug>.md`.

So it is validated, not merely required: lowercase kebab-case, letters,
digits and single interior hyphens (`^[a-z0-9]+(-[a-z0-9]+)*$`). Trailing
and doubled hyphens are rejected — they are harmless as a registry key but
round-trip differently through a namespace segment or a filename, which
turns a join into a silent miss.

Choose it once. Changing it is a rename across every system above, not a
config edit.

`owner` is the same slug at the registration envelope. Write it or omit
it — omitted, it defaults from `project.id`. Write both and they must
match; a config naming the project two different things is rejected.

### `capabilities` and `links`

Both are optional and both are portable — they describe the project
itself, not this machine's opinion of it. Anything Chrispian-local (an
inbox, launch preferences, which project card to plant) belongs to the
local control plane's own objects, never here.

`capabilities` is a free-form list of tags: what the project is built
with, or needs. `links` are typed pointers out of the project, and
`kind` is deliberately free-form rather than an enum — a closed
vocabulary would need a coordinated schema change in every reader for
each new relation. The blessed v1 kinds are:

| kind | target |
|---|---|
| `repo` | git URL or path |
| `docs` | path or URL |
| `pipeline` | pipeline id |
| `owned_by` | `org:<slug>` |
| `member_of` | `org:<slug>` or group ref |
| `requires_secret` | a `keychain://` or `helper://` reference, never a secret |

A kind outside that list validates fine. Both `kind` and `target` must be
non-empty.

### Before you write these fields

`capabilities` and `links` are new. A Cerberus that does not know them
still reads the config — unknown fields are warnings, not errors — but
**that leniency lives in the binary, not in the file**, and the binary
that matters is the running daemon, not the source tree.

A daemon older than the leniency parses strictly and drops the entire
project: its resources vanish from `resource list`, health and the
console, with the config itself still perfectly valid. That is the
2026-05-25 failure mode, and adding a field is enough to trigger it.

Check before adding them to a registered config:

```bash
cerberus resource list --project <slug>   # resources present?
# add capabilities/links, then check again — same count?
```

If the count drops, the daemon predates the leniency. Redeploy it first,
following the daemon-deploy procedure in `AGENTS.md` — never through its
own socket — and confirm the project reappears before relying on the new
fields anywhere else.

### What does not go here

No repo root, no Tesseract namespace, no inbox, no launch preferences.
The namespace is derived at materialization time from the slug plus the
local user; the repo root is `dirname(configPath)` once the config lives
in the repo it describes. Storing either is the stale state this shape
exists to avoid.

## Recommended Cerberus Resource Shape

Example artifact-backed API:

```yaml
resources:
  - id: my-api-uat
    name: "My API UAT"
    project: my-project
    type: process
    connector: local
    tags: [uat, api, go, launchd, artifact]
    config:
      dir: /absolute/path/to/repo
      command: ["./bin/myd", "serve", "--port", "8080"]
      build_strategy:
        kind: make_standard
        source:
          root: .
        rules:
          target: build
      url: http://127.0.0.1:8080
      port: 8080
      mode: os_service
      supervisor: launchd
      run_from: artifact
      env:
        MYD_ENV: uat
        MYD_DATA_DIR: ~/.myd/uat
```

Example workspace-backed Python service:

```yaml
resources:
  - id: my-python-service
    name: "My Python Service"
    project: my-project
    type: process
    connector: local
    tags: [uat, api, python, launchd, workspace]
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

Example frontend dev session:

```yaml
resources:
  - id: my-web-dev
    name: "My Web Dev"
    project: my-project
    type: process
    connector: local
    tags: [dev, frontend]
    config:
      dir: /absolute/path/to/repo/web
      command: ["npm", "run", "dev", "--", "--host", "127.0.0.1", "--port", "5177"]
      url: http://127.0.0.1:5177
      port: 5177
      mode: dev_session
```

## Operator Lifecycle Expectations

Choose lifecycle verbs by intent:

- `deploy`: build from the current source tree, sync the artifact, and activate it.
- `apply`: activate from an already-built artifact or workspace command; it does not build.
- `reload`: restart/kickstart the current installed service without syncing or rewriting service definitions.
- `sync`: update installed artifacts without applying the runtime backend.
- `stop`: stop runtime execution without deleting installed artifact or service state.
- `remove`: uninstall runtime state; for launchd-backed artifact services, this unloads the launch agent and removes the installed artifact tree.

Use `stop` for non-destructive stop/pause intent and keep `remove` uninstall-oriented.

## Migration Checklist

Before migrating a project from legacy `services:` to v2 `resources:`:

1. Decide whether the backend belongs on `dev_session` or `os_service`.
2. Ensure the repo has a deterministic production build path.
3. Ensure the runtime command uses a real filesystem path, not a PATH-only binary.
4. Build locally from the repo root.
5. Assign unique dev/UAT/release ports and data roots.
6. Add audience tags so operators and agents can filter resources safely.
7. If artifact-backed, verify the artifact exists in the repo after build.
8. Add the v2 `resource` entry.
9. Run `cerberus resource sync <id>` if artifact-backed.
10. Run `cerberus resource apply <id>`.
11. Verify with:
   - `cerberus resource status <id>`
   - `cerberus resource inspect <id>`
   - `cerberus resource doctor <id>`
12. Only then remove the legacy `service` entry.

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

Cerberus serializes builds in the same source tree, including build subdirectories
of one Git repository. A concurrent caller fails promptly with the holder's
resource ID, PID and lock age. The lock spans build, optional install and deploy
activation. Process exit releases it automatically; do not delete an active lock
file. Add this entry to the project's `.gitignore`:

```gitignore
.cerberus-build.lock
```

A pinned toolchain can wrap every build command (including Go matrix variants
and optional `make install`) without shell interpolation:

```yaml
build_strategy:
  kind: make_standard
  env_prefix: [mise, --no-config, exec, node@22.12.0, --]
  rules:
    target: build
    output: ./tangent
```

`rules.output` is required when deploying a built `run_from: artifact` resource.
It names the binary relative to the build directory, not its installed copy.
Deploy builds and installs that output and activates it; for a dev session it
restarts the owned process. Apply reports that no build ran, along with the
activated binary's path, hash and modification time when it is a direct binary.
These facts identify the build output; they do not prove it reflects source edits.
After changing source, use `resource deploy` or `resource ensure-fresh --force`.
Without `--force`, ensure-fresh only checks drift in binaries already built.
