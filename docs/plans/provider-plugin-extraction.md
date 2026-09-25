# Provider plugin extraction: cloudflare, digitalocean, forge, namecheap

Moving the four compiled-in provider connectors out to plugins in
`hollis-labs/cerberus-plugins`. This is the X-0 survey (taken against cerberus
`cd1729c` and cerberus-plugins `7c2929a`, 2026-09-25), the decisions taken on
it the same day, and the PR sequence those decisions produced. It supersedes
CERB-DEC-291's "stay put for now".

Read first: AGENTS.md "Core or plugin", `connector-work-packages.md` ("How a
connector verb is actually added"), `live-systems-security-target.md`
(section 10, Decisions 7, 12 and 14, "P1 cut") and ADR 0003.

## Decisions — 2026-09-25

| # | Question | Decision |
|---|---|---|
| 1 | The pseudo-neutral verbs (`cerberus dns`, `domain`, `server` and the unprefixed MCP tools) | **Dropped outright.** No CLI tombstones and no MCP tombstones; this departs from CERB-DEC-472 on purpose (24 dead tools would cost every agent context). A read-only DNS capability is revisited with CERB-DEC-797. |
| 2 | Removal before MCP generation | **No.** Each host removal waits for manifest-generated MCP tools (H3). |
| 3 | Who builds the MCP generator | **This workstream (H3), after P1-3**, using contract-derived hints. Exposure is **default-deny**: a generated plugin tool appears only for operations the operator enabled in config. P1-5 builds the install-review UX on top of that switch. |
| 4 | Forge | **Migrated, not retired** (still in use). It goes last. |
| 5 | Cloudflare's wrangler fallback | **Dropped.** It authenticated outside the declared-secret channel. |
| 6 | Live verification | Operator UAT on a machine that holds the tokens. PRs verify against fakes and recorded fixtures, and each plugin PR ships a live-check script or runbook. **A live-check script requires a read-only token** (for Cloudflare: Zone Read + DNS Read), because it previews writes with `--dry-run` against the real API and that preview is plugin-claimed. A read-only token turns a dry-run bug into a harmless 403. |
| 7 | Credential rotation for plugins | The host reloads a loaded plugin when the console saves one of its secrets. |
| 8 | Previews beyond parity | Yes to all three. DO `user_data` as a digest shipped first, as a leak fix (H0). Namecheap `set_dns_record_set` diffs against current records. Forge `update_deployment_script` gains a script diff preview. |
| 9 | Plugin install location | Flagged to P1-5: install copies out of a working checkout. |
| 10 | `pkg/connector.DryRunPreview` | Yes, after P1-1, so plugins emit the host's preview shape from a shared type. |
| 11 | Plugin dry runs needing `--ack` | **Kept until P1-5.** `pluginhost.OperationAllowed` gates ack before dry-run, so a plugin that ignored `dry_run` cannot execute an unacknowledged write. It comes off with P1-5's accepted-preview declaration ("the install review comes before plugin dry runs lose `--ack`"). |
| 12 | Gate test fixtures | When a built-in is removed, the gate tests that used it move onto a fake connector, not onto the next real one. |
| 13 | Error vocabulary | `operation_failed` (502) is the one code for an operation that ran and failed upstream, for plugins (H0) and built-ins (P1-1) alike. |
| 14 | Plugin settings (H2b) | Declared config fields were never delivered to a plugin (the host handed `Init` only secrets). They now come from an operator-edited `~/.cerberus/connector-config.yaml`, keyed `<plugin id>: {fields: {...}, mcp: {expose: [...]}}`. Cerberus never writes it. Undeclared fields and operations, wrong types and secret references refuse the load and are named in `managed list`. It is also H3's MCP exposure switch. |

## Status

| PR | Repo | What | State |
|---|---|---|---|
| H0 | cerberus #58 | Plugin value redaction, `operation_failed`, redacted notifications, DO `user_data` digest | merged |
| A1 | cerberus-plugins #4 | `cloudflare` plugin | merged. **Not installed as a managed plugin anywhere until H4**; the id is the built-in's. |
| H1 | cerberus #59 | Shared MCP helpers, `cerberus connectors exec` with typed args, `managed exec` through the admin lane | merged |
| H2b | cerberus #65 | `connector-config.yaml`: plugin fields delivered, MCP exposure switch | merged |
| H3 | cerberus #66 | MCP generator: `cerberus_<plugin>_<op>` for each exposed op, refreshed every 15s with tools/list_changed; the one-shot `plugin exec` prints the code | merged |
| H4 | cerberus | Remove the cloudflare built-in. Binary 62.8MB → 23.6MB stripped (88.1 → 34.1MB unstripped). Gate tests on a fake connector; `local` always reserved | this PR |
| H4 | cerberus | Remove the cloudflare built-in | waits for P1-1 and H3. Its UAT table carries A1's tightened behaviour: real-path validation of `create_dns_record`, bad numbers rejected, a `{deleted, zone_id, record_id}` delete result, and a health check with no network call |
| — | both | digitalocean, then namecheap, then forge | after H4 |

The survey follows, as it was sent. Where it recommends something the table
above decided differently, the table wins.

---

## 0. Three premises in the brief that don't hold

1. **"`cerberus dns …` and `cerberus_dns_*` span cloudflare and namecheap."**
   They don't. `cerberus dns`, `cerberus domain`, `cerberus_dns_list/create/delete`,
   `cerberus_domain_list/status`, `cerberus_nameservers_set` and
   `cerberus_get/set_dns_record_set` all hardcode `"namecheap"` as the connector.
   The help text says "DNS operations (Namecheap)" and sends Cloudflare users to
   `cerberus cloudflare dns` (`cmd/cerberus/cmd_namecheap.go:102-110`).
   `cerberus server` hardcodes `"digitalocean"` the same way. No verb is actually
   provider-neutral, so none of them needs routing.
2. **"Plugin ops reach MCP from the manifest today."** They don't. CERB-GAP-330
   and CERB-GAP-434 record this. `cmd_mcp.go` and `cmd_daemon.go` register only
   hand-written tools, and none of contextforge, azure or kubernetes is reachable
   over MCP. The only surfaces where a plugin gets the same treatment as a
   built-in are the socket/HTTP API (`POST /connectors/<id>/operations/<op>`
   through `ExternalConnectorService.Execute`) and the web console's generic
   Connectors page.
3. **"Pipelines, deploy profiles and resources that reference them."** Pipelines:
   none. Actions are local-only (`internal/pipeline/resolve.go:139`). Deploy
   profiles: metadata only. `DNSProvider`, `CloudflareZoneID` and `NamecheapDomain`
   are stored and shown, but `internal/infra/deploy.go` never calls either
   connector. Resources: nothing in `~/.cerberus/config.yaml` uses these four
   connectors. The unsupervised-handle code is generic apart from the ssh and
   docker next-step hints.

The consequence of (2) drives the rest of this proposal. **Once a built-in's
hand-written tools are removed, agents lose that connector entirely unless the
host can generate MCP tools from a manifest first.** That makes MCP generation a
prerequisite for removal, not something to follow up afterwards.

## 1. Host touchpoints, per connector

Common to all four. Each item has to be deleted or rewired when a built-in goes:

| Where | What |
|---|---|
| `internal/app/app.go:161-182` | `RegisterDefinition` + `RegisterFactory` (the reserved-id set shrinks on its own) |
| `internal/cerbapi/external_connector_service.go` | an import alias, a `case` in the `Execute` dispatch, an `execute<X>` func, and a `dryRunPreview` case |
| `internal/cerbapi/connector_payload.go` | typed decode cases. A plugin payload stays `json.RawMessage` (the default branch), so the CLI's typed `result.Data.(…)` assertions break |
| `cmd/cerberus/cmd_mcp.go` **and** `cmd_daemon.go` | tool registrations (both lists) |
| `internal/mcp/tools_<x>.go` | hand-written tools. **Trap:** `executeConnectorMCP` and `boolArg` live in `tools_cloudflare.go` and `stringArg` in `tools_namecheap.go`, and `tools_ssh.go`, `tools_forge.go` and `tools_digitalocean.go` use them. Move them to a shared file first |
| Tests | `cerbapi/external_connector_service_test.go` (~50 refs), `gate_test.go` (these connectors are the **fixtures for the ack/dry-run gate tests**), `connector_payload_test.go`, `mcp/hints_test.go`, `app_test.go`, `cmd_connectors_test.go`, `registry_test.go`, `pkg/connector/manifest_test.go`, `keychain_test.go` (uses a DO name as sample data only) |
| `go.mod` | `cloudflare-go/v4`, `godo` |
| Docs | README (16 CLI/MCP references), AGENTS.md "Core or plugin", `connector-work-packages.md` table, catalog |
| Catalog | CAP-204/205/206/207 (`locus: core` → `plugin`), TOOL-233…257 and TOOL-726, DEC-291 (resolve), DEC-295, DEC-472, GAP-280, GAP-281, GAP-285, GAP-436 |
| Web console | `internal/webui/infra.go` `providerCatalog()` lists cloudflare and namecheap secrets and fields (keychain presence plus a write path). The generic Connectors page needs no change |

Per connector:

**cloudflare** (5 ops)
- CLI: `cerberus cloudflare zones`, `zones create`, `dns list|create|delete` (`cmd_cloudflare.go`).
- MCP: `cerberus_cloudflare_zones`, `_zone_create`, `_dns_list`, `_dns_create`, `_dns_delete`, plus `cloudflare_priority_test.go`.
- Previews: `create_zone`, `create_dns_record`, `delete_dns_record`.
- Other:
  - A **wrangler CLI fallback backend** (`cli_backend.go`), used when no token resolves. It can't list or create zones, and it authenticates through wrangler's own login under `HOME`, which `pluginLaunchEnv` passes through. That is an ambient credential outside the declared-secret channel.
  - `ListTunnels` is implemented but not declared (GAP-285).
  - No tests (GAP-280).
  - Types are already hand-mapped DTOs.

**digitalocean** (7 ops)
- CLI: `cerberus server list|show|create|start|stop|destroy` (`cmd_server.go`).
- MCP: `cerberus_droplet_list|get|create|start|stop|destroy`. `status` has no tool (GAP-434).
- Previews: `create_droplet`, `stop`, `destroy`.
- Other:
  - `executeDigitalOcean` reaches the `contract.Connector` lifecycle methods through `externalResource`.
  - `create_droplet` has a partial-success shape: `{droplet_id}` comes back when the follow-up read fails, and `connector_payload.go` special-cases it.
  - `Backend` returns `godo` types and the mapping happens above it. `ListDroplets` reads one page of 100 with no pagination.

**forge** (7 ops)
- CLI: `cerberus forge servers|server|sites|deploy|script|set-script|exec` (`cmd_forge.go`).
- MCP: `cerberus_forge_servers|server|sites|deploy|exec`. `get/update_deployment_script` have no tool (GAP-434).
- Previews: `deploy_site`, `exec_site_command`. `update_deployment_script` is destructive with **no preview**, so `--dry-run` returns `preview_unsupported`.
- Other: the package describes itself as "a transitional connector that will be removed once migration from Forge is complete". No tests (GAP-280).

**namecheap** (8 declared ops + 2 retired)
- CLI: `cerberus domain list|status|nameservers set`, `cerberus dns list|create|delete` (the last two are tombstones).
- MCP: `cerberus_domain_list|status`, `cerberus_nameservers_set`, `cerberus_dns_list`, `cerberus_get_dns_record_set`, `cerberus_set_dns_record_set` (generated from `namecheap.Definition()`), and `cerberus_dns_create|delete` (tombstones), plus `namecheap_record_set_test.go`.
- Previews: `set_dns_record_set` and `set_custom_nameservers`, both with warnings.
- Host special cases:
  - `Execute` refuses `create/delete_dns_record` before resolution (`external_connector_service.go:191`). `dryRunPreview` refuses them again.
  - `namecheapRecordSetArgs` (host-side validation of `email_type` and records) lives in `cerbapi`, not in the connector.
- Redaction: `client.go:346` `redact.New(apiKey, url.QueryEscape(apiKey))`. The key travels in the query string, so a `net/http` error echoes it.

## 2. Provider-neutral verbs

Because of premise (1), there is nothing to route. Of the three options, I
recommend **dropping them**: each pseudo-neutral name becomes the plugin's own
operation name, and the old names are handled as in §3.

My reasons against a capability interface now:
- The two DNS models are incompatible exactly where it matters. Cloudflare does per-record CRUD by `zone_id/record_id`. Namecheap does whole-zone replace by `domain` plus `email_type`, and it refuses per-record writes on purpose (DEC-295). A neutral `dns create` would have to paper over the difference that `ErrUnsafePerRecordWrite` exists to stop.
- Routing "by provider id" needs a registration model that says which provider owns a domain. That is the ProviderRegistration split DEC-797 deferred.
- A capability interface is worth doing once there are two implementations of one semantics. The only candidate today is a read-only `list_records` normalised across providers. Record it as a follow-up tied to DEC-797, not part of this workstream.

## 3. How the replacements are exposed, and what a user loses

**What a plugin gets today:**
- **API/socket/web:** full. `ExternalConnectorService.Execute` dispatches to a loaded managed plugin, and the web Connectors page renders forms from the manifest.
- **CLI:** only `cerberus connectors plugin managed exec <id> <op> --arg k=v [--dry-run] [--ack]`. This has two gaps:
  - `--arg` values are **strings only** (`parsePluginArgs`). `nameservers` and `records` (namecheap), `ssh_keys` (DO) and `proxied` (cloudflare) cannot be expressed. Ints like `droplet_id` arrive as strings and work only if the plugin coerces them.
  - It calls `ExecuteManagedPlugin` directly, **bypassing `ExternalConnectorService.Execute`**. The host's dry-run guard and the future P1-4 audit point don't sit on that path; pluginhost repeats the ack and `supports_dry` checks itself.
- **MCP:** none.

**What the host has to build** before a built-in's surface can be removed:
- **H-MCP: MCP tools generated from manifests** (closes GAP-330/430/434).
  - Name: `plugin.ToolNameForOperation` → `cerberus_<id>_<op>`.
  - Schema: the manifest `input_schema`, plus `dry_run` when `supports_dry` and `acknowledged` when ack-gated (the `NewCerberusDNSRecordSetTools` pattern already does this).
  - Hints: derived (P1-3). Visibility: hidden until enabled (P1-5).
  - Registration: one registration point for the stdio server, the daemon and mcp-http. Today that is two hand-maintained lists.
  - **Needs a decision from you:** P1-5 already owns "plugin operations reach MCP hidden until enabled". Should cerberus-77 build the generator inside P1-5, or should I build it after P1-5 lands?
- **H-CLI: `cerberus connectors exec <id> <op>`** through `ExternalConnectorService.Execute`, covering built-ins and plugins (GAP-437).
  - Args are coerced by `input_schema` type, e.g. `--arg ttl=300` becomes an integer.
  - Repeated `--arg nameservers=a --arg nameservers=b` builds an array.
  - `--arg-json records='[…]'` and `--input file.json` cover structured values.
  - `managed exec` becomes an alias for it. Generated `cerberus <id> <op>` verbs can wait; they are optional.
- **H-REDACT: host-side value redaction for plugin-origin text** (WP-S2, plugin lane only).
  - The host already resolves every declared secret for the plugin. Keep a `redact.New(values…)`, including `url.QueryEscape` forms, per loaded plugin.
  - Apply it to plugin error text, health messages and MCP notifications.
  - This also closes a hole: `managed_plugin_connector_service.go:276` sends the plugin's raw `err.Error()` into `gmcp.NotifyMessage` without even `redact.Text`. A generic plugin error is also returned unwrapped (`managedPluginExecuteError` default branch), so it misses `ExternalConnectorError.Error()`'s `redact.Text`.

**Breaking changes.** One user, so these are acceptable, but each one is listed:

| Old | New |
|---|---|
| `cerberus cloudflare zones [create]`, `cloudflare dns list/create/delete` | `cerberus connectors exec cloudflare list_zones/create_zone/list_dns_records/create_dns_record/delete_dns_record` |
| `cerberus server list/show/create/start/stop/destroy` | `… exec digitalocean list_droplets/get_droplet/create_droplet/start/stop/destroy` (+ `status`) |
| `cerberus forge servers/server/sites/deploy/script/set-script/exec` | `… exec forge list_servers/get_server/list_sites/deploy_site/get_deployment_script/update_deployment_script/exec_site_command` |
| `cerberus domain list/status/nameservers set`, `cerberus dns list` | `… exec namecheap list_domains/get_domain_status/set_custom_nameservers/list_dns_records` (+ `get/set_dns_record_set`, which gain a CLI for the first time: GAP-436) |
| `cerberus dns create/delete` (tombstones) | gone |
| MCP `cerberus_cloudflare_zones`, `_zone_create`, `_dns_list`, `_dns_create`, `_dns_delete` | `cerberus_cloudflare_list_zones`, `_create_zone`, `_list_dns_records`, `_create_dns_record`, `_delete_dns_record` |
| MCP `cerberus_droplet_*` (6) | `cerberus_digitalocean_list_droplets`, `_get_droplet`, `_create_droplet`, `_start`, `_stop`, `_destroy`, `_status` |
| MCP `cerberus_forge_servers/server/sites/deploy/exec` | `cerberus_forge_list_servers/_get_server/_list_sites/_deploy_site/_exec_site_command` (+ both script ops, newly reachable) |
| MCP `cerberus_domain_list/status`, `cerberus_nameservers_set`, `cerberus_dns_list`, `cerberus_get/set_dns_record_set` | `cerberus_namecheap_list_domains/_get_domain_status/_set_custom_nameservers/_list_dns_records/_get_dns_record_set/_set_dns_record_set` |
| MCP `cerberus_dns_create/delete` (tombstones) | gone |
| Tables in CLI output | JSON (the typed renderers depend on `decodeConnectorPayload`) |
| Credential rotation takes effect on the next call | takes effect on `managed load <id>` (plugins resolve at load) |
| Plugin MCP tools visible by default | hidden until enabled (P1-5) |
| Cloudflare with no token uses wrangler | `credential_missing` (if we drop wrangler, see §7) |

In total that is 24 CLI leaves and 24 MCP tools renamed or removed, and 4
operations newly reachable over MCP (`digitalocean status` and forge
`get/update_deployment_script`, among others).

## 4. Credentials

**Existing references keep working with no migration, as long as the plugin
ids and secret names don't change.** The plugin secret channel looks each one up
as `<plugin id>/<secret name>` through `app.ConnectorSecrets`. That is the same
key a built-in uses (`secrets.Get(ctx, "<connector>", "<key>")`). So
`CERBERUS_CLOUDFLARE_API_TOKEN`, `cloudflare: api_token:` in
`connector-secrets.yaml`, keychain service `cerberus`, account
`cloudflare/api_token`, and the web console's keychain writes all resolve the same
way on either side of the boundary.

| Plugin id | Secrets to declare | Notes |
|---|---|---|
| `cloudflare` | `api_token` | `required: true` if wrangler is dropped |
| `digitalocean` | `api_token` (required) | |
| `forge` | `api_token` (required) | |
| `namecheap` | `api_user`, `api_key`, `username` (required), **`client_ip` (optional)** | see below |

Keep `env: CERBERUS_<ID>_<NAME>` on each, which is what the host actually reads,
so GAP-339's mismatch doesn't spread to these plugins.

Gaps found:
- **`client_ip` is read by the built-in but never declared** (`namecheap/connector.go:65`). The host hands a plugin only the secrets its manifest declares, so without a declaration it silently falls back to `127.0.0.1`. Declare it.
- **A second, pre-existing bug:** the web console treats `client_ip` as a *field* and saves it to `infra.yaml`, while the connector reads it from the *secret* store. An IP entered in the console never reaches the connector. Fix it during the namecheap move: the console writes it as a secret.
- **Rotation regresses.** A console secret save no longer takes effect until the plugin reloads. My recommendation: after a secret write for id X, the host reloads the loaded plugin X. It's small, and the console already knows the id.
- **Install ordering:** `managed install` of a plugin called `cloudflare` is refused until the built-in is gone from a *running* daemon, because of the reserved-id guard. The plugin can still be proven end to end beforehand through the one-shot `cerberus connectors plugin exec <dir> <op>`, which doesn't reserve ids and shares `pluginhost.Manager`.
- **On this machine none of the seven references exist:** no keychain entries, no `CERBERUS_*` env, no `connector-secrets.yaml`. So nothing here breaks, and there is also nothing to verify reads against (§7).

## 5. Safety logic that moves with each connector

For all four, the plugin does the following. Items 1-3 follow the kubernetes and contextforge patterns:
1. Declares every write with `destructive` + `supports_dry`, and serves `dry_run` itself. The preview is plugin-claimed (Decision 7).
2. Re-checks acknowledgment itself, like kubernetes' `writeMode`: extra protection under I10, never the gate.
3. Keeps vendor types confined behind a `Backend` that returns DTOs, with a sentinel test that a fully populated vendor response doesn't serialize credentials.
4. Scrubs its own resolved credential values from every error before it crosses the boundary. It can't import `internal/redact`, so this is a ~15-line local scrubber with a test that forces a transport error containing the value. H-REDACT is the host-side backstop.
5. Every new refusal or error gets a test that it survives `redact.Text`.
6. Emits the host's preview shape `{dry_run, summary, target, input, warnings}`, so the CLI and the future plan hash see one shape. My recommendation: move `ExternalConnectorDryRunPreview` into `pkg/connector` as the contract type rather than have each plugin copy it. This is a small public-contract addition.

P1-1 effects I'd declare (cerberus-77's P1-1 decides the built-ins; the plugin should match it):

| Connector | read | read_sensitive | write | lifecycle | destructive | exec |
|---|---|---|---|---|---|---|
| cloudflare | list_zones, list_dns_records | | create_zone, create_dns_record | | delete_dns_record | |
| digitalocean | list_droplets, get_droplet, status | | create_droplet (`cost: billable`) | start, stop | destroy | |
| forge | list_servers, get_server, list_sites | get_deployment_script | update_deployment_script | deploy_site | | exec_site_command |
| namecheap | list_domains, get_domain_status, list_dns_records, get_dns_record_set | | set_custom_nameservers | | set_dns_record_set (omitted records are deleted) | |

Two of these differ from today's flags:
- DO `start` needs ack for the first time (Decision 14).
- Forge `update_deployment_script` gains a preview (below).

Per connector:
- **cloudflare:**
  - Carry the three previews as they are.
  - Drop the wrangler backend (§7).
  - Drop the undeclared `ListTunnels`, or declare it as `read`.
- **digitalocean:**
  - **The `create_droplet` preview echoes `user_data` verbatim.** Cloud-init routinely carries secrets, and that preview lands in agent context and, from P1-4 on, the audit record. Show its length and sha256 instead.
  - Keep the partial-success `{droplet_id}` result.
- **forge:**
  - Add an `update_deployment_script` preview that shows the current script against the proposed one. That's a server read, so it's an honest preview.
  - Label `get_deployment_script` output `free_text`.
- **namecheap:**
  - Move `namecheapRecordSetArgs` and `DNSRecordSet.Validate` into the plugin, and run them on both the dry-run and real paths.
  - **Don't declare `create/delete_dns_record` at all.** The host then refuses them fail-closed ("does not declare operation") before dispatch, and the namecheap special case in `Execute:191` goes away with the built-in.
  - Keep `redact.New(apiKey, QueryEscape(apiKey))` semantics through the local scrubber, with a test forcing a `net/http` error whose URL carries the key.
  - Recommended upgrade: have the `set_dns_record_set` preview read current `getHosts` and list what would be deleted. That turns the most dangerous operation in the four into one whose preview shows the damage.

## 6. Sequencing against P1

Hard constraints:
- **Host removal waits for P1-1.** P1-1 edits these four Definitions, and the gate tests use them as fixtures.
- **Host removal also waits for H-MCP**, or agents lose the connector (§0).
- **Plugins pin `cerberus v0.4.0-beta.2`.** Declaring `effect` in a plugin manifest needs a Cerberus tag cut after P1-1. Until then a plugin ships with `destructive`/`supports_dry`, and an undeclared `effect` falls back to `exec` (ack on everything). That's a strict transition that only costs `--ack` on reads.
- **The live daemon loads plugins from `~/Projects-apps/cerberus-plugins/dist/<id>`, a working checkout.** Running `make dist` or `make clean` there swaps the binaries the running daemon uses, and once P1-5 compares hashes, the next load is refused. I'll work in my own clone and never build dist in that one. It's also worth deciding whether P1-5's in-process install should copy into `~/.cerberus/plugins/` (§7).

Proposed PRs, one per branch:

| # | Repo | PR | Can start | Depends on |
|---|---|---|---|---|
| A1 | plugins | `cloudflare/` plugin: API backend, DTOs, previews, local scrubber, fake-backend and canary tests, Makefile/CI matrix entry | now | — |
| H1 | cerberus | move shared MCP helpers out of `tools_cloudflare.go`/`tools_namecheap.go`; `connectors exec` with typed args (H-CLI) | now (cmd + mcp only; low overlap with P1-1) | — |
| H2 | cerberus | H-REDACT: per-plugin value redactor + the unredacted `NotifyMessage` | after P1-5 plans are known (touches pluginhost) | coordinate with cerberus-77 |
| H3 | cerberus | H-MCP generator | after P1-3/P1-5, or inside P1-5 | your call (§3) |
| H4 | cerberus | remove the cloudflare built-in: code, registrations, tests rewired to fakes/remaining built-ins, `go.mod`, README, AGENTS.md, catalog. Measure binary before/after | after P1-1 + H3 | A1 merged |
| — | operator | redeploy the daemon (build, `mv`, `kickstart`), `managed install` cloudflare, one read op | after H4 | |
| A2/H5 | both | digitalocean, same shape (drops `godo`) | after H4 is live | |
| A3/H6 | both | namecheap: record-set validation, diff preview, `dns`/`domain` commands and the `Execute` special case removed, console `client_ip` as a secret | after H5 | |
| A4/H7 | both | forge, or retire it (§7) | last | |

A1 can go to review while P1 runs. H4 is the first PR that touches P1's files.
For measured payoff: in the current 87.8MB binary, `cloudflare-go` accounts for
about 8.9MB of symbol size, `godo` 0.6MB, and forge+namecheap together 0.04MB.
The 33MB figure is module source size, not linked size; H4 records the real delta.

## 7. Questions put to the operator (answered above)

1. **Neutral verbs:** drop `cerberus dns/domain/server` and the unprefixed MCP names. *Recommend: drop.* Revisit a read-only DNS capability with DEC-797.
2. **Tombstones for the old names.** DEC-472 says to tombstone retired entries. With 24 CLI leaves and 24 MCP tools, that is heavy. *Recommend: CLI tombstones as hidden cobra commands with a `Deprecated` message naming the replacement (cheap, one release). No MCP tombstones: 24 dead tools cost every agent context on every session, and the replacements share a discoverable `cerberus_<id>_` prefix.* This departs from DEC-472, so it needs an explicit yes.
3. **Removal before MCP generation?** *Recommend: no.* Gate each host removal on H-MCP.
4. **Who builds H-MCP:** inside P1-5 (cerberus-77) or after it (me). *Recommend: inside P1-5*, since "hidden until enabled" is already its scope. Then I just consume it.
5. **Forge: migrate or retire?** Its own package comment calls it transitional and pending removal, and no Forge token exists on this machine. *Recommend: retire* (delete with CLI tombstones) unless Forge is still in use. The same question is worth one line for DigitalOcean.
6. **Cloudflare's wrangler fallback:** *Recommend: drop.* It authenticates outside the declared-secret channel (ambient `HOME` login), can't do zone operations, and its only benefit is a no-token path.
7. **Live read verification:** none of the seven credentials exist here. *Recommend:* the operator supplies read-scoped tokens (a Cloudflare token scoped to Zone:Read + DNS:Read, a DigitalOcean read-scope token) to `connector-secrets.yaml` in a scratch HOME. Otherwise we accept fake-backend verification only and say so in each PR. Namecheap has no read scope and needs the IP whitelist, so any live check there is a full-account key.
8. **Rotation:** *Recommend:* when a console secret save hits a loaded plugin's id, the host auto-reloads that plugin, rather than just documenting "run `managed load`".
9. **Previews beyond parity:** the DO `user_data` digest, a Namecheap diff against current records, and a Forge script diff. *Recommend: all three.* The first is a leak fix; the other two are cheap and make the riskiest writes show their effect.
10. **Plugin install location:** installing from a working checkout's `dist/` is fragile. *Recommend: P1-5's in-process install copies into `~/.cerberus/plugins/<id>/<version>/`.* That's a decision for P1-5; I'm flagging it here.
11. **`pkg/connector` gains a `DryRunPreview` type** so plugins emit the host's shape. *Recommend: yes*, and after P1-1, since it's the same package.
