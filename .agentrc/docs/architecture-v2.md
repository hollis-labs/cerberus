# Cerberus v2 Architecture

> Reference document for all agents working on Cerberus. Describes the target architecture, domain model, key design decisions, and current implementation status.

## Vision

Cerberus evolves from a local process manager into a **universal infrastructure control plane**. It manages local dev services, cloud servers, DNS, containers, CI/CD, SSH, and deployments through a unified connector pattern. Agent-first (CLI + MCP), with a Wails desktop app as the eventual GUI.

## Current Status (as of 2026-03-28)

### Completed

**Phase 1 — Foundation:**
- Domain model (`internal/domain/`) — Resource, Project, State, Connector, Store, Action, SecretProvider
- Connector registry (`internal/connector/`) with tests
- Local connector (`internal/connector/local/`) wrapping existing service package, with mapper + round-trip tests
- SQLite store (`internal/store/sqlite/`) with migrations, CRUD, and tests
- Secrets provider (`internal/secrets/`) — keychain + env fallback with tests
- Config v2 (`internal/config/`) — v2 schema, v1-to-v2 auto-migration, `LoadUnified()`, 16 tests
- App struct (`internal/app/`) — central dependency container
- CLI split — `cmd/cerberus/main.go` split into 14+ per-command files
- New CLI commands: `config migrate`, `project list/show`, `resource list/show`
- MCP backward compat — all 8 original tools unchanged, wired through App

**Phase 2 — External Connectors + Pipelines:**
- Pipeline engine (`internal/pipeline/`) — fluent builder, DAG executor, parallel stages, rollback, 11 tests
- Pipeline actions (`internal/pipeline/actions/`) — Shell, Build, Start, Stop, HealthWait
- Pipeline config support — `PipelineDef`, `StageDef`, `ActionDef` in v2 config
- Pipeline resolver (`internal/pipeline/resolve.go`) — config to executable pipeline
- GitHub connector (`internal/connector/github/`) — **dual backend pattern**: API (go-github v72) + CLI (`gh`)
- DigitalOcean connector (`internal/connector/digitalocean/`) — full CRUD via godo
- CLI: `pipeline list/show/run`, `github status/releases/runs`, `server list/show`
- MCP: 15 tools total (8 original + 7 new)

### Next Session — Connector Batch

Five new connectors in one session:
1. **SSH** — remote command execution for agent work on servers
2. **Namecheap** — domain registration, DNS management, SSL certs
3. **Laravel Forge** — read-only view/review of existing Forge-managed servers (transitional — moving away from Forge)
4. **Cloudflare** — DNS zones/records, tunnels, SSL
5. **Docker** — container/compose lifecycle for local dev

TUI refresh deferred to last.

## Core Principles

1. **CLI-first** — every operation is a testable CLI command. MCP is a thin layer exposing CLI to agents.
2. **Connector pattern** — all external services implement a common `Connector` interface. Inspired by conduit's Provider pattern.
3. **Dual backend pattern** — connectors that have both an API SDK and a CLI tool (GitHub/gh, Docker/docker, Cloudflare/wrangler) use a Backend interface with two implementations. Auto-selection: API if token available, CLI if tool installed.
4. **Project as first-class concept** — a Project groups related Resources.
5. **Pipeline pattern** — multi-step workflows use a fluent builder + DAG executor with parallel stages and rollback.
6. **Store abstraction** — SQLite default, interface allows future DB adapters. Lazily opened.
7. **Backward compatible** — v1 config works forever. Migration is in-memory only.

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
├── main.go              -- rootCmd, runTUI, loadServices, filterServices
├── cmd_up.go            -- up command
├── cmd_down.go          -- down command
├── cmd_restart.go       -- restart command
├── cmd_status.go        -- status command
├── cmd_logs.go          -- logs command
├── cmd_build.go         -- build command
├── cmd_rebuild.go       -- rebuild command
├── cmd_validate.go      -- validate command
├── cmd_doctor.go        -- doctor command
├── cmd_init.go          -- init command
├── cmd_daemon.go        -- daemon command (uses App struct)
├── cmd_mcp.go           -- MCP server (uses App struct, registers all 15 tools)
├── cmd_install.go       -- install/uninstall launchd agent
├── cmd_pause.go         -- pause/resume
├── cmd_config.go        -- config migrate
├── cmd_project.go       -- project list/show
├── cmd_resource.go      -- resource list/show
├── cmd_pipeline.go      -- pipeline list/show/run
├── cmd_github.go        -- github status/releases/runs
└── cmd_server.go        -- server list/show

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
│   └── app.go           -- App struct (config, store, registry, secrets, services)
├── config/
│   ├── config.go        -- v1 Config/ServiceDef, Load(), LoadUnified()
│   ├── v2.go            -- ConfigV2, ProjectDef, ResourceDef, PipelineDef, StageDef, ActionDef
│   ├── migrate.go       -- MigrateV1ToV2()
│   └── *_test.go        -- 16 tests
├── store/sqlite/
│   ├── store.go         -- SQLiteStore implementing domain.Store
│   ├── migrations.go    -- Embedded migration runner
│   ├── migrations/*.sql -- 4 migration files
│   └── store_test.go    -- CRUD tests
├── connector/
│   ├── registry.go      -- Registry (Register/Get/List/IDs) + tests
│   ├── local/
│   │   ├── connector.go -- LocalConnector wrapping service.ManagedService
│   │   ├── mapper.go    -- ServiceDefToResource / ResourceToServiceDef
│   │   └── mapper_test.go
│   ├── github/
│   │   ├── types.go     -- RepoStatus, Release, WorkflowRun
│   │   ├── backend.go   -- Backend interface
│   │   ├── api_backend.go  -- go-github v72 SDK
│   │   ├── cli_backend.go  -- gh CLI wrapper
│   │   └── connector.go    -- Unified facade, auto-selects API vs CLI
│   └── digitalocean/
│       ├── types.go     -- DropletStatus
│       └── connector.go -- Full CRUD via godo
├── pipeline/
│   ├── pipeline.go      -- Pipeline, Stage types
│   ├── builder.go       -- Fluent builder API
│   ├── dag.go           -- Stage DAG with cycle detection + Kahn's sort
│   ├── executor.go      -- DAG executor, parallel levels, rollback
│   ├── resolve.go       -- Config PipelineDef -> executable Pipeline
│   ├── executor_test.go -- 11 tests
│   └── actions/
│       ├── shell.go     -- ShellAction (sh -c)
│       ├── build.go     -- BuildAction (wraps ManagedService.BuildSync)
│       ├── connector.go -- Start/Stop actions (wraps connector interface)
│       └── health.go    -- HealthWaitAction (poll URL)
├── secrets/
│   ├── keychain.go      -- KeychainProvider (go-keyring + env fallback)
│   └── keychain_test.go
├── mcp/
│   ├── server.go        -- JSON-RPC 2.0 MCP server over stdio
│   ├── tools.go         -- cerberus_status tool
│   ├── tools_lifecycle.go    -- start/stop/restart/rebuild tools
│   ├── tools_observability.go -- logs/build/health tools
│   ├── tools_resources.go    -- project_list/resource_list tools
│   ├── tools_pipeline.go     -- pipeline_list/pipeline_run tools
│   └── tools_github.go       -- github_status/github_releases/github_runs tools
├── service/             -- Existing process lifecycle (unchanged)
├── daemon/              -- Health monitor, auto-restart
├── pausectl/            -- File-flag pause/resume
└── tui/                 -- Bubble Tea views
```

## MCP Tools (15 total)

| Tool | Source File | Description |
|------|-----------|-------------|
| `cerberus_status` | tools.go | Service status with daemon state |
| `cerberus_start` | tools_lifecycle.go | Start a service |
| `cerberus_stop` | tools_lifecycle.go | Stop a service (requires reason) |
| `cerberus_restart` | tools_lifecycle.go | Restart a service (requires reason) |
| `cerberus_rebuild` | tools_lifecycle.go | Build + restart (requires reason) |
| `cerberus_logs` | tools_observability.go | Tail service logs |
| `cerberus_build` | tools_observability.go | Run build command |
| `cerberus_health` | tools_observability.go | Health check results |
| `cerberus_project_list` | tools_resources.go | List projects |
| `cerberus_resource_list` | tools_resources.go | List resources with filters |
| `cerberus_pipeline_list` | tools_pipeline.go | List pipelines |
| `cerberus_pipeline_run` | tools_pipeline.go | Execute a pipeline |
| `cerberus_github_status` | tools_github.go | GitHub repo status |
| `cerberus_github_releases` | tools_github.go | GitHub releases |
| `cerberus_github_runs` | tools_github.go | GitHub Actions runs |

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

## Next Connector Batch (Planned)

### SSH Connector
- **Purpose:** Remote command execution on servers for agent work
- **Backend:** Pure Go SSH via `golang.org/x/crypto/ssh`
- **Resource type:** `server` (same as DO, but for arbitrary SSH-accessible hosts)
- **Operations:** Status (ping/connect), Start (no-op or wake), Stop (shutdown command), Create/Destroy (no-op)
- **Extra methods:** `Exec(ctx, host, command)` for running arbitrary commands
- **Config keys:** `host`, `port` (default 22), `user`, `key_file` or `key` (from secrets)
- **CLI:** `cerberus ssh exec <resource> <command>`, `cerberus ssh status <resource>`
- **MCP:** `cerberus_ssh_exec`, `cerberus_ssh_status`

### Namecheap Connector
- **Purpose:** Domain registration, DNS management, SSL certificates
- **SDK:** `github.com/namecheap/go-namecheap-sdk/v2` or direct API (XML API)
- **Resource types:** `domain`, `dns-record`
- **Operations:** Status (check domain/DNS), Create (register domain, add DNS record)
- **Config keys:** `domain`, `record_type`, `host`, `value`, `ttl`
- **Secrets:** `namecheap/api_user`, `namecheap/api_key`
- **CLI:** `cerberus domain list`, `cerberus domain status <domain>`, `cerberus dns list <domain>`
- **MCP:** `cerberus_domain_list`, `cerberus_domain_status`, `cerberus_dns_list`

### Laravel Forge Connector (Read-Only, Transitional)
- **Purpose:** View/review existing Forge-managed servers and sites during migration away from Forge
- **SDK:** Forge has a REST API — use direct HTTP client, no official Go SDK
- **Resource types:** `server`, `site`
- **Operations:** Status only (read-only). No Create/Start/Stop/Destroy.
- **Config keys:** `server_id`, `site_id`
- **Secrets:** `forge/api_token`
- **CLI:** `cerberus forge servers`, `cerberus forge sites <server-id>`, `cerberus forge server <server-id>`
- **MCP:** `cerberus_forge_servers`, `cerberus_forge_sites`, `cerberus_forge_server`
- **Note:** This is transitional — will be removed once all services are migrated to direct DO/SSH management

### Cloudflare Connector
- **Purpose:** DNS zones/records, tunnels, SSL management
- **SDK:** `github.com/cloudflare/cloudflare-go` (official)
- **Dual backend:** API (cloudflare-go) + CLI (wrangler) if applicable
- **Resource types:** `domain`, `dns-record`, `tunnel`
- **Operations:** Create/Status/Destroy for DNS records, Status for zones
- **Config keys:** `zone_id`, `record_type`, `name`, `content`, `ttl`, `proxied`
- **Secrets:** `cloudflare/api_token`
- **CLI:** `cerberus cloudflare zones`, `cerberus cloudflare dns list <zone>`, `cerberus cloudflare dns create`
- **MCP:** `cerberus_cloudflare_zones`, `cerberus_cloudflare_dns_list`, `cerberus_cloudflare_dns_create`

### Docker Connector
- **Purpose:** Container and compose stack lifecycle for local dev
- **SDK:** `github.com/docker/docker/client` (official)
- **Dual backend:** API (Docker SDK) + CLI (`docker` / `docker compose`)
- **Resource types:** `container`, `compose-stack`
- **Operations:** Create, Start, Stop, Destroy, Status, Logs
- **Config keys:** `image`, `ports`, `volumes`, `environment`, `compose_file`
- **CLI:** `cerberus docker ps`, `cerberus docker logs <container>`, `cerberus docker up/down`
- **MCP:** `cerberus_docker_ps`, `cerberus_docker_logs`, `cerberus_docker_up`, `cerberus_docker_down`

## Key Design Decisions

1. **Local connector wraps service.go as-is** — no refactoring to minimize regression risk
2. **Config map for connector-specific fields** — `Resource.Config` is `map[string]any`, avoiding god struct
3. **DAG reuse** — pipeline executor uses same topological sort pattern as `service/dag.go`
4. **Store is optional** — lazily opened, no DB for local-only users
5. **Fluent pipeline builder + YAML declaration** — pipelines in code (tests) or config (users)
6. **Dual backend pattern** — connectors auto-select API vs CLI based on available credentials/tools
7. **Forge is read-only** — transitional connector, will be removed after migration
