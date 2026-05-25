# Cerberus Self-Release Via Pipeline

## Purpose

The first dogfood release shape for Cerberus is:

- Cerberus remains the operator surface
- release packaging is encoded, not remembered
- the actual archive production runs through a small repo-owned script
- Cerberus pipelines orchestrate that script

This is intentionally modest. The current pipeline system is already good
enough to drive a real repeatable release candidate build without inventing a
parallel release tool first.

## Current Decision

For the first self-release flow:

- model the release flow as a **Cerberus pipeline**
- use `shell` actions for packaging steps
- keep the packaging logic in `scripts/release-beta.sh`

This is the lowest-friction path because the existing pipeline system already
supports `shell` actions, while release artifacts and distribution records are
not yet first-class runtime objects.

## Packaging Contract

The current release script:

- requires `VERSION`
- accepts optional `BUILD_DATE`
- writes artifacts to `DIST_DIR` or `./dist` by default
- builds:
  - `cerberus_<version>_darwin_arm64.tar.gz`
  - `cerberus_<version>_darwin_amd64.tar.gz`
- writes a matching `.sha256` file for each archive

Run it directly:

```bash
VERSION=0.3.0-beta.1 ./scripts/release-beta.sh
```

Or through `make`:

```bash
VERSION=0.3.0-beta.1 make release-beta
```

## Recommended First Pipeline Shape

The pipeline should stay small and explicit:

1. verify repo state and test the CLI package
2. build the beta archives
3. inspect the produced archive contents

That yields a real release candidate artifact without pretending Cerberus
already owns upstream publishing, release notes, or GitHub prerelease creation.

## Current Repo-Owned Config

Cerberus now has a real app-owned project config in this repo:

```text
cerberus.cerberus.yaml
```

It defines:

- `cerberus-daemon-service`
- `cerberus-release-beta`

Register it with Cerberus:

```bash
cerberus register /Users/chrispian/dev/hollis-labs/apps/cerberus/cerberus.cerberus.yaml
```

Run the release pipeline:

```bash
VERSION=0.3.0-beta.1 cerberus pipeline run cerberus-release-beta
```

## Config Shape

The current config uses an explicit repo-local path:

```yaml
kind: cerberus-project/v1
owner: cerberus
namespace: local
project:
  id: cerberus
  name: Cerberus
pipelines:
  - id: cerberus-release-beta
    name: Cerberus Release Beta
    description: Build unsigned macOS beta archives for Cerberus itself.
    stages:
      - name: verify
        actions:
          - type: shell
            dir: /Users/chrispian/dev/hollis-labs/apps/cerberus
            command: |
              git status --short
              go test ./cmd/cerberus
      - name: package
        depends_on: [verify]
        actions:
          - type: shell
            dir: /Users/chrispian/dev/hollis-labs/apps/cerberus
            command: |
              : "${VERSION:?set VERSION, for example VERSION=0.3.0-beta.1}"
              ./scripts/release-beta.sh
      - name: inspect
        depends_on: [package]
        actions:
          - type: shell
            dir: /Users/chrispian/dev/hollis-labs/apps/cerberus
            command: |
              : "${VERSION:?set VERSION, for example VERSION=0.3.0-beta.1}"
              ls -la dist
              tar -tzf "dist/cerberus_${VERSION}_darwin_arm64.tar.gz"
              tar -tzf "dist/cerberus_${VERSION}_darwin_amd64.tar.gz"
```

## Why Not A Resource First

A release build is not a long-running runtime object. It is an encoded,
repeatable workflow. That makes `pipeline` the better first fit than a fake
release `resource`.

The helper script exists because:

- current pipeline actions do not yet model archive artifacts directly
- release builds need shared shell logic across CLI, docs, and future pipeline
  runs
- the script is easy to invoke from both Cerberus and humans during bring-up

The explicit absolute repo path is acceptable for this first dogfood config
because Cerberus project configs are app-owned local files, not portable package
manifests. If the repo path changes later, update the config in the repo and
re-register it.

## What This Does Not Solve Yet

This first self-release flow does not yet:

- publish a GitHub prerelease
- upload assets anywhere
- generate release notes
- validate a clean-machine install automatically
- persist release metadata inside Cerberus

Those can come later. The current target is simpler: make Cerberus capable of
building its own candidate release artifacts from a defined workflow.
