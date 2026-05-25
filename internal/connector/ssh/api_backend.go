package ssh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// APIBackend implements Backend using the golang.org/x/crypto/ssh library.
type APIBackend struct {
	client *ssh.Client
}

// NewAPIBackend creates an unconnected APIBackend. Call Connect before use.
func NewAPIBackend() *APIBackend {
	return &APIBackend{}
}

func (a *APIBackend) Connect(ctx context.Context, host string, port int, user string, keyFile string, hostKey HostKeyConfig) error {
	keyData, err := os.ReadFile(keyFile) //nolint:gosec // keyFile is from user config, not external input
	if err != nil {
		return fmt.Errorf("reading SSH key %s: %w", keyFile, err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("parsing SSH key %s: %w", keyFile, err)
	}

	hostKeyCallback, err := hostKey.callback(host, port)
	if err != nil {
		return fmt.Errorf("configure SSH host key verification for %s:%d: %w", host, port, err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("ssh connect to %s: %w", addr, err)
	}

	type handshakeResult struct {
		client *ssh.Client
		err    error
	}
	ch := make(chan handshakeResult, 1)
	go func() {
		clientConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
		if err != nil {
			ch <- handshakeResult{err: err}
			return
		}
		ch <- handshakeResult{client: ssh.NewClient(clientConn, chans, reqs)}
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		return fmt.Errorf("ssh connect to %s: %w", addr, ctx.Err())
	case res := <-ch:
		if res.err != nil {
			_ = conn.Close()
			return fmt.Errorf("ssh connect to %s: %w", addr, res.err)
		}
		a.client = res.client
		return nil
	}
}

func (a *APIBackend) Exec(ctx context.Context, command string) (*ExecResult, error) {
	if a.client == nil {
		return nil, fmt.Errorf("ssh exec: not connected")
	}

	session, err := a.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh new session: %w", err)
	}
	defer session.Close() //nolint:errcheck

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run with context cancellation support.
	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return nil, fmt.Errorf("ssh exec %q: %w", command, ctx.Err())
	case err := <-done:
		result := &ExecResult{
			Stdout:   strings.TrimRight(stdout.String(), "\n"),
			Stderr:   strings.TrimRight(stderr.String(), "\n"),
			ExitCode: 0,
		}
		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok { //nolint:errorlint
				result.ExitCode = exitErr.ExitStatus()
			} else {
				return nil, fmt.Errorf("ssh exec %q: %w", command, err)
			}
		}
		return result, nil
	}
}

func (a *APIBackend) Ping(ctx context.Context) error {
	result, err := a.Exec(ctx, "echo pong")
	if err != nil {
		return fmt.Errorf("ssh ping: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("ssh ping: exit code %d", result.ExitCode)
	}
	return nil
}

func (a *APIBackend) Close() error {
	if a.client == nil {
		return nil
	}
	err := a.client.Close()
	a.client = nil
	return err
}
