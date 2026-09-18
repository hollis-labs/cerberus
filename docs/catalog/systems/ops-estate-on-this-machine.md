---
id: "CERB-CAP-600"
class: "capability"
name: "The operational estate on this machine"
summary: "What Cerberus actually supervises here right now: ten resources across eight projects, all defined inline in the global config, with nothing registered and nothing artifact-backed."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "every claim re-run against the installed binary and running daemon on 2026-09-17"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "~/.cerberus/config.yaml"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "estate"
  - "resources"
  - "registry"
  - "ground-truth"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-605"
    note: "the registry is the mechanism that is not being used here"
  - type: "relates_to"
    target: "CERB-GAP-649"
    note: "nothing here exercises os_service or run_from: artifact"
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "Area 1 owns the supervision code; this record owns what that code is actually pointed at"
---

# The operational estate on this machine

`cerberus resource list` reports ten resources across the eight projects in
`cerberus project list`. Nine are `local`/`process`/`dev_session`: `postgres`,
`jaeger`, `tether-daemon`, `tether-sysop`, `hadron-daemon`, `tesseract-api`,
`torque-api` and `tunnel-muctlvaig` running, `nanite-local` stopped and
additionally marked paused by a `~/.cerberus/paused.nanite-local` marker. The
tenth, `muctlvaig`, is `server`/`ssh` and reports `unsupervised` — a named
handle for connector operations, exactly as the model intends.

Three facts about this estate matter more than the inventory.

**Nothing is registered.** `cerberus registry list` and `cerberus registry
health` both answer "No project configs registered", and `cerberus validate`
with no arguments says "Config OK: 0 registered, resolved to 8 projects, 10
resources". Every resource is defined inline in `~/.cerberus/config.yaml`. The
per-repo descriptor lane — `cerberus register`, `internal/registry/`, the
registry health check, the console's registry page — has never been exercised
on this machine, and the 1792 samples in `~/.cerberus/state/overview_snapshots.json`
show `registry_entries: 0` for every sample back to 2026-09-15. The catalog
should not claim maturity for descriptor discovery on the strength of this
machine.

**Nine descriptors exist across the estate and none of them can be
registered.** `cerberus.cerberus.yaml` and `infrastructure.cerberus.yaml` in
this repo, plus one each in `hadron`, `nanite`, `tachyon`, `tangent`,
`tesseract`, `tether` and `torque`. Every one carries at least one `dir:` under
`/Users/<other-user>`, a user that does not exist here. AGENTS.md is right that
they are templates rather than configuration, but slightly wrong about why in
one case: `infrastructure.cerberus.yaml` uses the portable
`dir: /opt/homebrew/var` for PostgreSQL and Jaeger and only fails on its third
resource, which points at `/Users/<other-user>/.cerberus/bin`. It is two thirds
portable, not wholly foreign.

**Nothing here is artifact-backed.** Every resource reports `run_from:
workspace`, `mode: dev_session`, and a blank artifact column. No resource uses
`os_service`. The consequence is concrete rather than theoretical:
`cerberus resource doctor postgres` runs exactly one check, `runtime_status`,
because the launchd, plist, install-path, artifact-staleness and log-path
checks all apply only to `os_service`. `doctor` on this machine is a one-line
restatement of `status`.

One reassuring negative: for this audit, source and binary agree. The installed
`~/go/bin/cerberus` reports commit `4ab19d0`, and `git diff
4ab19d0..HEAD -- '*.go'` in the audit worktree is empty — only documentation has
moved since. The standing "changed source is not deployed source" caveat does
not bite today, so behaviour observed live and behaviour read out of the code
are the same codebase.
