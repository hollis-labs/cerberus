package cerbapi

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
)

// ResourceLookup returns the configured resource with the given id, from
// whichever config the serving process resolved.
type ResourceLookup func(id string) (*config.ResourceDef, bool)

// sshOperationFields are the only keys an SSH operation's Config may carry.
// Everything that decides where and how to connect comes from the configured
// resource named by id, so no caller can aim the daemon's SSH at a host, key
// or trust setting the operator did not configure.
var sshOperationFields = map[string]bool{
	"id":          true,
	"command":     true,
	"local_path":  true,
	"remote_path": true,
}

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

// resolveSSHTarget replaces an SSH operation's caller-supplied Config with
// the configured resource's, keeping only the operation's own fields. It is
// the one resolver every surface shares: socket, web, MCP and the in-process
// CLI all send an id and nothing else about the target.
func (s *ExternalConnectorService) resolveSSHTarget(args ExternalConnectorOperationArgs) (ExternalConnectorOperationArgs, error) {
	var refused []string
	for key := range args.Config {
		if !sshOperationFields[key] {
			refused = append(refused, key)
		}
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf(
			"refusing fields %s: an ssh operation takes a configured resource id, not connection settings; pass id=<resource-id> (see `cerberus resource list`) and set host, user and keys on the resource",
			strings.Join(refused, ", ")))
	}
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
