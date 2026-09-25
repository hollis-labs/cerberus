package docker

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// The config keys the Docker connector reads, in one table. The connector's
// readers iterate these lists, and each operation's key table (Inputs) is
// built from the same lists, so an alias added here is classified the moment
// it exists: a target or compose key is local-only, refused to the socket,
// the web console and MCP.
var (
	// TargetHostKey and TargetContextKey select the Docker daemon.
	TargetHostKey    = "host"
	TargetContextKey = "context"

	// ComposeFileKeys name a compose file, in the order they are consulted.
	// A compose file chooses images, commands and bind mounts, so it is a
	// target, not an operation field.
	ComposeFileKeys = []string{"compose_file", "composeFile", "file"}

	// ContainerKeys name a container on the target daemon, in the order they
	// are consulted.
	ContainerKeys = []string{"container", "container_id", "container_name", "name"}

	// ResourceKey names a declared docker resource. The admin lane resolves
	// it, and the resource supplies the target: host or context, compose
	// file, container.
	ResourceKey = "resource"
)

func keySchema(key string) map[string]any {
	switch key {
	case ResourceKey:
		return contract.StringSchema("ID of a declared docker resource (type: container, connector: docker; see `cerberus resource list`). Its Docker host or context, compose file and container come from the declaration.")
	case "container":
		return contract.StringSchema("Name or ID of a container on the Docker daemon of the machine running Cerberus.")
	case "id":
		return contract.StringSchema("Identity reported back for the operation. Defaults to the container or resource.")
	case "lines":
		return contract.IntegerSchema("Number of log lines to return. Default 50.")
	case TargetHostKey:
		return contract.StringSchema("Docker daemon to target, as a DOCKER_HOST value. Your shell only: `--host`.")
	case TargetContextKey:
		return contract.StringSchema("Docker context to target. Your shell only: `--context`.")
	}
	for _, alias := range ComposeFileKeys {
		if key == alias {
			return contract.StringSchema("Compose file for the stack. Your shell only: `-f`.")
		}
	}
	return contract.StringSchema("Alias of container.")
}

// targetInputs are the ad-hoc target keys, accepted only from the operator's
// shell. Over the socket, the web console and MCP a docker operation takes a
// declared resource instead, because a compose file chooses images, commands
// and bind mounts and so amounts to code execution on whichever daemon runs
// it.
func targetInputs() []contract.Input {
	inputs := []contract.Input{
		contract.Field(TargetHostKey, keySchema(TargetHostKey)).LocalOnly(),
		contract.Field(TargetContextKey, keySchema(TargetContextKey)).LocalOnly(),
	}
	for _, key := range ComposeFileKeys {
		inputs = append(inputs, contract.Field(key, keySchema(key)).LocalOnly())
	}
	return inputs
}

// daemonInputs cover an operation over a whole daemon: a declared resource,
// or an ad-hoc target from the operator's shell.
func daemonInputs() []contract.Input {
	return append([]contract.Input{contract.Field(ResourceKey, keySchema(ResourceKey))}, targetInputs()...)
}

// containerInputs cover an operation on one container or stack: a declared
// resource, or a container under any of its keys, plus extra operation keys.
func containerInputs(extra ...string) []contract.Input {
	inputs := []contract.Input{contract.Field(ResourceKey, keySchema(ResourceKey))}
	for _, key := range ContainerKeys {
		inputs = append(inputs, contract.Field(key, keySchema(key)))
	}
	for _, key := range append([]string{"id"}, extra...) {
		inputs = append(inputs, contract.Field(key, keySchema(key)))
	}
	return append(inputs, targetInputs()...)
}

// containerTarget is what names a container or stack, in the order the
// connector consults it. At least one must be present.
func containerTarget() []string {
	keys := append([]string{ResourceKey}, ContainerKeys...)
	return append(keys, ComposeFileKeys...)
}

// TargetKeys are every key that aims an operation at a daemon or a stack.
func TargetKeys() []string {
	return append([]string{TargetHostKey, TargetContextKey}, ComposeFileKeys...)
}
