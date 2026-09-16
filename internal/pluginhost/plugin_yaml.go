package pluginhost

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	plugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// The plugin descriptor is part of the public authoring contract: a plugin
// module outside this repository must be able to build one without importing
// internal/. It lives in pkg/plugin; these aliases keep the host side reading
// as it did.
type (
	PluginYAML          = plugin.PluginYAML
	Entrypoint          = plugin.Entrypoint
	CerberusPluginBlock = plugin.CerberusPluginBlock
)

// PluginYAMLFromManifest builds the descriptor for a subprocess plugin serving
// the given connector manifest.
func PluginYAMLFromManifest(manifest contract.Manifest, entrypoint Entrypoint) PluginYAML {
	return plugin.PluginYAMLFromManifest(manifest, entrypoint)
}
