package ssh

import "context"

// Backend defines how the SSH connector communicates with remote hosts.
type Backend interface {
	// Connect establishes an SSH connection to the given host.
	Connect(ctx context.Context, host string, port int, user string, keyFile string) error

	// Exec runs a command on the connected remote host.
	Exec(ctx context.Context, command string) (*ExecResult, error)

	// Ping verifies the connection is alive by running a simple command.
	Ping(ctx context.Context) error

	// Close terminates the SSH connection.
	Close() error
}
