# Cerberus Release Readiness Plan

## Status

Draft

## Date

2026-05-24

## Purpose

Define the shortest credible path from the current repo state to an easy enough
unsigned macOS beta release for **Cerberus by Hollis Labs**, released as fully
permissive open source.

This plan is specifically about making Cerberus itself easy to install, easy to
bootstrap, and credible as the build/release surface for the wider portfolio.

## Product Positioning

- public product name: **Cerberus by Hollis Labs**
- common name in repo, CLI, binary, and docs: **Cerberus**
- release posture: **macOS-first beta**
- distribution posture: **unsigned**
- license posture: **MIT**
- commercial posture: Cerberus itself is intended to be fully open and
  permissive, not held back as a proprietary monetization surface

## Goal

Cerberus is ready for a public beta when a fresh macOS operator can:

1. download a released archive
2. install the binary without cloning the repo
3. bootstrap config and daemon without tribal knowledge
4. understand the v2 resource model quickly
5. manage at least one local resource successfully
6. recover from common setup/runtime failures using the built-in output and docs

## Non-Goals

These should not block the first public beta:

- code signing
- notarization
- package installers (`.pkg`, Homebrew, etc.)
- auto-update
- cross-platform parity
- polished desktop distribution
- complete cloud/deployment automation across every connector

## Current State

Cerberus already has much of the core beta substrate:

- a documented macOS archive install path
- `cerberus init`
- `cerberus install` / `cerberus uninstall`
- a canonical release binary location at `~/.cerberus/bin/cerberus`
- v2 resource lifecycle commands
- beta quickstart and beta release process docs

What is still missing is not the core runtime model. The remaining work is
release-readiness work:

- OSS/legal posture needs to be explicit in-repo
- install/setup needs to feel more straightforward for a non-contributor
- release steps need to be reproducible and dogfooded
- release verification needs to be treated as a real gate

## Release Readiness Workstreams

### 1. OSS Packaging And Identity

Objective:
- make the repo clearly publishable as a public OSS project

Tasks:

- add an MIT `LICENSE`
- ensure README and release docs consistently use `Cerberus by Hollis Labs`
  where product identity matters
- remove or avoid language implying restricted commercial rights or reserved
  product rights for Cerberus itself
- keep repo/CLI/binary naming simple as `cerberus`

Definition of done:

- the repo is unambiguous about license and naming posture
- a public reader can tell what Cerberus is, who publishes it, and how it is
  licensed

### 2. Fresh Install Experience

Objective:
- reduce the number of manual judgment calls required for a first-time beta
  operator

Tasks:

- verify the current archive install flow from a clean macOS user context
- decide whether the initial beta uses:
  - documented manual `tar` + `install`, or
  - a thin install script layered over that same archive
- document unsigned macOS realities clearly:
  - expected quarantine/Gatekeeper friction
  - how to inspect/clear it if needed
- tighten the quickstart so the first 10 minutes are concrete and sequential
- ensure `cerberus --version`, `cerberus init`, and `cerberus install` are the
  first success milestones

Definition of done:

- a fresh operator can install and bootstrap Cerberus from release artifacts
  without repo knowledge

### 3. Bootstrap And Recovery Hardening

Objective:
- make the daemon bootstrap path boring and trustworthy

Tasks:

- verify `cerberus install` behavior against the released binary path
  `~/.cerberus/bin/cerberus`
- verify uninstall/reinstall recovery paths
- verify behavior when `~/.cerberus/config.yaml` does not yet exist
- verify behavior when the daemon is down and CLI fallback is expected
- audit operator-facing output for bootstrap failure cases:
  - missing config
  - launch agent load failure
  - wrong binary path
  - stale launch agent state

Definition of done:

- the bootstrap path and the recovery path both work from the released binary
- the common failure modes are obvious enough to diagnose quickly

### 4. Dogfood Cerberus Releasing Cerberus

Objective:
- prove Cerberus can serve as the release/build surface for portfolio apps by
  using it on itself first

Tasks:

- define a Cerberus-managed release resource or pipeline for building Cerberus
  archives
- make the release flow produce the canonical macOS beta artifacts
- make Cerberus capture the build metadata needed for release output
- ensure the release path can run repeatably without ad hoc local shell history
- decide what belongs in:
  - a `resource`
  - a `pipeline`
  - a release helper script invoked by Cerberus

Current decision:

- first self-release flow = **pipeline**
- packaging logic = **release helper script**
- future richer release model can evolve later if Cerberus starts persisting
  release metadata or publishing upstream assets directly

Definition of done:

- the preferred release path for Cerberus is run through Cerberus itself
- release steps are encoded, not remembered

### 5. Release Artifact And Verification Discipline

Objective:
- make each beta release feel deliberate instead of improvised

Tasks:

- keep the existing archive/checksum naming convention as the initial beta
  artifact contract
- produce both `darwin_arm64` and `darwin_amd64` artifacts
- make the release-candidate smoke checklist a real pre-tag gate
- validate at least one clean install from the actual built archive
- record the known gaps explicitly in release notes instead of hiding them

Definition of done:

- a beta tag corresponds to installable, verified artifacts
- release confidence comes from a repeatable checklist, not memory

## Suggested Execution Order

### Phase 1: Publishability

- add MIT `LICENSE`
- align README and release docs with `Cerberus by Hollis Labs`
- remove stale ambiguity around commercial restrictions

### Phase 2: Installability

- run a clean install test from archive
- decide whether to add a thin install script
- document unsigned macOS setup friction clearly

### Phase 3: Bootstrap Reliability

- harden install/uninstall/reinstall behavior and messaging
- tighten bootstrap/recovery docs and command output where needed

### Phase 4: Dogfood Release Flow

- encode Cerberus's own build/release path in Cerberus
- use that path to produce candidate beta artifacts

### Phase 5: Beta Gate

- run the release-candidate smoke checklist from produced artifacts
- publish only after the install/bootstrap/resource smoke passes

## Immediate Next Slice

The highest-leverage next slice is:

1. add MIT `LICENSE`
2. align README/release docs with the `Cerberus by Hollis Labs` posture
3. define the first Cerberus-managed self-release flow
4. run one real archive-based install/bootstrap smoke test

## Open Questions

- Should the first public beta ship with only manual archive install, or is a
  thin install script worth doing immediately?
- Should the Cerberus self-release path be modeled first as a `pipeline`, a
  `resource`, or a small release script that Cerberus invokes?
- How much release metadata should Cerberus itself emit versus what remains in
  Git tags and external release notes?
