---
id: "CERB-CAP-503"
class: "capability"
name: "Config validation"
summary: "Checks a project config or bundle manifest against its kind contract and the hard runtime invariants — and touches the filesystem for nothing a config names."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "check list read from source and the blind spots proved empirically by running the installed binary against eight broken configs"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/registry/schema.go, cmd/cerberus/cmd_config.go"
tags:
  - "area5"
  - "blind-spots"
  - "cerberus"
  - "class:capability"
  - "config"
  - "validation"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-502"
    note: "the no-argument form validates what the registry points at"
  - type: "implements"
    target: "CERB-DEC-551"
    note: "the port: 0 ban is schema-enforced here"
  - type: "implements"
    target: "CERB-DEC-553"
    note: "strict at author time, lenient at runtime"
  - type: "blocks"
    target: "CERB-GAP-530"
    note: "no path in a config is ever checked for existence"
  - type: "blocks"
    target: "CERB-GAP-531"
    note: "type and connector are checked for presence only"
  - type: "blocks"
    target: "CERB-GAP-532"
    note: "secret references are never parsed at author time"
  - type: "blocks"
    target: "CERB-GAP-533"
    note: "a literal credential in env: validates clean"
---

# Config validation

`cerberus validate` is two different commands behind one name, and the
difference matters.

With a path it validates that one file — project config or bundle manifest —
and is deliberately strict: it rejects schema errors, promotes a within-config
duplicate-port warning to a fatal error, and rejects unrecognised fields. That
last one is the author-time half of a deliberate split; see CERB-DEC-553.

With no argument it health-checks every registered config and reports what the
whole tree resolves to. This form is *not* strict: `checkEntry` fails an entry
only on error-severity issues, so unrecognised fields and port warnings come
back as `ok` with a detail string. It also prints every resolver warning to
stderr before the summary line.

What it checks, from `registry.ValidateProjectConfig`:
`kind` equals `cerberus-project/v1`; `owner` present and lowercase kebab-case;
`namespace` kebab-case when set; `project.id` present and matching a stricter
slug pattern (single interior hyphens only, because the value is also a
Tesseract namespace segment and an agent-setup template basename) and equal to
`owner`; each `project.links[]` entry has a non-empty `kind` and `target`;
resource ids present and unique within the config; resource `type` and
`connector` non-empty; `port: 0` rejected outright; unknown local-process
config keys warned via `localconn.ProcessConfigWarnings`; deprecated `build:`
warned; `depends_on` targets not in this config warned as possibly
cross-config; duplicate local TCP ports warned; pipeline ids present and
pipeline action `resource` references resolved against this config's resources.

**What it does not check is the more useful half.** Verified empirically on
2026-09-17 by running the installed binary against eight deliberately broken
configs in a scratch directory; every one below returned `OK` and exit 0:

- No filesystem path is ever checked. A `dir`, `env_file`, `log_file` or
  `command[0]` that does not exist validates clean. `cerberus validate
  ./cerberus.cerberus.yaml` returns `OK` on this machine even though its every
  `dir:` points at `/Users/chrispian/dev/hollis-labs/apps/cerberus`.
- `type:` and `connector:` are checked for presence only, never against the set
  of connectors the binary actually has. `connector: totally-not-a-connector`
  validates clean.
- Secret references are not parsed. `secretref.Parse` would reject
  `keychain://`, `keychain://svc` and `helper://../../evil/x`, and is never
  called from any author-time path — only from `Resolver.Resolve` at service
  exec time.
- A literal credential in `env:` is accepted silently, and `writePlist` copies
  it verbatim into the generated launchd plist. The `connector-secrets.yaml`
  reader refuses literals; a resource's `env:` does not.
- `depends_on` cycles are not detected. Two resources depending on each other
  validate clean.
- `health_check.url`, `.interval` and `.timeout` are unparsed:
  `interval: banana`, `timeout: -5` and `url: "not a url at all"` all pass.
  `port: 65536` passes.
- Pipeline action `type:` is not checked against the known action set, and
  stage `depends_on` is not resolved against sibling stages. This repo's own
  descriptor uses `deploy_app` and `build_app`, neither of which appears in
  `config.ActionDef`'s documented list.
- A port already bound on this machine is not checked, although
  `service.CheckPortConflict` exists and does exactly that at dev-session start.

Enforcement confirmed: `port: 0` errors with the omit-the-field recovery text,
and within-config duplicate ports fail the single-file form, both verified by
running the binary. `TestValidateProjectConfigPortZeroIsError` passes.
