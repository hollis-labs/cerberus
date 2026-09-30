---
id: "CERB-CAP-203"
class: "capability"
name: "GitHub connector"
summary: "Reads one repository's status, releases and workflow runs over the GitHub REST API, as the github plugin in hollis-labs/cerberus-plugins; no longer compiled in, and no write operations at all."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.85
confidence_label: "24 tests in the plugin against a fake API and recorded response shapes, and a real-subprocess load through the one-shot host; live reads wait for a read-only token"
last_reviewed: "2026-09-30"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "hollis-labs/cerberus-plugins:github/internal/ghplugin/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "github"
  - "read-only"
  - "releases"
  - "workflow-runs"
  - "plugin"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "A plugin connector since 2026-09-30, reached through the admin lane"
  - type: "relates_to"
    target: "CERB-DEC-290"
    note: "Was the one vendor-SDK connector kept compiled in; core is now local, ssh and docker"
  - type: "relates_to"
    target: "CERB-GAP-281"
    note: "The plugin maps GitHub's responses onto Cerberus DTOs, with a recorded-shape test"
  - type: "relates_to"
    target: "CERB-GAP-273"
    note: "The live failure here is what proved the redaction defect"
---

# GitHub connector

> Reads one repository's status, releases and workflow runs over the GitHub REST API, as the github plugin in hollis-labs/cerberus-plugins; no longer compiled in, and no write operations at all.

Three operations, all reads: `status`, `list_releases`, `list_workflow_runs`.
Nothing here writes. It was the one connector that carried a vendor SDK and
stayed compiled in, on the argument that "Cerberus's own release and pipeline
story leans on it". A survey on 2026-09-30 found nothing in the host that did,
so when core was narrowed to the primitives (`local`, `ssh`, `docker`) it moved
out to a plugin, as the four providers had
(`docs/plans/provider-plugin-extraction.md`, addendum).

**A plugin since 2026-09-30.** It is the `github` plugin, released as
`github/v0.1.0`. On the CLI it is `cerberus connectors exec github <op>`; the
`cerberus github` group is gone, with no tombstone. Over MCP it is the generated
tools `cerberus_github_status`, `cerberus_github_list_releases` and
`cerberus_github_list_workflow_runs`, served once the operator lists them under
`github: mcp: expose:` in `connector-config.yaml`. The built-in's
`cerberus_github_releases` and `cerberus_github_runs` are gone. The operations,
input schemas, output shapes and untrusted labels did not change.

One backend, the REST API over net/http. The built-in's go-github client went
with it. So did its `gh` CLI fallback: gh authenticates through its own login
under `HOME`, outside the declared-secret channel, which is why Cloudflare's
wrangler fallback was dropped too. The secret is still `github/token`
(`CERBERUS_GITHUB_TOKEN`), so no credential reference moves. An operator who
ran on gh's login stores a token with `cerberus secrets set github/token`.
With no token, the plugin loads and each call fails as `credential_missing`,
with the recovery named.

The built-in's failure is how the 2026-09-17 audit found a live redaction
defect. `credential_missing: [REDACTED] connector: ...` ate the word `github`,
because the error code matched the credential-assignment rule (GAP-273, fixed
in #34).

The plugin answers ADR 0003's audit question for this connector. It decodes
GitHub's responses into wire structs that name only what the DTOs carry, then
maps them field by field. A recorded-shape test holds that none of the
following reach the output: owner and actor objects, permissions, clone and
upload URLs, release bodies and assets, or a run's head commit with its author
email.

## Owns

- Repository status: default branch, visibility, counts
- Recent releases, newest first, to a caller limit (1-100)
- Recent Actions workflow runs, to a caller limit (1-100)
- Holding `owner` and `repo` to GitHub's name grammar before any call

## Does not own

- Any write. No operation creates, edits, closes or dispatches anything
- Release publishing, despite Cerberus's own release story
- Pipeline execution. cerberus pipeline is a separate capability
- Git. It speaks to the GitHub API, not to a working tree
- The `gh` CLI or its login
