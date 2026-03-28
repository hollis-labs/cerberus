# Backend Context — Cerberus

> Project-specific backend conventions. Loaded by the backend agent role when working in this project.
> Lives at `cerberus/.agentrc/backend.md`.

## v2 Architecture

Cerberus is evolving from a local service manager into a universal infrastructure control plane. **Read `.agentrc/docs/architecture-v2.md` first** — it has the complete architecture, current implementation status, package layout, all 15 MCP tools, and the connector implementation guide.

Phase 1 (foundation) and Phase 2 (pipeline engine, GitHub, DO connectors) are complete. Next batch: SSH, Namecheap, Laravel Forge (read-only), Cloudflare, Docker connectors. Use `internal/connector/github/` as the reference implementation for the dual-backend pattern.

## Stack

- **Go version:** 1.25.3
- **Module path:** `github.com/chrispian/cerberus`
- **Router:** None (not an HTTP API service)
- **Database:** None (file-based state: PID files, lock files, pause flag files in `~/.cerberus/`)
- **CLI framework:** Cobra (`github.com/spf13/cobra v1.10.2`)
- **TUI framework:** Bubble Tea (`github.com/charmbracelet/bubbletea v1.3.10`) + Lip Gloss (`github.com/charmbracelet/lipgloss v1.1.0`) + Bubbles (`github.com/charmbracelet/bubbles v1.0.0`)
- **Config format:** YAML (`gopkg.in/yaml.v3`)
- **Notable dependencies:** None beyond the above. No database drivers, no HTTP routers, no auth libraries. This is a CLI/TUI tool.

## Project Structure

```
cmd/
└── cerberus/
    └── main.go             # All CLI commands (TUI, up, down, restart, status, logs,
                            #   build, rebuild, validate, doctor, init, daemon, mcp,
                            #   install, uninstall, pause, resume) — 1051 lines
internal/
├── config/
│   ├── config.go           # YAML config loading, ServiceDef/Config structs, tilde expansion
│   └── config_test.go      # Config loading tests (5 test cases)
├── daemon/
│   ├── monitor.go          # Health monitor loop, auto-restart logic, Volon alerting
│   └── pidguard.go         # Daemon PID file management, single-instance guard
├── mcp/
│   ├── server.go           # JSON-RPC 2.0 MCP server over stdio
│   ├── tools.go            # cerberus_status tool + cerberus_health tool
│   ├── tools_lifecycle.go  # cerberus_start/stop/restart/rebuild tools with audit logging
│   └── tools_observability.go  # cerberus_logs, cerberus_build, cerberus_health tools
├── pausectl/
│   └── pausectl.go         # File-flag-based pause/resume for auto-restart (avoids import cycles)
├── service/
│   ├── service.go          # ManagedService: Start/Stop/Build/Poll/status detection — 588 lines
│   ├── manager.go          # ServiceManager: DAG-ordered StartAll/StopAll/StopSubset
│   ├── dag.go              # Dependency DAG: BuildDAG, TopologicalOrder (Kahn's algorithm)
│   ├── dag_test.go         # DAG tests (10 test cases: linear, parallel, cycle, diamond)
│   ├── healthcheck.go      # HealthChecker: periodic URL/command health checks
│   ├── healthcheck_test.go # Health check tests (10 test cases)
│   ├── autorestart.go      # RestartPolicy: exponential backoff auto-restart
│   ├── autorestart_test.go # Auto-restart tests (11 test cases)
│   ├── pidfile.go          # PID file + meta file CRUD in ~/.cerberus/pids/
│   ├── pidfile_test.go     # PID file tests (8 test cases)
│   ├── lockfile.go         # flock-based service locks with stale lock detection
│   ├── lockfile_test.go    # Lock file tests (5 test cases)
│   ├── portcheck.go        # Port conflict detection via lsof, process name lookup
│   ├── portcheck_test.go   # Port check + doctor tests (9 test cases)
│   ├── orphan.go           # Orphan process detection (detect-only, never kills)
│   ├── doctor.go           # Diagnostic checks: port, binary, directory
│   └── lifecycle_log.go    # Structured JSON lifecycle logger + audit logging
└── tui/
    ├── model.go            # Bubble Tea Model: Update loop, keybindings, filtering
    ├── view.go             # TUI rendering: table, scrollbar, summary bar, help footer
    ├── styles.go           # Lip Gloss style definitions (colors, badges)
    ├── grouping.go         # Service grouping by project, tag filtering, flat item list
    ├── groupview.go        # Group header rendering, tag filter indicator
    └── logview.go          # Log viewer sub-model with scroll, follow mode
schema/
└── cerberus.yaml.schema.json  # JSON Schema for config validation
init/
└── launchd/
    └── com.fragments-engine.cerberus.plist.tmpl  # macOS launch agent template
bin/
└── cerberus                # Pre-built binary
```

## Package Inventory

| Package | Location | Responsibility |
|---------|----------|----------------|
| config | `internal/config/` | YAML config loading, `ServiceDef`/`Config` structs, tilde expansion, default config generation |
| service | `internal/service/` | Core service lifecycle: `ManagedService` (start/stop/build/poll), `ServiceManager` (DAG-ordered operations), `DAG` (dependency graph), PID files, lock files, port checks, health checks, auto-restart, orphan detection, doctor diagnostics, lifecycle logging |
| daemon | `internal/daemon/` | Background daemon: `Monitor` (periodic health checks + auto-restart with cooldowns), `PIDGuard` (single-instance daemon management), Volon alert integration |
| mcp | `internal/mcp/` | MCP tool server: JSON-RPC 2.0 over stdio, 7 tools (status, start, stop, restart, rebuild, logs, build, health) with audit logging for destructive operations |
| pausectl | `internal/pausectl/` | File-flag pause/resume mechanism for auto-restart. Separated from service/daemon to avoid import cycles |
| tui | `internal/tui/` | Bubble Tea TUI: grouped/flat service list, status badges, filtering, sorting, log viewer, scrolling viewport |

## Patterns to Follow

### Error Handling
- Errors are wrapped with `fmt.Errorf("context: %w", err)` consistently throughout. Never raw errors.
- Service operations return `[]error` for bulk operations (see `manager.go:StartAll`, `StopAll`) to collect errors from concurrent goroutines.
- MCP tools never return Go errors to the caller. They return JSON with `success: false` and `error` fields. This is a deliberate pattern — see `marshalResult()` in `tools_lifecycle.go`.
- Lifecycle log captures all service events as structured JSON via `slog` to `~/.cerberus/cerberus.log`.

### Configuration
- Config loaded once from `~/.cerberus/config.yaml` (default) or `--config` flag path.
- `config.Config` struct contains `Version` (int) and `Services` (slice of `ServiceDef`).
- `ServiceDef` is the central struct — 20 fields covering service identity, runtime, health checks, auto-restart, and profiles.
- Tilde expansion happens during `config.Load()` for `Dir` and `LogFile` fields.
- Legacy `health` field auto-mapped to `HealthCheckCfg.URL` for backward compat.
- **CRITICAL:** Never set `port: 0` on any service. Omit the field entirely. See `CLAUDE.md` for the full explanation.

### CLI Commands (Cobra)
- All commands defined in `cmd/cerberus/main.go`. Each command is a `*cobra.Command` var with `RunE` handler.
- Common pattern: `loadServices()` -> `filterServices(services, args, tag)` -> operate on targets.
- Tag filtering via `--tag` flag on `up`, `down`, `restart`, `build`, `rebuild`.
- Commands that modify service state pause auto-restart via `pausectl.PauseService()` before operating, with `defer ResumeService()` for restart/rebuild (but NOT for stop — deliberate stop should stay stopped).

### MCP Tools
- Each tool is a `Tool` struct with `Name`, `Description`, `InputSchema` (JSON Schema), and `Handler` func.
- Destructive operations (stop, restart, rebuild) require a `reason` field and log an audit entry via `logAudit()`.
- Protected services (`protected: true` in config) reject stop/restart/rebuild via MCP — only the daemon auto-recovery can manage them.
- Tool registration happens in `cmd/cerberus/main.go` in both the `daemon` and `mcp` commands.

### Process Management
- PID files stored in `~/.cerberus/pids/<serviceID>.pid` with companion `.meta` JSON files.
- Lock files in `~/.cerberus/locks/<serviceID>.lock` using `flock(2)` with 5-second timeout and stale lock detection.
- Status detection: PID file (fast path) -> port-based lsof (fallback, only during startup grace period).
- Process groups: services started with `Setpgid: true` so `Stop()` can `kill(-pid, SIGTERM)` the entire group.
- SIGKILL escalation after 10 seconds if SIGTERM doesn't work.

### Testing
- Tests use `t.TempDir()` for isolation — never touch the real `~/.cerberus/` directory.
- All file-based subsystems have `*At(base, ...)` variants for testability (e.g., `WritePIDFileAt`, `AcquireLockAt`).
- Table-driven tests used in `autorestart_test.go` (duration parsing) and `config_test.go`.
- HTTP tests use `httptest.NewServer` for health check testing.
- Port tests use ephemeral ports via `net.Listen("tcp", "127.0.0.1:0")`.
- No mocking frameworks — all test doubles are inline.

## Anti-Patterns to Avoid

- **Package cycles** — `pausectl` was extracted specifically to break a cycle between `daemon` and `service`. Don't introduce new cross-package dependencies without checking for cycles.
- **God file: `cmd/cerberus/main.go`** (1051 lines) — Contains all 16 CLI commands in a single file. Each command handler is 20-60 lines of inline logic. Consider splitting into `cmd/cerberus/cmd_*.go` files if adding new commands. *File: `cmd/cerberus/main.go`*
- **Duplicated env setup logic** — The environment construction (inherit env, ensurePath, load env file, expand tilde in env values) is duplicated across `Start()` (line 196-220), `Build()` (line 312-328), and `BuildSync()` (line 516-533) in `service.go`. Should be extracted to a shared helper. *File: `internal/service/service.go:196`, `:312`, `:516`*
- **Duplicated service lookup in MCP tools** — `tools_observability.go` has inline `for _, s := range services` loops to find a service by ID (lines 113-120, 180-187) instead of using the `findService()` helper already defined in `tools_lifecycle.go:78`. *File: `internal/mcp/tools_observability.go:113`, `:180`*
- **Hardcoded Volon URL** — The alert endpoint `http://127.0.0.1:8085/v1/notifications` is hardcoded in `monitor.go:358`. Should be configurable or at least a const. *File: `internal/daemon/monitor.go:358`*
- **Hardcoded default max restart attempts** — The value `3` appears as a magic number in `monitor.go:238`, `tools.go:99`, and `tools_observability.go:314` instead of referencing a shared constant. *File: `internal/daemon/monitor.go:238`, `internal/mcp/tools.go:99`, `internal/mcp/tools_observability.go:314`*
- **Version string drift** — `CerberusVersion` in `pidfile.go` is `"0.2.0"` while `version` in `main.go` is `"0.3.0"` and the MCP server registers as `"0.1.0"`. Three different version strings. *File: `internal/service/pidfile.go:19`, `cmd/cerberus/main.go:33`, `cmd/cerberus/main.go:782`*
- **Blocking `time.Sleep` in TUI** — The restart key handler (`r` and `R` in `model.go:205-249`) calls `time.Sleep(500 * time.Millisecond)` which blocks the TUI thread. Should use a Bubble Tea command. *File: `internal/tui/model.go:207`, `:229`*
- **Bubble sort in TUI** — `sorted()` in `model.go:399-420` uses O(n^2) bubble sort. Fine for small service lists but should use `sort.Slice`. *File: `internal/tui/model.go:399`*
- **Untested packages** — `daemon/`, `mcp/`, `pausectl/`, and `tui/` have zero test files. The `service/` package has good coverage but the other 4 packages have none.
- **Fat handlers** — The MCP tool handlers in `tools_lifecycle.go` and `tools_observability.go` contain all business logic inline (validation, audit, pause/resume, build, stop, start). No separation between request handling and domain logic.

## Reference Implementations

| Pattern | Reference File | Why it's good |
|---------|---------------|---------------|
| DAG + tests | `internal/service/dag.go` + `dag_test.go` | Clean algorithm implementation (Kahn's), comprehensive tests covering linear, parallel, diamond, cycle detection, missing deps |
| Config with backward compat | `internal/config/config.go` + `config_test.go` | Shows how to evolve config format while maintaining backward compatibility. Legacy `health` field transparently maps to new `health_check.url` |
| Testable file operations | `internal/service/pidfile.go` + `pidfile_test.go` | Every function has a `*At(base, ...)` variant for test isolation. Tests use `t.TempDir()`. Good pattern for any file-based subsystem |

## Build & Run

- **Build:** `make build` or `go build -o cerberus ./cmd/cerberus`
- **Install:** `make install` or `go install ./cmd/cerberus`
- **Test:** `make test` or `go test ./...`
- **Lint:** `make lint` or `go vet ./...` (full lint: `golangci-lint run --new`)
- **Run TUI:** `./cerberus` (or just `cerberus` if installed)
- **Run daemon:** `cerberus daemon` (forks to background) or `cerberus daemon --foreground`
- **Run MCP server:** `cerberus mcp` (stdio JSON-RPC)
- **Install as launch agent:** `cerberus install` (macOS only, writes `~/Library/LaunchAgents/` plist)

## Notes

- Config lives at `~/.cerberus/config.yaml` by default (overridable with `--config`).
- All runtime state (PID files, lock files, logs, alerts, pause flags) stored under `~/.cerberus/`.
- The daemon mode runs both the health monitor AND an MCP server simultaneously — the MCP server in daemon mode receives a non-nil `monitor` reference for enriched status/health responses.
- Pre-commit hooks via lefthook: `gofmt`, `goimports`, `golangci-lint --new`, `go vet`. Pre-push: `go test ./...`.
- The `protected` field on services means "protected from external stop via MCP tools" — it does NOT mean auto-restart. Those are separate concerns (`protected` vs `auto_restart`).
