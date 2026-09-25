---
id: "CERB-CAP-206"
class: "capability"
name: "Laravel Forge connector"
summary: "Reads Forge servers and sites, rewrites deployment scripts with a diff preview, deploys, and runs commands on a site, as the forge plugin in hollis-labs/cerberus-plugins; no longer compiled in."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.8
confidence_label: "35 tests in the plugin, including the diff preview and the scrubber; no Forge token on this machine, so live reads wait for the operator's UAT"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "hollis-labs/cerberus-plugins:forge/internal/forgeplugin/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "forge"
  - "exec"
  - "plugin"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "A plugin connector since 2026-09-25, reached through the admin lane"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "The last of the four to migrate"
  - type: "relates_to"
    target: "CERB-GAP-280"
    note: "The built-in had no tests; the plugin has them"
---

# Laravel Forge connector

> Reads Forge servers and sites, rewrites deployment scripts with a diff preview, deploys, and runs commands on a site, as the forge plugin in hollis-labs/cerberus-plugins; no longer compiled in.

Seven operations:

- `list_servers`, `get_server` and `list_sites` are `read`.
- `get_deployment_script` is `read_sensitive` with free-text output, because a
  deployment script is text Cerberus did not write and can carry secrets.
- `update_deployment_script` is `write`.
- `deploy_site` is `lifecycle`.
- `exec_site_command` is `exec`: it runs a caller-supplied command on the
  server, which is the class that always needs approval once the policy
  engine lands (Decision 5).

Since 2026-09-25 this is a plugin, not a built-in
(`docs/plans/provider-plugin-extraction.md`, H7), and it was the last provider
connector compiled into the binary. The id and the secret name are unchanged,
so `CERBERUS_FORGE_API_TOKEN`, a `forge: api_token:` entry in
`connector-secrets.yaml` and `keychain://forge/api_token` resolve exactly as
before.

What changed for a caller:

- **CLI.** `cerberus forge …` is gone, with no tombstone.
  `cerberus connectors exec forge <op>` runs any operation.
- **MCP.** The hand-written `cerberus_forge_servers`, `_server`, `_sites`,
  `_deploy` and `_exec` are gone. The generated tools are
  `cerberus_forge_<op>`, served for the operations the operator lists under
  `forge: mcp: expose:` in `connector-config.yaml`. The suggested default is
  the three structured reads only.
- **A preview for the script rewrite.** `update_deployment_script`'s dry run
  reads the current script and returns a line diff against the proposed one.
  The proposed content itself appears only as `{bytes, sha256}`, and the diff
  is scrubbed of the token. The preview is plugin-claimed and makes a network
  read, so it needs the token. The built-in had no preview at all.
- **Tighter than the built-in.** `server_id` and `site_id` must be positive
  whole numbers, whitespace-only script content is refused, the writes return
  `{server_id, site_id, action}` rather than null, calls time out after 30
  seconds, and health makes no network call.

## Owns

- Server and site reads
- Deployment script read and rewrite, deploy, and site command execution
- The Forge HTTP client, confined to one file behind a DTO-returning backend

## Does not own

- Server provisioning. That is DigitalOcean and, later, OpenTofu
- The deploy itself: Forge runs it, Cerberus only asks
- A place in the Cerberus binary
