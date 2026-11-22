# Changelog

fleet-install is new software. Releases are tagged `0.x`; there are no compatibility guarantees before 1.0.

## Unreleased

## v0.0.1 - 2026-10-25

First tagged release. Installs a CI-built release described by a `deploy.yaml` shipped in the release tarball: download and verify (sha256), extract, back up databases, switch the `current` symlink, restart and health-check, with rollback to the previous release.
