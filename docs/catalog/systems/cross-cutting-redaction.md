---
id: "CERB-CAP-700"
class: "capability"
name: "Credential redaction"
summary: "Rewrites anything that parses as a credential out of every operator-facing string leaving the daemon, on the error path of all five surfaces."
state_field: "maturity"
state_label: "partial"
review_status: "reviewed"
confidence_score: 0.95
confidence_label: "Rules read directly; both live defects reproduced against redact.Text during reconciliation"
last_reviewed: "2026-09-17"
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
after `Bearer` is left alone, and `NamesOnlyKey` exempts JSON fields whose values
are credential *names* rather than values — which is how `missing_secrets`
survives as `["token"]` instead of `["[REDACTED]"]`.

Both escape hatches were added after the redactor ate its own guidance. That has
now happened six times, and `AGENTS.md` records the rule the repository settled
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

The mechanism works — it redacts real credentials, and the two documented
escape hatches hold. But two distinct rules are currently corrupting text that
is prose by construction, on every surface, verified live:

- `assignment` matches the error *code* `credential_missing:` and eats the first
  word of the cause (CERB-GAP-273), including the verb `reload` in a recovery
  instruction.
- `flag` matches `X-API-Key ` — the internal hyphen satisfies `--?` — and eats
  the next word of a plugin manifest's documentation field (CERB-GAP-447).

A component whose failure mode is silently rewriting the operator's recovery
instructions is not `shipped` while two such rewrites are live, however complete
the code. The fix for both is narrow and the exclusion mechanism already exists;
what is missing is the test discipline that would have caught them
(CERB-GAP-274).
