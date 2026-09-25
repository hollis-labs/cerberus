---
id: "CERB-CAP-302"
class: "capability"
name: "Plugin secret channel"
summary: "Resolves exactly the credentials a plugin's manifest declares and hands them over the plugin/init config map, keyed by secret name, never through the environment."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.95
confidence_label: "Allow-list read directly; missing-credential path and names-only reporting both verified live against contextforge"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/pluginhost/secrets.go"
tags:
  - "plugin"
  - "secrets"
  - "credentials"
  - "security-boundary"
  - "cerberus"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-DEC-351"
    note: "secrets travel in the Init config channel, never the environment"
  - type: "implements"
    target: "CERB-DEC-352"
    note: "a missing credential fails an operation, not a load"
  - type: "implements"
    target: "CERB-DEC-359"
    note: "resolve-at-load rather than resolve-per-call"
  - type: "relates_to"
    target: "CERB-CAP-504"
    note: "same provider chain as the built-in connectors"
  - type: "blocks"
    target: "CERB-GAP-331"
    note: "the recovery instruction for a missing credential is a no-op"
---

# Plugin secret channel

A plugin declares its credentials; the host resolves them. That single sentence
is the whole design, and every property worth having follows from refusing to
move it.

The manifest's `config.secrets` is a list of `{name, description, env,
required}`. At load, `resolvePluginSecrets` walks exactly that list and looks
each entry up as `<plugin id>/<secret name>` through the same `SecretResolver`
the built-in connectors use — so `CERBERUS_<ID>_<NAME>`, a `<id>: <name>:` entry
in `~/.cerberus/connector-secrets.yaml`, and `keychain://<id>/<name>` mean the
same thing either side of the plugin boundary. Resolved values go into the
`plugin/init` config map keyed by the manifest secret name; a plugin declaring
`token` reads `config["token"]`, via `plugin.SecretFromConfig` or the SDK's
`ConfigReader`. The interface the host exposes to the plugin lane is read-only
by construction: `SecretResolver` has a `Get` and nothing else, deliberately
narrower than `secret.Provider`, because nothing in the plugin lane should be
able to write to or delete from the operator's store.

Two negatives are load-bearing. **A plugin never receives a secret it did not
declare**, because the loop iterates the plugin's own manifest and nothing else
— it is never handed the store, and never another connector's credential.
**Credentials never travel in the environment.** `pluginLaunchEnv()` in
`internal/cerbapi/plugin_connector_service.go` is a fourteen-entry allow-list —
`PATH`, `HOME`, `TMPDIR`, `TMP`, `TEMP`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`,
`DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CONFIG`, `XDG_RUNTIME_DIR`,
`SSH_AUTH_SOCK`, plus a `GO_WANT_` prefix for test re-exec — and the subprocess
gets `cmd.Env` built from that list alone, never `os.Environ()`. No credential
appears in it, and none may be added: environment is ambient, so a credential
there would reach *every* plugin rather than the one that asked. Both hosts use
the same function. The one thing in that list worth naming is `SSH_AUTH_SOCK`:
it is not a credential, but it is a handle to the operator's ssh-agent, so a
plugin can authenticate as the operator over SSH without ever being handed a
key.

A missing credential is not fatal. `resolvePluginSecrets` returns the values it
found, the names of the required ones it did not, and redacted diagnostics for
lookups that errored — and the load continues. `managed list` then reports
`missing_secrets` (names only; `redact.NamesOnlyKey` exempts that field, because
`SensitiveKey` matches it on the very word that makes it useful), the load
warning names `credential_missing` on stderr, and the first operation that
actually needed the credential fails as `credential_missing` with the recovery
spelled out. That is verified live here: `contextforge` is `loaded: true` with
`missing_secrets: ["token"]`; `get_health` returns `{"ok": true, "status":
"healthy"}` because `/health` is open; `list_gateways`, `list_virtual_servers`
and `list_tools` all 401 and say which credential and which variable. Keeping
`get_health` working while the authenticated calls fail is not a nicety — it is
how you tell a down tunnel from a down gateway.

The channel resolves once, at load, not per call. That is the one sharp edge.
`MissingSecretsError` tells the operator to supply the credential "then reload
it with `cerberus connectors plugin managed load <id>`" — and
`Manager.Load` returns `nil` immediately when the plugin is already in
`m.running`. Verified: running that command against the already-loaded
`contextforge` returns `loaded: true, missing_secrets: ["token"]` and changes
nothing. The instruction succeeds and does nothing; the working recovery is
`unload` then `load`, which `docs/plans/connector-work-packages.md` records and
neither the error text nor `docs/secrets.md` does.

## Since WP-S2

A declared secret carries a `kind`: `credential` (the default), `path` or
`name`. An unknown kind is refused by `Manifest.Validate`. The plugin redactor
is built from credentials only. It uses `redact.Forms`, the expansion the
request scope uses, with the same 8-byte floor, so a team slug or a key file's
path a plugin keeps beside its token stays in the output that names it.

Resolution at load is detached from the loading request's scope, because the
values belong to the plugin for its load lifetime. Each operation merges the
plugin's credentials into its own request scope at `CallTool`, so a successful
result that echoes a credential is removed on every surface that renders it.
The plugin redactor, which only runs over failures, never saw that case. The
plugin's stderr is forwarded to the daemon's stderr as whole lines through the
redactor, not as raw bytes.
