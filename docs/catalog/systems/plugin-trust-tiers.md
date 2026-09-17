---
id: "CERB-CAP-306"
class: "capability"
name: "Plugin trust policy and tiers"
summary: "Records a trust tier at install from a policy mode and caller-asserted signature claims, and checks that tier on every operation."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.9
confidence_label: "Policy and gate read in full; devmode refusal and self-asserted signed install both verified live; no signature verification exists"
last_reviewed: "2026-09-17"
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
    note: "unsigned sits with the trusted tiers; provenance vs intent"
  - type: "implements"
    target: "CERB-DEC-355"
    note: "the host computes the entrypoint hash itself"
  - type: "blocks"
    target: "CERB-GAP-335"
    note: "the only gating tiers need a devmode build, and signed is unverified"
  - type: "blocks"
    target: "CERB-GAP-336"
    note: "the computed hash is discarded"
---

# Plugin trust policy and tiers

`TrustPolicy` decides whether a plugin may be installed and records a
`TrustTier` on the result; `OperationAllowed` consults that tier on every
operation. Three modes: `catalog_signed` (the default, requiring a catalog
signature, an archive hash and an archive signature), `local` (unsigned from a
local path, requiring the hash only), and `developer` (unsigned from an
allow-listed root, and only in a `devmode` build). Six tiers: `builtin`,
`signed`, `local_dev`, `unsigned_dev`, `unsigned`, `untrusted`.

The gating rule is short. `builtin`, `signed` and `unsigned` may run anything,
subject to `--ack` on a `destructive` operation that declares `requires_ack`.
`local_dev` and `unsigned_dev` are refused every destructive operation outright.
`untrusted` and the empty tier are refused everything. Putting `unsigned` with
the trusted tiers is deliberate and argued in the code: a signature attests to
*provenance*, the acknowledgment attests to *intent*, and an operator who built
or vetted a plugin themselves has established provenance out of band — grouping
it with the dev tiers would make the plugin lane read-only, and a read-only
plugin lane cannot carry the provider integrations it exists for.

Which tiers are reachable is a different question from which tiers exist, and
the answer is uncomfortable. `DevModeEnabled` is a build-tag constant, `false`
in every build the Makefile produces; `--dev` on the installed binary fails with
"developer trust mode requires a devmode build" (verified live). So
`local_dev` and `unsigned_dev` — the only tiers that gate anything on trust —
cannot be reached by any shipped binary. And `signed` is not verified: there is
no signature verification anywhere in the codebase, no cosign, no minisign, no
key material. `CatalogSigned` and `ArchiveSigned` are booleans the *caller*
asserts, reachable as `--catalog-signed` / `--archive-signed` on the CLI;
passing both against an unsigned plugin directory is accepted (verified live).
`TrustPolicy` is therefore honest about what it recorded and dishonest about
nothing — it is the tier *names* that imply verification that does not exist.

The hash is the one integrity mechanism that is real, and it is half-wired. The
installer computes the entrypoint's SHA-256 itself when the caller supplies
none, on the sound argument that asking an operator to paste a digest of a
binary they just built is friction that buys nothing. But the result is only
stored on the in-memory `InstalledPlugin`: it is not written to
`~/.cerberus/plugin-connectors.json`, not compared on restore or reload, and not
in the `managed list` payload. Both plugins on this machine are `unsigned`, and
neither declares a destructive operation — so the tier gate, the `--ack` path
and the whole trust apparatus are, in practice, exercised by nothing that runs
here. The only plugin artifact in existence with a destructive operation is the
Docker prototype, whose id is reserved and which can therefore only be run
through the one-shot lane.
