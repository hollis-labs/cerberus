---
id: "CERB-CAP-700"
class: "capability"
name: "Credential redaction"
summary: "Rewrites anything that parses as a credential out of every operator-facing string leaving the daemon, on the error path of all five surfaces."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.95
confidence_label: "redact.go and the P0 refusal tests re-read on main after P0 (#48 to #54); earlier findings as recorded 2026-09-18"
last_reviewed: "2026-09-25"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/redact/redact.go"
tags:
  - "cerberus"
  - "class:capability"
  - "redaction"
  - "security"
  - "cross-cutting"
  - "errors"
  - "diagnostics"
  - "locus:core"
relationships:
  - type: "relates_to"
    target: "CERB-CAP-200"
    note: "the admin lane formats its error code into the string the redactor then rewrites"
  - type: "relates_to"
    target: "CERB-CAP-301"
    note: "plugin manifest descriptions pass through it on the way to every surface"
  - type: "relates_to"
    target: "CERB-CAP-404"
    note: "it sits on the error path of all five surfaces, so a defect here is a defect everywhere"
  - type: "relates_to"
    target: "CERB-CAP-504"
    note: "redaction is the last line of defence for the credential chain, not part of it"
  - type: "relates_to"
    target: "CERB-GAP-273"
    note: "live defect in the assignment rule"
  - type: "relates_to"
    target: "CERB-GAP-447"
    note: "live defect in the flag rule"
  - type: "relates_to"
    target: "CERB-GAP-742"
    note: "live defect in the assignment rule's one-word value capture"
  - type: "relates_to"
    target: "CERB-GAP-743"
    note: "live defect in the assignment rule's one-word value capture"
---

# Credential redaction

`internal/redact` is the last thing that touches operator-facing text. Every
error, every DTO field and every log line that leaves the daemon for a human or
an agent passes through `redact.Text`, which rewrites anything that parses as a
credential. It is a single pass with no context: it sees a string, not a
sentence, and it cannot tell guidance from a secret.

Three rules do the work. `assignment` matches a key containing `token`,
`secret`, `password`, `credentials?`, `authorization` and friends followed by
`=` or `:`, and replaces what comes next. `flag` does the same for `--flag value`
and `--flag=value`. A third pass removes known credential values that appear
with no label at all. Two escape hatches exist: a short, purely alphabetic word
after `Bearer` is left alone, and `NamesOnlyKey` exempts fields whose values are
credential *names* rather than values — though that second hatch has one call
site and it is inside the JSON walk, so `missing_secrets` survives as
`["token"]` in a DTO and is still eaten in prose (CERB-GAP-743).

Both escape hatches were added after the redactor ate its own guidance. That has
now happened nine times, and `AGENTS.md` records the rule the repository settled
on: **do not run redaction over a value that is a name by construction**, and if
an error carries a recovery instruction, add a test that it survives
`redact.Text` intact — because a safety net that eats the instruction is worse
than no instruction.

## Why this record exists

Nothing in the first audit pass owned this system. Six gap records across four
independent areas pointed at redaction, and two of them carried an unresolved
`TBD` reference to "the redaction capability, wherever it is catalogued". It was
catalogued nowhere. Redaction is on every error path of every surface, so it
belongs to no single area's territory and each area recorded only the damage it
could see from where it stood.

That is a property of the fan-out, not of the code, and it is the clearest
illustration in this catalog of what a per-area audit is structurally blind to:
a cross-cutting system is invisible to every area that crosses it.

## State, and why it is `partial`

The two defects this record was opened with — `assignment` eating the word after
the error code `credential_missing:` (CERB-GAP-273) and `flag` eating the word
after `X-API-Key` (CERB-GAP-447) — shipped in #34, and both now hold when
re-run against `redact.Text`. Three more were found on 2026-09-17, and they
share the shape those fixes did not address: `assignment` captures a single
whitespace-delimited word and calls it the value.

- `Authorization: Basic <base64>` comes out as `Authorization: [REDACTED]
  <base64>` — the scheme redacted, the credential not, because `bearer` is the
  only scheme-aware rule and nothing fires on `Basic` (CERB-GAP-742). This is
  the first finding in this family that leaks a credential rather than
  corrupting a sentence, and it changes what this record is about: the failure
  mode is no longer only that the operator loses the instruction.
- The names-only exemption never runs on prose, so the `missing_secrets` field
  the exemption exists for is still redacted whenever it is rendered into an
  error message instead of a DTO (CERB-GAP-743).

A component whose failure mode is silently rewriting the operator's recovery
instructions is not `shipped`, and one that can pass a credential through is
further from it than when this record was written. Eight narrow fixes have each
been correct and none has been structural (the latest, 2026-09-25, stopped
`assignment` eating the word after a Go type-name prefix such as azidentity's
`AzureCLICredential: ERROR:`); `AGENTS.md` already names the answer
— redact at the value boundary, where the caller still holds the key and the
value as separate things, and keep `Text` as a last-resort net over text
Cerberus did not compose. What is still missing is the test discipline that
would have caught any of them (CERB-GAP-274).

**The eighth fix has a known residual.** A letters-only value of 19 characters
or fewer after a multi-word UpperCamel key now passes `redact.Text`:
`ClientSecret: someplainpassword` comes through intact, because the rule can
no longer tell it from `AzureCLICredential: failed`. The per-plugin value
redactor still removes it whenever the value was one the host resolved, and
nothing removes it when it was not. That is the argument for the structural
fix in one line: the value boundary knows which word is the secret, and a
regex over prose never will.

**P0 held the line without a rule change.** The P0 work (PRs #48 to #52) added
four refusal families: the non-loopback `--listen` refusal, the retired
`plugin_dir` routes, the SSH connection-field refusal and the docker ad-hoc
target refusal. Each has a test that it survives `redact.Text`, and `key_file`
and `known_hosts_file` values were checked against `redact.Marshal`.
`preview_unsupported` joined the error-code vocabulary before it could lose the
word after it, and `TestGateRefusalsSurviveRedaction` runs every error code,
followed by a recovery sentence, through `redact.Text`. That is the discipline
this record asks for, applied by hand. It is not yet a gate (CERB-GAP-274).

## Since P1-5

`plugin_changed` joined the error-code vocabulary and `redact.errorCodes` in the
same change that introduced it. Its recovery sentence, which names
`cerberus connectors plugin managed load <id> --accept-changes`, is tested to
survive `redact.Text`, as are the retired-install refusal and the refusal of a
review from a non-terminal.

## Since WP-S2

WP-S2 is the structural fix. Its first piece is `redact.Scope`
(`internal/redact/scope.go`): a request's value redactor, carried in ctx
through `WithScope` and `ScopeFrom`. A credential is registered with `Add`
where it is resolved, while it is still a value, and `Text`, `Error` and
`Marshal` remove every registered value, in raw and URL-escaped form, before
the regex net runs. That removes a credential from a vendor SDK's message even
when the SDK gives it no label, which no rule can do.

It shares the plugin host's rules: a value under `MinValueLength` (8 bytes) is
not matched, because it would be cut out of ordinary words, and it is reported
by name through `Unprotected`. A nil scope is the regex net alone, so a path
that has no scope yet renders exactly as before. A scope never prints its
values: `String`, `GoString` and `MarshalJSON` show a count.

Every request entry point creates one through `cerbapi.BeginRequest`, which
marks the caller surface and adds a scope in the same call, so an entry point
cannot mark a surface and forget the scope. The five entry points are the socket
server's `wrap`, which also serves `cerberus mcp` and mcp-http, the console's
`markWebSurface`, the in-process CLI transport, each resource monitor check,
and each MCP tool call, since an MCP server has no middleware chain and the
daemon's stdio server runs tools in-process. A ctx that already has a scope
keeps it, so nested entry points share one. An entry point that is bypassed
leaves a nil scope, which renders as the regex net alone. Each entry point
has a test for both halves.

Resolution registers into it. `app.ConnectorSecrets`, the one provider every
connector lane, the plugin host and now the web console resolve through,
wraps the reference chain in `secrets.Registering`, so each value resolved on
a request is added to that request's scope under `service/key`. The only
values it skips are ones that are not credentials. The first is a secret a
built-in connector's definition declares with `Path: true`: ssh's
`ssh/<resource-id>/key` is a key file's path, and a path is what "reading SSH
key <path>: no such file" has to show. The second is `vercel/scope`, a team
slug the deploy prints. Which secrets are paths is read from the definitions,
not listed by hand.

Two leaks of the same kind were closed in the same change. The `secretref`
helper's stderr is no longer copied into its error, which now names the
command that shows it. A failed deploy credential lookup now stops the plan
with a recovery sentence instead of deploying without the token. Both
messages are tested to survive `redact.Text`.

The render edges read the scope back.

- **HTTP.** `BeginHTTPRequest` hands the request's scope to its response
  writer. The socket's and the console's JSON writers, and each envelope of
  the progress stream, render through it, so no handler can skip it.
- **Connector errors.** `ExternalConnectorService.Execute` wraps its error in
  the scope before returning it. It also gives itself a scope when its caller
  began no request.
- **MCP tools.** Every served tool renders its result, its error, in each
  shape go-mcp reads, and its mid-call notifications through its call's scope.
  That closed the daemon's stdio server, whose tools return raw
  `InProcessClient` text. Budgeted lists now go through the regex net as well.
- **In-process CLI.** A connector result goes through the same scope, marshal
  and decode round trip as a socket result, so `ssh exec` output and
  `docker logs` are no longer printed raw.
- **Logs.** `~/.cerberus/cerberus.log` is mode 0600, and a file created 0644
  earlier is narrowed on open. Records go through `redact.Handler`, and the
  daemon wraps slog's default logger in it too. The pipeline executor logs on
  its request's context, so its stage failures lose that request's
  credentials.

**The acceptance test.** `internal/testfixture/sentinel` is a credential
that matches no rule, and the real GitHub connector behind a factory that
resolves it through the registering provider. Its API is an httptest server
whose 401 echoes the bearer token, unlabelled, so go-github composes the
message around it. `TestResolvedCredentialNeverReachesAnySurface`
(`cmd/cerberus`) runs the failure through the in-process CLI, the socket's
error and progress stream, MCP over the socket (the tool list `cerberus mcp`
and mcp-http serve) and the daemon's stdio MCP, and reads the audit log.
`TestConsoleNeverShowsAResolvedCredential` covers the console, and
`TestDeployOutputNeverShowsTheResolvedToken` covers a Vercel step that echoes
its token. Each test first checks that the regex net alone misses the
sentinel. With registration switched off, every surface leaks it.

ssh, docker and local resolve no credential value, so they have no case. ssh
resolves a key file's path, which is deliberately not registered. docker and
local resolve nothing, and local's `FromEnv` coverage of env literals is
unchanged.

Plugins joined in S2-5. A plugin's credentials, resolved at load, merge into
each operation's scope at `CallTool`. Its stderr reaches the daemon log
redacted, a whole line at a time. A declared secret can say `kind: path` or
`kind: name`, as ssh's key does, to be left alone. The built-in `Path` flag
became that `Kind`. `TestPluginSuccessResultNeverCarriesItsCredential` is the
plugin half of the acceptance test.
The tenth casualty landed with PR #77: the Vercel plan's own placeholder,
`--token [vercel token]`, came back as `--token [REDACTED] token]`, and was
fixed by changing the placeholder rather than the rule.

**The Message/Detail split (S2-6).** Cerberus's own refusals are
`redact.Guidance`, or `redact.Prose` for an error made where redact cannot be
imported, such as `pkg/connector`'s key-table refusal. A `Renderer` renders
itself once, where it is made: prose kept, a wrapped cause through the scope and
the rules. `ExternalConnectorError` renders its head (connector, operation, code)
without the rules, and its cause as a Renderer or through them. `scopeError`
records the result in the request's rendered set. An edge that meets exactly
that text again, which is all an edge ever does, only removes values: exact
match, in-process, per request. On the socket, the daemon marks an error body
or stream envelope `rendered: true` only when its text and detail are in that
set. `daemonError` trusts a marked body as `PreRendered` and runs the rules over
an unmarked one, so a CLI and a daemon on different versions fall back to the
rules. `redact.ErrorText` is the scope-less edge's call, used by the CLI's
`main` and by every MCP connector tool through `connectorFailure`.

Eleven refusal sites are converted: ack, local-filesystem ack, preview
unsupported, undeclared operation, the ssh input refusal and its hint, the
runtime gate's three, the plugin host's ack, preview and input refusals, and
`MissingSecretsError`'s recovery sentence. `TestConvertedRefusalsSurviveEveryLane`
holds each one unchanged through the in-process CLI, the socket body and a
socket client. `TestGuidanceTheNetWouldEatArrivesIntact` sends prose the rules
provably eat through all three. `TestClientTrustsDaemonTextOnlyWhenMarked`
covers both directions of version skew. This closes WP-S2.

## Since P2-1

`principal_refused` joins the error codes the assignment rule exempts. The
socket's peer refusal is `redact.Guidance` (the unreadable-credentials case is
`GuidanceWrap` over the kernel's error), so it is rendered once where it is
made. It names its recovery ("run cerberus as that user"), and
`TestSocketRefusesAnotherUser` holds that text intact through the socket
client, and through `redact.Text` for a scope-less edge.
