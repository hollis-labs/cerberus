# Agent Authority and Secrets

Cerberus can reach almost anything: processes, hosts over SSH, container
daemons, DNS, VPS providers, a Kubernetes API server, and whatever the next
plugin adds. That reach is the product. The question this document answers is a
different one — **what an agent is allowed to do with that reach, what it is
allowed to see, and what is written down afterwards.**

Read `docs/adr/0003-connector-response-dtos.md` first: the DTO boundary is the
part of this story that already works, and everything below assumes it.

Written 2026-09-18. This is direction and a work breakdown, not a description
of what exists — the inventory below is deliberately honest about the gap.

## The premise

A human operator with a shell is bounded by intent and attention. An agent is
bounded only by what the tools permit, and it will retry, chain and explore. So
three properties matter more for Cerberus than they would for a CLI a person
drives by hand:

1. **What comes back is what was allowed to come back** — credentials and
   personal data must not ride out in a tool result, a log line or an error.
2. **Some actions require a human, and the human must be a human** — a gate an
   agent can satisfy on its own is a speed bump, not a control.
3. **Everything is attributable after the fact** — because the failure modes
   that matter here are detected, not prevented.

The OWASP Top 10 for Agentic Applications (2026) names the same shapes:
`ASI02 Tool Misuse`, `ASI03 Identity & Privilege Abuse`, `ASI05 Unexpected Code
Execution` and `ASI10 Rogue Agents` are all cases where the agent's tools worked
exactly as designed.

## Where we are today

### What already holds

- **Reference-only secret storage, enforced in code.**
  `internal/secrets/references.go` *rejects a literal credential* in
  `connector-secrets.yaml` rather than accepting one, so the mapping file
  carries `keychain://` and `helper://` references and nothing else.
- **Credentials never reach generated launchd plists.** `cerberus run-secrets`
  resolves references in the service's own process and `execve`s the target, so
  the plist holds references and the daemon holds the value only in memory.
- **Capability-scoped secret delivery to plugins.** A plugin declares what it
  needs in its manifest; the host resolves it and passes it over the init-config
  channel keyed by secret name. Secrets deliberately do not travel in the
  environment — `pluginLaunchEnv()` is an allow-list — because the environment
  is ambient and would hand a credential to every plugin rather than the one
  that asked. Built-in connector ids are reserved, so a plugin cannot claim
  `ssh` in order to be handed `ssh`'s credentials.
- **Per-call resolution.** Rotation takes effect without restarting the daemon,
  and connector discovery never retrieves a secret.
- **DTOs as an allow-list.** ADR 0003. Verified against live objects in the
  Kubernetes connector: a real Secret and a pod carrying a literal credential in
  its environment, read by eleven operations across two surfaces with debug
  logging on, and the canary appeared in none of them.
- **Intent gating and previews.** `Destructive` operations require an explicit
  acknowledgment, most support `--dry-run`, and MCP tools carry
  `ReadOnlyHint` / `DestructiveHint` annotations.
- **Plugin provenance.** `TrustPolicy` covers signing, archive hash and a
  host-computed entrypoint hash, so a plugin binary that changes underneath us
  is detectable.

### The four gaps, in severity order

**1. There is no audit trail, and there is already a function for one.**

*Status 2026-09-25: done.* Every connector operation, plugin operation,
plugin install, load and unload, resource mutation, pipeline run, deploy
profile run and monitor restart is recorded to `~/.cerberus/audit/`. The
log is append-only and hash-chained, and the `cerberus audit`
tail/query/verify/prune commands read and manage it (#67 and #71, P1-4 in
`live-systems-security-target.md`). `LogAudit` was removed as dead code. The
text below describes the state before that.

`LogAudit(operation, serviceID, reason, taskID, sessionID)` exists in
`internal/service/lifecycle_log.go` with **zero callers**. Nothing records who
asked for an operation, which credential it resolved, what it targeted, or what
came back. The admin lane — the lane with all the reach — is silent.

This is the first thing to fix, not because it prevents anything, but because
`ASI03` and `ASI10` are *detected* through attribution. A control plane an agent
drives without an audit log cannot answer "what did it do?" after the fact,
which is the only question that matters once something has gone wrong.

**2. The acknowledgment gate is not a human gate.** `acknowledged` is a
caller-supplied argument (`internal/cerbapi/plugin_connector_service.go`), and
an MCP caller sets it the same way it sets any other tool argument. So today it
is an **intent** gate: it prevents an accidental destructive call, because the
caller has to assert acknowledgment deliberately. It does not put a person in
the loop, and it should not be described as though it does.

The protocol already has the right primitive and we do not use it: **MCP
elicitation** is present in the upstream SDK (`Elicit`, `ElicitMode`,
`ElicitContentValidation`) and appears nowhere in this repo. Elicitation asks
the *client's human* a question mid-call, which is a gate the agent cannot
satisfy for itself.

**3. Redaction still runs over rendered prose, and the structural fix is
already written.** AGENTS.md records seven patches to the same regexes and
names the answer: redact at the value boundary, where the credential is still a
field. That API exists — `redact.New(values...)` — and it has **two callers in
the entire repo** (`internal/connector/namecheap/client.go`,
`internal/connector/local/config_warnings.go`). Everything else goes through
`redact.Text`, the regex net. The structural fix is not a rewrite; it is
wiring.

**4. PII is at zero.** No detection, no policy, no tests. This was tolerable
while connectors returned infrastructure metadata. It is not tolerable now that
the Kubernetes connector returns **pod logs and cluster events**, both of which
routinely carry user data — and we have already demonstrated that a container
logging its own configuration hands a credential straight back through
`get_logs`. PII is the same exposure with none of the machinery.

### Smaller, real

- **`secret.Provider` requires `Set` and `Delete`.** A read-only enterprise
  vault cannot implement that interface honestly, which blocks any native vault
  backend until it is split.
- **`helper://` is a fork and exec per secret read**, with no allow-list of
  helper binaries, and it is subject to the daemon's minimal `PATH` — the same
  condition that produced the Docker connector's silent outage.
- **No lease, TTL or zeroing.** A resolved credential is an ordinary Go string
  for the life of the call. There is no record of which operation consumed
  which credential, which is also part of gap 1.

## The standards question

**There is no single standard for agent secrets.** Four pieces are real, and
they solve different problems; conflating them is how this gets over-engineered.

| Piece | Status | What it actually solves |
|---|---|---|
| **SPIFFE/SPIRE** | CNCF-graduated | *Workload identity.* Short-lived cryptographic SVIDs instead of shared API keys, with delegation chains that appear in the audit record. The direction of travel for agent identity. |
| **MCP authorization**, 2026-07-28 | Shipped spec | *Who may call this MCP server, and for whom.* Servers are OAuth 2.1 resource servers and MUST serve RFC 9728 Protected Resource Metadata; clients MUST send RFC 8707 Resource Indicators so a token cannot be replayed at a different server. Dynamic Client Registration is deprecated in favour of Client ID Metadata Documents. |
| **MCP elicitation** | In the SDK | *Getting a human decision mid-call.* The missing half of gap 2. |
| **OWASP Top 10 for Agentic Applications 2026** | Published 2025-12-09 | *A checklist to argue against.* `ASI01`–`ASI10`. Not a control, but the right framework to map work packages onto. |

Two observations worth recording:

- **RFC 8707 audience binding solves token replay, not consent.** A correctly
  scoped token still does not mean anyone agreed to the action. Policy and
  elicitation are not made redundant by getting the OAuth story right.
- **We already claim the 2026-07-28 spec** (`go-mcp v0.4.3`, commit `711ed80`),
  but `cerberus mcp-http` is not an OAuth 2.1 resource server. That is a gap
  against a specification we have nominally adopted, which is a worse position
  than not claiming it.

## Decisions taken — 2026-09-18

1. **Both vaults get native Go SDK providers**, not helper shell-outs:
   `github.com/1password/onepassword-sdk-go` and
   `github.com/keeper-security/secrets-manager-go/core`. This removes the
   fork-exec per read, the helper's exposure to the daemon's `PATH`, and an
   unbounded exec surface, in one change. 1Password's `op://vault/item/field`
   reference shape is the same one the existing helper already parses, so the
   reference syntax does not change. Keeper is zero-knowledge with client-side
   crypto, which is the stronger story for anything audited.
2. **Human-in-the-loop is a policy file plus MCP elicitation.** Policy is the
   rule — per connector, operation, host and resource. Elicitation is the
   channel that reaches a person. Neither alone is sufficient: policy without
   elicitation cannot ask, and elicitation without policy cannot express
   "read-only against this host".

   Two inputs for this design, from building the Kubernetes plugin's writes.
   First, the policy has to be per target, not per connector. The same
   `restart_workload` is routine on a dev cluster and needs a human on a shared
   one. Second, it has to decide what a dry run needs. Today the host gates a
   plugin's dry run on `--ack` exactly as it gates a write, because it cannot
   verify that the plugin honours `dry_run` (CERB-GAP-652). A policy that
   allows dry runs freely while a human approves the write needs a way to
   trust the plugin's dry-run claim first.
3. **SPIFFE is recorded as direction, not planned.** It needs infrastructure
   Cerberus does not have. Trigger conditions are in WP-S9.
4. **PII is its own work package**, not folded into the redaction rework,
   because it has different detection (no key-shaped pattern to match),
   different policy (redact on egress rather than never emit), and different
   consumers.

## Where enforcement goes: the interception layer

Asked while scoping WP-S1, and the answer shapes WP-S2, WP-S5 and WP-S6, so it
is recorded here rather than in one handoff.

**Middleware already exists**, and more of it applies than it first appears:
the daemon socket is **`net/http` over a unix listener**
(`internal/cerbapi/socket_server.go`), not a hand-rolled protocol, and
`SocketServer.wrap` already negotiates an API version header. `internal/webui`
has `withLogging`. `cerberus mcp` re-dials the socket per tool call, so MCP
traffic arrives as HTTP as well — one middleware on the socket server sees the
CLI-over-socket, MCP and web-console paths.

**Two layers, distinct jobs.** Getting this split wrong is the main way this
work produces coverage that is not coverage.

| Layer | Belongs there | Does not belong there |
|---|---|---|
| HTTP middleware | Caller identity into the context; the policy gate (WP-S5); the request-scoped redactor (WP-S2) | The audit record |
| Service layer — `ExternalConnectorService.Execute`, the six resource mutators | The authoritative record: connector, operation, target, credential names, acknowledgment, dry-run, policy decision, outcome | Inferring who the caller was |

Two reasons the record cannot live in middleware:

- **It cannot see what the record needs.** Method, path and status do not include
  the connector, the target host, which credentials resolved, or whether the
  acknowledgment gate fired. A record built from method and path is a request
  log, and `wrap` already writes one.
- **It misses the in-process path.** `cmd_transport.go` hands the CLI a local
  service and the CLI calls `Execute` directly, no HTTP involved. That path is
  not an edge case — AGENTS.md's `DaemonUnreachableError` invariant exists so a
  mutation can re-run in-process. Enforcement that covers everything except the
  fallback is blind exactly when things are already going wrong.

**So middleware earns its place, but for identity and policy rather than for
the record.** Three things make it worth using:

1. It is the one point per surface that knows which surface it is — the thing
   the service layer structurally cannot know.
2. WP-S5's policy gate lands there and covers every HTTP-delivered surface at
   once, with the in-process path calling the same evaluator directly.
3. WP-S2's request-scoped redactor is created there and lives in the context for
   the whole request.

**Two coverage traps to design around.** `mcp-http` has no middleware chain at
all today, so a policy gate added only to the socket server would leave it
uncovered — and it is the surface with no authentication either (WP-S8). And
the in-process path has no middleware by construction, so anything placed in
middleware needs a second, explicit call from the CLI root. One shared helper,
two callers, so they cannot drift.

## Work packages

### WP-S1 — Audit the admin lane

**Why first:** it is the smallest change with the largest effect, and
`LogAudit` already exists with no callers.

*Done 2026-09-25 (#67, #71). `LogAudit` was removed rather than wired: the
audit record is a DTO written by the service layer, as this section says.*

**Do:** record every connector operation — caller surface (CLI, socket, HTTP,
MCP), connector, operation, target, whether it was destructive, whether
acknowledgment was supplied, dry-run or not, outcome, duration, and **the
credential names resolved** (names, never values; the `probe-*` convention).
Records go somewhere append-only that survives a daemon restart.

**Constraints:**
- The audit record is a **DTO**, not a dump of the request. ADR 0003 applies to
  it exactly as it applies to a tool result — an audit log that captures
  arguments verbatim becomes the credential store nobody meant to build.
- Every field goes through value-boundary redaction, not `redact.Text`.
- A failure to write the audit record must not silently drop it. Decide
  deliberately whether an unwritable audit log fails the operation; for
  destructive operations it probably should.

**Acceptance:** a destructive operation, a read, a dry-run and a denied
operation each produce a record; a test asserts no credential value appears in
any of them.

### WP-S2 — Redact at the value boundary

**Why:** the seventh patch is the finding. `redact.New(values...)` exists and is
unwired.

**Do:** resolved credentials get registered with a request-scoped redactor at
the point of resolution, and every error and log path on that request renders
through it. `redact.Text` stays as a last-resort net over text Cerberus did not
compose — not as the primary mechanism.

**Constraints:**
- **Do not run redaction over a value that is a name by construction.** This is
  already an AGENTS.md rule; it has been violated seven times.
- Any error carrying a recovery instruction gets a test that the instruction
  survives. A safety net that eats the instruction is worse than no
  instruction.

**Acceptance:** a credential resolved during an operation cannot appear in that
operation's error text even when the message is composed by a vendor SDK.
Measured by a test that resolves a sentinel and forces a failure in each
connector.

### WP-S3 — Split `secret.Provider`

**Do:** a read-only interface (`Get`) and a read-write one that embeds it.
Resolution depends on the reader; only the web UI's entry management needs the
writer.

**Why it is separate:** it unblocks WP-S4 and it is a pure refactor, so it
should not be entangled with a new backend.

### WP-S4 — Native 1Password and Keeper providers

**Blocked on WP-S3.**

**Do:** two `secret.Provider` readers behind the existing reference syntax, so
`op://` and a Keeper equivalent resolve without a helper process.

**Design notes:**
- **Caching is a real tension.** Keeper's SDK offers a cache; Cerberus
  deliberately resolves per call so rotation takes effect without a restart.
  Default to no cache. If one is added it needs a TTL short enough that
  rotation is not silently ignored, and the audit record from WP-S1 should say
  when a value came from cache.
- Both SDKs authenticate with their own credential (a service-account token, a
  Keeper device registration). That credential is the one thing that cannot
  live in the vault it unlocks — keep it in the OS keychain and document the
  bootstrap explicitly.
- A vault that is unreachable must fail as `credential_missing` with the
  recovery named, never fall back to a different credential or report success.
  This rule already exists for references; it applies unchanged.
- Keep `helper://` working. It is the escape hatch for a vault with no Go SDK,
  and removing it would strand anyone using it.

### WP-S5 — A policy engine for operator-settable rules

**Do:** operator-owned policy, evaluated per operation, expressing at least:
deny outright; require approval; allow read-only; constrain by connector,
operation, resource, target host and caller surface.

**Design notes:**
- Policy is evaluated in the **serving runtime**, not in a caller. A rule a CLI
  enforces is a rule an MCP call bypasses.
- ~~Default posture is the current behaviour, so adopting the engine is not a
  breaking change. Then tighten deliberately.~~ **Superseded 2026-09-25:** the
  baseline is strict for humans and agents from day one, rolled out in shadow
  mode until the first approval path exists. See Decisions 2 and 3 in
  `docs/plans/live-systems-security-target.md`, which is the end state this
  work package builds toward.
- Policy denial is a distinct error code from acknowledgment-required and from
  `credential_missing`. An agent that cannot tell "you may not" from "ask a
  human" will retry the wrong thing.
- The evaluated decision goes in the WP-S1 audit record, including which rule
  matched. A policy engine whose decisions are not recorded cannot be debugged.

### WP-S6 — MCP elicitation as the approval channel

**Blocked on WP-S5.**

**Do:** when policy says a human must decide, ask through elicitation rather
than refusing with "pass acknowledgment". Fall back to refusal for clients that
do not support it, and record which path was taken.

**Constraint:** an elicitation response is a decision by whoever is at the
client. It is not proof of identity, and the doc should not claim it is. It
raises the bar from "the agent asserted a flag" to "a person was asked", which
is the actual improvement.

### WP-S7 — PII detection and egress policy

**Do:** treat PII as an egress concern on the paths that carry free text —
`get_logs`, `list_events`, process output, anything that returns text Cerberus
did not compose.

**Design notes:**
- Structured fields are the tractable part and where the work should start. A
  DTO field that is an email address can be typed as one.
- Free-text logs are the hard part and honesty is required: pattern detection
  over arbitrary application output will have both false positives and misses.
  The useful posture is probably to make the *exposure* explicit — an operation
  that returns unfiltered text says so, and policy decides whether an agent may
  call it — rather than to claim a filter that works.
- Do not silently drop content. A redacted log line that is indistinguishable
  from an empty one sends an operator hunting a problem that is not there.

**Acceptance:** an operation returning free text is labelled as such in its
manifest; policy can restrict it; and the labelling is asserted by a test so a
new text-returning operation cannot arrive unlabelled.

### WP-S8 — `mcp-http` as an OAuth 2.1 resource server

**State it accurately first.** `cmd_mcp_http.go` performs **no authentication of
any kind** and serves no `.well-known` metadata. It does default to
`127.0.0.1:4785`, so today's exposure is *any local process*, not the network —
which is a real mitigation and the reason this is WP-S8 rather than WP-S1.

Two things make it worth closing anyway:

- **`--listen` takes any address.** One flag moves an unauthenticated endpoint
  carrying every connector operation from loopback onto a network, with nothing
  in the code objecting. That is a foot-gun with no guard rail, and the guard
  rail is cheap: refuse a non-loopback bind unless authentication is configured.
- **Loopback is not a trust boundary on a shared machine**, and it is not one
  between a well-behaved agent and a compromised local process either.

**Do:** the `--listen` guard first, because it is small and removes the sharp
edge. Then RFC 9728 Protected Resource Metadata, RFC 8707 audience binding, and
Client ID Metadata Documents rather than Dynamic Client Registration.

`cerberus mcp` over stdio is a different case and is defensible as it stands: it
inherits the trust of the process that launched it.

### WP-S9 — SPIFFE/SPIRE — direction, not planned

Short-lived workload identity is where this ends up. **Trigger conditions:**
more than one Cerberus instance needing distinct identity; a connector target
that accepts workload identity instead of a shared key; or a delegation chain
across agents that needs to appear in an audit record. None holds today. Revisit
when one does, rather than building attestation infrastructure for a
single-node control plane.

## Sequencing

```
WP-S1 audit            ─┐
WP-S2 value redaction  ─┼─► independent, start either
WP-S3 provider split   ─┘      └─► WP-S4 native vault SDKs

WP-S5 policy engine ──► WP-S6 elicitation
WP-S7 PII egress       (after WP-S2; shares no code, shares the discipline)
WP-S8 mcp-http OAuth   independent
WP-S9 SPIFFE           deferred, trigger conditions above
```

WP-S1 and WP-S2 are the two that pay immediately and neither needs a decision
from anyone else. WP-S1 also produces the evidence that makes WP-S5 debuggable,
so doing policy before audit is the wrong order.

## Not in scope

- **Sandboxing connector execution.** Real, and a different problem — the
  plugin lane's `SandboxProfile` is where it belongs.
- **Prompt injection through tool results.** `ASI06` is genuine and partly
  mitigated by the DTO boundary already, but defending an agent against its own
  context is not a control plane's job to solve alone.
- **Encrypting data at rest in `~/.cerberus`.** Worth doing; not part of this
  story.
