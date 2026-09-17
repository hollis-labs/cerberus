---
id: "CERB-CAP-206"
class: "capability"
name: "Laravel Forge connector"
summary: "Reads Forge servers and sites, rewrites deployment scripts, and runs arbitrary commands on a site — with no test files in the package."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.8
confidence_label: "No token on this machine and zero test files; the flag gaps are read from the definition"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/connector/forge/connector.go"
tags:
  - "cerberus"
  - "class:capability"
  - "connector"
  - "deploy"
  - "forge"
  - "migrating-to-plugin"
  - "remote-exec"
  - "untested"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Compiled in today, scheduled to migrate out"
  - type: "relates_to"
    target: "CERB-DEC-291"
    note: "Last in the migration order, having no SDK to shed"
  - type: "blocks"
    target: "CERB-GAP-279"
    note: "update_deployment_script writes what every future deploy runs and is not flagged destructive"
  - type: "blocks"
    target: "CERB-GAP-280"
    note: "Zero test files in the package that owns arbitrary remote command execution"
---

# Laravel Forge connector

> Reads Forge servers and sites, rewrites deployment scripts, and runs arbitrary commands on a site — with no test files in the package.

Seven operations, and the interesting thing about them is the distribution of
risk against the distribution of care.

`deploy_site` and `exec_site_command` are correctly flagged: destructive,
dry-runnable, with previews that name the server, the site and — for exec — the
command, which is what an operator reads before acknowledging.
`exec_site_command` is the broadest write verb anywhere in the lane; it runs an
arbitrary command inside a production site.

`update_deployment_script` replaces the script that runs on *every future
deploy* of that site, and is flagged neither destructive nor dry-runnable. It
takes no `--ack` and offers no preview. It is also CLI-only —
`cerberus forge set-script` — with no MCP tool, which limits the blast radius
for an agent caller but not for a person. Its read counterpart,
`get_deployment_script`, is likewise CLI-only.

`internal/connector/forge/` has **no test files**. `go test
./internal/connector/...` reports `[no test files]`. The package holding the
lane's arbitrary-remote-execution verb has no tests of its own; what coverage
exists is the service-level dispatch test in `internal/cerbapi`. Forge is also
the connector with no vendor SDK to shed, which is why it is last in the
migration order to plugins — the 33MB Cloudflare win comes first — so the
untested state is not something a migration will incidentally fix.

There is no Forge token on this machine, `live` reads `no`, and nothing here was
run.

## Owns

- Forge server list and get
- Site list per server
- Deployment script read and replace
- Triggering a site deployment
- Running an arbitrary command inside a site

## Does not own

- Provisioning servers. Forge creates them; this connector only reads them
- Site creation, SSL, queues, daemons or scheduled jobs
- Any vendor SDK — it is a hand-written HTTP client
- Its own tests. There are none
