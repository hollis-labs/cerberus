# Secrets handling (Cerberus's half)

Cerberus keeps credentials out of the launchd plists it generates. A resource's
`env:` / `env_file:` value may be a reference instead of a literal:

    keyring://<service>/<key>                   → the OS credential store, service "cerberus" (go-keyring)
    keychain://<service>/<key>                  → the same; keychain:// is the original, macOS-flavoured name
    helper://<helper>/<authority>/<path>        → `<helper> resolve keychain://<authority>/<path>`
    op://<vault>/<item>/<field>                 → 1Password, through the onepassword secret-backend plugin
    keeper://<record>/<selector>/<name>         → Keeper, through the keeper secret-backend plugin

The OS credential store is the macOS Keychain, the Windows Credential Manager or
the Linux Secret Service. `keyring://` and `keychain://` name the same entry and
behave identically; `keychain://` is kept for good.

**When the store is not there.** On a headless Linux machine, or in a systemd
user unit, no Secret Service answers on the D-Bus session bus. A locked
keychain can also refuse, and a Windows process that is not the signed-in user
cannot reach that user's Credential Manager. Any of these is
`credential_missing` naming what makes the store available, never an empty
value that looks like a missing entry. A credential in its
`CERBERUS_<CONNECTOR>_<KEY>` environment variable still works without a store.
The headless-Linux plan (systemd credentials, never plaintext) is CERB-GAP-904.

**Size limits.** The store refuses a large value, and Cerberus refuses it first,
naming the limit:

| Platform | Limit |
|---|---|
| Windows | 2560 bytes |
| macOS | about 3000 bytes |
| Linux | 100 KiB, as a sanity bound |

A Keeper configuration or a 1Password service account token is about 1 KB.

**Tests never touch the real store.** A test binary that reaches it without
calling `secrets.MockStoreForTests()` from `TestMain` is refused.

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

→ `~/dev/operator/ops/secrets-handling.md` (private — inventories the estate)
→ app-c knowledge: `user/operator/knowledge/ops`, key `ops.secrets-handling`

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
unmapped keychain entries for the providers it lists. `cerberus secrets set
<service>/<key>` stores any entry from an interactive terminal (see "Setting up
a secret backend" below).

## Vault references and secret-backend plugins

`op://` and `keeper://` name a secret in a vault. They are resolved by a
**secret-backend plugin**: an installed plugin whose `plugin.yaml` claims the
scheme (`cerberus.secret_backend.scheme`). The daemon routes each reference to
the loaded plugin that claims its scheme and asks it for the value, over the
plugin protocol's `command/execute`. That request is not a connector
operation, so no CLI command, API operation or MCP tool reaches it.

- **One claimant per scheme.** A second plugin claiming the same scheme is not
  registered. `keychain`, `keyring`, `helper`, `env`, `file`, `http` and
  `https` are reserved.
- **The install review says so.** A backend's review opens with `SECRET
  BACKEND: this plugin will see every secret resolved through op://`, and a
  change of scheme is an upgrade diff line.
- **A backend's own credential comes from the core chain only**: the
  environment, `connector-secrets.yaml` and the OS credential store. It never
  comes from another vault. A backend whose credential is mapped to a vault
  reference fails to get it (`credential_missing: a secret backend's own
  credential must come from the OS credential store ...`). So no backend depends
  on another, and none can unlock itself.
- **Backends load first.** At daemon start they restore before every other
  plugin. A resolve that arrives while its backend is still loading waits for
  it, within the request's deadline and the host's load deadline.
- **Every failure is `credential_missing` with the recovery named**, and there
  is no fallback to another credential. The cases are:
  - no plugin claims the scheme (install one);
  - the backend is not loaded (`cerberus connectors plugin managed load <id>`);
  - it is still loading;
  - the vault refused or could not be reached (the backend's own reason).
- **Redaction.** A resolved value is registered with the request's redaction
  scope like any other credential. It also joins the backend's own redactor,
  so the backend's later stderr, errors and status lose it.
- **A vault reference is never a literal.** `op://` and `keeper://` are
  references whether or not a backend is installed. Before, an `op://` value
  in the environment was handed to a connector as its token. Now, without a
  backend, it fails.

Where it works:

- **Connectors and plugins resolved by the daemon**: the daemon routes to the
  loaded backend.
- **A managed service's environment** (`cerberus run-secrets`): the shim reads
  the installed plugins, loads only the backend a reference needs, in the
  service's own process, and stops it before it execs the service. It does not
  ask the daemon, so a service starts while the daemon is down, and there is no
  socket call that returns a value. It refuses a backend whose install review
  is pending, and it records the backend's load in the audit log
  (`via: run_secrets`). A 1Password backend's first resolve in a process
  compiles its SDK's WASM core, about two seconds, so a service that references
  `op://` starts that much slower.
- **A CLI process with no daemon**: the reference fails, naming `cerberus
  daemon start`.

**Where each credential came from** is on every gated call's audit outcome, as
`credential_sources`. It is names and sources only, never a value or a
reference's path:

```json
"credential_sources": {
  "cloudflare/api_token": "mapping:op via onepassword@0.1.0",
  "github/token": "keyring",
  "namecheap/client_ip": "missing"
}
```

A source is where the value or reference was found (`env`, `mapping`,
`keyring`, or `binding:<label>` for a per-access binding such as
`binding:targets[0].write`), then the reference's scheme, then the backend
plugin for a vault. `, unresolved` marks a lookup that failed, and `, none` a
binding that sets the credential to null.

### Setting up a secret backend

Both backends live in `hollis-labs/cerberus-plugins`. Run the commands below
from a checkout of it, in your own terminal: installing a plugin and storing a
secret are both interactive, and both refuse a script or an agent.

**Keeper (`keeper://`)**

1. Build the plugin: `make -C keeper dist`. This writes `dist/keeper`.
2. Install it: `cerberus connectors plugin managed install "$PWD/dist/keeper"`.
   The review opens with `SECRET BACKEND: this plugin will see every secret
   resolved through keeper://`. Type `keeper` to accept.
3. In Keeper, create a Secrets Manager application. Share the records Cerberus
   may read with it, read-only. Create a one-time access token for it.
4. Bind the token **outside Cerberus**, with Keeper's own CLI:
   `ksm init default <one-time token>`. It prints the application's
   configuration, base64. Binding consumes the token. The plugin never binds
   one, and it refuses an unbound configuration.
5. Store the configuration: `cerberus secrets set keeper/ksm_config`, and
   paste it.
6. Load the plugin: `cerberus connectors plugin managed load keeper`. If it was
   already loaded, unload it first, because a plugin reads its credential when
   it loads.
7. Check it: `cerberus connectors exec keeper status` should say
   `configured: true` and name the Keeper region. It makes no network call.
8. Name secrets by reference, for example in `connector-secrets.yaml`:
   ```yaml
   cloudflare:
     api_token: keeper://<record uid>/field/password
   ```
   Prefer a record UID to a title. A title makes Keeper return every record
   shared with the application.

**1Password (`op://`)**

1. Build the plugin: `make -C onepassword dist`. This writes
   `dist/onepassword`.
2. Install it: `cerberus connectors plugin managed install
   "$PWD/dist/onepassword"`, and type `onepassword` to accept the review.
3. In 1Password, create a service account with **read** access to only the
   vaults Cerberus may read, and copy its token (`ops_...`).
4. Store the token: `cerberus secrets set onepassword/service_account_token`,
   and paste it.
5. Load the plugin: `cerberus connectors plugin managed load onepassword`.
6. Check it: `cerberus connectors exec onepassword status` should say
   `configured: true` and name the sign-in address. It makes no network call.
   The token must name a production 1Password domain (`1password.com`, `.ca`
   or `.eu`).
7. Name secrets by reference, for example `op://<vault>/<item>/<field>` in
   `connector-secrets.yaml` or in a resource's `env:`.

Each plugin's `scripts/live-check.sh` resolves one reference you name, twice,
against the real vault. It prints the value's length and never the value. The
1Password check also makes 1Password unreachable after a real resolve and
requires the next one to fail, which shows the SDK does not answer from memory.

The `onepassword` and `keeper` plugins live in `cerberus-plugins`; each README
covers its bootstrap credential and what it pins in its vendor's SDK.

## Read and write bindings

A connector's entry can bind a credential separately for reads and for
writes, and per target. Then a policy bug meets a credential that can't
write:

```yaml
cloudflare:
  api_token: keychain://cloudflare/api_token        # as before: used for both, when nothing below binds it
  read:  { api_token: keychain://cloudflare/api_token_ro }
  write: { api_token: keychain://cloudflare/api_token_rw }
  targets:
    - match: { env: prod }                          # the same target matching policy uses
      read:  { api_token: op://Prod/cf-readonly/token }
      write: { api_token: null }                    # no write credential for prod at all
```

**Which binding a call uses:**

- The operation's effect decides. `read` and `read_sensitive` use the read
  binding. Everything else uses the write binding, including anything with
  no declared effect.
- A dry run or a plan request uses the read binding, because a preview must
  not need to write.

**Which level applies:** Cerberus uses the first `targets` entry that binds
the key, then the connector level, then the chain from before (the
environment, the flat key, the keychain). The most specific level that
mentions a key decides it, for both accesses. That's how `write: null` on
prod refuses a prod write even though the connector level has a write
credential. There is never a fallback from one access to the other.

**The rules:**

- **`null` means none.** A call with no credential for its access is refused
  as `credential_missing` before anything runs, and the refusal names the
  binding.
- **Bind both halves.** If a level binds a key for read, it must bind it (or
  `null` it) for write too, and the other way round.
- **References only**, as everywhere in this file. `read`, `write` and
  `targets` are reserved names.
- An environment value can't stand in for a key that has a per-access
  binding.

`policy explain <connector.op>` has a `Credentials:` line saying which binding
each credential would use, and when the call would be refused for having
none. The audit log records each credential as `connector/key@binding`, for
example `cloudflare/api_token@targets[0].read`. It records names only, never
values.

**Plugins.** A plugin receives its credentials once, when it loads. So when
its credentials are bound per access, Cerberus runs two copies of it:

- **The plugin as loaded holds only its read credentials** and serves every
  read.
- **A write copy**, started on the first write, holds only the write
  credentials and serves the writes. It stops after 15 idle minutes.

A read operation can't write, whatever the plugin does, because its process
never received a write credential. Which copy serves a call follows the
effect the plugin's manifest declares, which you reviewed at install. Both
copies run under the same deadlines and limits, and both stop with the
plugin. Per-target bindings aren't supported for plugins yet. A plugin whose
entry has `targets:` is refused at load, not quietly given the connector-level
credentials.

**Cerberus can't prove a read credential can't write.** That depends entirely
on how you scope the token with its provider. Use the provider's scoped
tokens: a Cloudflare token with only `Zone:Read`, a DigitalOcean token with
read scope, or a GitHub fine-grained token with read-only contents. The
bindings make sure the write token is never *handed* to a read. The
provider is what makes the read token unable to write.

**Older binaries** read this file as a flat `connector: key: reference` map.
A file that uses `read:`, `write:` or `targets:` makes an older binary refuse
the whole file, so every connector credential fails rather than a write
credential being used by mistake. Update the daemon before you add bindings.

## Plugin capabilities

A credential is not the only thing a plugin can be handed. An SSH agent socket
or a Docker endpoint is a *handle* — not credential-shaped, and just as
powerful. Those are declared, not ambient:

```yaml
capabilities:
  - name: ssh_agent
    reason: reaches remote hosts on the operator's behalf
```

`internal/pluginhost/capability.go` owns the vocabulary this host understands —
`ssh_agent` (unlocks `SSH_AUTH_SOCK`) and `docker_socket` (unlocks the
`DOCKER_*` set). A plugin that declares nothing receives neither. An unknown
name is refused at install, and `cerberus connectors plugin managed list`
reports what each plugin declared and what it was granted.

Granting is currently on the strength of the manifest: a plugin that declares a
capability receives it. That makes the request reviewable before the plugin
runs and visible afterwards; it is not yet an operator approval of the specific
grant.

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

A declared secret is a credential unless it says otherwise. Some values are
kept beside credentials and read through the same chain but are not secret: a
key file's path, a team slug, an account user name, an allow-listed IP. Declare
those with `kind`:

```yaml
      secrets:
        - name: api_key
          required: true
        - name: username
          kind: name       # an account name the operation's output shows
        - name: key_file
          kind: path       # a path to a credential file, not the credential
```

`kind` is `credential` (the default), `path` or `name`, and anything else is
refused at install. The host value-redacts every credential it resolved for a
plugin from all text that plugin produces: errors, stderr, telemetry, and, merged
into each operation's request scope, its successful results on every surface.
It leaves a `path` or a `name` alone, because redacting one cuts it out of the
very message that has to show it. Built-in connectors declare the same field;
ssh's per-resource key is `kind: path`.

Values are resolved at load and handed over in `plugin/init`. Unlike a built-in
connector, which resolves per call, a plugin does not see a credential added or
rotated afterwards until it is reloaded:
`cerberus connectors plugin managed load <id>`. `cerberus connectors plugin
managed list` reports `missing_secrets` for a plugin that loaded without one —
credential *names*, which is why `redact.NamesOnlyKey` exempts that field from
redaction.

## Plugin settings

A plugin's non-secret settings, meaning the config fields its manifest declares,
come from `connector-config.yaml` beside the global config (normally
`~/.cerberus/connector-config.yaml`). The same file decides which of a plugin's
operations are served as MCP tools:

```yaml
contextforge:
  fields:
    address: http://127.0.0.1:14444
  mcp:
    expose: [get_health, list_gateways]   # default: nothing
namecheap:
  fields:
    sandbox: true
```

Some of these fields choose the system a plugin acts on: a gateway address,
an API server, a kubeconfig. That is why they come from a file the operator
edits and never from a caller. **Cerberus reads this file and never writes
it**: no socket, web or MCP path changes it.

The rules:

- **Only declared fields and declared operations.** Each field value is
  checked against its declared type. An undeclared field, a wrong type, or an
  unknown operation in `mcp.expose` refuses the plugin's load. The problem is
  named in `cerberus connectors plugin managed list` under `config_problems`
  rather than dropped. A dropped field meant to choose a target would leave
  the plugin acting on its default, which is the worse failure.
- **Literal values only.** A `keychain://`, `helper://` or `op://` reference is
  refused. Credentials belong in `connector-secrets.yaml`.
- **Read at load.** An edit takes effect when the plugin is reloaded
  (`cerberus connectors plugin managed load <id>`). `managed list` reports the
  delivered field names (`config_fields`), the exposed operations
  (`mcp_expose`) and the file's fingerprint (`config_sha256`), never the
  values.
- **MCP exposure is default-deny.** A listed operation is served as the MCP
  tool `cerberus_<plugin id>_<operation>`, with its input schema from the
  manifest and its hints from its contract. `cerberus mcp`, `mcp-http` and the
  daemon's stdio server refresh their generated tools every 15 seconds, and
  tell a subscribed client with `notifications/tools/list_changed`. So after
  an edit and a `managed load`, the tool appears within about 15 seconds,
  with no restart. A name that would shadow a built-in tool is refused and
  logged.
- **Not a secret, but a steering wheel.** The file does not have to be 0600,
  but Cerberus warns if it is group- or world-writable.

Values reach the plugin in the same Init config map as its secrets, as the
strings the SDK's `ConfigReader` parses: booleans as `true`/`false`, integers
as digits, and lists and mappings as JSON. The manifest refuses a field and a
secret that share a name, so neither can shadow the other.
