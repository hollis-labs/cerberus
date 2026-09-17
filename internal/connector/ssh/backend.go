package ssh

import "context"

// Backend defines how the SSH connector communicates with remote hosts.
type Backend interface {
	// Connect establishes an SSH connection to the given host.
	Connect(ctx context.Context, host string, port int, user string, keyFile string, hostKey HostKeyConfig) error

	// Exec runs a command on the connected remote host.
	Exec(ctx context.Context, command string) (*ExecResult, error)

	// Ping verifies the connection is alive by running a simple command.
	Ping(ctx context.Context) error

	// Put copies a local file to remotePath on the connected host, creating or
	// truncating it, and returns the number of bytes written.
	Put(ctx context.Context, localPath, remotePath string) (int64, error)

	// Get copies remotePath from the connected host to localPath, creating or
	// truncating it, and returns the number of bytes written.
	Get(ctx context.Context, remotePath, localPath string) (int64, error)

	// PutDir recursively copies the localDir tree to remoteDir on the
	// connected host, and reports what it moved.
	PutDir(ctx context.Context, localDir, remoteDir string) (*DirTransferResult, error)

	// GetDir recursively copies the remoteDir tree from the connected host to
	// localDir, and reports what it moved.
	GetDir(ctx context.Context, remoteDir, localDir string) (*DirTransferResult, error)

	// Close terminates the SSH connection.
	Close() error
}
