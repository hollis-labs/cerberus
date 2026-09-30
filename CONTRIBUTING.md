# Contributing

How a change gets from your clone into `main`. This is deliberately short: most of what you need is already written somewhere closer to the thing it describes, and this page points at it rather than keeping a second copy that drifts.

## Before your first change

`README.md` and `docs/install.md` have the clone-and-build steps. Run `lefthook install` — a tracked config installs no hooks by itself, and a clone that skips it has no commit or push checks and says nothing about it.

`AGENTS.md` is the fastest orientation to the layout: which package does what, the two execution lanes, and the boundaries that are not obvious from reading the code. It is written for an agent working in the repo, which makes it unusually direct about the things that bite. **Read its "This may manage real systems" section before running anything** — Cerberus drives real hosts and APIs.

## The sequence

1. **Branch.** `<type>/<short-slug>`, where the type matches the change — `feat`, `fix`, `docs`, `chore`. Nothing enforces this; it is what the history does.
2. **Change one thing.** A branch carrying two unrelated changes costs the reviewer the ability to accept one and question the other.
3. **Run the checks** when the change is done: `make test` and `make lint`, plus `make typecheck` if you touched `web/`. Pre-push runs the full `go test`.
4. **Push, and open a pull request** against `main`.

Commit subjects follow the conventional-commit shape — a type, an optional scope, a colon, then the summary; a `!` after the type marks a breaking change. To see what is actually in use rather than trusting this sentence:

```
git log --no-merges -40 --format='%s' | grep -oE '^[a-z]+(\([a-z-]+\))?!?:' | sort -u
```

## What a pull request should carry

The reviewer was not there when you made the decisions. State what the change does, what it deliberately leaves alone, and the evidence that it works — the commands you ran and what came back, not a claim that it passes. If a number appears in the description, put the command that produced it beside it.

A change to a gate, a policy rule, a redaction rule or an approval path should say which test proves it holds, and that the test fails without the change.

## The one that cannot be undone

**Tests and experiments that reach a live system.** A connector operation run against a real host, cluster or SaaS tenant can write for real, and a destructive operation cannot be taken back. Point tests at fakes, use `--dry-run` and read the preview, and never pass `--ack` to a target you do not own. A test that needs a live target to pass does not belong in the suite. The rule and its rationale are in `AGENTS.md` under "This may manage real systems".

## Things that surprise people

- **`internal/webui/dist/.gitkeep` must stay.** `go:embed` fails to compile without a match. Do not delete it and do not commit the built bundle beside it. Use `make all`, not `make build`, when `web/` changes.
- **A build is not a deploy.** For a `run_from: artifact` resource, only `cerberus resource deploy <id>` reinstalls the running copy; `resource status` reports `artifact_stale`.
- **Never deploy the daemon through its own socket.** The call dies mid-operation and can leave the launchd job booted out. `AGENTS.md` has the safe sequence.
- **A new provider connector is a plugin, not a built-in.** The core is `local`, `ssh` and `docker`; if you are adding a vendor SDK to `go.mod`, you are in the wrong lane. See `docs/plugins.md` and `docs/plans/connector-work-packages.md`.
- **Never set `port: 0`** in a resource; two guards enforce it.
- **Redaction cannot read.** Write refusals as `redact.Guidance`, not free text; see the Redaction section of `AGENTS.md`.
- **`docs/catalog/`** has its own `validate.py` and `add_record.py`; use them rather than editing `catalog.json` by hand.

## What this does not cover

- **Which change is worth making.** There is no roadmap commitment here by design; that conversation happens in issues.
- **Releasing.** See `docs/release/beta-release-process.md`; it is a separate subject with separate mechanics.
- **Plugin authoring.** A plugin is a subprocess against the published `pkg/plugin` contract, not a contribution to this repo; see `docs/plugins.md`.
