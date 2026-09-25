package docker

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
)

// TargetKeys are every key that aims an operation at a daemon or a stack.
func TargetKeys() []string {
	return append([]string{TargetHostKey, TargetContextKey}, ComposeFileKeys...)
}
