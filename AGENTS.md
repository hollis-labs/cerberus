# Cerberus

Cerberus is a single-binary Go control plane for local infrastructure. It
builds, deploys, supervises and inspects the daemons, dev servers and
background services the portfolio runs, over one runtime service the CLI,
daemon socket, HTTP API, web console and MCP adapter all share. It owns
execution and derived operational state — not the definitions a project writes
about itself, the data it holds, or the credentials it needs. Operationally it
is v2-only: `resources:` are the model, the legacy `services:` lane is frozen.

## Start Here

- `README.md` — install paths, CLI reference, port map.
- `docs/adr/0002-resource-only-local-workload-model.md` — why v2 is the only
  model, and what "frozen" means for `services:`.
- `internal/cerbapi/resource_runtime_service.go` — the shared runtime service
  every surface routes through; behavior changes belong here, not in a caller.
- `internal/domain/connector.go` — the interface every provider implements.
- `internal/connector/local/` — `dev_session.go` and `launchd.go` are the two
  runtime modes, `artifact.go` owns the build→install join.
- `internal/registry/` — discovery and validation of per-repo `*.cerberus.yaml`.
- `cerberus.cerberus.yaml` — this repo is managed by the binary it builds.
- `docs/guides/setting-up-a-project-for-cerberus-v2.md` — the repo-side
  contract for joining the v2 lane.
- `docs/secrets.md` — how a resource names a credential without carrying one.

## Commands

```bash
make test        # go test ./cmd/cerberus ./internal/... ./pkg/...
make build       # → bin/cerberus
make all         # web bundle into internal/webui/dist, then the binary
make lint        # go vet, golangci-lint, staticcheck, errcheck, govulncheck
make typecheck   # web/ TypeScript
```

Use `make all`, not `make build`, whenever `web/` changes: the console is
`go:embed`-ed from `internal/webui/dist`, which is gitignored, so a plain build
embeds whatever bundle is sitting there.

Lefthook is the gate; there is no CI. Pre-commit runs gofmt/goimports,
`golangci-lint --new` and `go vet` on staged Go, pre-push the full `go test`.

## Boundaries

**Never set `port: 0`.** `lsof -ti :0` returns arbitrary system PIDs, read as a
false-positive "running" by the daemon monitor. Omit `port` for processes that
do not listen. Two guards hold this and neither should be removed:
`findPIDByPort` in `internal/service/service.go` refuses `port <= 0`, and
registry validation rejects it under
`TestValidateProjectConfigPortZeroIsError`.

**Changed source is not deployed source.** A `run_from: artifact` resource runs
an installed copy under `~/.cerberus/apps/<project>/<resource>/bin/`. `go
build`, `make build`, `go install`, `reload` and the console's Restart leave
that copy untouched; only `cerberus resource deploy <id>` rebuilds and
reinstalls it, and `cerberus resource status <id>` reports `artifact_stale`
with a next step. `resolveArtifactSource` in
`internal/connector/local/artifact.go` installs the build strategy's declared
`output`, falling back to `command[0]` without one — so a strategy missing an
`output` rule can install a binary the build never wrote.

**Never deploy the daemon through its own socket.** `deploy` or `ensure-fresh`
on `cerberus-daemon-service` restarts the daemon mid-operation: the call dies on
EOF, the artifact is left half-synced, and the launchd job can end up booted out
where `KeepAlive` will not bring it back. Nothing refuses this yet. Build to a
temp path, `mv` it over the artifact, then `launchctl kickstart -k
gui/$(id -u)/com.fragments-engine.cerberus`.

**Do not reintroduce `selfexec.WatchAndExit` in `cerberus mcp`** (see the
comment in `cmd/cerberus/cmd_mcp.go`). Deploying the daemon replaces the binary
on disk, so every running `cerberus mcp` child would notice and exit — wiping
MCP access fleet-wide under hosts that do not respawn children. Each tool call
re-dials the socket, so the subprocess already survives daemon restarts.

**`cerberus.cerberus.yaml` is live** — it registers the daemon, web console and
release artifacts against this machine's launchd, so editing it changes what is
supervised here, not just what a test asserts. Its resource configs name
credentials and never carry them: `keychain://` or `helper://`, resolved by
`cerberus run-secrets` inside the service's own process.

**`infrastructure.cerberus.yaml` is live too**, and it is not this project — it
carries the machine-level services nothing else owns: PostgreSQL 16, Ollama and
Jaeger, all under launchd. It lives here because a config belongs in a repo and
this is the only repo that plausibly owns machine infrastructure, not because
those services are part of Cerberus. Editing it changes whether the portfolio's
database is supervised.

**A config that only exists on a branch is a config that disappears.** Both
files above are registered by absolute path, so checking out a branch without
them drops those projects from the runtime — resources vanish from
`resource list`, health and the console while the registry still points at the
path. Register a new config only once it is on `main`.
