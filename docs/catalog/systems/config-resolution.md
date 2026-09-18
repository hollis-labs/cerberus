---
id: "CERB-CAP-501"
class: "capability"
name: "Config schema and resolution"
summary: "Assembles the effective v2 runtime config by merging the operator-owned ~/.cerberus/config.yaml with every registered project config, registered winning and failures reported rather than swallowed."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.92
confidence_label: "resolution and precedence read from source and confirmed live against the running binary; the v1 legacy structs are provably unwired"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/registry/resolve.go, internal/config/"
tags:
  - "area5"
  - "cerberus"
  - "class:capability"
  - "config"
  - "resolution"
  - "v2"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-502"
    note: "resolution reads the registry index for its second source"
  - type: "relates_to"
    target: "CERB-CAP-505"
    note: "the paths it reads come from the dotdir/XDG split"
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "the resolved ConfigV2 is what the supervision lane operates on"
---

# Config schema and resolution

Cerberus's effective runtime config is assembled, not loaded. `registry.Resolve`
merges two sources in a fixed order: the operator-owned monolith at
`~/.cerberus/config.yaml` first, then every project config named by
`~/.cerberus/registry.yaml`, owner-sorted for determinism. A registered config
wins any id collision it touches — project, resource or pipeline — and the
override is recorded as a warning rather than silently applied.

The asymmetry in failure handling is the load-bearing part. A broken
*registered* config is skipped and every other project keeps resolving, because
one app's bad file must not wedge the runtime. A broken *global* file is fatal,
because it is operator-owned and may carry every resource on the machine. The
resolver reports both outcomes: `ResolvedConfig.Skipped` names what was dropped
and `Warned` names what resolved but carries warnings, and `resource list`
prints a trailing stderr notice built from them. That notice exists because on
2026-05-25 the registry was healthy, the configs were being dropped, and the
operator saw a bare "No resources found".

Only `version: 2` parses. `config.LoadUnified` peeks the version field and
refuses anything else by name — "Cerberus now requires version: 2" — which is
how the frozen v1 `services:` lane is enforced at the parse boundary rather than
by convention.

Normalization is where the portability story actually lives. `NormalizeV2`
expands `~` and `~/` in a resource's `dir`, `env_file`, `log_file`,
`artifact_path`, `install_root`, `install_work_dir`, `command`, `env` values,
`health_check.command` and the `build_strategy` `source`/`rules` maps — but only
for a resource whose connector is `local` and whose type is `process`. There is
no `$VAR` interpolation and no per-machine override layer. That is why this
repo's own `cerberus.cerberus.yaml` carries
`dir: /Users/<other-user>/dev/hollis-labs/apps/cerberus` and resolves nowhere on
this machine: the file is a template, not configuration.

Every read is fresh. The registry holds no in-memory index and re-reads its
index on every operation; `config.FileSource.Snapshot` re-parses on each call by
design, to remove the entire class of "daemon had stale config" bugs.

Verified live on 2026-09-17: `cerberus validate` reports
`Config OK: 0 registered, resolved to 8 projects, 10 resources`. The entire
running estate comes from the monolith and the registry contributes nothing.
