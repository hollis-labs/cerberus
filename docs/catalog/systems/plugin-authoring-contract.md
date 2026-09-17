---
id: "CERB-CAP-304"
class: "capability"
name: "Plugin authoring contract (pkg/plugin)"
summary: "The public plugin.yaml schema, entrypoint rules, MCP tool naming and manifest validation an out-of-repo plugin module compiles against."
state_field: "maturity"
state_label: "shipped"
review_status: "draft"
confidence_score: 0.85
confidence_label: "Contract read in full and both real plugins compile against it; the manifest env field is documented but unused by the host"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "pkg/plugin/"
tags:
  - "plugin"
  - "contract"
  - "manifest"
  - "pkg"
  - "cerberus"
  - "class:capability"
  - "locus:core"
relationships:
  - type: "implements"
    target: "CERB-DEC-358"
    note: "the authoring half is public, the host half stays internal"
  - type: "relates_to"
    target: "CERB-CAP-301"
    note: "the host installs and launches what this defines"
  - type: "blocks"
    target: "CERB-GAP-339"
    note: "manifest secrets[].env is validated but never resolved through"
  - type: "blocks"
    target: "CERB-GAP-340"
    note: "no stability guarantee on the authoring contract"
---

# Plugin authoring contract (pkg/plugin)

`pkg/plugin` is the public authoring surface. A plugin module outside this
repository imports it together with `pkg/connector` and `pkg/resource`, and
nothing else: the manager, installer, trust policy and launcher stay in
`internal/pluginhost` because they are host decisions and must not be something
a plugin can influence. `internal/pluginhost/plugin_yaml.go` is type aliases
back onto `pkg/plugin` so the host side reads as it always did.

The contract is four things. `PluginYAML` — `schema_version`, `id`, `version`,
`protocol` (must be `plugin-sdk/subprocess`), `runtime` (must be `subprocess`),
`entrypoint`, and a `cerberus.connector` block holding the connector manifest.
`Entrypoint` — a command relative to the plugin directory, validated to reject
shell strings (anything containing `` \t\n\r;&|`$<> `` or a space), absolute
paths, and anything that cleans to `.` or escapes upward; `ResolveEntrypoint`
re-validates at launch and additionally stats the file, refuses a directory and
refuses a non-executable mode, so there is no shell and no `PATH` lookup
anywhere in the launch path. `ToolNameForOperation` — `cerberus_<id>_<op>`, the
name a plugin must serve from its `MCPCallTool` handler, in `pkg/plugin`
precisely because it is the host/plugin rendezvous. And `SecretFromConfig`, the
one-line reader for a credential the host resolved.

Validation reports every problem at once rather than the first. `PluginYAML.Validate`
checks the envelope and requires `plugin.id == cerberus.connector.id`;
`Manifest.Validate` requires at least one resource type and one operation, an
`input_schema` on every operation, `requires_ack: true` on every `destructive`
operation, unique field/secret/operation names, and — a detail that repays
reading — refuses a secret whose name collides with a config field, because both
land in the same `plugin/init` map and which one won would depend on map
ordering. It also requires every secret to declare either `required: true` or an
`env` fallback.

That `env` field is where the contract and the host disagree. Nothing resolves
through it: `resolvePluginSecrets` uses `<plugin id>/<secret name>` and the
provider's own `CERBERUS_<ID>_<NAME>` convention, and `secret.Env` is read only
by the validator that insists it be present. Both shipped plugins declare names
the host does not read — `env: CONTEXTFORGE_TOKEN` and `env: AZURE_CLIENT_SECRET`
against an actual `CERBERUS_CONTEXTFORGE_TOKEN` and
`CERBERUS_AZURE_CLIENT_SECRET`. The ContextForge error text is careful about
this ("set `CONTEXTFORGE_TOKEN` when running the plugin directly") but an
operator reading the manifest alone would get it wrong.

`cerberus connectors write-plugin-prototype docker <dir>` is the worked example.
It writes a `plugin.yaml` generated from the compiled-in Docker connector's own
definition — same operations, same schemas, including `destroy` with
`destructive: true, requires_ack: true` — plus an empty `bin/`. With
`--build-binary` it also runs `go build ./cmd/cerberus-docker-plugin` into that
`bin/`; without it the directory is a manifest scaffold, and installing it fails
at the hash step with `hash plugin entrypoint …: no such file or directory`.
Note what the prototype is and is not: `docker` is a reserved id, so its output
can never be installed into the managed lane, which is exactly why the one-shot
`plugin exec` lane does not reserve ids. `internal/plugins/dockerplugin` — the
package, distinct from `internal/pluginhost` — is the in-repo reference plugin
that prototype is generated from: the host half is `pluginhost`, the
example-plugin half is `plugins`.
