package docker

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// The config keys the Docker connector reads, in one table. The readers below
// iterate these lists, and the admin lane's socket/web check is built from
// the same lists, so an alias added here is classified the moment it exists:
// a target or compose key is refused from socket and web callers, and only an
// operation key is let through.
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

	// OperationKeys are the other operation fields: the resource identity
	// the admin lane fills in, and logs' line count.
	OperationKeys = []string{"id", "lines"}

	// ResourceKey names a declared docker resource. The admin lane resolves
	// it, and the resource supplies the target: host or context, compose
	// file, container.
	ResourceKey = "resource"
)

// CallerKeys are every key a socket, web or MCP caller may send: the
// enforcement allow-list, and the union of the operations' input schemas.
func CallerKeys() []string {
	keys := []string{ResourceKey}
	keys = append(keys, ContainerKeys...)
	return append(keys, OperationKeys...)
}

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
	default:
		return contract.StringSchema("Alias of container.")
	}
}

// operationSchema is an object schema over the given caller keys. Ad-hoc
// targets (--host, --context, -f) are not in it: they run only from the
// operator's shell, and the socket refuses them.
func operationSchema(keys ...string) map[string]any {
	props := make(map[string]any, len(keys))
	for _, key := range keys {
		props[key] = keySchema(key)
	}
	return contract.ObjectSchema(props)
}

// containerOperationSchema covers the operations that act on one container
// or stack: a declared resource, or a local container under any of its keys.
func containerOperationSchema(extra ...string) map[string]any {
	keys := append([]string{ResourceKey}, ContainerKeys...)
	keys = append(keys, "id")
	return operationSchema(append(keys, extra...)...)
}

// TargetKeys are every key that aims an operation at a daemon or a stack.
func TargetKeys() []string {
	return append([]string{TargetHostKey, TargetContextKey}, ComposeFileKeys...)
}
