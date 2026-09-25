# Cerberus Capability Catalog

What Cerberus can do, what it cannot do yet, and why — as data.

Two artefacts, kept in sync:

- **`catalog.json`** — the machine-readable record set, shaped for
  `~/Projects-apps/tools/leaderboard-explorer` so it can be visualised without a
  conversion step.
- **`systems/*.md`** — one document per major system: YAML frontmatter that
  mirrors the record envelope, then prose that explains the system the way a
  person would want it explained.

The JSON is generated from, and must agree with, the documents. A contributor
who adds a capability updates both in the same PR.

## Why it lives here

A catalog in a separate repo drifts from the code within a sprint. In-repo, a
PR that adds a connector and does not touch the catalog is visible in review.

## Record envelope

Every record in `catalog.json` carries the fields the explorer reads. Anything
project-specific goes in `data`.

| Field | Meaning |
|---|---|
| `id` | Stable, e.g. `CERB-CAP-001`. Never renumbered — things link to it. |
| `class` | `capability` · `tool` · `gap` · `decision` |
| `name` | Short human name |
| `summary` | One sentence. What it does, not how. |
| `state_field` | The dimension `state_label` measures — usually `maturity` or `status` |
| `state_label` | `shipped` · `partial` · `planned` · `deferred` · `blocked` |
| `review_status` | `draft` · `reviewed` |
| `confidence_score` | 0–1. How sure the author is this record is accurate. |
| `confidence_label` | Why that score, in a few words |
| `last_reviewed` | ISO date |
| `tags` | Freeform, plus `cerberus` and `class:<class>` |
| `relationships` | Ids this record depends on, implements, or supersedes |
| `body` | Prose. For a `capability`, the body of its `systems/*.md`. |
| `locus` | `core` · `plugin` · `vendor` — where the implementation lives |
| `pointer_locator` | Where the thing actually lives — a path, or `docs/…` |
| `data` | Class-specific payload, below |

### `locus` — who builds it, and what breaks without them

On every `capability` and `tool`:

- **`core`** — compiled into the Cerberus binary. Ships and versions with it.
- **`plugin`** — a standalone plugin, loaded at runtime, versioned separately.
- **`vendor`** — not ours. A third-party binary we shell out to, an SDK we wrap,
  or a sibling tool we depend on. Name it in `data.vendor`.

This is orthogonal to `does_not_own`, and both are needed. `locus` answers *who
builds this and what breaks if they change it*; `does_not_own` answers *what
does this system deliberately refuse to do*. The Docker capability is `core` and
still does not own container supervision — that is the resource lane. Recording
only one of those answers half the question.

`vendor` is the one that pays off later. It makes "what breaks if Docker Desktop
is not installed" or "what depends on a v0.x SDK" a query rather than an
investigation. Where a `core` capability leans on a vendor binary — Docker does,
via the CLI — set `locus: core` and still list the dependency in
`data.vendor`, because the failure mode belongs to the vendor even though the
code is ours.

### `class: capability`

A major system. Roughly one per thing you would name in a sentence describing
Cerberus: resource supervision, the connector lane, the plugin lane, secrets.

`data`: `{ owns, does_not_own, vendor[], entry_points[], surfaces[], key_files[], adrs[] }`

`does_not_own` is not filler. Half the questions about a system are about its
boundary, and a catalog that only records what something does answers the easy
half.

### `class: tool`

One verb, command, connector operation or MCP tool. The leaves.

`data`: `{ capability, invocation, surfaces[], destructive, verified, vendor[] }`

`verified` is one of `live` (run against the real system), `test` (covered by
tests only), `unverified`. Do not mark `live` for something only exercised
against a fake — that distinction is the point of the field.

### `class: gap`

Something not built. **These carry the same weight as what exists** — a catalog
that lists only what works is marketing, not a catalog.

`data`: `{ capability, priority, blocked_by, unlocks_when, effort }`

`priority`: `must-have` · `nice-to-have` · `speculative`.
`blocked_by` is empty when only effort is missing. Say so plainly; "blocked on
nobody" is useful information.

### `class: decision`

A choice made, with the reasoning, especially one that closed off an option.

`data`: `{ decision, alternatives_considered, rationale, reversible, superseded_by }`

A deferral is a decision: record it as one rather than leaving a silent gap.

## House rules

- **Record what is true, not what is aspirational.** `shipped` means someone
  can run it today.
- **An unverified capability is `partial`,** however complete the code.
- **Every `gap` needs a priority and a reason.** "Would be nice" with no
  rationale is noise; the point is to make the next decision cheaper.
- **Prefer a specific summary.** "Manages Docker" says nothing;
  "Runs Docker operations against a per-call target, local or over SSH" is a
  record.

## Adding and merging records

More than one branch writes this catalog at once, often from different agents.
Two things used to collide. Ids are numbered **globally across classes**, so two
branches that each take "the next number" take the same one. And records were
appended at the tail, so any two branches' new records touched the same lines.
`add_record.py` handles both:

```bash
python3 docs/catalog/add_record.py add record.json  # allocate, insert, write; prints the id
python3 docs/catalog/add_record.py resolve          # during a rebase or merge conflict
python3 docs/catalog/add_record.py sync             # capability bodies from systems/*.md
python3 docs/catalog/add_record.py sort             # canonical order
python3 docs/catalog/test_add_record.py             # its tests, against real git repos
```

- **Canonical order.** Records are sorted by class (capability, decision, gap,
  tool), then by number, and `validate.py` refuses any other order. A new gap
  lands at the end of the gap block and a new tool at the end of the tool
  block, so branches that add different classes merge without a conflict.
- **Allocate late.** `add` takes the next number above every id in your
  checkout and on `origin/main`, which it fetches. Run it after rebasing onto
  current main, just before you push. Leave `id` out of `record.json`. A
  capability is a `systems/*.md` document first, so give it the id in the
  document's frontmatter and add its record by hand.
- **On a conflict, don't hand-merge the JSON.** Run `resolve`. It merges the
  base, the other side and yours record by record:
  - a record only one side changed takes that side's version;
  - a record you added keeps its id unless the other side used that id or its
    number, and then it is renumbered;
  - the new id is written into your other new records and into the
    `systems/*.md` files where the other side never used the old id.

  It prints each renumbering so you can fix commit messages and PR
  descriptions. A record both sides changed differently is left for you, except
  a capability whose only difference is its body, which it rebuilds from its
  document.
- Output keeps the house serialization: indent 2, non-ASCII as is, and a
  trailing newline.
