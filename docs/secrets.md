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
