---
id: "CERB-CAP-505"
class: "capability"
name: "On-disk layout and local state"
summary: "Resolves and reports where Cerberus reads and writes: the go-apppaths XDG roots for the main database only, and the hand-made ~/.cerberus/ dotdir for everything else."
state_field: "maturity"
state_label: "shipped"
review_status: "reviewed"
confidence_score: 0.93
confidence_label: "cerberus path run live and its output diffed against the actual dotdir contents; the SQLite store is provably never created on this machine"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/config/layout.go, cmd/cerberus/cmd_path.go, internal/store/sqlite/"
tags:
  - "area5"
  - "cerberus"
  - "class:capability"
  - "layout"
  - "paths"
  - "sqlite"
  - "state"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-501"
    note: "config.yaml's location comes from here"
  - type: "implements"
    target: "CERB-DEC-554"
    note: "only the main DB moved onto go-apppaths"
  - type: "blocks"
    target: "CERB-GAP-537"
    note: "everything that owns the estate lives in one unbacked directory"
  - type: "blocks"
    target: "CERB-GAP-545"
    note: "cerberus path reports far less than the dotdir actually holds"
---

# On-disk layout and local state

Cerberus's state lives in two places and the split is deliberate.

`cerberus path` prints what the binary resolves. Verified live on 2026-09-17:

    app            cerberus
    project-mode   false
    data           ~/.local/share/cerberus
    state          ~/.local/state/cerberus
    cache          ~/.cache/cerberus
    config         ~/.config/cerberus
    workspace      default
    workspace-dir  ~/.local/share/cerberus/workspaces/default
    main-db        ~/.local/share/cerberus/workspaces/default/main.db
    config-yaml    ~/.cerberus/config.yaml

Everything above `config-yaml` comes from `go-apppaths` via
`config.ResolveLayout`, honouring `CERBERUS_DB_PATH`, `CERBERUS_WORKSPACE`,
`$XDG_*` and `--db`. Only the main database was moved onto that layout
(CW-20260517-0065). `config.yaml`, `registry.yaml`, the `apps/<project>/`
install roots, sockets, pids and locks stay in the hand-made `~/.cerberus/`
dotdir by design: it is the deploy substrate other Hollis Labs plists and
runbooks point into, and it is referenced by absolute path across the codebase.
`paths.WithoutMaterialize()` keeps `cerberus path` side-effect free, which also
means it happily prints a path that does not exist.

The actual dotdir on this machine holds thirteen things `cerberus path` does
not mention: `config.yaml` plus seven hand-made `config.yaml.bak-*` snapshots,
`cerberus.log`, `cerberus.pid`, `cerberus.sock`, `daemon.lock`, `infra.yaml`,
`plugin-connectors.json`, `registry-profile.yaml`, `paused.nanite-local`, and
the `alerts/ bin/ locks/ logs/ pids/ state/` directories. `cerberus path`'s own
doc comment claims it prints `registry.yaml`; the code prints only
`config.DefaultPath()`.

The SQLite store is `internal/store/sqlite`: WAL, foreign keys on, four
migrations (projects, resources, pipeline_runs, schema_migrations), opened
lazily by `App.OpenStore`. Verified live: the workspace directory exists and is
**empty** — no `main.db` has ever been created here. Nothing in the running
system depends on it, which is worth knowing before treating it as the state of
record.

`~/.cerberus/pids/<id>.meta` is the closest thing to a second copy of a
resource's identity, and it carries only `pid`, `started_at`, a `config_hash`
and `cerberus_version` — a hash, not the config. `state/overview_snapshots.json`
carries per-minute counts and no definitions.

## Since P1-5

`~/.cerberus/plugins/<id>/<digest>/` holds the reviewed copies managed plugins
run from, mode 0700, with a `.staging-*` directory present only during a review.
`plugin-connectors.json` is written as version 2 by rename, beside a
`plugin-connectors.json.lock` that serialises the CLI and the daemon. `cerberus path`
reports neither (CERB-GAP-545).
