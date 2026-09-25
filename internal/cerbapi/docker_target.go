package cerbapi

import (
	"fmt"

	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
)

// resolveDockerResource replaces `resource: <id>` with the configured docker
// resource's config. Fields the caller sent alongside it are kept and win, as
// `-f` beats a resource's compose file on the CLI. The key table has already
// refused the target fields to every caller but the operator's own shell.
func (s *ExternalConnectorService) resolveDockerResource(args ExternalConnectorOperationArgs) (ExternalConnectorOperationArgs, error) {
	id := stringFromConfig(args.Config, dockerconn.ResourceKey, "")
	if id == "" {
		return args, nil
	}
	if s.resources == nil {
		return args, externalConnectorError(args, ExternalConnectorUnavailable, fmt.Errorf("no resource configuration is loaded, so docker resource %q cannot be resolved", id))
	}
	def, ok := s.resources(id)
	if !ok {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf("resource %q not found in config; run `cerberus resource list` to see available resources", id))
	}
	if def.Connector != "docker" {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf("resource %q is %s/%s, not a docker resource; try: %s", def.ID, def.Type, def.Connector, UnsupervisedNextStep(def.ID, def.Connector)))
	}

	merged := make(map[string]any, len(def.Config)+len(args.Config)+3)
	for key, value := range def.Config {
		merged[key] = value
	}
	for key, value := range args.Config {
		if key != dockerconn.ResourceKey {
			merged[key] = value
		}
	}
	name := def.Name
	if name == "" {
		name = def.ID
	}
	merged["id"] = def.ID
	merged["name"] = name
	// A resource that names a compose file operates as a stack; only a
	// single-container resource needs a container name inferred for it.
	if _, ok := merged["container"]; !ok && merged["compose_file"] == nil {
		merged["container"] = name
	}
	args.Config = merged
	return args, nil
}
