# Task: build a capability catalog for this project

Adapted from the Cerberus audit. Produces a catalog of what a system can do,
what it cannot do yet, and why — as data a person can plan from without reading
the code, and as prose a person can actually read.

Replace `<PROJECT>` throughout. Where this prompt and the project disagree,
the project wins — but say so in the PR rather than silently diverging.

## Deliverables

1. `docs/catalog/catalog.json` — records conforming to the envelope below,
   shaped for `~/Projects-apps/tools/leaderboard-explorer`.
2. `docs/catalog/systems/<slug>.md` — one per `capability`: YAML frontmatter
   mirroring the record envelope, then prose.
3. `docs/catalog/README.md` — the schema as this project applies it, including
   any class or field you added.
4. A PR description saying what surprised you.

The JSON `body` for a capability is the prose from its document. One thing, two
formats. If they disagree the audit failed.

## Record envelope

Fields the explorer reads; project-specific payload goes in `data`.

`id` · `class` · `name` · `summary` · `state_field` · `state_label` ·
`review_status` · `confidence_score` · `confidence_label` · `last_reviewed` ·
`tags` · `relationships` · `body` · `locus` · `pointer_locator` · `data`

`locus` is `core` (built and shipped by this project) · `plugin` (ours, loaded
separately) · `vendor` (not ours — a third-party binary, SDK or sibling tool;
name it in `data.vendor`). It is orthogonal to `does_not_own`: `locus` says who
builds it and what breaks when they change, `does_not_own` says what the system
deliberately refuses to do. A `core` capability that shells out to a vendor
binary is still `core` — list the dependency anyway, because the failure mode
belongs to the vendor.

Ids are stable and never renumbered: `<PROJ>-CAP-001`, `<PROJ>-GAP-014`.
`state_label` is one of `shipped` · `partial` · `planned` · `deferred` ·
`blocked`.

**Four classes:**

- **`capability`** — a major system, roughly one per thing you would name in a
  sentence describing the project.
  `data`: `{ owns, does_not_own, entry_points[], surfaces[], key_files[] }`
  `does_not_own` is not filler — half the questions about a system are about its
  boundary.
- **`tool`** — one verb, command, endpoint or operation. The leaves.
  `data`: `{ capability, invocation, surfaces[], destructive, verified }`
  `verified` is `live` · `test` · `unverified`. Something exercised only against
  a fake is not `live`.
- **`gap`** — something not built. Same weight as what exists.
  `data`: `{ capability, priority, blocked_by, unlocks_when, effort }`
  `priority` is `must-have` · `nice-to-have` · `speculative`.
- **`decision`** — a choice made, especially one that closed off an option.
  `data`: `{ decision, alternatives_considered, rationale, reversible, superseded_by }`

## Ground rules

**Verify, do not read.** The most valuable output is the difference between what
the code appears to do and what it actually does. Run the verb. Hit the endpoint.
Check the running process. If you cannot, mark it `unverified` and say why.

This is not hypothetical: the Cerberus audit was commissioned after a connector
was found dead for weeks while its own health output reported it fine. A catalog
built by reading that code would have recorded a working feature.

**Gaps are first-class.** Anyone can list what exists. A catalog earns its keep
by making the next decision cheaper, which is mostly about what is missing and
what it would cost. Budget real effort here.

**A deferral is a decision.** Where something was consciously not built, write a
`decision` record with the reasoning. Mine existing ADRs, plan documents and PR
discussions for decisions already made and carry the reasoning over — do not
restate only the conclusion.

**Do not inflate maturity.** `shipped` means runnable today by someone who is
not you.

**Prefer specific summaries.** "Handles auth" is not a record. "Issues and
refreshes session tokens against the org IdP, 12-hour expiry" is.

## Fan-out

Adapt to the project's real shape. A structure that usually works:

1. **Core domain** — what the project is fundamentally for.
2. **Interfaces and surfaces** — CLI, API, UI, SDK, events. One capability often
   reaches three and is missing from the fourth; that asymmetry is a gap.
3. **Integrations** — everything talking to something external, plus its auth
   and failure modes.
4. **Data, config and state** — what persists, what validates it, what happens
   on upgrade.
5. **Operations** — deploy, monitoring, backup, recovery, audit. Frequently the
   thinnest area and the one producing the highest-priority gaps.
6. **Operational reality** — what is deployed and running right now versus what
   the repo claims. Where they differ, the difference is the finding.

Subagents overlap at the edges. Reconcile at the end rather than partitioning
rigidly.

## Questions that surface real gaps

Ask these of every project. They are boring and they work.

- If this machine died, what is the recovery path, and has anyone run it?
- What is the story for rotating a credential this system holds?
- Is there a record of who did what? If agents are callers, this matters more,
  not less.
- What does a new contributor have to do to get a working environment, and is it
  written down or was it reconstructed by someone who already knew?
- Which capability exists on one surface but not the others?
- What has never been run against the real thing?

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

- `catalog.json` parses; every record validates; ids unique and stable.
- Every `capability` has a document and every document has a record.
- The explorer renders it. A field it reads and cannot find means the record is
  wrong, not that the explorer is.
- Re-run five `verified: live` claims at random. If one fails, lead the PR with
  that.
