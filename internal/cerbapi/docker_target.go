package cerbapi

import (
	"fmt"
	"sort"
	"strings"
)

// dockerAdHocTargetFields aim a docker operation at a daemon or a stack the
// operator did not declare: a DOCKER_HOST, a docker context, or a compose file
// (which chooses images, commands and host bind mounts, so it amounts to code
// execution on whichever daemon runs it). They are accepted only in-process,
// from the operator's own shell (`cerberus docker … --host/--context/-f`).
// Over the socket and the web console a docker operation names a configured
// resource instead, and its target is resolved from there.
var dockerAdHocTargetFields = []string{"host", "context", "docker_host", "docker_context", "compose_file"}

// RefuseAdHocDockerTarget is the socket's and the web console's check on a
// docker operation's caller-supplied config. It runs on the raw request,
// before a configured resource is resolved, so fields that come from the
// resource are unaffected.
func RefuseAdHocDockerTarget(args ExternalConnectorOperationArgs) error {
	if args.Connector != "docker" {
		return nil
	}
	var refused []string
	for _, key := range dockerAdHocTargetFields {
		if _, ok := args.Config[key]; ok {
			refused = append(refused, key)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf(
		"refusing fields %s: ad-hoc docker targets (--host, --context, -f) run only from your shell; over the socket, the web console and MCP, name a configured docker resource with resource=<id> (see `cerberus resource list`)",
		strings.Join(refused, ", ")))
}

// resolveDockerResource replaces `resource: <id>` with the configured docker
// resource's config. Fields the caller sent alongside it are kept and win, as
// `-f` beats a resource's compose file on the CLI; RefuseAdHocDockerTarget has
// already removed the target fields from socket and web callers.
func (s *ExternalConnectorService) resolveDockerResource(args ExternalConnectorOperationArgs) (ExternalConnectorOperationArgs, error) {
	id := stringFromConfig(args.Config, "resource", "")
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
		if key != "resource" {
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
