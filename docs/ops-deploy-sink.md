# Posting deploy outcomes to Ops

Cerberus posts the outcome of a deploy to the Ops service, so Ops can show it on the item it belongs to and pass its deploy gate. The contract is design `caster://ops/design.system-of-record-v2@2`, sections 5.2 and 5.3. The sink is **off unless `ops.enabled` is set**.

## What is posted

One `ops_status_post` per deploy **outcome**, never per intermediate state:

| Outcome (`kind`) | `state` | When |
|---|---|---|
| `deployed` | `done` | `resource deploy` succeeded, or `deploy record --outcome deployed` |
| `deploy_failed` | `blocked` | `resource deploy` failed, or `deploy record --outcome deploy_failed` |
| `rolled_back` | `note` | `deploy record --outcome rolled_back` |

The arguments are flat and top-level. `ops_id`, `workstream_id` and `task` are each left out when unknown:

```json
{
  "ops_id": "ops-system-of-record",
  "workstream_id": "workstream:01M7Z8Q1TYGJRP0X85Z7Z88N90",
  "task": "CW-20261121-0155",
  "state": "done",
  "kind": "deployed",
  "summary": "tether-hub 263eff6a1b2c deployed, health http://127.0.0.1:8991/healthz 200",
  "evidence": [
    {"kind": "cerberus.deploy", "ref": "<operation id>"},
    {"kind": "commit", "ref": "263eff6a1b2c"},
    {"kind": "health", "ref": "http://127.0.0.1:8991/healthz 200"}
  ],
  "idempotency_key": "cerberus-deploy-<operation id>-deployed"
}
```

- The evidence `kind` is `commit` when the revision looks like a commit hash, otherwise `revision`.
- A deploy with no known id **is still posted**; Ops files it as unlinked. Cerberus never skips a post for a missing id and never refuses a deploy for one.
- Cerberus writes no Torque comment itself. Ops mirrors the post to the task.
- For `resource deploy`, the revision is the checkout's short commit and `health` is `<health url> ok|failed` when the resource declares `health_check.url`, else plain `ok|failed`. `deploy record` carries whatever `--revision` and `--health` say.

The same fields (`ops_id`, `workstream_id`, `task`) are carried flat on the exported `deploy.state_changed` event, and on the audit record (new fields `ops_id`, `workstream_id`, `task`, `revision`, `health`, `deploy_outcome`).

## Naming the item

`cerberus resource deploy|reload|apply|ensure-fresh` and `cerberus deploy record` take `--ops-id`, `--workstream` and `--task`. Each is resolved on its own, first match wins:

1. the flag;
2. the caller's environment: `OPS_ID` (ops id), `TETHER_WORKSTREAM_ID` (workstream), `TETHER_TASK_ID` (task);
3. the mapping in `events.yaml` (`ops.map`): the resource's entry, then its project's (ops id and workstream only; a task has no mapping);
4. empty.

## `cerberus deploy record`

For a deploy Cerberus did not run, such as the release scripts for the Tether hub, outposts and Surface:

```
cerberus deploy record --resource tether-hub --revision v1.4.2 \
  --outcome deployed --health "http://127.0.0.1:8991/healthz 200" \
  [--ops-id ...] [--workstream ...] [--task ...]
```

It writes the audit record (the exporter turns it into one terminal `deploy.state_changed`: `succeeded`, `failed` or `rolled_back`) and posts the outcome. It runs in the calling process, like the other in-process commands, and waits up to 30 seconds for the post. Nothing is deployed or restarted.

## Configuration

The `ops:` section of `~/.cerberus/events.yaml` (the exporter's settings file). The daemon and the CLI read it when they start; the mapping is re-read on every call.

```yaml
ops:
  enabled: true                       # default false
  endpoint: http://127.0.0.1:8994/mcp # Ops MCP endpoint
  token_file: /var/lib/fabric/tether/run/operator.token
  actor: msg://agent/agent-mux/agt_azos2rzing   # X-Actor
  tool: status_post                   # the tools/call name
  timeout: 5                          # seconds per attempt
  retries: 2                          # attempts after the first
  map:
    resources:
      tether-hub: {ops_id: tether, workstream_id: "workstream:01..."}
    projects:
      surface: {ops_id: surface-ops}
```

The request is a JSON-RPC `tools/call` over HTTP POST with `Authorization: Bearer <token file contents>`, `X-Actor`, and `X-Task-Id` when a task is known. The token is read from the file on each attempt and never logged.

## Failure behavior

Posting is best effort and runs off the deploy's path. It never fails or delays a deploy or a `deploy record`. A transport error, timeout, HTTP 5xx or 429 is retried (bounded by `retries`, same idempotency key); a refusal from Ops (a JSON-RPC error or a tool error) is final. Failures are logged as `ops_sink.post_failed` with Ops's error code. Refusals are expected until the Ops service accepts the extended `ops_status_post` arguments.
