---
id: "CERB-CAP-600"
class: "capability"
name: "Reading an installation's operational ground truth"
summary: "How to tell what a Cerberus installation actually supervises, as opposed to what its code supports: inline versus registered resources, templates that cannot register, and a doctor that is thin for dev_session resources."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "every check re-run against an installed binary and running daemon on 2026-09-17"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "~/.cerberus/config.yaml"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "resources"
  - "registry"
  - "ground-truth"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-605"
    note: "the registry is the mechanism an inline-only installation does not use"
  - type: "relates_to"
    target: "CERB-GAP-649"
    note: "an installation of dev_session resources exercises neither os_service nor run_from: artifact"
  - type: "relates_to"
    target: "CERB-CAP-100"
    note: "Area 1 owns the supervision code; this record owns how to see what that code is actually pointed at"
---

# Reading an installation's operational ground truth

The catalog records what the code can do. What a given installation exercises is
a separate question, and it is easy to overstate maturity by reading one as the
other. At audit time the installation checked had ten resources across eight
projects: nine `local`/`process`/`dev_session` workloads, and one `server`/`ssh`
handle reporting `unsupervised`, a named handle for connector operations, as the
model intends. Four facts about such an installation matter more than its
inventory.

**Inline is not registered.** `cerberus registry list` and `cerberus registry
health` can both answer "No project configs registered" while `cerberus
validate` reports "Config OK: 0 registered, resolved to 8 projects, 10
resources", because every resource is defined inline in `~/.cerberus/config.yaml`.
Then the per-repo descriptor lane (`cerberus register`, `internal/registry/`,
the registry health check, the console's registry page) has never run there, and
the overview snapshots in `~/.cerberus/state/overview_snapshots.json` show
`registry_entries: 0` for every sample. Descriptor discovery should not be
claimed as mature on the strength of such an installation.

**A descriptor with another user's paths is a template, not configuration.**
`dir:` is a literal path, so a descriptor written on another machine, with every
`dir:` under another user's home, validates and resolves nowhere. Portability is
per resource: a descriptor can use a portable `dir:` (a Homebrew prefix, say) for
some resources and a foreign one for the rest, and is then partly registerable,
not wholly foreign.

**Nothing artifact-backed means a one-line doctor.** When every resource reports
`run_from: workspace` and `mode: dev_session`, `cerberus resource doctor <id>`
runs exactly one check, `runtime_status`, because the launchd, plist,
install-path, artifact-staleness and log-path checks apply only to
`os_service`. `doctor` is then a restatement of `status`.

**Check that source and binary agree before reading behaviour out of the code.**
Compare the installed binary's reported commit with the checkout (`git diff
<commit>..HEAD -- '*.go'`). When the diff is empty, behaviour observed live and
behaviour read from the code are the same codebase; when it isn't, "changed
source is not deployed source" applies.
