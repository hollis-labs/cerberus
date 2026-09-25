---
id: "CERB-CAP-306"
class: "capability"
name: "Plugin install origin and operation gate"
summary: "Records how a plugin was installed, installed or dev, and gates every operation on that origin, the operation's contract and, since P1-5, the review the operator accepted: the bundle digest and host range at load, and accepted previews for dry runs. It makes no trust claim."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "internal/pluginhost/policy.go, bundle.go and manager.go read on the P1-5 branch; CheckBundle and accepted-preview tests run"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/pluginhost/policy.go"
tags:
  - "plugin"
  - "trust"
  - "security-boundary"
  - "unverified-claim"
  - "cerberus"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-DEC-354"
    note: "unsigned sat with the trusted tiers; superseded by CERB-DEC-815"
  - type: "implements"
    target: "CERB-DEC-355"
    note: "the host computes the entrypoint hash itself"
  - type: "blocks"
    target: "CERB-GAP-335"
    note: "the tiers constrained nothing reachable; closed in PR #51"
  - type: "blocks"
    target: "CERB-GAP-336"
    note: "closed in P1-5: the bundle digest is compared on every load"
  - type: "relates_to"
    target: "CERB-DEC-815"
    note: "origin replaced the trust tiers"
  - type: "blocks"
    target: "CERB-GAP-848"
    note: "closed in P1-5: devmode-only, decided and documented"
---

# Plugin install origin and operation gate

This record was "Plugin trust policy and tiers" until PR #51, and the rename is
the finding. At audit time `TrustPolicy` recorded one of six tiers (`builtin`,
`signed`, `local_dev`, `unsigned_dev`, `unsigned`, `untrusted`) from a mode and
two caller-asserted booleans, `--catalog-signed` and `--archive-signed`. There
was no signature verification anywhere, so passing both flags against an
unsigned directory recorded `signed` (verified live at the time), and
`OperationAllowed` treated `signed`, `unsigned` and `builtin` identically anyway.
The only tiers that gated anything needed a devmode build that no Makefile
target produces (CERB-GAP-335).

PR #51 removed the vocabulary instead of implementing signing, because Cerberus
does not sign or vet plugins (Decision 12 in
`docs/plans/live-systems-security-target.md`). What replaced it is an install
origin, which states how a plugin was installed and what the host restricts, and
makes no claim about who built it (CERB-DEC-815):

- **`installed`**: installed from a local directory. Its operations are gated
  like a built-in's: a `destructive` one needs `--ack`.
- **`dev`**: a development install with `--dev`. It needs a devmode build and a
  source under an allowed developer root, and every destructive operation is
  refused, acknowledged or not. That is a restriction, not a lower trust level.

`ValidateInstall` checks the manifest, requires that the entrypoint could be
fingerprinted, refuses a requested sandbox profile while no sandbox is enforced
(nothing sets `SandboxEnforced`), and returns the origin. `OperationAllowed`
runs on every operation. It refuses a `dev` plugin's destructive operations and
any unknown origin, including the legacy tier names, then demands
acknowledgment for any `destructive` operation. Since PR #49 it no longer reads
`requires_ack`, so a manifest cannot opt a destructive operation out of the gate
(CERB-GAP-286). A dry run reaches the plugin only for an operation that declares
`supports_dry`, and that preview is the plugin's claim (CERB-DEC-814).

Two things are unfinished. `dev` has the old reachability problem under a new
name: `DevModeEnabled` is a build-tag constant, false in every build the
Makefile produces, so a release build refuses `--dev` with "a development
install (--dev) requires a devmode build" (CERB-GAP-848). And the entrypoint
hash is half-wired. The host always computes it, since `--archive-sha256` is
gone, and `managed list` reports it as `entrypoint_sha256`. It is not written to
`plugin-connectors.json`, and restore recomputes it from disk, so nothing
compares it (CERB-GAP-336). The P1 plan pairs comparing it with a re-review.

State compatibility is handled rather than migrated. An old state entry's
`trust` object, and an older CLI's `trust` install argument, are read for
`dev_mode` alone. The signing keys and `archive_sha256` are ignored, and the
next write uses `options`.

## Since PR #60

`OperationAllowed` reads the operation's effective contract, not the legacy
flags:

- **Acknowledgment:** any operation whose contract needs it, which includes
  every operation with no declared `effect` (treated as `exec`,
  CERB-DEC-821).
- **`dev` origin:** refuses an operation that is `destructive` by its effect,
  or by the legacy flag when no effect is declared.
- **Key table:** the host checks the plugin's `input_schema` before calling
  it.
- **Coded refusals:** acknowledgment, undeclared-operation and key-table
  refusals are coded for the admin lane, and a plugin that is not loaded is
  `connector_unavailable`.

## Since P1-5

The entrypoint hash is no longer half-wired. What load compares is the digest of
the whole bundle, which covers the entrypoint: it is accepted in the install
review, persisted as `bundle_digest`, and checked by `pluginhost.CheckBundle` on
every load. A mismatch refuses the plugin as `plugin_changed` (CERB-GAP-336,
closed). `entrypoint_sha256` stays as the audit record's fingerprint of the
binary.

Two new rules sit beside the origin. The first is the host range: a plugin may
declare `cerberus.host: {min_contract, max_contract}`, and a host whose
`pkg/plugin.ContractVersion` falls outside it refuses the plugin at install and at
load. The second is Decision 7: `pluginhost.PreviewAccepted` lets a dry run skip
acknowledgment when the operation declares a preview and the operator accepted
it in the plugin's review. A plugin still pending review has accepted nothing.
The real run always needs `--ack`, and every plugin dry run is recorded
`plugin_claimed`.

`--dev` keeps needing a devmode build. The install review decided that rather
than widening it, and `docs/plugins.md` says so (CERB-GAP-848, closed). A dev
install is reviewed and digest-checked like any other; it only runs from its
source directory instead of a store copy.
