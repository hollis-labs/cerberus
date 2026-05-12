# Beta Sprint 4 Portfolio Validation

Date: 2026-05-11

Scope: real local managed portfolio resources from `~/.cerberus/config.yaml`.

## Summary

Cerberus beta validated successfully across the required local runtime shapes:

- Tangent managed dev instance: `tangent-dev`
- artifact-backed Go service: `conduit-api-service`
- workspace-backed durable service: `fast-triage`
- frontend/dev-session workflow: `nanite-frontend`
- desktop-adjacent workflow: `nil-dev`
- MCP workflow: standalone `cerberus mcp` subprocess routed through the running daemon socket

One true beta blocker was found and fixed: raw launchd inspect output could expose sensitive environment variable values to CLI/API/MCP consumers.

## Commands And Results

Baseline:

```sh
./cerberus validate
./cerberus resource list
```

Result:

- config valid: 12 projects, 22 resources defined
- managed portfolio includes live `dev_session`, workspace-backed `os_service`, and artifact-backed `os_service` resources

Tangent managed dev instance:

```sh
./cerberus resource status tangent-dev
./cerberus resource inspect tangent-dev
./cerberus resource doctor tangent-dev
curl -fsS -m 3 http://127.0.0.1:7842/ -o /tmp/cerberus-validate-tangent.out
./cerberus resource logs tangent-dev --lines 20
```

Result:

- status: running
- mode: `dev_session`
- run_from: `workspace`
- doctor: all checks passed
- endpoint returned 392 bytes
- logs showed live HTTP activity

Artifact-backed Go service:

```sh
./cerberus resource status conduit-api-service
./cerberus resource inspect conduit-api-service
./cerberus resource doctor conduit-api-service
curl -fsS -m 3 http://127.0.0.1:8089/ -o /tmp/cerberus-validate-conduit.out
./cerberus resource logs conduit-api-service --lines 20
```

Result:

- status: running
- mode: `os_service`
- supervisor: `launchd`
- run_from: `artifact`
- artifact installed and current
- doctor: all checks passed
- endpoint returned 667 bytes
- logs command succeeded; no recent log lines were present

Workspace-backed durable service:

```sh
./cerberus resource status fast-triage
./cerberus resource inspect fast-triage
./cerberus resource doctor fast-triage
curl -fsS -m 3 http://localhost:5177/ -o /tmp/cerberus-validate-fast-triage.out
./cerberus resource logs fast-triage --lines 20
```

Result:

- status: running
- mode: `os_service`
- supervisor: `launchd`
- run_from: `workspace`
- doctor: all checks passed
- endpoint returned 470 bytes
- logs showed MCP server startup lines

Frontend/dev-session workflow:

```sh
./cerberus resource status nanite-frontend
./cerberus resource inspect nanite-frontend
./cerberus resource doctor nanite-frontend
curl -fsS -m 3 http://localhost:5176/ -o /tmp/cerberus-validate-nanite.out
./cerberus resource logs nanite-frontend --lines 10
```

Result:

- status: running
- mode: `dev_session`
- run_from: `workspace`
- doctor: all checks passed
- endpoint returned 1421 bytes
- logs showed Vite ready

Desktop-adjacent workflow:

```sh
./cerberus resource status nil-dev
./cerberus resource inspect nil-dev
./cerberus resource doctor nil-dev
cat ~/.cerberus/pids/nil-dev.pid
ps -p "$(cat ~/.cerberus/pids/nil-dev.pid)" -o pid,ppid,command
./cerberus resource logs nil-dev --lines 20
```

Result:

- status: running
- mode: `dev_session`
- run_from: `workspace`
- doctor: all checks passed
- PID file pointed at a live app process
- logs command reported a missing temporary dev-session log file

MCP workflow:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"portfolio-validation","version":"0.0.0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cerberus_resource_status","arguments":{"resource_id":"tangent-dev"}}}' \
  | ./cerberus mcp
```

Result:

- MCP initialized successfully
- `cerberus_resource_status` returned `tangent-dev` as running through the daemon socket

Additional MCP validation:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"portfolio-validation","version":"0.0.0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cerberus_resource_list","arguments":{}}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"cerberus_resource_doctor","arguments":{"resource_id":"tangent-dev"}}}' \
  | ./cerberus mcp
```

Result:

- resource list returned the configured managed portfolio
- Tangent doctor returned all checks passed

Regression tests:

```sh
go test ./internal/connector/local
go test ./...
```

Result:

- all tests passed

## Beta Blocker Fixed

`resource inspect` for launchd-backed resources included raw `launchctl print` output. That output can include environment variable values. Because inspect data also flows through the daemon socket and MCP resource inspect tool, this was a true beta blocker for operator and agent safety.

Fix:

- sanitize launchd raw inspection records before they leave the launchd backend
- redact values for sensitive environment variable names
- preserve non-sensitive launchd diagnostics, state, PID, paths, and ordinary environment values
- add regression coverage proving sensitive values are redacted and non-sensitive values are preserved

## Operator And Agent Feedback

- Resource list is useful for portfolio triage because it exposes mode, supervisor, run source, status, artifact state, next action, and tags in one pass.
- `doctor` is high-signal for healthy resources, but `dev_session` doctor remains thin: it currently proves runtime state only.
- `inspect` is essential for launchd-backed services, but raw launchd records must stay sanitized because agents routinely paste or process full output.
- Artifact freshness recommendations are visible and actionable, especially when stale resources recommend `deploy` versus `apply`.
- The MCP subprocess path is credible: it initializes cleanly and routes runtime calls through the daemon without keeping its own config state.

## Remaining Non-Blocking Issues

- `nil-dev` is running and doctor passes, but `resource logs nil-dev` points at a missing temporary log file. This is a desktop-adjacent observability gap, not a beta blocker because status/doctor/PID validation still proves the workflow is live.
- `nanite-frontend` logs show a Node/Vite version warning even though the server is running. This belongs to the Nanite local environment rather than Cerberus runtime behavior.
- Several artifact-backed services are running with stale artifact recommendations (`sigil-serve`, `clockwork-api-service`, Cerberus self-services). These are operator follow-ups, not blockers for the validated portfolio slice.
- `dev_session` doctor output only checks runtime state. Post-beta, it should add optional checks for URL reachability, log existence, and configured workspace paths.
