# Cerberus clients and callers (Substrate port inventory)

Task: CW-20261114-0090 (mission CW-20261112-0099). Snapshot taken 2026-11-14,
read-only: no caller was changed and no write verb was sent to the live Cerberus.
Repo paths are relative to the hollis-labs checkout root unless stated.

This public document keeps what is derived from code and what the port needs as a
contract. Observations of the live operator host (which units, configs and scripts
exist there, their paths, line numbers and counts) are in the Tesseract workspace item
`01M7DS5N6RRQ0WV2GCMWNTZF5C` (`project/cerberus/workspace/substrate-port`, key `inventory-clients-full`).

This is the client list the cutover plan needs (docs/adopting-substrate.md §11,
"Clients"). Every row says where the call happens and which of the five things a
port could move would break it:

- **bin**: binary name or install path (`~/.local/bin/cerberus`, `cerberus-web`)
- **sock**: daemon socket path `~/.cerberus/cerberus.sock`
- **port**: web console listener `127.0.0.1:4783` (MCP-over-HTTP is `127.0.0.1:4785`; no caller uses it)
- **mcp**: MCP server id `cerberus` or a `cerberus_*` tool name
- **home**: the `~/.cerberus/` state directory layout or a file in it
- **route**: a socket/HTTP route or response shape
- **pkg**: the Go import path `github.com/hollis-labs/cerberus/pkg/...`

## 1. Cerberus's own surface (what callers bind to)

| Surface | Where defined | Value |
|---|---|---|
| CLI binary | `cmd/cerberus` | Installed at `~/.local/bin/cerberus` by convention (`make homebrew-install PREFIX=$HOME/.local`) |
| Other binaries | `cmd/cerberus-presence`, `cmd/cerberus-outpost`, `cmd/cerberus-docker-plugin` | presence: one-shot macOS helper; outpost: subprocess plugin run by Outpost; docker-plugin: subprocess plugin |
| Daemon socket | `internal/cerbapi/client.go:108-119` | `~/.cerberus/cerberus.sock`, from `$HOME`, no env override. Plain HTTP over unix socket, header `X-Cerberus-Api: v1`, peer uid must match (`socket_server.go:203,213-218`) |
| Socket routes | `internal/cerbapi/socket_server.go:233-274` | `/health`, `/resources[/{id}/{verb}[/plan\|/confirm\|/break-glass]]`, `/hosts...`, `/pipelines...`, `/approvals...`, `/brakes...`, `/schedules/v1/{op}`, `/data/v1/query`, `/portfolio/health` + `/v1/portfolio/health` (`cerberus.portfolio.health.v1`) |
| Web console | `cmd/cerberus/cmd_web.go:26` | `cerberus web --listen 127.0.0.1:4783`, API under `/api/*` |
| MCP stdio | `cmd/cerberus/cmd_mcp.go:21-50` | `cerberus mcp`, thin client over the socket. Tool list `internal/mcp/registry.go:19-61` (all `cerberus_*`), plus generated `cerberus_<connector>_<op>` plugin tools |
| MCP over HTTP | `cmd/cerberus/cmd_mcp_http.go:30` | `cerberus mcp-http`, `127.0.0.1:4785/mcp`, config `~/.cerberus/mcp-http.yaml` |
| Outpost protocol (outbound) | `pkg/outpost/protocol.go:99,103` | Cerberus calls `POST <outpost>/api/plugins/cerberus/execute` |
| Main DB | `internal/config/layout.go:9-25` | `~/.local/share/cerberus/workspaces/default/main.db` (`CERBERUS_DB_PATH`, `CERBERUS_WORKSPACE`, `--db`) |
| State dir | `internal/app/app.go:218-272` | `~/.cerberus/`: `config.yaml`, `registry.yaml`, `projects/*.cerberus.yaml`, `hosts.yaml`, `audit/`, `approvals/`, `brakes/`, `oauth/`, `policy/`, `plugins/`, `plugin-connectors.json`, `web/`, `apps/<project>/<resource>/`, `events-tokens/` |
| Supervised unit names | `cmd/cerberus/cmd_install_systemd.go:24-40` | daemon `com.hollis-labs.cerberus.service`; managed resources `com.hollis-labs.cerberus.<project>.<resource>.service` |
| Descriptor schema | `internal/registry/projectconfig.go:35,46` | `kind: cerberus-project/v1`, suffix `.cerberus.yaml` |

## 2. Client classes on the operator host

Callers on the operator host, by class. The individual configs, units and scripts, with
their paths, are in workspace item `01M7DS5N6RRQ0WV2GCMWNTZF5C`.

| Class | Interface | What it binds to | Breaks on | Change at cutover |
|---|---|---|---|---|
| Agent MCP configs (Claude Code, Codex) | MCP stdio | `~/.local/bin/cerberus mcp`, server id `cerberus`; Codex per-tool approval keys on `cerberus_*` names | bin, mcp | none if the binary path, server id and tool names stay |
| Agent CLI allow-rules | CLI | `cerberus register`, `cerberus resource` | bin, subcommand names | none if verbs stay |
| Agent skills (`deploy`, `service-start`, `service-stop`, `qhealth`) | MCP through the gateway | `cerberus_resource_*`, `cerberus_docker_{up,down}`, `cerberus_health` | mcp | none if tool names stay (see finding 1) |
| Daemon unit written by `cerberus install` | runs bin | `cerberus daemon --foreground`, unit `com.hollis-labs.cerberus.service` | bin, unit name | replaced by the new process's unit (plan section 7) |
| Event-export credential and its renewal | file | `~/.cerberus/events-tokens/` | home | token migrated (plan section 6) |
| Web console unit and its self-descriptor resource `cerberus-web` | runs bin, listener | `cerberus web --listen 127.0.0.1:4783` | bin, port | retired; console moves to the new process |
| Reverse proxy and sign-in helper for the public console host | HTTP, CLI | loopback `:4783`, `cerberus web open` | port | proxy target changes in the window |
| Tachyon (environment file) | socket | `CERBERUS_ADDR=unix:~/.cerberus/cerberus.sock` | sock | needs the HTTP address and an API key |
| Outpost plugin config and pairings | plugin binary, identity | plugin id `cerberus`, a staged `cerberus-outpost` binary | outpost binary, plugin id | unchanged protocol |
| Keyring-unlock helper | systemctl | restarts the daemon unit | unit name | points at the new unit |
| Units Cerberus generates and supervises | supervised | `com.hollis-labs.cerberus.<project>.<resource>.service`, `~/.cerberus/apps/<project>/<resource>/` | unit prefix, home | adopted in place; renderer parity (plan 4.2) |
| Resource descriptors outside repos (`<host>.cerberus.yaml`) | descriptor | `kind: cerberus-project/v1` | descriptor schema | unchanged |

## 3. Callers in other repos (code)

| # | Repo | path:line | Interface | Exact call | Breaks on |
|---|---|---|---|---|---|
| R1 | tether (sysop) | `apps/tether/apps/sysop/cmd/tether_sysop/main.go:5376,5381,5394` | socket | hard-coded `~/.cerberus/cerberus.sock`; `POST /resources/{id}/{action}`, `GET /resources/{id}/status` | sock, route |
| R2 | tether (sysop) | same file `:5347` | CLI fallback | `cerberus resource <action> <id>` on PATH | bin |
| R3 | tether (sysop) | same file `:5436,5475` | file + CLI | reads `~/.cerberus/registry.yaml`; `cerberus register <tmp.yaml>` | home, bin |
| R4 | tether | `apps/tether/internal/registry/bootstrap_cerberus.go:13,51,54,88` | file read/write | reads `~/.cerberus/registry.yaml` and project files (`cerberus-project/v1`), writes URNs back | home, descriptor schema |
| R5 | tether | `apps/tether/internal/registry/backfill.go:154` | path heuristic | targets containing `.cerberus/` | home |
| R6 | tether | `apps/tether/internal/launch/mcp_servers.go:35`, `internal/mcpadapter/machine_authority.go:85`, `internal/launch/resolver.go:108` | MCP server id | `MachineChangingMCPServers = {"cerberus"}` | mcp: renaming the MCP server id disables the gateway's authority gate for it |
| R7 | tether | catalog `mcp-servers/cerberus.yaml` (fixture `internal/launchresolve/testdata/catalog/mcp-servers/cerberus.yaml`; live catalog not located) | MCP launch | `/usr/local/bin/cerberus mcp` | bin, mcp |
| R8 | torque | `apps/torque/internal/config/mux_servers.go:24,44`, `internal/runtime/bootstrap/mux.go:60`, `internal/runtime/agent/deps.go:135` | MCP via mux proxy | `mux mcp --proxy --servers vanta,torque,cerberus` | mcp |
| R9 | torque | `apps/torque/apps/gui/src/console/agents/plan.ts:67,75` | UI constant | `KNOWN_TOOLS` / `SENSITIVE_TOOLS` include `cerberus` | mcp |
| R10 | tachyon | `apps/tachyon/cmd/tachyon/main.go:96-97`, `internal/cerbcontrol/cerbcontrol.go:100-233` | socket or HTTP | `CERBERUS_ADDR` (`unix:`/`tcp:`/`http(s)://`), `CERBERUS_TOKEN_FILE`; `GET /health`, `GET /resources`, `GET /resources/{id}/{status,inspect,doctor,logs}`, `POST /resources/{id}/{verb}` `{"acknowledged":true}` | route (address from its environment file) |
| R11 | tachyon | `internal/objectsync/machines.go:16,22,88`, `internal/server/register_access.go:34` | socket or HTTP | `GET /hosts` | route |
| R12 | tachyon | `internal/server/register_cerbcontrol.go:22-52` | proxies Cerberus | `/api/plugins/console/v1/cerberus/...` | route (passes through) |
| R13 | tachyon | `internal/actionrun/registry.go:65-88`, `manager.go:470`, `transport.go:125` | CLI (local or SSH) | `cerberus resource {apply,deploy,reload,remove,stop} <id> --ack`, `cerberus resource list -o json`; adds `$HOME/.local/bin` to PATH | bin, CLI flags, JSON output |
| R14 | tachyon | `internal/approvals/gate.go:63`, `producer.go:165,325`, `trusted.go:72,224`, `batch.go:155`, `workload.go:131` | inbound | special-cases `ApplicationID == "cerberus"` | Cerberus's application id |
| R15 | tangent | `internal/smoke/deployment_test.go:333-350` (opt-in, `TANGENT_SMOKE_ENV=1`) | CLI | `cerberus resource status tangent-dev`, parses the `Status` line | bin, status text |
| R16 | nanite | `cmd/nanite/plugin_cmd.go:216` | CLI | `cerberus restart <service>` | already broken: there is no top-level `restart` (only `cerberus daemon restart`) |
| R17 | nanite | `internal/service/write_claim_guard.go:83`, `internal/runtime/agent/cli_roots_policy.go:37` | tool names, path policy | classifies `cerberus_*` tools; `.cerberus` is a protected root | mcp, home |
| R18 | libs/libs (harness) | `harness/core/interception/filters/classify/rules.go:29-41` | tool-name hints | `cerberus_resource_deploy`, `cerberus_resource_reload` | mcp |
| R19 | libs/libs (plugin-sdk) | `mcp/plugin-mcp/plugin-sdk/manifest/manifest.go:68`, `validate.go:83`, TS `build/manifest.ts:19-98`, `types.ts:29` | manifest schema | `cerberus` block in plugin manifests | manifest key |

Scripts in other repos that call Cerberus (deploy and backup helpers, plugin live checks,
Outpost staging, a plugin load check, a cutover script) are rows R20 to R26 of workspace
item `01M7DS5N6RRQ0WV2GCMWNTZF5C`. They bind to the same surfaces: the binary on `PATH` or at
`~/.local/bin/cerberus`, `~/.cerberus/` files, the `cerberus-outpost` binary and plugin id,
unit names, and the MCP server id.

Data-only mentions with no runtime coupling: tether `internal/store/session_refs.go:35`
(`cerberus_deploy` ref kind), tesseract `internal/contextstore/namespace_seed.go:13-60`
(project slugs).

## 4. Go importers of `github.com/hollis-labs/cerberus/pkg/`

Command: `grep -rln "hollis-labs/cerberus/pkg" ~/dev --include=*.go --include=go.mod`
(excluding the cerberus repo, its worktrees, vendor and node_modules).

| Importer | Modules | Packages used | Pin |
|---|---|---|---|
| `plugins/plugins/cerberus-*` (module `github.com/hollis-labs/plugins/cerberus-<x>`) | 21: apt, azure, caddy, cloudflare, contextforge, digitalocean, fail2ban, forge, github, journalctl, keeper, kubernetes, namecheap, onepassword, restic, rsync, sysmon, tailscale, tmux, ufw, vercel | `pkg/plugin` (as `cerbplugin`), `pkg/connector` (as `contract`), `pkg/connector/conformance`, `pkg/resource` | `v0.5.0-beta.1.0.20261009054531-02ac61b86b4e`, no `replace` |
| `plugins/cerberus-plugins/<x>` (older repo hollis-labs/cerberus-plugins, module `github.com/hollis-labs/cerberus-plugins/<x>`) | the same 21 | same | same |

`pkg/outpost` and `pkg/secret` have no importers outside Cerberus. Two test-only
hits are not imports: `libs/libs/workflow/engine/internal/importguard/guard_test.go:43,170`
(a deny-list) and `apps/tachyon/internal/forgeapi/stub_test.go:163` (a string).
`apps/cerberus-outpost` and `apps/cerberus-wt-*` are worktrees of the cerberus repo,
not importers.

## 5. Cross-app contracts (changing any of these needs @archie's approval)

| Contract | Defined in Cerberus | Depended on by |
|---|---|---|
| Go module path `github.com/hollis-labs/cerberus/pkg/{plugin,connector,connector/conformance,resource}` and their exported symbols (`cerbplugin.PluginYAML`, `ToolNameForOperation`, `contract.Manifest`, `Operation`, `Effect*`, ...) | `pkg/` | 42 plugin modules (§4) |
| Connector manifest `cerberus.connector/v1`, `plugin.yaml`, issued credentials `cerberus.issuance.v1` | `pkg/connector/manifest.go:8`, `pkg/plugin` | plugin repos, libs plugin-sdk (R19) |
| Plugin env names `CERBERUS_*_TOKEN`, `CERBERUS_TAILSCALE_*`, `CERBERUS_KEEPER_KSM_CONFIG`, ... | `cerbplugin.EnvNames` | plugin repos, connector secrets |
| Descriptor schema `cerberus-project/v1`, `*.cerberus.yaml`, `service_name: com.hollis-labs.cerberus.*` | `internal/registry/projectconfig.go` | descriptors in most app repos (tether, torque, tachyon, tangent, loom, sigil, parallax, mentat, outpost, caster, nanite, tesseract, hadron and others), descriptors on the operator host, tether R4 |
| Registry file `~/.cerberus/registry.yaml` | `internal/registry/index.go:16,51` | tether R3, R4; a backup script |
| Daemon socket `~/.cerberus/cerberus.sock`, header `X-Cerberus-Api: v1`, routes `/health`, `/hosts`, `/resources[/{id}/{verb}]` and their JSON shapes | `internal/cerbapi` | tether sysop R1; tachyon R10-R12 |
| CLI verbs `resource {apply,deploy,reload,remove,stop,status,list} [--ack] [-o json]`, `register`, `connectors plugin exec` | `cmd/cerberus` | tether R2-R3, tachyon R13, tangent R15, scripts, agent allow-rules |
| Binary location `~/.local/bin/cerberus` (and `/usr/local/bin/cerberus` in the Tether catalog) | Makefile / install | agent MCP configs, the daemon unit, R7, R13, scripts |
| MCP server id `cerberus` | MCP configs | Tether machine-authority gate R6 (renaming the MCP server id disables the gateway's authority gate for it), torque mux R8-R9, agent MCP configs |
| MCP tool names `cerberus_*` | `internal/mcp/registry.go` | agent skills, Codex approvals, nanite R17, libs harness R18 |
| Outpost plugin protocol (`POST /api/plugins/cerberus/execute`, verbs probe/units/logs/start/stop/restart), binary `cerberus-outpost`, plugin id `cerberus` | `pkg/outpost/protocol.go` | Outpost config and staging |
| Unit naming `com.hollis-labs.cerberus.<project>.<resource>.service` and `~/.cerberus/apps/<project>/<resource>/` | `internal/connector/local` | generated units, Outpost protected units, deploy checks |
| Approval application id `cerberus` | approval producer | tachyon R14 |
| Web console loopback `127.0.0.1:4783` and its passkey relying-party id | `cerberus web` | the console unit, its self-descriptor, the reverse proxy, the sign-in helper |
| Events credential under `~/.cerberus/events-tokens/` | events export | the daemon unit's drop-in, the renewal job |

## 6. Findings to carry into the port plan

1. **The agent skills call `cerberus_*` tools through the MCP gateway, and that path was
   not confirmed.** The direct MCP config registers server `cerberus`, but the gateway's
   proxied server list does not include it, and no `cerberus_*` tools were seen through
   the gateway. Either another launch path adds them, or the deploy, service-start and
   service-stop skills resolve to nothing today. Not verified by running anything.
   Flagged for @archie.
2. **The console port and binary move as a set.** The console unit, its self-descriptor,
   the reverse proxy and the sign-in helper all name `4783` and `cerberus-web`. The new
   app cannot take 4783 while the old one holds it, so the cutover switches them together.
3. **Only one code caller hard-codes the socket path** (tether sysop R1). Tachyon reads it
   from its environment file.
4. **The descriptor schema is the widest contract.** The port keeps reading
   `cerberus-project/v1` unchanged.
5. **`~/.cerberus/` is read directly** by tether (R3-R5), nanite policy (R17), the events
   credential and scripts (backup, deploy check). Moving state off `~/.cerberus` breaks
   these, mostly without an error; a backup would just stop including the files.
6. **Two callers are already broken** and are not port regressions: nanite R16
   (`cerberus restart`) and a plugin load-check script (`TestLoadCheck` does not exist).
7. **Self-supervision.** Cerberus supervises its own web console (resource `cerberus-web`)
   and generates the daemon unit. The new app's units must not be generated by the old
   Cerberus under the same names, or `resource apply` will overwrite them.

## Method

Read-only searches, run 2026-11-14:

```
grep -rIn --exclude-dir={node_modules,.git} -e cerberus \
  <agent config dirs> <user config dir> <user bin dir> <agent-setup checkout>
systemctl --user list-units --all '*cerberus*'; systemctl --user cat <each>
grep -rln "hollis-labs/cerberus/pkg" ~/dev --include=*.go --include=go.mod
grep -rIn --exclude-dir={node_modules,vendor,.git} \
  -e 'exec.Command("cerberus"' -e 'cerberus.sock' -e 'cerberus_' -e 'CERBERUS_' \
  -e '4783' -e '4785' -e 'cerberus-project/v1' ~/dev
find ~/dev <user config dir> -name '*.cerberus.yaml' -not -path '*/node_modules/*'
```

Per-caller rows with paths and counts are in workspace item `01M7DS5N6RRQ0WV2GCMWNTZF5C`.
Docs-only and comment-only mentions are excluded.
