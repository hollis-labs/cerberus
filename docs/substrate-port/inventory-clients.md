# Cerberus clients and callers (Substrate port inventory)

Task: CW-20261114-0090 (mission CW-20261112-0099). Snapshot taken 2026-11-14,
read-only: no caller was changed and no write verb was sent to the live Cerberus.
Repo paths are relative to the hollis-labs checkout root unless stated.

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
| CLI binary | `cmd/cerberus` | Installed at `~/.local/bin/cerberus`. `~/.local/bin/cerberus-web` is a byte-identical copy (same size and mtime) used only by `cerberus-web.service` |
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

## 2. Callers on the operator machine (by role)

Described by role. Exact host paths, hostnames and ids are kept out of this public
repo; the full table is in the port's private workspace (linked from the Torque task).

| # | Caller (role) | Interface | Exact call | Breaks on |
|---|---|---|---|---|
| M1 | Claude Code MCP config | MCP stdio | `~/.local/bin/cerberus mcp`, server id `cerberus` | bin, mcp |
| M2 | Codex MCP config | MCP stdio | same command; per-tool approval keys for `cerberus_ssh_exec`, `cerberus_resource_deploy`, `cerberus_resource_reload` | bin, mcp |
| M3 | Codex exec allow-rules | CLI | `cerberus register`, `cerberus resource` | bin (PATH name), subcommand names |
| M4 | Agent skill `deploy` (agent-setup) | MCP, gateway-proxied | `cerberus_resource_{status,deploy,apply,reload,sync,ensure_fresh}` | mcp |
| M5 | Agent skill `service-start` | MCP, gateway-proxied | `cerberus_resource_{list,status,apply,deploy,ensure_fresh}`, `cerberus_docker_up` | mcp |
| M6 | Agent skill `service-stop` | MCP, gateway-proxied | `cerberus_resource_{list,status,stop,remove}`, `cerberus_docker_down` | mcp |
| M7 | Agent skill `qhealth` (not installed) | MCP, gateway-proxied | `cerberus_health` | mcp |
| M8 | Daemon user unit, written by `cerberus install` | runs bin | `~/.local/bin/cerberus daemon --foreground`, unit `com.hollis-labs.cerberus.service` | bin, unit name |
| M9 | Event-export drop-in on the daemon unit | file | events credential under `~/.cerberus/events-tokens/` | home |
| M10 | Web console user unit `cerberus-web.service` | runs bin, listener | `%h/.local/bin/cerberus-web web --listen 127.0.0.1:4783 --open=false`, `After=`/`Wants=` the daemon unit | bin, port, unit name |
| M11 | Cerberus self-descriptor (resource `cerberus-web`) | descriptor | same command, `port: 4783`, public URL | bin, port (must match M10) |
| M12 | Reverse proxy for the public console host | HTTP | public host to `127.0.0.1:4783` | port |
| M13 | Sign-in helper script | CLI | wraps `cerberus-web web open --browser=false` and rewrites the local `:4783` URL to the public host | bin, port |
| M14 | Tachyon environment file | socket | `CERBERUS_ADDR=unix:~/.cerberus/cerberus.sock` | sock |
| M15 | Outpost plugin config | plugin binary | plugin id `cerberus`, a staged `cerberus-outpost` binary; protects the Outpost unit generated by Cerberus | outpost binary/plugin id, unit name |
| M16 | Outpost pairings | identity | pairing ids and scope named for Cerberus | pairing ids |
| M17 | Keyring-unlock script | systemctl | restarts the daemon unit (overridable) | unit name |
| M18 | Events credential renewal timer | file | renews the credential under `~/.cerberus/events-tokens/` | home |
| M19 | User units Cerberus generates and supervises (34) | supervised | `cerberus resource apply` rewrites them; logs and bins under `~/.cerberus/apps/<project>/<resource>/` | unit-name prefix, home |
| M20 | Live resource descriptors outside repos (about 22) | descriptor | `kind: cerberus-project/v1`, `service_name: com.hollis-labs.cerberus.<res>` | descriptor schema, unit-name prefix |

Checked and not callers: agent hook and permission settings, crontab, a TLS
certificate-renewal timer for a Postgres resource Cerberus manages (only its own
`/etc` paths), a tachyon unit that sets `CERBERUS_ADDR` empty on purpose, and a
monitor that mails the Cerberus *agent* rather than the app.

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
| R10 | tachyon | `apps/tachyon/cmd/tachyon/main.go:96-97`, `internal/cerbcontrol/cerbcontrol.go:100-233` | socket or HTTP | `CERBERUS_ADDR` (`unix:`/`tcp:`/`http(s)://`), `CERBERUS_TOKEN_FILE`; `GET /health`, `GET /resources`, `GET /resources/{id}/{status,inspect,doctor,logs}`, `POST /resources/{id}/{verb}` `{"acknowledged":true}` | route (address comes from M14) |
| R11 | tachyon | `internal/objectsync/machines.go:16,22,88`, `internal/server/register_access.go:34` | socket or HTTP | `GET /hosts` | route |
| R12 | tachyon | `internal/server/register_cerbcontrol.go:22-52` | proxies Cerberus | `/api/plugins/console/v1/cerberus/...` | route (passes through) |
| R13 | tachyon | `internal/actionrun/registry.go:65-88`, `manager.go:470`, `transport.go:125` | CLI (local or SSH) | `cerberus resource {apply,deploy,reload,remove,stop} <id> --ack`, `cerberus resource list -o json`; adds `$HOME/.local/bin` to PATH | bin, CLI flags, JSON output |
| R14 | tachyon | `internal/approvals/gate.go:63`, `producer.go:165,325`, `trusted.go:72,224`, `batch.go:155`, `workload.go:131` | inbound | special-cases `ApplicationID == "cerberus"` | Cerberus's application id |
| R15 | tangent | `internal/smoke/deployment_test.go:333-350` (opt-in, `TANGENT_SMOKE_ENV=1`) | CLI | `cerberus resource status tangent-dev`, parses the `Status` line | bin, status text |
| R16 | nanite | `cmd/nanite/plugin_cmd.go:216` | CLI | `cerberus restart <service>` | already broken: there is no top-level `restart` (only `cerberus daemon restart`) |
| R17 | nanite | `internal/service/write_claim_guard.go:83`, `internal/runtime/agent/cli_roots_policy.go:37` | tool names, path policy | classifies `cerberus_*` tools; `.cerberus` is a protected root | mcp, home |
| R18 | libs/libs (harness) | `harness/core/interception/filters/classify/rules.go:29-41` | tool-name hints | `cerberus_resource_deploy`, `cerberus_resource_reload` | mcp |
| R19 | libs/libs (plugin-sdk) | `mcp/plugin-mcp/plugin-sdk/manifest/manifest.go:68`, `validate.go:83`, TS `build/manifest.ts:19-98`, `types.ts:29` | manifest schema | `cerberus` block in plugin manifests | manifest key |
| R20 | plugins/plugins | `plugins/cerberus-{digitalocean,tailscale,cloudflare}/scripts/live-check*.sh:41,57` | CLI | `${CERBERUS_BIN:-cerberus} connectors plugin exec ...` | bin (overridable), subcommand |
| R21 | plugins (cerberus-plugins) | `scripts/loadcheck.sh:9-26,61,85`, `Makefile:46` | builds Cerberus internals | `go test -c ./internal/pluginhost -test.run '^TestLoadCheck$'` | already broken: `TestLoadCheck` does not exist in cerberus |
| R22 | outpost | `apps/outpost/scripts/stage-machine.py:36-105`, `deploy/tools/outpost/scripts/activate-bundle.py:41`, `outpost_units.py:10-13` | plugin binary, unit drop-ins | runs `<bundle>/cerberus-outpost` as plugin `cerberus`; writes `<cerberus service_name>.service.d/lifecycle.conf` | outpost binary name, plugin id, unit name |
| R23 | infra | `compute-hosting/hub-deploy-tether.sh:36,46` | CLI over SSH | `~/.local/bin/cerberus resource reload <resource>` on a remote host | bin |
| R24 | infra | `compute-hosting/backup/hub-backup:55-68` | file read (tar) | `~/.cerberus/{config.yaml,registry.yaml,plugin-connectors.json,audit}` | home (backup silently misses files) |
| R25 | tesseract | `scripts/deploy-check.sh:63-68` | paths, labels | `~/.cerberus/apps/tesseract/tesseract-api-service/bin/...`; labels `com.hollis-labs.cerberus.hub-tesseract.service` | home (`apps/`), unit name |
| R26 | agent-os | `team/tether-cutover/cutover.sh:359` | MCP server list | edits the Claude server list (`...,cerberus,...`) | mcp |

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
| Descriptor schema `cerberus-project/v1`, `*.cerberus.yaml`, `service_name: com.hollis-labs.cerberus.*` | `internal/registry/projectconfig.go` | about 28 repo descriptors (tether, torque, tachyon x3, tangent, loom, sigil, parallax, mentat, outpost, caster, nanite, tesseract, hadron, flux, fragments-engine, frag, audio-suite, video-suite, sysop-ui, infra, sites, llama-local, station-usage), about 22 live ones (M20), tether R4 |
| Registry file `~/.cerberus/registry.yaml` | `internal/registry/index.go:16,51` | tether R3, R4; infra R24 |
| Daemon socket `~/.cerberus/cerberus.sock`, header `X-Cerberus-Api: v1`, routes `/health`, `/hosts`, `/resources[/{id}/{verb}]` and their JSON shapes | `internal/cerbapi` | tether sysop R1; tachyon R10-R12 (via M14) |
| CLI verbs `resource {apply,deploy,reload,remove,stop,status,list} [--ack] [-o json]`, `register`, `connectors plugin exec` | `cmd/cerberus` | tether R2-R3, tachyon R13, tangent R15, plugins R20, infra R23, Codex rules M3 |
| Binary location `~/.local/bin/cerberus` (and `/usr/local/bin/cerberus` in the Tether catalog) | Makefile / install | M1, M2, M8, R7, R13, R23 |
| MCP server id `cerberus` | MCP configs | Tether machine-authority gate R6, torque mux R8-R9, agent-os R26, M1, M2 |
| MCP tool names `cerberus_*` | `internal/mcp/registry.go` | skills M4-M7, Codex approvals M2, nanite R17, libs harness R18 |
| Outpost plugin protocol (`POST /api/plugins/cerberus/execute`, verbs probe/units/logs/start/stop/restart) and binary `cerberus-outpost`, plugin id `cerberus` | `pkg/outpost/protocol.go` | outpost M15, R22 |
| Unit naming `com.hollis-labs.cerberus.<project>.<resource>.service` and `~/.cerberus/apps/<project>/<resource>/` | `internal/connector/local` | M19, R22, R25, outpost `protected_units` (M15) |
| Approval application id `cerberus` | approval producer | tachyon R14 |
| Web console origin (public host → `127.0.0.1:4783`) and its passkey relying-party id | `cerberus web` | M10, M11, M12, M13 |
| Events credential under `~/.cerberus/events-tokens/` | events export | M9, M18 |

## 6. Findings to carry into the port plan

1. **The agent skills call `cerberus_*` tools through the MCP gateway, and that path was
   not confirmed.** The direct MCP config registers server `cerberus`, but the gateway's
   proxied server list does not include it, and this session saw no `cerberus_*` tools
   through the gateway. Either another launch path adds them, or the deploy,
   service-start and service-stop skills resolve to nothing today. Not verified by
   running anything. Flagged for @archie.
2. **The port and binary for the web console move as a set.** M10, M11, M12, M13 all
   hard-code `4783` and `cerberus-web`. The new app cannot take 4783 while the old one
   holds it, so the cutover needs one coordinated switch of these four.
3. **Only one caller hard-codes the socket path** (tether sysop R1); tachyon takes it from
   its environment file (M14). A new socket path is two edits.
4. **The descriptor schema is the widest contract** (about 50 files across repos and the
   operator machine). The port should keep reading `cerberus-project/v1` unchanged.
5. **`~/.cerberus/` is read directly** by tether (R3-R5), infra backup (R24), tesseract
   (R25), nanite policy (R17) and the events token (M9, M18). Moving state off
   `~/.cerberus` breaks these without an error in most cases; the hub backup would just
   stop including the files.
6. **Two callers are already broken** and are not port regressions: nanite R16
   (`cerberus restart`) and cerberus-plugins R21 (`TestLoadCheck`).
7. **Self-supervision:** Cerberus supervises its own web console (M11, resource
   `cerberus-web`) and generates the daemon unit (M8). The new app's units must not be
   generated by the old Cerberus under the same names, or `resource apply` will
   overwrite them.

## Method

Read-only searches, run 2026-11-14:

```
grep -rIn --exclude-dir={node_modules,.git} -e cerberus \
  <agent config dirs> <user config dir> <user bin dir> <agent-setup checkout>
  # skipped: transcripts, history, backups, *.bak, *.pre-*
systemctl --user list-units --all '*cerberus*'; systemctl --user cat <each>
grep -rl cerberus /etc/systemd/system; crontab -l
grep -rln "hollis-labs/cerberus/pkg" ~/dev --include=*.go --include=go.mod
grep -rIn --exclude-dir={node_modules,vendor,.git} \
  -e 'exec.Command("cerberus"' -e 'cerberus.sock' -e 'cerberus_' -e 'CERBERUS_' \
  -e '4783' -e '4785' -e 'cerberus-project/v1' ~/dev
find ~/dev <user config dir> -name '*.cerberus.yaml' -not -path '*/node_modules/*'
```

Counts: 20 machine callers (M1-M20) and 26 repo callers (R1-R26), so 46 rows. M19 and
M20 group generated units and live descriptors. Add the 42 plugin modules importing
`pkg/`. Docs-only and comment-only mentions are excluded: about 240 doc files across 25
repos, plus backups and old binaries.
