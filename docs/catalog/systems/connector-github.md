---
id: "CERB-CAP-203"
class: "capability"
name: "GitHub connector"
summary: "Reads one repository's status, releases and workflow runs through the go-github API or the gh CLI, with no write operations at all."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.85
confidence_label: "No credential on this machine; the live run returned credential_missing and exposed a redaction defect"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/github/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "github"
  - "read-only"
  - "releases"
  - "workflow-runs"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Compiled in because the release and pipeline story leans on it"
  - type: "relates_to"
    target: "CERB-DEC-290"
    note: "The one vendor-SDK connector that earns a built-in slot"
  - type: "relates_to"
    target: "CERB-GAP-281"
    note: "Returns go-github types rather than a Cerberus DTO"
  - type: "relates_to"
    target: "CERB-GAP-273"
    note: "The live failure here is what proved the redaction defect"
---

# GitHub connector

> Reads one repository's status, releases and workflow runs through the go-github API or the gh CLI, with no write operations at all.

Three operations, all reads: `status`, `list_releases`, `list_workflow_runs`.
Nothing here writes. That is not a scope decision recorded anywhere — it is
simply where the connector stopped, and it is worth naming because `github` is
the one connector that carries a vendor SDK and is still compiled in, on the
argument that "Cerberus's own release and pipeline story leans on it". The
release story leans on reading runs and releases; it does not yet create one.

Two backends, selected at construction: the go-github API client when a token is
available, and the `gh` CLI otherwise. With neither, construction fails with a
message naming both recoveries — "set CERBERUS_GITHUB_TOKEN or install gh" —
which is the right shape for a credential error.

That message is also how this audit found a live redaction defect. Running
`cerberus github status hollis-labs/cerberus` on this machine returns:

    credential_missing: [REDACTED] connector: no API token and gh CLI not found
    — set CERBERUS_GITHUB_TOKEN or install gh

The word `github` was eaten. Not by a provider-token pattern — the message alone
survives `redact.Text` unchanged. It is eaten because the admin lane formats the
error as `"<connector> <op>: <code>: <cause>"`, and `credential_missing:`
matches the redactor's credential-assignment rule, so the first token of the
cause is replaced. The recovery instruction survives; the name of the thing that
failed does not. This is the fifth instance of a failure mode `AGENTS.md`
already documents four of, and the rule it breaks is the one written directly
above it: do not run redaction over a value that is a name by construction. An
error *code* is a name by construction.

The connector returns `go-github` types rather than Cerberus DTOs, which ADR
0003 explicitly does not treat as a retroactive violation — but the audit
question it sets is narrow and unanswered here: does any type this connector
returns carry a token, password, key, header value or OAuth blob? There is no
serialization test asserting it does not.

## Owns

- Repository status: default branch, visibility, counts
- Recent releases, newest first, to a caller limit
- Recent Actions workflow runs, to a caller limit
- Two interchangeable backends: the go-github API and the gh CLI

## Does not own

- Any write. No operation creates, edits, closes or dispatches anything
- Release publishing, despite Cerberus's own release story
- Pipeline execution. cerberus pipeline is a separate capability that reads this one
- Git. It speaks to the GitHub API, not to a working tree
