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
