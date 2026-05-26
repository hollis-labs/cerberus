# Cerberus

## agentrc
- If `.agentrc/boot-prompt.md` exists, read it first for session context.
- If the user says "Boot <agent>", look up the agent in `.agentrc/config.yaml` under `agents:`. Load each role file from `~/.agentrc/roles/` (using the `file:` path from `~/.agentrc/config.yaml` role definitions), load the listed skills, and read the project context file from `.agentrc/` if specified.
- If the user says "Boot <role>" and no agent matches, fall back to loading that single role from `~/.agentrc/roles/` by type directory (domain/, stack/, meta/).
- After context compaction, re-read the active role and project context files.
- Do not guess when uncertain. Stop and ask.
- Prefer focused, minimal output. No trailing summaries.
- Sub-agent output stays in the sub-agent. Main context gets one-line confirmations.

## Project Overview

Cerberus is an agent-first, single-binary Go control plane for managing local OS processes (daemons, dev servers, background services) across the portfolio. It exposes one consistent surface — CLI, daemon, HTTP/socket API, web console, and MCP — over the v2 `resource` model.

## Build & Test

```bash
go build ./cmd/cerberus/
go test ./...
```

## Architecture

- `cmd/cerberus/` — Entry point: Cobra CLI, daemon mode, MCP adapter
- `internal/config/` — v2 resource config loading and validation
- `internal/cerbapi/` — Shared resource runtime service (CLI/API/MCP route through here)
- `internal/service/` — Process lifecycle management
- `internal/daemon/` — Health monitor, auto-restart, supervisor
- `internal/mcp/` — MCP tools (resource, registry, platform connectors)
- `internal/webui/` — Web console server + embedded assets
- `internal/registry/` — v2 project/resource registry

## CRITICAL: Never Set port: 0

**Do NOT set `port: 0` on any service in the config.** Omit the `port` field entirely for services that don't listen on a TCP port (CLIs, daemons without HTTP, build-only entries).

**Why:** Cerberus uses `lsof -ti :<port>` for status detection. `lsof -ti :0` returns arbitrary macOS system PIDs (identityservicesd, mDNSResponder, etc.), causing false-positive "running" status in the daemon monitor. This has caused repeated incidents.

**Correct:**
```yaml
- id: my-daemon
  command: ["./my-daemon"]
  # No port field — detection uses PID file only
  tags: [daemon]
```

**Wrong:**
```yaml
- id: my-daemon
  command: ["./my-daemon"]
  port: 0        # NEVER do this
  tags: [daemon]
```

## CRITICAL: To update a running service, DEPLOY — don't just build or restart

For any `run_from: artifact` resource (most `os_service` backends), the running
process launches an **installed copy** under `~/.cerberus/apps/<project>/<resource>/bin/`,
**not** the binary in your repo. Building or restarting does **not** update that copy.

**The only command that makes a running service match your source is:**
```bash
cerberus resource deploy <resource-id>   # build + sync artifact + activate
```

**These do NOT update the running service (common cause of "stale binary" incidents):**
- `go build` / `make build` / `go install` — builds in the repo (or PATH), never touches the installed artifact.
- `go test ./...` — verifies; deploys nothing.
- `cerberus resource reload` / a web-console "Restart" — **relaunches the existing (possibly stale) artifact**; no rebuild, no re-sync.
- `cerberus resource apply` — activates an already-built artifact; does **not** build.

**Rule of thumb:** changed source → `cerberus resource deploy`. Already built, just need it running → `apply`. Only restarting an unchanged service → `reload`.

**Check before assuming it worked:** `cerberus resource status <id>` reports `artifact_stale` and a `recommended_next_step` — act on it. (Note: `mode: dev_session` resources have no staleness detection yet — for those, restart the dev session after rebuilding.)
