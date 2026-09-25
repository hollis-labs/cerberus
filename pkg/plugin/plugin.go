// Package plugin is the public authoring surface for Cerberus connector
// plugins. A plugin module outside this repository imports it, together with
// pkg/connector and pkg/resource, to declare its plugin.yaml and to name the
// MCP tools the host routes operations through.
//
// Operation metadata lives in pkg/connector's ManifestOperation. Two fields
// govern what the host lets through: destructive (always acknowledgment-gated)
// and supports_dry (a dry run is refused unless it is set). requires_ack is
// deprecated and ignored for gating; keep setting it on destructive operations
// for older hosts.
//
// The host side — the manager, installer, trust policy and subprocess
// launcher — deliberately stays in internal/pluginhost. Those are host
// decisions and must not be something a plugin can influence.
package plugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// ToolNameForOperation creates the plugin-sdk MCP tool name Cerberus uses to
// route a connector operation through a subprocess plugin. A plugin serves the
// names this returns from its MCPCallTool handler.
func ToolNameForOperation(connectorID, operation string) string {
	return "cerberus_" + connectorID + "_" + operation
}

// OperationFromToolName resolves an MCP tool name back to the manifest
// operation it was derived from.
func OperationFromToolName(connectorID, toolName string, manifest contract.Manifest) (contract.ManifestOperation, bool) {
	for _, op := range manifest.Operations {
		if toolName == ToolNameForOperation(connectorID, op.Name) {
			return op, true
		}
	}
	return contract.ManifestOperation{}, false
}

// SecretFromConfig reads a credential the host resolved on the plugin's behalf
// out of the Init config map.
//
// The host resolves every secret a plugin's manifest declares — through the
// same process-env / connector-secrets.yaml / keychain chain a built-in
// connector uses — and passes the values in SDK init params, keyed by the
// manifest secret name. A plugin declaring `token` reads it as
// SecretFromConfig(params.Config, "token").
//
// Two properties of that channel a plugin can rely on:
//
//   - A plugin receives only the secrets its own manifest declares, never the
//     operator's store.
//   - Credentials do not travel in the environment. The host launches plugins
//     with an allow-listed environment that carries none, because env is
//     ambient and would reach every plugin rather than the one that asked.
//
// A secret that did not resolve is absent rather than empty-but-present, and
// the plugin still loads. Report the failure from the operation that needed
// it; the host adds the actionable "set CERBERUS_<ID>_<NAME>, or …" guidance
// on the way out.
//
// Values arrive at Init and are not refreshed: an operator who adds or rotates
// a credential reloads the plugin to pick it up.
//
// A plugin built on plugin-sdk can instead wrap the same map with
// subprocess.NewConfigReader(params.Config) and read it through
// ConfigReader.Secret, which additionally registers the value with the SDK
// logger's redaction tracker. Prefer that where it is available; this helper is
// for plugins that hold the map directly.
func SecretFromConfig(config map[string]string, name string) (string, bool) {
	value, ok := config[name]
	if !ok || value == "" {
		return "", false
	}
	return value, true
}
