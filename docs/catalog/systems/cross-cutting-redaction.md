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
further from it than when this record was written. Seven narrow fixes have each
been correct and none has been structural; `AGENTS.md` already names the answer
— redact at the value boundary, where the caller still holds the key and the
value as separate things, and keep `Text` as a last-resort net over text
Cerberus did not compose. What is still missing is the test discipline that
would have caught any of them (CERB-GAP-274).

**P0 held the line without a rule change.** The P0 work (PRs #48 to #52) added
four refusal families: the non-loopback `--listen` refusal, the retired
`plugin_dir` routes, the SSH connection-field refusal and the docker ad-hoc
target refusal. Each has a test that it survives `redact.Text`, and `key_file`
and `known_hosts_file` values were checked against `redact.Marshal`.
`preview_unsupported` joined the error-code vocabulary before it could lose the
word after it, and `TestGateRefusalsSurviveRedaction` runs every error code,
followed by a recovery sentence, through `redact.Text`. That is the discipline
this record asks for, applied by hand. It is not yet a gate (CERB-GAP-274).
