---
id: "CERB-CAP-502"
class: "capability"
name: "Project config registry"
summary: "Registers app-owned *.cerberus.yaml files as absolute-path pointers in ~/.cerberus/registry.yaml, validating at register time and recomputing health from the pointed-to file on demand."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.88
confidence_label: "fully read and unit-tested, but zero registered entries on this machine across 1791 monitor samples, so the register/resolve path is unexercised here"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/registry/"
tags:
  - "area5"
  - "cerberus"
  - "class:capability"
  - "config"
  - "register"
  - "registry"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-DEC-552"
    note: "the index stores a path handle, never config content"
  - type: "relates_to"
    target: "CERB-CAP-501"
    note: "provides the second source resolution merges"
  - type: "blocks"
    target: "CERB-GAP-544"
    note: "the lane exists but is not in use on this machine"
---

# Project config registry

The registry is an opt-in pointer index. An app commits a
`<name>.cerberus.yaml` (kind `cerberus-project/v1`) in its own repo,
`cerberus register <path>` validates it and writes one row to
`~/.cerberus/registry.yaml`, and that row holds an absolute path — never the
config body. Resolution always re-reads the owning app's file, so the app stays
the author of its own definitions and Cerberus owns only the handle.

One config declares exactly one project plus its resources and pipelines. A
multi-app repo uses a bundle manifest (kind `cerberus-bundle/v1`, conventionally
`bundle.cerberus.yaml`) listing paths; the manifest is discovery sugar that
produces one per-owner row each and is never itself registered. Manifest
recursion is cycle-guarded and a manifest registering the same owner twice is
refused.

`owner` is the registry key and must equal `project.id`; omitting `owner`
defaults it from `project.id`. Registration is atomic and all-or-nothing —
every referenced config is loaded and validated before the index is touched, so
one invalid file aborts the whole command with no partial write. The index is
saved by render-to-temp-then-rename, so a crash mid-write cannot truncate it.
Register additionally runs a whole-index port-conflict preview and refuses if
the incoming config would collide with an already-registered one.

Health is never stored. `cerberus registry health` recomputes it per entry:
the pointed-to file must exist (`missing`), parse and pass schema validation
(`invalid`), otherwise `ok`, plus a cross-config port-conflict pass that can
mark an otherwise-clean entry `invalid`. `cerberus registry list` prints the
rows; `cerberus deregister <owner>` removes one row and leaves the app's file
untouched.

Registration is deliberately declarative. It makes resources visible and builds
nothing, and the command says so and names `cerberus resource deploy <id>` as
the activation step.

Verified live on 2026-09-17: `~/.cerberus/registry.yaml` does not exist,
`cerberus registry list` prints "No project configs registered", and all 1791
samples in `~/.cerberus/state/overview_snapshots.json` from 2026-09-15T17:31 to
2026-09-17T10:52 report `registry_entries: 0`. The register lane is code that
works in tests and has never been exercised on this machine.
