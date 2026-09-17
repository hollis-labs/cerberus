# Secrets handling (Cerberus's half)

Cerberus keeps credentials out of the launchd plists it generates. A resource's
`env:` / `env_file:` value may be a reference instead of a literal:

    keychain://<service>/<key>                  → login keychain, service "cerberus" (go-keyring)
    helper://<helper>/<authority>/<path>        → `<helper> resolve keychain://<authority>/<path>`

When any env value is a reference, `writePlist` fronts the service with
`cerberus run-secrets -- <program> …`. That shim resolves the references in the
service's own process and `execve`s the target, so launchd still supervises the
real process. The plist carries references; the credentials never reach disk.

- `internal/secretref` — reference parsing and resolution
- `internal/secrets` — the go-keyring `KeychainProvider` it resolves through
- `internal/connector/local/launchd.go` — the wiring
- `cmd/cerberus/cmd_run_secrets.go` — the shim

**Do not write Cerberus keychain entries with `security add-generic-password`.**
go-keyring stores a `go-keyring-base64:` prefix that the `security` CLI does not
decode. Full explanation, the portfolio audit, and the rotation runbook:

→ `~/dev/chrispian/ops/secrets-handling.md` (private — inventories the estate)
→ Tesseract knowledge: `user/chrispian/knowledge/ops`, key `ops.secrets-handling`

## Connector credentials

Connectors resolve credentials for each operation, so adding or rotating a
credential does not require restarting the daemon. Discovery itself does not
retrieve secrets. Configure references in `connector-secrets.yaml` beside the
global config (normally `~/.cerberus/connector-secrets.yaml`):

```yaml
namecheap:
  api_user: helper://cerberus-1password-helper/VAULT/ITEM/API_USER_FIELD
  api_key: helper://cerberus-1password-helper/VAULT/ITEM/API_KEY_FIELD
  username: helper://cerberus-1password-helper/VAULT/ITEM/USERNAME_FIELD
  client_ip: helper://cerberus-1password-helper/VAULT/ITEM/WHITELIST_IP_FIELD
cloudflare:
  api_token: keychain://cloudflare/api_token
```

The file accepts references only. Explicit `CERBERUS_<CONNECTOR>_<KEY>` process
environment values take precedence, then this mapping, then the existing
Cerberus keychain entry. Environment values may also be references. Updating a
parent shell's environment cannot change an already-running daemon's environment;
use the mapping file or credential store for live changes.

For 1Password, install `scripts/cerberus-1password-helper` into `~/.local/bin/`
and authenticate the `op` CLI. The adapter translates the existing helper
contract into `op read op://<vault>/<item>/<field>`; it does not copy credentials
into a second store. Use item/field IDs when names contain path separators.
Namecheap's `client_ip` must match the address allowed by the account's API
whitelist. An unresolved reference fails the operation without falling back to a
different credential or reporting success. The Cerberus web UI can still manage
unmapped keychain entries; there is no `cerberus secrets set` command.

## Plugin credentials

A plugin declares the credentials it needs in its manifest, under
`config.secrets`, and the host resolves them through the chain above. It does
not reach the store itself:

```yaml
cerberus:
  connector:
    config:
      secrets:
        - name: token
          description: ContextForge admin JWT.
          required: true
```

Cerberus looks each one up as `<plugin id>/<secret name>`, so
`CERBERUS_CONTEXTFORGE_TOKEN`, a `contextforge: token:` entry in
`connector-secrets.yaml` and `keychain://contextforge/token` all mean the same
thing they would for a built-in connector. Resolved values reach the plugin in
its SDK init config, keyed by the manifest secret name — a plugin declaring
`token` reads `params.Config["token"]`, or `plugin.SecretFromConfig` from
`pkg/plugin`.

Three properties of that channel are deliberate:

- **Credentials do not travel in the environment.** A subprocess inherits
  ambient environment, so a credential there would reach every plugin rather
  than the one that declared it. `pluginLaunchEnv()` is an allow-list with no
  credential entries and must stay that way.
- **A plugin receives only what its own manifest declares**, never the store,
  and never another connector's secret.
- **A missing credential does not fail the load.** The plugin starts, and the
  operation that needed the credential fails with `credential_missing` naming
  the variable that would supply it. Operations that do not need it keep
  working — ContextForge's `get_health` is open, and is how you tell a down
  tunnel from a down gateway.

A declared secret can also be genuinely optional, where its absence selects a
different mechanism rather than degrading. The Azure plugin declares
`client_secret` with `required: false`: supplied, it authenticates as that
service principal; absent, it authenticates as the signed-in Azure CLI user and
every operation works. What it refuses is the half-configured case — a tenant
and client id with no secret — because falling back silently there would read
the estate as an unexpected identity.

Values are resolved at load and handed over in `plugin/init`. Unlike a built-in
connector, which resolves per call, a plugin does not see a credential added or
rotated afterwards until it is reloaded:
`cerberus connectors plugin managed load <id>`. `cerberus connectors plugin
managed list` reports `missing_secrets` for a plugin that loaded without one —
credential *names*, which is why `redact.NamesOnlyKey` exempts that field from
redaction.
