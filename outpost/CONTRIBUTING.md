# Contributing

Outpost is pre-release software being built in the open. Discuss changes to the
plugin protocol, authentication or process lifecycle in an issue before changing
those contracts.

Use Go 1.26.9 or later. From this directory:

```sh
go mod verify
go mod tidy -diff
go vet ./...
go test -race -count=1 ./...
golangci-lint run
make build
```

CI uses golangci-lint v2.11.4 and runs the Go tests on Linux and macOS. Tests
must use temporary inventories, credentials and state directories. Do not
activate services or enroll a real worker as part of the test suite.

## Boundaries to preserve

- Outpost composes the public app-host, plugin-mcp and auth libraries. It does
  not import Tether or Cerberus; their binaries are independently installed plugins.
- Each pairing authorizes one plugin. Verification reloads the pairing store
  for every request; revocation takes effect without restarting the host.
- Plugin environments are explicit. Bearer credentials stay at the HTTP boundary
  and are never forwarded to plugin processes.
- Plugins execute with the host user's operating-system authority. This is not
  a sandbox or a tenant isolation mechanism.
- Plugin startup and initial health checks must complete before HTTP admission.
  Plugins are required by default. A required failure exits the host; an
  explicitly optional failure disables that plugin and reports degraded
  readiness. Failure to clean up a plugin remains fatal. Deployment acceptance
  requires every configured plugin healthy, even optional plugins.
- Shutdown must preserve independent session shims. Linux supervision uses
  process-only termination. SSH remains the deployment and recovery path.

Use synthetic examples in issues and tests. Follow [SECURITY.md](SECURITY.md)
for private vulnerability reports.
