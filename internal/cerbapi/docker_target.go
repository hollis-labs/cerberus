package cerbapi

import (
	"fmt"
	"sort"
	"strings"

	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
)

// dockerCallerFields is the allow-list for a docker operation's config from a
// socket or web caller: a declared resource, a container on the local daemon,
// and the operation's own fields. Everything else is refused by name — in
// particular every target key (DOCKER_HOST, docker context, and each compose
// file alias), because a compose file chooses images, commands and bind mounts
// and so amounts to code execution on whichever daemon runs it. Ad-hoc targets
// are accepted only in-process, from the operator's own shell
// (`cerberus docker … --host/--context/-f`); a declared resource supplies its
// target from the declaration.
//
// It is built from the connector's own key table, so a new alias is refused
// until someone classifies it as an operation field.
func dockerCallerFields() map[string]bool {
	allowed := map[string]bool{}
	for _, key := range dockerconn.CallerKeys() {
		allowed[key] = true
	}
	return allowed
}

// RefuseAdHocDockerTarget is the socket's and the web console's check on a
// docker operation's caller-supplied config. It runs on the raw request,
// before a configured resource is resolved, so fields that come from the
// resource are unaffected.
func RefuseAdHocDockerTarget(args ExternalConnectorOperationArgs) error {
	if args.Connector != "docker" {
		return nil
	}
	allowed := dockerCallerFields()
	var refused []string
	for key := range args.Config {
		if !allowed[key] {
			refused = append(refused, key)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return externalConnectorError(args, ExternalConnectorInvalidArgs, fmt.Errorf(
		"refusing fields %s: over the socket, the web console and MCP a docker operation takes a configured docker resource (resource=<id>, see `cerberus resource list`) or a local container name; ad-hoc targets (--host, --context, -f) run only from your shell",
		strings.Join(refused, ", ")))
}

// resolveDockerResource replaces `resource: <id>` with the configured docker
// resource's config. Fields the caller sent alongside it are kept and win, as
// `-f` beats a resource's compose file on the CLI; RefuseAdHocDockerTarget has
// already removed the target fields from socket and web callers.
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
