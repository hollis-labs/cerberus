# Unit renderer parity

Each directory is a synthetic descriptor shape (`resource.yaml`, one resource as in a
`*.cerberus.yaml`) and the unit the **old** renderer (on `main`) produces for it
(`unit.golden`; its first line names the unit). `parity_test.go` renders each with this
branch's renderer and requires byte equality. The environment is fixed: HOME `/u/op`, the
serving daemon's PATH `/u/op/.local/bin:/usr/local/bin:/usr/bin:/bin`, cerberus
`/u/op/.local/bin/cerberus`.

To regenerate after a deliberate change on `main`, follow `golden_gen_on_main.go.txt`.

## Real units (rehearsal only, never committed)

`TestRendererMatchesRealUnits` reads `$CERBERUS_PARITY_FIXTURES` when it is set:

    <dir>/env.yaml              home: <HOME>, path: <the old daemon's PATH>, cerberus: <its executable>
    <dir>/<case>/resource.yaml  the resource as the registry resolves it
    <dir>/<case>/unit           the unit file on disk, verbatim
    <dir>/<case>/name           the unit file's name

The fixtures are taken from the operator host at rehearsal. They name real hosts, paths and
environment values, so they never enter this public repository.
