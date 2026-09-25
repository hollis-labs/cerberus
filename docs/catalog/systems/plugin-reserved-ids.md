---
id: "CERB-CAP-305"
class: "capability"
name: "Built-in connector id reservation"
summary: "Refuses at install any plugin claiming a connector id this binary serves itself, before trust, hashing or any subprocess."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.9
confidence_label: "Wiring read end to end and three tests cover refusal, non-refusal and restore; not exercised live because installing is a mutation"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/pluginhost/installer.go"
tags:
  - "plugin"
  - "security-boundary"
  - "reserved-ids"
  - "cerberus"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-DEC-353"
    note: "reserved ids derived from BuiltInIDs() rather than hardcoded"
  - type: "implements"
    target: "CERB-DEC-357"
    note: "the one-shot lane deliberately does not reserve"
  - type: "depends_on"
    target: "CERB-CAP-302"
    note: "the secret channel namespaces by connector id, which is why the collision is a credential boundary"
  - type: "relates_to"
    target: "CERB-DEC-350"
    note: "the reserved set shrinks as connectors migrate out to plugins"
---

# Built-in connector id reservation

A plugin may not claim a connector id this binary serves itself.
`DirectoryInstaller.ReservedIDs` is checked first in `Install` — before trust is
evaluated, before the entrypoint is hashed, before any subprocess exists — and a
collision returns a `ReservedIDError` naming the id and the two ways out
("rename the plugin, or remove the built-in first"). The refused plugin never
reaches the inventory.

Two reasons, and the second is the one people miss. A plugin claiming a built-in
id would shadow the connector Cerberus serves itself. And because the secret
channel namespaces by connector id, a plugin called `github` would be handed
`CERBERUS_GITHUB_TOKEN` — the built-in's credential — simply by declaring a
secret named `token`. The guard is a credential boundary, not only a naming one.

`pluginhost` has no opinion about what a host compiled in, so the caller
supplies the set. The daemon fills it from `Registry.BuiltInIDs()`, the union of
registered instances, factories and definitions. Derived rather than hardcoded
is the point, and it worked as intended: when `cloudflare` moved out to a plugin
on 2026-09-25 it stopped being registered, and a `cloudflare` plugin became
installable the same day with no guard to edit. The set is now `ssh`, `docker`,
`github`, `digitalocean`, `forge` and `namecheap`.

One id was never in it. `local` is served by the supervision lane, not the
connector registry, so `BuiltInIDs` could not report it, and a plugin could
claim the id `local` despite `AGENTS.md` naming it as reserved. The managed
lane now always reserves the ids the host serves outside the registry
(`hostServedIDs`, currently `local`) on top of whatever the daemon passes, and
`TestManagedPluginInstallRefusesLocalWithoutBeingTold` holds it.

Only the managed lane reserves. `connectors plugin exec` installs into a
throwaway host for one call and registers nothing, so it cannot shadow anything
— and refusing there would break the Docker prototype, whose whole purpose is to
demonstrate authoring against a built-in's shape. An inventory registered before
the guard existed can still hold a shadowing plugin; restore skips it with a
warning and keeps the registration rather than dropping the operator's record or
taking the daemon down. WP-0's older safety net — an installed-but-unloaded
plugin falls back to the built-in it shadows, because unloading one once left
`cerberus docker ps` permanently broken with no uninstall command — stays in
place for exactly that case.
