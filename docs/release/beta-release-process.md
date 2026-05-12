# Cerberus Beta Release Process

## Scope

This process is macOS-first and covers Cerberus itself: the CLI, daemon, launch
agent bootstrap, and local resource runtime checks. It does not define
distribution for portfolio applications managed by Cerberus.

## Versioning And Artifact Names

Beta tags use SemVer prerelease tags:

```text
v<major>.<minor>.<patch>-beta.<n>
```

Examples:

```text
v0.3.0-beta.1
v0.3.0-beta.2
```

The released macOS binary archive is named:

```text
cerberus_<version>_darwin_<arch>.tar.gz
```

Where:

- `<version>` is the tag without a leading `v`, for example `0.3.0-beta.1`.
- `<arch>` is `arm64` or `amd64`.

Each archive contains the executable named `cerberus` at the archive root.
Publish a matching checksum file named:

```text
cerberus_<version>_darwin_<arch>.tar.gz.sha256
```

## Canonical macOS Install Path

Released beta binaries install to:

```text
~/.cerberus/bin/cerberus
```

This is the canonical path used by `cerberus install` when it writes the
`com.fragments-engine.cerberus` user launch agent. Operators may put
`~/.cerberus/bin` on `PATH`, but the launch agent should point directly at the
canonical binary path rather than a shell-resolved `cerberus`.

Repo-local development installs through `go install` remain supported for
contributors, but release docs and release-candidate validation should treat
`~/go/bin/cerberus` as a fallback only.

## Build Checklist

1. Start from a clean tag candidate and verify the intended version:

   ```bash
   git status --short
   git describe --tags --dirty --always
   ```

2. Run the test suite:

   ```bash
   go test ./...
   ```

3. Build macOS archives for both supported beta architectures:

   ```bash
   VERSION=0.3.0-beta.1
   BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   mkdir -p dist

   GOOS=darwin GOARCH=arm64 go build \
     -trimpath \
     -ldflags "-s -w -X main.version=${VERSION} -X main.buildDate=${BUILD_DATE}" \
     -o dist/cerberus ./cmd/cerberus
   tar -C dist -czf "dist/cerberus_${VERSION}_darwin_arm64.tar.gz" cerberus
   shasum -a 256 "dist/cerberus_${VERSION}_darwin_arm64.tar.gz" \
     > "dist/cerberus_${VERSION}_darwin_arm64.tar.gz.sha256"

   GOOS=darwin GOARCH=amd64 go build \
     -trimpath \
     -ldflags "-s -w -X main.version=${VERSION} -X main.buildDate=${BUILD_DATE}" \
     -o dist/cerberus ./cmd/cerberus
   tar -C dist -czf "dist/cerberus_${VERSION}_darwin_amd64.tar.gz" cerberus
   shasum -a 256 "dist/cerberus_${VERSION}_darwin_amd64.tar.gz" \
     > "dist/cerberus_${VERSION}_darwin_amd64.tar.gz.sha256"
   ```

4. Verify archive contents:

   ```bash
   tar -tzf "dist/cerberus_${VERSION}_darwin_arm64.tar.gz"
   tar -tzf "dist/cerberus_${VERSION}_darwin_amd64.tar.gz"
   ```

5. Create the beta tag and prerelease only after the release-candidate smoke
   checklist passes.

## Fresh Install Checklist

Use a release archive, not a repo-local binary:

```bash
VERSION=0.3.0-beta.1
ARCH=arm64

mkdir -p ~/.cerberus/bin
tar -xzf "cerberus_${VERSION}_darwin_${ARCH}.tar.gz"
install -m 0755 cerberus ~/.cerberus/bin/cerberus
export PATH="$HOME/.cerberus/bin:$PATH"

cerberus --version
cerberus init
cerberus install
launchctl print "gui/$(id -u)/com.fragments-engine.cerberus"
```

Expected results:

- `cerberus --version` prints the beta version and build date.
- `cerberus init` creates or preserves `~/.cerberus/config.yaml`.
- `cerberus install` reports `Binary: ~/.cerberus/bin/cerberus` expanded to the
  absolute home path.
- The launch agent is loaded under `com.fragments-engine.cerberus`.
- Daemon logs are under `~/.cerberus/logs/`.

## Release-Candidate Smoke Checklist

Run these checks on a macOS machine from the released binary path.

1. Bootstrap and daemon health:

   ```bash
   ~/.cerberus/bin/cerberus init
   ~/.cerberus/bin/cerberus install
   launchctl print "gui/$(id -u)/com.fragments-engine.cerberus"
   ```

2. CLI fallback when the daemon is unavailable:

   ```bash
   ~/.cerberus/bin/cerberus uninstall
   ~/.cerberus/bin/cerberus resource list
   ~/.cerberus/bin/cerberus install
   ```

3. Resource smoke for one local `dev_session` resource:

   ```bash
   cerberus resource status <dev-resource-id>
   cerberus resource apply <dev-resource-id>
   cerberus resource logs <dev-resource-id>
   cerberus resource stop <dev-resource-id>
   ```

4. Resource smoke for one macOS `os_service` artifact-backed resource:

   ```bash
   cerberus resource status <service-resource-id>
   cerberus resource deploy <service-resource-id>
   cerberus resource inspect <service-resource-id>
   cerberus resource reload <service-resource-id>
   cerberus resource doctor <service-resource-id>
   cerberus resource stop <service-resource-id>
   cerberus resource apply <service-resource-id>
   ```

5. Artifact freshness smoke:

   ```bash
   cerberus resource sync <service-resource-id>
   cerberus resource status <service-resource-id>
   ```

   The status output should report whether the artifact is current or recommend
   `deploy`, `sync`, or `apply`.

6. Recovery smoke:

   ```bash
   cerberus uninstall
   cerberus install
   launchctl print "gui/$(id -u)/com.fragments-engine.cerberus"
   ```

## Release Checklist

Before announcing a beta:

- The tag follows `v<major>.<minor>.<patch>-beta.<n>`.
- macOS `darwin_arm64` and `darwin_amd64` archives are built.
- Each archive contains a root-level `cerberus` executable.
- SHA-256 checksum files are published with the archives.
- A fresh install uses `~/.cerberus/bin/cerberus`.
- `cerberus install` points the launch agent at `~/.cerberus/bin/cerberus`.
- `cerberus init`, daemon bootstrap, launchd daemon inspection, and uninstall/reinstall
  recovery pass.
- At least one `dev_session` and one artifact-backed `os_service` resource pass
  the release-candidate smoke checklist.
- Known bootstrap gaps are listed in the release notes.

## Current Bootstrap Gaps

- The beta process does not define code signing, notarization, or an updater.
- Installation is manual archive extraction plus `install`; there is no package
  installer yet.
- The launch agent is user-scoped and macOS-only.
- `go install` remains a contributor fallback but is not the release install
  path.
