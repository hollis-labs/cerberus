---
id: "CERB-CAP-212"
class: "capability"
name: "The operation contract"
summary: "Every operation declares its effect class, target, preview, output, cost, local-filesystem access and input key table as data, and acknowledgment, argument refusals, discovery schemas and MCP annotations are all derived from that one declaration."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "pkg/connector/contract.go, hints.go, runtime_gate.go, internal/mcp/hints.go and the gate tests read on the P1-3 branch after #60 and #63"
last_reviewed: "2026-09-25"
created_at: "2026-09-25"
namespace: "cerberus"
locus: "core"
pointer_locator: "pkg/connector/contract.go"
tags:
  - "cerberus"
  - "class:capability"
  - "contract"
  - "acknowledgment"
  - "discovery"
  - "mcp-hints"
  - "p1"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-210"
    note: "discovery serves the finalized contract"
  - type: "relates_to"
    target: "CERB-CAP-200"
    note: "the admin lane gates on it"
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "the resource runtime gates on the local contract"
  - type: "relates_to"
    target: "CERB-CAP-402"
    note: "MCP hints are derived from it"
  - type: "relates_to"
    target: "CERB-CAP-604"
    note: "acknowledgment follows the effect"
  - type: "relates_to"
    target: "CERB-DEC-816"
    note: "ack per Decision 14 plus local_fs"
  - type: "relates_to"
    target: "CERB-DEC-818"
    note: "hints derived, destructive equals requires_ack"
  - type: "relates_to"
    target: "CERB-DEC-821"
    note: "a plugin gap is exec"
---

# The operation contract

Every operation Cerberus runs declares what it does, as data on its
`contract.Operation`, and every gate, annotation and discovery surface reads
that declaration rather than a hand-set flag. It landed in PR #60 (P1-1), was
extended to the supervision lane and pipelines in PR #63 (P1-2), and the MCP
annotations were derived from it on the P1-3 branch.

**The fields.** `effect` is one of `read`, `read_sensitive`, `write`,
`lifecycle`, `destructive`, `exec` or `admin`. Alongside it sit `reversible`, a
`target` (a kind such as `digitalocean.droplet` and the input fields that name
the instance), `preview` (`server`, `host`, `plugin` or `none`), `output`
(`structured`, `free_text` or `file`), `cost` (`none` or `billable`) and
`local_fs` (`none`, `reads` or `writes`, for what the operation does to the
machine running Cerberus).

**The key table.** `Inputs` lists every config key the operation accepts, its
JSON schema, whether it is required, and its scope. A `caller` input may come
from any surface. A `local` input — docker's `host`, `context` and every
compose-file alias — comes only from the operator's own shell. `OneOf` names
groups of which at least one key must be present. The four provider connectors
and github declare their inputs as an `input_schema`, the form a plugin
manifest uses, and the key table is read from it.

**Derived, never set by hand.** `Finalize` computes `input_schema` from the
caller-scope inputs (local inputs are not advertised), `destructive` as
`effect == destructive`, `supports_dry` as `preview != none`, and
`requires_ack`. Every built-in `Definition()` returns its definition finalized,
and `ValidateDefinition` rejects an operation missing any contract field.

**Acknowledgment follows the effect** (Decision 14): every class except `read`
and `read_sensitive` needs it, and so does any operation with
`local_fs: writes`, whatever its effect. `ssh get` and `get_dir` are
`read_sensitive` egress but overwrite a local path the caller chose, so they
take `--ack`.

**The check runs before anything is resolved.** `CheckInputs` refuses an
undeclared key, a local-only key from a remote surface, a missing required key,
an unmet one-of, and — since P1-3 — a value whose JSON type or enum does not
match its schema. It accepts exactly what the executors accept: an integer may
arrive as any Go integer, an integral float, a `json.Number` or a decimal
string. The refusal names keys in parentheses, never values, and never puts a
key directly before a colon: `refusing fields (token): the operation does not
declare them`. That shape exists because `token: the` let `redact.Text` eat the
word after it. `TestArgumentRefusalsNeverNeedACredential` sweeps every declared
operation and asserts `invalid_args` with zero connector resolutions.

**Plugins.** A manifest carries the same fields and `ManifestFromDefinition`
copies them. An operation with no `effect` is a gap, not a refusal: the host
treats it as `exec`, so it needs acknowledgment, and `plugin managed list`
reports it in `contract_gaps`, which is always present and `[]` when there are
none. A plugin's `input_schema` is enforced as its key table on both routes to
it, and undeclared keys are refused when the schema is closed. Since
cerberus-plugins PR #5 (P1-6a) the azure, contextforge, kubernetes and
cloudflare plugins declare an effect on every operation.

**Beyond connectors.** `RuntimeDefinitions()` adds four contracts that no
connector dispatch serves. `local` covers deploy, apply, reload and stop
(lifecycle), sync (write), remove (destructive), `ensure_fresh`, and the
supervision reads. `pipeline` covers run (exec) and list. `infra` covers
`run_profile` (exec). `cerberus` covers the control plane's own reads: health,
project list and connector discovery. The resource runtime service and the
deploy-profile runner gate on them. Namecheap's refused per-record writes have
contracts of their own (`DisabledOperations()`), so the tools that explain the
refusal carry derived annotations too.

**The annotations.** `contract.HintsFor(op)` is the one derivation every MCP
tool's hints come from. ReadOnly is `!requires_ack`, Destructive is
`requires_ack`, Idempotent is claimed only by a read, and OpenWorld is true
for a target outside Cerberus's own `local.`, `pipeline` and `cerberus.` kinds.
`internal/mcp/hints.go` binds each built-in tool to its operation and is the
only file in `internal/mcp` that sets a hint. A tool generated from a plugin
manifest gets its hints through the same `mcp.WithHints`.

**Conformance.** The P1-3 suite (`pkg/connector/conformance`) runs over every
built-in definition, the runtime definitions and fixture copies of the
installed plugins' manifests. It checks that an effect is present, hints agree
with the derivation, acknowledgment agrees with Decision 14 plus `local_fs`,
`supports_dry` agrees with `preview`, and `input_schema` agrees with the key
table.

Marked `partial`: `reversible`, `target`, `cost` and most of `output` are
declared and not yet consumed. Policy (P2) and the audit log (P1-4) are their
readers.
