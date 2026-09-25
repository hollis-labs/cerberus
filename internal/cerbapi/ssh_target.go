package cerbapi

import (
	"errors"
	"fmt"

	"github.com/hollis-labs/cerberus/internal/config"
)

// ResourceLookup returns the configured resource with the given id, from
// whichever config the serving process resolved.
type ResourceLookup func(id string) (*config.ResourceDef, bool)

// SetResourceLookup gives the service the resources SSH operations resolve
// against. Without one, SSH operations are refused.
func (s *ExternalConnectorService) SetResourceLookup(lookup ResourceLookup) {
	if s != nil {
		s.resources = lookup
	}
}

// ConfigResourceLookup looks resources up in a fixed, already-resolved config.
func ConfigResourceLookup(cfg *config.ConfigV2) ResourceLookup {
	return func(id string) (*config.ResourceDef, bool) {
		def := findResourceDef(cfg, id)
		return def, def != nil
	}
}

// resolveSSHTarget merges the configured resource's connection settings into
// an SSH operation's config. The key table has already refused every field
// but the operation's own (sshconn.OperationFields), on every surface
// including the in-process CLI, so the host, key and trust settings can only
// come from the resource.
func (s *ExternalConnectorService) resolveSSHTarget(args ExternalConnectorOperationArgs) (ExternalConnectorOperationArgs, error) {
	id := stringFromConfig(args.Config, "id", "")
	if id == "" {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, errors.New("id is required: pass a configured ssh resource id (see `cerberus resource list`)"))
	}
	if s.resources == nil {
		return args, externalConnectorError(args, ExternalConnectorUnavailable, errors.New("no resource configuration is loaded, so ssh resource ids cannot be resolved"))
	}
	def, ok := s.resources(id)
	if !ok {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf("resource %q not found in config; run `cerberus resource list` to see available resources", id))
	}
	if def.Connector != "ssh" {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf("resource %q uses connector %q, not ssh", id, def.Connector))
	}

	merged := make(map[string]any, len(def.Config)+len(args.Config)+2)
	for key, value := range def.Config {
		merged[key] = value
	}
	for key, value := range args.Config {
		merged[key] = value
	}
	merged["id"] = def.ID
	merged["name"] = def.Name
	args.Config = merged
	return args, nil
}
