# Task: build the Cerberus capability catalog

You are orchestrating an audit of Cerberus. The output is a catalog of what this
system can do, what it cannot do yet, and why — accurate enough that someone can
plan from it without reading the code.

Read `docs/catalog/README.md` first. It defines the record envelope, the four
classes and the house rules. This prompt is the plan; that file is the schema.

## Deliverables

1. `docs/catalog/catalog.json` — records conforming to the envelope, shaped for
   `~/Projects-apps/tools/leaderboard-explorer`.
2. `docs/catalog/systems/<slug>.md` — one per `capability` record: YAML
   frontmatter mirroring that record's envelope fields, then prose.
3. A short PR description saying what surprised you. If nothing did, the audit
   was too shallow.

The JSON `body` for a capability is the prose from its document. They are one
thing in two formats; if they disagree, the audit failed.

## Ground rules

**Verify, do not read.** The single most valuable thing this audit can produce
is the difference between what the code appears to do and what it actually does.
This session found a Docker connector that was dead for weeks while
`connectors list` reported it healthy. A catalog built by reading that code
would have recorded a working connector.

So: run the verb. Check the daemon. If you cannot run it, mark it `unverified`
and say why. **`verified: live` is a claim you must have evidence for.**

**Gaps are first-class.** Budget real effort on `class: gap` records. Anyone can
list what exists. The catalog earns its keep by making the next decision cheaper,
and that is mostly about what is missing and what it would cost.

**A deferral is a decision.** Where something was consciously not built, record a
`decision` with the reasoning, not a silent absence. Several such decisions were
made this session and the reasoning is in `docs/plans/connector-work-packages.md`
and `docs/adr/` — carry it over rather than restating the conclusion.

**Do not inflate maturity.** `shipped` means runnable today by someone who is not
you. Anything proven only against a fake is `partial`.

## Suggested fan-out

One subagent per area, each producing records plus its capability document. They
overlap at the edges — reconcile at the end rather than partitioning rigidly.

1. **Resource supervision** — `internal/cerbapi/resource_runtime_service.go`,
   `internal/connector/local/`, dev_session vs os_service, artifact staleness,
   the launchd path. Note what the lane deliberately does *not* own.
2. **The admin/connector lane** — `internal/cerbapi/external_connector_service.go`,
   every connector under `internal/connector/`, their operations, destructive
   flags and dry-run support. `cerberus connectors describe <id>` is
   authoritative for the operation list; use it rather than reading switches.
3. **The plugin lane** — `internal/pluginhost/`, `pkg/plugin/`, trust tiers, the
   secret channel, install/load/uninstall, and the ContextForge plugin in
   `~/Projects-apps/cerberus-plugins`.
4. **Surfaces** — CLI, daemon socket API, HTTP API, web console, MCP. The same
   capability often reaches three of these and is missing from the fourth; that
   asymmetry is worth recording as gaps.
5. **Config, registry and secrets** — `internal/registry/`, `internal/secrets/`,
   `docs/secrets.md`, what `cerberus validate` does and does not check.
6. **Operational reality** — what is actually registered and running on this
   machine (`cerberus resource list`, `connectors list`, `plugin managed list`),
   versus what the repo's own `*.cerberus.yaml` files describe. These differ.

## What this session already established

Carry these in rather than rediscovering them. Verify anything you depend on.

- The live registry is `~/.cerberus/config.yaml`. The repo's
  `cerberus.cerberus.yaml` and `infrastructure.cerberus.yaml` are **not
  registered** — they carry another user's paths.
- Connector liveness means "constructible right now", not "installed".
- Resources that are not local/process are named handles for connector
  operations, not broken workloads. `muctlvaig` is the worked example.
- `list_gateways` on the ContextForge plugin has **never run against a real
  gateway** — no JWT on this machine. It is `verified: test`, not `live`.
- Azure is read/probe only by decision, and the subscription has no compute
  provider registered. Cost, Key Vault and Resource Graph are deferred with
  recorded unlock conditions.
- Known missing, already briefed: privilege elevation for `ssh exec` (WP-8),
  dry-run extraction (WP-1, skipped not done). Recursive directory transfer
  (WP-9) shipped 2026-09-17 as `ssh put_dir` / `get_dir`.

## Seed list — must-have gaps worth confirming or refuting

Starting points, not conclusions. Argue with them.

- **Backups.** There is no backup capability for `~/.cerberus/` — config,
  registry, plugin state, the SQLite store. Nothing backs up the thing that owns
  everything else. Likely `must-have`.
- **Health and alerting.** `internal/daemon/monitor.go` and an `alerts/`
  directory exist; establish what actually fires and where it goes.
- **Audit log.** Destructive operations require `--ack`, but is there a record
  of who ran what and when? Agents are callers here too.
- **Secret rotation.** Plugins resolve secrets at load. What is the rotation
  story for a built-in connector?
- **Bootstrapping a new machine.** The path from clone to working daemon is
  undocumented and was reconstructed by hand this session.
- **Config portability.** `dir:` is a literal path with no interpolation, which
  is why this repo's own descriptors do not resolve here. Recorded as a known
  limitation; confirm whether it is a `gap`.

## Two passes, and the second is not a re-run

Run this audit twice. The second pass takes the first pass's catalog as input
and looks for what it missed. Do not re-audit from scratch — you will find the
same things and gain false confidence.

A first pass finds what the code advertises. It is systematically blind to
absence: a capability nobody implemented has no file to read, and a surface
where a verb is missing looks identical to one where it was never wanted. The
second pass is aimed at exactly that blindness.

**Techniques, in rough order of yield:**

1. **Inverse sweep.** Walk the code and find what has no record. Every exported
   operation, CLI command, MCP tool, API route and config key. Anything present
   in the system and absent from the catalog is either a miss or a deliberate
   omission — and if it is deliberate, it needs a record saying so.
2. **Surface asymmetry.** For each capability, tabulate which surfaces expose it
   — CLI, API, MCP, console. A verb on three surfaces and missing from the
   fourth is a gap the first pass will have recorded as "shipped" because the
   part it looked at worked.
3. **Challenge every `verified: live`.** Re-run a sample. A claim that cannot be
   reproduced is the highest-value finding in the whole exercise, because it
   means the catalog is confidently wrong rather than merely incomplete.
4. **Challenge low confidence.** Any record under `confidence_score: 0.7` is the
   first pass telling you where it was unsure. Resolve or explain each one.
5. **Whole missing capabilities.** Harder and more valuable than missing tools.
   Ask the boring operational questions — backup, recovery, rotation, audit,
   onboarding, upgrade, uninstall — and check whether each has a record at all.
   An entire capability with no file to read is exactly what pass one cannot see.
6. **Decisions with no record.** Mine git history, ADRs, plan documents and PR
   discussions for choices that were made and never written down. A decision
   recovered from a commit message is worth more than one restated from code.
7. **Read the gap records adversarially.** Is each priority defensible? Is
   anything marked `nice-to-have` that would actually block a new contributor on
   day one?

**Output of the second pass** is a diff, not a replacement: records added,
records corrected, and claims withdrawn. State plainly what pass one got wrong.
If the second pass adds nothing, say so — but examine whether it was run
independently enough to be capable of disagreeing.

## Acceptance

- `catalog.json` parses, every record validates against the envelope, and ids
  are unique and stable.
- Every `capability` has a document; every document has a record.
- The explorer renders it: `cd ~/Projects-apps/tools/leaderboard-explorer` and
  point it at the file. If a field it reads is missing, the record is wrong.
- Spot-check five `verified: live` claims by rerunning them. If any fails, that
  is the finding to lead with.
