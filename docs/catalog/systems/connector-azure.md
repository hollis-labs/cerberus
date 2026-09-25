---
id: "CERB-CAP-208"
class: "capability"
name: "Azure connector (plugin)"
summary: "Reads Azure subscriptions, resource groups, resources and AI model deployments from a runtime-loaded plugin — read and probe only, by decision."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.8
confidence_label: "Plugin loads with no missing secrets, but this audit ran no Azure operation; locked paths are documented not built"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "plugin"
pointer_locator: "hollis-labs/cerberus-plugins: azure/ (plugin manifest)"
tags:
  - "ai-deployments"
  - "azure"
  - "cerberus"
  - "class:capability"
  - "connector"
  - "locked-paths"
  - "plugin"
  - "read-only"
  - "locus:plugin"
relationships:
  - type: "implements"
    target: "CERB-CAP-200"
    note: "Its operations dispatch through the admin lane"
  - type: "depends_on"
    target: "CERB-CAP-301"
    note: "Loaded at runtime; the host resolves its declared secret"
  - type: "relates_to"
    target: "CERB-DEC-290"
    note: "The worked example of a provider integration shipping as a plugin"
  - type: "relates_to"
    target: "CERB-DEC-298"
    note: "Its VM lifecycle operations are documented as locked with unlock conditions"
---

# Azure connector (plugin)

> Reads Azure subscriptions, resource groups, resources and AI model deployments from a runtime-loaded plugin — read and probe only, by decision.

Azure is a plugin, not a built-in, and it is the worked example of the rule:
a provider integration that carries a vendor SDK ships on its own schedule and
is optional per user. It loads from
a build of the separate plugins repository, recorded at audit time as
`trust_tier: unsigned` (since PR #51, `origin: installed`), v0.1.0,
reporting no missing secrets — because the plugin's one declared secret, a
service principal client secret, is genuinely optional: with no service
principal configured it authenticates as the signed-in `az` CLI user.

Six operations, all reads: `list_subscriptions`, `get_subscription`,
`list_resource_groups`, `list_resources`, `list_ai_accounts`,
`list_model_deployments`. None is destructive and none is dry-runnable, which is
correct — making reads prompt empties the gate of meaning.

The reason it stops there is a decision with its evidence recorded, and that is
the part worth carrying. The original brief asked for VM lifecycle. It is
blocked, and not on code: `Microsoft.Compute` is `NotRegistered` on this
subscription, so no VM can exist to list. Registering it needs subscription
Contributor or Owner — there is no narrower built-in role that grants only
registration, verified failing with `AuthorizationFailed … 'Microsoft.Compute/
register/action'`. VM management would additionally need Contributor scoped to a
resource group, because `Virtual Machine Contributor` alone does not cover the
VNet, NIC and public IP a new VM attaches to. And there is a governance question
that was named rather than assumed away: this is a shared quality-management
subscription, and a persistent billable VM in it is a decision for whoever owns
it. Those conditions are written into the plugin's README as "not implemented,
here is why", so the next person finds the reason rather than the gap.

Operationally there are two environment facts that bit and are worth
remembering: `az` is not on the daemon's PATH, and azidentity offers no lever
but PATH to fix that; and `managed load` does not restart an already-running
plugin subprocess, so a credential change needs more than a reload.

## Owns

- Subscription list and get, including resolving the default one operations act on
- Resource group and resource listing
- Azure AI / Cognitive Services account listing
- Model deployment listing under an AI account
- Optional service principal auth, falling back to the signed-in az CLI user

## Does not own

- Any VM lifecycle. list_vms, start_vm, create_vm and their siblings are documented as locked, not built
- Cost, Key Vault and Resource Graph — deferred with recorded unlock conditions
- Any write at all
- Its own credential storage. The host hands it the declared secret over the Init channel
