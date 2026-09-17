// Package plugin is the public authoring surface for Cerberus connector
// plugins. A plugin module outside this repository imports it, together with
// pkg/connector and pkg/resource, to declare its plugin.yaml and to name the
// MCP tools the host routes operations through.
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
