---
id: "CERB-CAP-504"
class: "capability"
name: "Secret references and the credential chain"
summary: "Lets a resource or connector name a credential rather than carry one, resolving keychain:// and helper:// references in the consuming process at the moment it needs them."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.8
confidence_label: "code and tests read in full and the per-call rotation property confirmed from the factory wiring, but no reference is in use on this machine so nothing was resolved live"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/secretref/, internal/secrets/, cmd/cerberus/cmd_run_secrets.go"
tags:
  - "area5"
  - "cerberus"
  - "class:capability"
  - "credentials"
  - "keychain"
  - "secretref"
  - "secrets"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-302"
    note: "plugins resolve through this same chain but are handed values once at load"
  - type: "relates_to"
    target: "CERB-CAP-700"
    note: "resolved values never enter an error; only the reference does"
  - type: "blocks"
    target: "CERB-GAP-532"
    note: "the parser exists and nothing calls it at author time"
  - type: "blocks"
    target: "CERB-GAP-540"
    note: "there is no CLI write surface for a credential"
---

# Secret references and the credential chain

A Cerberus resource names a credential; it does not carry one. The reason is
concrete: `writePlist` renders a managed service's environment into
`~/Library/LaunchAgents/*.plist`, so a literal credential in a project file
becomes a second plaintext copy in a file operators and agents read routinely.

Two reference schemes are understood, parsed by `internal/secretref`:

    keychain://<service>/<key>              the Cerberus login-keychain entry
    helper://<helper>/<authority>/<path>    `<helper> resolve keychain://<authority>/<path>`

The helper scheme exists so a secret shared with another Hollis Labs app does
not need a second copy in a second namespace — Tether's key is reachable as
`helper://mux-apikey-helper/openai/work`, resolving the same single entry
Tether's own resolver reads.

There are two resolution paths and they are not the same.

**Service environment, at exec time.** When any value in a resource's `env:` is
a reference, `launchd.go` fronts the program with `cerberus run-secrets --
<program> …`. The shim resolves every reference in its own environment,
bounded at 30 seconds so a blocked keychain ACL prompt fails loudly rather than
hanging in launchd's spawn state, then `syscall.Exec`s the target so launchd
supervises the real process — PID, exit status and `KeepAlive` all behave as
they would without it. The plist carries references; the credential exists only
in the service's own process.

**Connector credentials, per call.** `app.ConnectorSecrets` builds a
`ReferenceProvider` over the go-keyring `KeychainProvider`, reading
`connector-secrets.yaml` beside the global config. Precedence per lookup:
explicit `CERBERUS_<CONNECTOR>_<KEY>` process environment, then the mapping
file, then the keychain entry. The mapping file accepts references only and
refuses a literal by name. Every built-in connector factory resolves through
this provider per operation, so **adding or rotating a credential needs no
daemon restart** — and `ReferenceProvider.Get` re-reads the mapping file on
every call, so a mapping change is live too. What cannot be changed live is the
daemon's own process environment: a `CERBERUS_*` value exported in a shell after
the daemon started is invisible to it. Plugins differ — they are handed their
declared secrets once at load and need an explicit reload.

Four failure modes are handled explicitly and each one was a real bug:
a missing keychain entry returns `("", nil)` from the provider but is an error
for a reference, because handing a service a blank credential fails far from
its cause; go-keyring's `go-keyring-base64:` marker is decoded, because an
older helper reading through the `security` CLI returns it verbatim and it has
the shape of a credential; `sanitizedEnviron` strips reference-valued variables
before running a helper, because a helper that consults a conventional env var
first would otherwise echo the reference straight back as the credential; and
`Resolve` refuses a value that is itself a reference as a backstop on any other
path to that outcome. Resolved values never appear in an error — only the
reference, which is non-sensitive by construction.

Writes are one-directional. `KeychainProvider` has `Set` and `Delete`, but
there is no `cerberus secrets` CLI; the only write surface is the web console,
which constructs a bare `NewKeychainProvider` and therefore does not see
`connector-secrets.yaml` at all.

Verified on this machine 2026-09-17: `~/.cerberus/connector-secrets.yaml` does
not exist, and no resource in `~/.cerberus/config.yaml` uses a reference in its
`env:`. The reference lane is entirely `verified: test` here.
