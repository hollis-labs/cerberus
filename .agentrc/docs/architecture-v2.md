# Cerberus v2 Architecture

> Reference document for all agents working on Cerberus. Describes the target architecture, domain model, key design decisions, and current implementation status.

## Vision

Cerberus evolves from a local process manager into a **universal infrastructure control plane**. It manages local dev services, cloud servers, DNS, containers, CI/CD, SSH, and deployments through a unified connector pattern. Agent-first (CLI + MCP), with a Wails desktop app as the eventual GUI.

## Current Status (as of 2026-04-29)

### Completed

**Phase 1 — Foundation:**
- Domain model (`internal/domain/`) — Resource, Project, State, Connector, Store, Action, SecretProvider
- Connector registry (`internal/connector/`) with tests
- Local connector (`internal/connector/local/`) with typed process specs, native `dev_session` runtime state, and `launchd` support for `os_service`
- SQLite store (`internal/store/sqlite/`) with migrations, CRUD, and tests
- Secrets provider (`internal/secrets/`) — keychain + env fallback with tests
- Config v2 (`internal/config/`) — v2 schema and `LoadUnified()` loader
- App struct (`internal/app/`) — central dependency container
- CLI split — `cmd/cerberus/main.go` split into 14+ per-command files
- New CLI commands: `project list/show`, `resource list/show/status/apply/sync/remove`

**Phase 2 — External Connectors + Pipelines:**
- Pipeline engine (`internal/pipeline/`) — fluent builder, DAG executor, parallel stages, rollback, 11 tests
- Pipeline actions (`internal/pipeline/actions/`) — Shell, Build, Start, Stop, HealthWait
- Pipeline config support — `PipelineDef`, `StageDef`, `ActionDef` in v2 config
- Pipeline resolver (`internal/pipeline/resolve.go`) — config to executable pipeline
- GitHub connector (`internal/connector/github/`) — **dual backend pattern**: API (go-github v72) + CLI (`gh`)
- DigitalOcean connector (`internal/connector/digitalocean/`) — full CRUD via godo
- CLI: `pipeline list/show/run`, `github status/releases/runs`, `server list/show`
- MCP: 15 tools total (8 original + 7 new)

**Phase 3 — Connector Batch:**
- SSH connector (`internal/connector/ssh/`) — remote command execution via `golang.org/x/crypto/ssh`, public key auth, exec + status
- Namecheap connector (`internal/connector/namecheap/`) — domain list/status, DNS record listing via XML API (no external SDK)
- Laravel Forge connector (`internal/connector/forge/`) — read-only server/site listing via REST API (transitional)
- Cloudflare connector (`internal/connector/cloudflare/`) — **dual backend**: API (cloudflare-go v4) + CLI (wrangler), zones, DNS CRUD
- Docker connector (`internal/connector/docker/`) — CLI-only backend (`docker`/`docker compose`), container + compose lifecycle
- CLI: `ssh exec/status`, `domain list/status`, `dns list`, `forge servers/server/sites`, `cloudflare zones/dns list/dns create`, `docker ps/logs/up/down`
- MCP: 29 tools total (15 prior + 14 new)

The legacy TUI and service-management lane have now been removed from the active runtime surface.

**Phase 4 — Local Runtime Backend Split (in progress):**
- Typed local process spec (`internal/connector/local/spec.go`) for `mode`, `supervisor`, `run_from`, and install metadata
- Dual local runtime seam: `dev_session` and `os_service`
- macOS `launchd` backend for local `process` resources
- User-area artifact install/sync layout under `~/.cerberus/apps/<project>/<resource>/...`
- Home-path expansion for v2 local process config before launchd/runtime use
- Daemon/socket API + MCP support for resource runtime status, apply, sync, and remove
- Shared resource runtime service (`internal/cerbapi/resource_runtime_service.go`) so CLI fallback, daemon/socket API, and MCP hit the same v2 execution layer
- Resource-native daemon monitor for `dev_session` auto-restart (`internal/cerbapi/resource_monitor.go`)
- Resource status now exposes artifact drift plus recommended operator actions
- Resource list now surfaces compact runtime summary when the daemon is reachable
- Legacy service transport and operator-facing legacy lifecycle surfaces have been removed from the active CLI/MCP/daemon runtime path
- Cerberus self-daemon is now represented as `cerberus-daemon-service` in the v2 resource lane

## Core Principles

1. **CLI-first** — every operation is a testable CLI command. MCP is a thin layer exposing CLI to agents.
2. **Connector pattern** — all external services implement a common `Connector` interface. Inspired by conduit's Provider pattern.
3. **Dual backend pattern** — connectors that have both an API SDK and a CLI tool (GitHub/gh, Docker/docker, Cloudflare/wrangler) use a Backend interface with two implementations. Auto-selection: API if token available, CLI if tool installed.
4. **Project as first-class concept** — a Project groups related Resources.
5. **Pipeline pattern** — multi-step workflows use a fluent builder + DAG executor with parallel stages and rollback.
6. **Store abstraction** — SQLite default, interface allows future DB adapters. Lazily opened.
7. **Single active model** — local workload management is v2-only. Config must be `version: 2`.
8. **Agent-facing output matters** — CLI help, MCP descriptions, and runtime diagnostics should actively steer agents toward the correct verb and lane.

## Domain Model

### Resource

The universal unit of managed infrastructure.

```go
type Resource struct {
    ID        string         `json:"id" yaml:"id"`
    Name      string         `json:"name" yaml:"name"`
    Type      ResourceType   `json:"type" yaml:"type"`
    ProjectID string         `json:"project_id" yaml:"project_id"`
    Connector string         `json:"connector" yaml:"connector"`
    Config    map[string]any `json:"config" yaml:"config"`
    Tags      []string       `json:"tags,omitempty" yaml:"tags,omitempty"`
    DependsOn []string       `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
    CreatedAt time.Time      `json:"created_at" yaml:"-"`
    UpdatedAt time.Time      `json:"updated_at" yaml:"-"`
}
```

### ResourceType Enum

| Type | Connector(s) | Description |
|------|-------------|-------------|
| `process` | local | Local OS process |
| `server` | digitalocean, ssh, forge | Cloud VM / remote server |
| `container` | docker | Docker container or compose stack |
| `domain` | cloudflare, namecheap | DNS zone or record |
| `repository` | github | Git repository |
| `pipeline` | (internal) | Multi-step workflow |

### State Enum

`stopped`, `starting`, `running`, `healthy`, `unhealthy`, `building`, `failed`, `destroyed`, `unknown`

## Key Interfaces

### Connector

```go
type Connector interface {
    ID() string
    ResourceTypes() []string
    Create(ctx context.Context, res *Resource) error
    Start(ctx context.Context, res *Resource) error
    Stop(ctx context.Context, res *Resource) error
    Destroy(ctx context.Context, res *Resource) error
    Status(ctx context.Context, res *Resource) (State, error)
    Capabilities() ConnectorCapabilities
}
```

### Backend (per-connector, for dual API/CLI pattern)

```go
// Example: internal/connector/github/backend.go
type Backend interface {
    RepoStatus(ctx context.Context, owner, repo string) (*RepoStatus, error)
    ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error)
    ListWorkflowRuns(ctx context.Context, owner, repo string, limit int) ([]WorkflowRun, error)
}
```

### Store, Action, SecretProvider

See `internal/domain/store.go`, `pipeline.go`, `secrets.go`.

## Package Layout (Current)

```
cmd/cerberus/
├── main.go              -- rootCmd + top-level command registration
├── cmd_validate.go      -- validate command
├── cmd_init.go          -- init command
├── cmd_daemon.go        -- daemon command (uses App struct)
├── cmd_mcp.go           -- MCP server (uses App struct, registers active resource + connector tools)
├── cmd_install.go       -- bootstrap/recovery helper for the daemon launch agent
├── cmd_project.go       -- project list/show
├── cmd_resource.go      -- resource list/show/status/apply/deploy/sync/remove/logs/inspect/doctor/reload
├── cmd_pipeline.go      -- pipeline list/show/run
├── cmd_github.go        -- github status/releases/runs
├── cmd_server.go        -- server list/show
├── cmd_ssh.go           -- ssh exec/status
├── cmd_namecheap.go     -- domain list/status, dns list
├── cmd_forge.go         -- forge servers/server/sites
├── cmd_cloudflare.go    -- cloudflare zones, dns list/create
└── cmd_docker.go        -- docker ps/logs/up/down

internal/
├── domain/              -- Pure types and interfaces
│   ├── resource.go      -- Resource, ResourceType
│   ├── project.go       -- Project
│   ├── state.go         -- State enum
│   ├── connector.go     -- Connector interface, ConnectorCapabilities
│   ├── store.go         -- Store interface
│   ├── pipeline.go      -- PipelineRun, Action, PipelineEnv
│   └── secrets.go       -- SecretProvider interface
├── app/
│   └── app.go           -- App struct (config, store, registry, secrets, local connector, resource runtime service)
├── cerbapi/
│   ├── resource_runtime_service.go -- shared v2 resource execution layer
│   ├── inprocess.go     -- daemon-backed client adapter
│   ├── socket_server.go -- HTTP/socket transport
│   ├── socket_client.go -- thin remote client
│   └── *.go            -- DTOs, logs, tests
├── config/
│   ├── config.go        -- config loader + default config helpers
│   ├── v2.go            -- ConfigV2, ProjectDef, ResourceDef, PipelineDef, StageDef, ActionDef
│   └── *_test.go        -- loader/watcher/spec tests
├── store/sqlite/
│   ├── store.go         -- SQLiteStore implementing domain.Store
│   ├── migrations.go    -- Embedded migration runner
│   ├── migrations/*.sql -- 4 migration files
│   └── store_test.go    -- CRUD tests
├── connector/
│   ├── registry.go      -- Registry (Register/Get/List/IDs) + tests
│   ├── local/
│   │   ├── connector.go -- Local connector dispatching by process runtime mode
│   │   ├── mapper.go    -- ServiceDefToResource / ResourceToServiceDef
│   │   ├── spec.go      -- Typed local process config (`mode`, `supervisor`, `run_from`)
│   │   ├── runtime.go   -- `dev_session` vs `os_service` runtime backends
│   │   ├── launchd.go   -- macOS launchd apply/status/remove backend
│   │   ├── install_layout.go -- User-area artifact/install path derivation
│   │   ├── artifact.go  -- Artifact sync + install manifest
│   │   ├── apply_result.go -- Operator-facing apply outcome formatting
│   │   ├── status_advice.go -- Recommended action derivation for resource status
│   │   └── *_test.go
│   ├── github/
│   │   ├── types.go     -- RepoStatus, Release, WorkflowRun
│   │   ├── backend.go   -- Backend interface
│   │   ├── api_backend.go  -- go-github v72 SDK
│   │   ├── cli_backend.go  -- gh CLI wrapper
│   │   └── connector.go    -- Unified facade, auto-selects API vs CLI
│   ├── digitalocean/
│   │   ├── types.go     -- DropletStatus
│   │   └── connector.go -- Full CRUD via godo
│   ├── ssh/
│   │   ├── types.go     -- ExecResult, HostStatus
│   │   ├── backend.go   -- Backend interface (Connect, Exec, Ping, Close)
│   │   ├── api_backend.go -- golang.org/x/crypto/ssh implementation
│   │   └── connector.go -- Connector facade, key file resolution
│   ├── namecheap/
│   │   ├── types.go     -- Domain, DNSRecord, DomainStatus
│   │   ├── client.go    -- HTTP client wrapping Namecheap XML API
│   │   └── connector.go -- Connector facade, SplitDomain helper
│   ├── forge/
│   │   ├── types.go     -- Server, Site, Deployment
│   │   ├── client.go    -- HTTP client for Forge REST API
│   │   └── connector.go -- Read-only connector (CanHealth only)
│   ├── cloudflare/
│   │   ├── types.go     -- Zone, DNSRecord, Tunnel
│   │   ├── backend.go   -- Backend interface
│   │   ├── api_backend.go -- cloudflare-go v4 SDK
│   │   ├── cli_backend.go -- wrangler CLI wrapper
│   │   └── connector.go -- Dual backend facade, auto-selects API vs CLI
│   └── docker/
│       ├── types.go     -- Container, ComposeStack, ComposeService
│       ├── backend.go   -- Backend interface (9 methods)
│       ├── cli_backend.go -- docker/docker compose CLI wrapper
│       └── connector.go -- Connector facade, compose-aware
├── pipeline/
│   ├── pipeline.go      -- Pipeline, Stage types
│   ├── builder.go       -- Fluent builder API
│   ├── dag.go           -- Stage DAG with cycle detection + Kahn's sort
│   ├── executor.go      -- DAG executor, parallel levels, rollback
│   ├── resolve.go       -- Config PipelineDef -> executable Pipeline
│   ├── executor_test.go -- 11 tests
│   └── actions/
│       ├── shell.go     -- ShellAction (sh -c)
│       ├── build.go     -- BuildAction (runs local process build commands from v2 resource specs)
│       ├── connector.go -- Start/Stop actions (wraps connector interface)
│       └── health.go    -- HealthWaitAction (poll URL)
├── secrets/
│   ├── keychain.go      -- KeychainProvider (go-keyring + env fallback)
│   └── keychain_test.go
├── mcp/
│   ├── server.go        -- JSON-RPC 2.0 MCP server over stdio
│   ├── tools_result.go       -- shared MCP JSON result helpers
│   ├── tools_observability.go -- health tool
│   ├── tools_resources.go    -- project/resource runtime tools
│   ├── tools_pipeline.go     -- pipeline_list/pipeline_run tools
│   ├── tools_github.go       -- github_status/github_releases/github_runs tools
│   ├── tools_ssh.go          -- ssh_exec/ssh_status tools
│   ├── tools_namecheap.go    -- domain_list/domain_status/dns_list tools
│   ├── tools_forge.go        -- forge_servers/forge_server/forge_sites tools
│   ├── tools_cloudflare.go   -- cloudflare_zones/dns_list/dns_create tools
│   └── tools_docker.go       -- docker_ps/docker_logs/docker_up/docker_down tools
├── service/             -- Legacy process lifecycle package still present in-tree but no longer on the active operator path
├── daemon/              -- Daemon restart helpers + remaining support code
├── pausectl/            -- File-flag pause/resume
└── tui/                 -- Legacy Bubble Tea UI code still present in-tree but no longer on the active operator path
```

## MCP Tools (active runtime + connector set)

| Tool | Source File | Description |
|------|-----------|-------------|
| `cerberus_health` | tools_observability.go | Health check results |
| `cerberus_project_list` | tools_resources.go | Project list with resource counts |
| `cerberus_resource_list` | tools_resources.go | Resource list with local runtime metadata |
| `cerberus_resource_status` | tools_resources.go | Runtime status for a specific resource |
| `cerberus_resource_inspect` | tools_resources.go | Detailed runtime/install inspection for a specific resource |
| `cerberus_resource_doctor` | tools_resources.go | Runtime/install checks plus operator guidance |
| `cerberus_resource_logs` | tools_resources.go | Tail resource log output |
| `cerberus_resource_reload` | tools_resources.go | Restart or kickstart the current installed resource |
| `cerberus_resource_deploy` | tools_resources.go | Build then apply a resource |
| `cerberus_resource_apply` | tools_resources.go | Apply a specific resource through its runtime backend |
| `cerberus_resource_sync` | tools_resources.go | Sync installed artifacts for an artifact-backed resource |
| `cerberus_resource_remove` | tools_resources.go | Remove a resource from its runtime backend |
| `cerberus_pipeline_list` | tools_pipeline.go | List pipelines |
| `cerberus_pipeline_run` | tools_pipeline.go | Execute a pipeline |
| `cerberus_github_status` | tools_github.go | GitHub repo status |
| `cerberus_github_releases` | tools_github.go | GitHub releases |
| `cerberus_github_runs` | tools_github.go | GitHub Actions runs |
| `cerberus_ssh_exec` | tools_ssh.go | Execute command on remote host |
| `cerberus_ssh_status` | tools_ssh.go | Check SSH host connectivity |
| `cerberus_domain_list` | tools_namecheap.go | List Namecheap domains |
| `cerberus_domain_status` | tools_namecheap.go | Domain registration status |
| `cerberus_dns_list` | tools_namecheap.go | List DNS records for domain |
| `cerberus_forge_servers` | tools_forge.go | List Forge servers |
| `cerberus_forge_server` | tools_forge.go | Forge server details |
| `cerberus_forge_sites` | tools_forge.go | List sites on Forge server |
| `cerberus_cloudflare_zones` | tools_cloudflare.go | List Cloudflare zones |
| `cerberus_cloudflare_dns_list` | tools_cloudflare.go | List DNS records for zone |
| `cerberus_cloudflare_dns_create` | tools_cloudflare.go | Create DNS record |
| `cerberus_docker_ps` | tools_docker.go | List Docker containers |
| `cerberus_docker_logs` | tools_docker.go | Container logs |
| `cerberus_docker_up` | tools_docker.go | Start container/compose stack |
| `cerberus_docker_down` | tools_docker.go | Stop container/compose stack |

## External Dependencies

| Dependency | Version | Purpose |
|-----------|---------|---------|
| `modernc.org/sqlite` | latest | Pure Go SQLite (no CGO) |
| `github.com/zalando/go-keyring` | v0.2.8 | OS keychain access |
| `github.com/digitalocean/godo` | v1.180.0 | DigitalOcean API |
| `github.com/google/go-github/v72` | v72.0.0 | GitHub API |
| `github.com/charmbracelet/bubbletea` | v1.3.10 | TUI framework |
| `github.com/spf13/cobra` | v1.10.2 | CLI framework |
| `gopkg.in/yaml.v3` | v3.0.1 | YAML config |
| `golang.org/x/crypto` | v0.49.0 | SSH client (ssh connector) |
| `github.com/cloudflare/cloudflare-go/v4` | v4.6.0 | Cloudflare API |

## Connector Implementation Guide

When adding a new connector:

1. Create `internal/connector/<name>/` directory
2. Define `types.go` with normalized domain types
3. If the service has both an API and CLI: define `backend.go` interface, `api_backend.go`, `cli_backend.go`
4. If API-only: just implement the connector directly
5. Create `connector.go` implementing `domain.Connector`
6. Add CLI commands in `cmd/cerberus/cmd_<name>.go`
7. Add MCP tools in `internal/mcp/tools_<name>.go`
8. Register MCP tools in `cmd_mcp.go` and `cmd_daemon.go`
9. Register CLI command in `main.go` init()

### Secrets convention

- Keychain service: `"cerberus"`
- Keychain key: `"<connector>/<key>"` (e.g. `"namecheap/api_key"`)
- Env var: `CERBERUS_<CONNECTOR>_<KEY>` (e.g. `CERBERUS_NAMECHEAP_API_KEY`)

### Config convention for external resources

```yaml
resources:
  - id: my-server
    type: server
    connector: digitalocean
    project: my-project
    config:
      # All connector-specific fields go in config map
      region: nyc3
      size: s-1vcpu-1gb
      droplet_id: 12345
```

## Connector Reference

### SSH (`internal/connector/ssh/`)
- **Backend:** `golang.org/x/crypto/ssh` (API-only, no CLI backend)
- **Resource type:** `server`
- **Config keys:** `host`, `port` (default 22), `user`, `key_file`
- **Secrets:** `ssh/<resource-id>/key` or `CERBERUS_SSH_KEY_FILE` env
- **Capabilities:** CanStatus, CanStop

### Namecheap (`internal/connector/namecheap/`)
- **Backend:** Direct HTTP + XML API (no external SDK)
- **Resource type:** `domain`
- **Config keys:** `domain`
- **Secrets:** `namecheap/api_user`, `namecheap/api_key`, `namecheap/username`, `CERBERUS_NAMECHEAP_CLIENT_IP`
- **Capabilities:** CanStatus only

### Laravel Forge (`internal/connector/forge/`) — Read-Only, Transitional
- **Backend:** Direct HTTP + JSON (Forge REST API)
- **Resource type:** `server`
- **Config keys:** `server_id`
- **Secrets:** `forge/api_token`
- **Capabilities:** CanStatus only (all writes return "not supported: forge connector is read-only")
- **Note:** Will be removed once migration from Forge is complete

### Cloudflare (`internal/connector/cloudflare/`)
- **Backend:** Dual — API (`cloudflare-go/v4`) + CLI (`wrangler`)
- **Resource type:** `domain`
- **Config keys:** `zone_id`, `record_type`, `name`, `content`, `ttl`, `proxied`
- **Secrets:** `cloudflare/api_token`
- **Capabilities:** CanStatus, CanCreate, CanDestroy

### Docker (`internal/connector/docker/`)
- **Backend:** CLI-only (`docker`, `docker compose`) — Backend interface ready for future API backend
- **Resource type:** `container`
- **Config keys:** `container_name` or `compose_file` (determines mode)
- **Capabilities:** CanStatus, CanStart, CanStop, CanDestroy

## Key Design Decisions

1. **V2 resource clients stay thin** — CLI fallback, socket API, and MCP should call the shared resource runtime service rather than implementing resource logic independently
2. **Local connector still wraps parts of service.go for `dev_session`** — acceptable transition seam for now, but this is implementation debt behind the v2 runtime layer
3. **Config map for connector-specific fields** — `Resource.Config` is `map[string]any`, avoiding god struct
4. **DAG reuse** — pipeline executor uses same topological sort pattern as `service/dag.go`
5. **Store is optional** — lazily opened, no DB for local-only users
6. **Fluent pipeline builder + YAML declaration** — pipelines in code (tests) or config (users)
7. **Dual backend pattern** — connectors auto-select API vs CLI based on available credentials/tools
8. **Forge is read-only** — transitional connector, will be removed after migration
