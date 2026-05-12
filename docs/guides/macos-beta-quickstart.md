# macOS Beta Quickstart

This guide gets a macOS operator from a fresh Cerberus install to one managed local resource.

Cerberus beta is macOS-first. The active local workload surface is the v2 `resources:` model and the `cerberus resource ...` CLI/MCP lane.

## 1. Install The CLI

For released beta builds, install the binary to the canonical user-owned path:

```bash
mkdir -p ~/.cerberus/bin
tar -xzf cerberus_<version>_darwin_<arch>.tar.gz
install -m 0755 cerberus ~/.cerberus/bin/cerberus
export PATH="$HOME/.cerberus/bin:$PATH"
```

Repo-local development can still use:

```bash
go install ./cmd/cerberus/
```

Verify the binary:

```bash
cerberus --version
cerberus --help
```

## 2. Create The Config

```bash
cerberus init
cerberus validate
```

The live config is:

```text
~/.cerberus/config.yaml
```

It must use the v2 shape:

```yaml
version: 2
projects: []
resources: []
```

## 3. Bootstrap The Daemon

For normal macOS beta operation, run Cerberus as a launch agent:

```bash
cerberus install
```

This bootstraps the daemon label:

```text
com.fragments-engine.cerberus
```

Use these commands to inspect or recover the daemon:

```bash
cerberus daemon stop
cerberus daemon restart
cerberus install
```

If your config contains the v2 resource `cerberus-daemon-service`, prefer the resource lane for ongoing lifecycle work:

```bash
cerberus resource status cerberus-daemon-service
cerberus resource apply cerberus-daemon-service
cerberus resource reload cerberus-daemon-service
```

Keep `cerberus install` and `cerberus uninstall` for bootstrap and recovery when the daemon socket is not available.

## 4. Add One Project

Edit `~/.cerberus/config.yaml` and add one project plus one local `process` resource.

For a durable macOS service, prefer `mode: os_service`, `supervisor: launchd`, and `run_from: artifact`:

```yaml
version: 2

projects:
  - id: my-project
    name: My Project

resources:
  - id: my-api-uat
    name: My API UAT
    type: process
    project: my-project
    connector: local
    tags: [uat, api, launchd, artifact]
    config:
      dir: /absolute/path/to/my-project
      command: ["./bin/my-api", "serve", "--port", "8088"]
      build: ["make", "build"]
      url: http://127.0.0.1:8088
      port: 8088
      mode: os_service
      supervisor: launchd
      run_from: artifact
```

For an interactive dev loop, use `dev_session`:

```yaml
  - id: my-web-dev
    name: My Web Dev
    type: process
    project: my-project
    connector: local
    tags: [dev, frontend]
    config:
      dir: /absolute/path/to/my-project/web
      command: ["npm", "run", "dev", "--", "--host", "127.0.0.1", "--port", "5177"]
      url: http://127.0.0.1:5177
      port: 5177
      mode: dev_session
```

Validate after editing:

```bash
cerberus validate
cerberus resource list
```

## 5. Operate The Resource

For an artifact-backed `os_service`:

```bash
cerberus resource status my-api-uat
cerberus resource deploy my-api-uat
cerberus resource status my-api-uat
cerberus resource logs my-api-uat --stream stderr --lines 100
cerberus resource stop my-api-uat
cerberus resource apply my-api-uat
```

Use the lifecycle verbs precisely:

- `deploy`: run the declared build, sync the artifact, and activate the service.
- `apply`: sync the currently-built artifact and load/reload the backend; it does not build.
- `reload`: restart/kickstart the currently installed service without building, syncing, or rewriting service definitions.
- `sync`: copy the artifact into the install layout without applying the runtime backend.
- `stop`: stop runtime execution without deleting installed artifact or service state.
- `remove`: unload the service and remove installed runtime artifacts.

Use `stop` for non-destructive stop/pause intent. Do not use `remove` as a casual stop operation; it is uninstall-oriented for `os_service` resources.

## 6. First Recovery Commands

When something is unclear:

```bash
cerberus resource status <resource-id>
cerberus resource doctor <resource-id>
cerberus resource inspect <resource-id>
cerberus resource logs <resource-id> --stream stderr --lines 100
```

For deeper recovery paths, use [local-runtime-troubleshooting.md](local-runtime-troubleshooting.md).
