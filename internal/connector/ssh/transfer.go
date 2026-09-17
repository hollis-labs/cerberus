package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	sftpsync "github.com/hollis-labs/go-sftpsync"
	"github.com/pkg/sftp"
)

// sftpSession opens an SFTP subsystem over the existing SSH connection. The
// connection is reused rather than dialed again, so a transfer costs one
// channel, not another handshake.
func (a *APIBackend) sftpSession() (*sftp.Client, error) {
	if a.client == nil {
		return nil, errors.New("ssh: not connected")
	}
	client, err := sftp.NewClient(a.client)
	if err != nil {
		return nil, fmt.Errorf("open sftp subsystem: %w", err)
	}
	return client, nil
}

// Put copies a local file to the remote host.
//
// The write goes to a temporary name in the destination directory and is then
// renamed over the target. A deploy that dies midway therefore leaves the
// previous file intact instead of a truncated one — which matters when the
// target is a compose file or an env file that something is about to read.
func (a *APIBackend) Put(ctx context.Context, localPath, remotePath string) (int64, error) {
	local, err := os.Open(localPath) //nolint:gosec // operator-supplied path
	if err != nil {
		return 0, fmt.Errorf("open local file %s: %w", localPath, err)
	}
	defer local.Close() //nolint:errcheck

	info, err := local.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat local file %s: %w", localPath, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%s is a directory; ssh put transfers a single file", localPath)
	}

	client, err := a.sftpSession()
	if err != nil {
		return 0, err
	}
	defer client.Close() //nolint:errcheck

	tempPath := fmt.Sprintf("%s.cerberus-%d.tmp", remotePath, os.Getpid())
	remote, err := client.Create(tempPath)
	if err != nil {
		return 0, fmt.Errorf("create remote file %s: %w", tempPath, err)
	}

	written, copyErr := copyWithContext(ctx, remote, local)
	closeErr := remote.Close()
	if copyErr != nil {
		_ = client.Remove(tempPath)
		return 0, fmt.Errorf("write remote file %s: %w", remotePath, copyErr)
	}
	if closeErr != nil {
		_ = client.Remove(tempPath)
		return 0, fmt.Errorf("close remote file %s: %w", tempPath, closeErr)
	}

	// Preserve the local mode so an uploaded script stays executable; SFTP
	// creates with a default mode that would not.
	if err := client.Chmod(tempPath, info.Mode().Perm()); err != nil {
		_ = client.Remove(tempPath)
		return 0, fmt.Errorf("chmod remote file %s: %w", tempPath, err)
	}

	// Rename does not overwrite on every SFTP server, so clear the target
	// first. PosixRename is atomic where the extension is supported.
	if err := client.PosixRename(tempPath, remotePath); err != nil {
		_ = client.Remove(remotePath)
		if renameErr := client.Rename(tempPath, remotePath); renameErr != nil {
			_ = client.Remove(tempPath)
			return 0, fmt.Errorf("install remote file %s: %w", remotePath, renameErr)
		}
	}

	return written, nil
}

// Get copies a file from the remote host to a local path.
func (a *APIBackend) Get(ctx context.Context, remotePath, localPath string) (int64, error) {
	client, err := a.sftpSession()
	if err != nil {
		return 0, err
	}
	defer client.Close() //nolint:errcheck

	remote, err := client.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("open remote file %s: %w", remotePath, err)
	}
	defer remote.Close() //nolint:errcheck

	if info, statErr := remote.Stat(); statErr == nil && info.IsDir() {
		return 0, fmt.Errorf("%s is a directory; ssh get transfers a single file", remotePath)
	}

	// Same rename dance locally: a failed download must not replace a good file
	// with a partial one.
	tempPath := fmt.Sprintf("%s.cerberus-%d.tmp", localPath, os.Getpid())
	local, err := os.Create(tempPath) //nolint:gosec // operator-supplied path
	if err != nil {
		return 0, fmt.Errorf("create local file %s: %w", tempPath, err)
	}

	written, copyErr := copyWithContext(ctx, local, remote)
	closeErr := local.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return 0, fmt.Errorf("write local file %s: %w", localPath, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return 0, fmt.Errorf("close local file %s: %w", tempPath, closeErr)
	}
	if err := os.Rename(tempPath, localPath); err != nil {
		_ = os.Remove(tempPath)
		return 0, fmt.Errorf("install local file %s: %w", localPath, err)
	}

	return written, nil
}

// copyWithContext copies src to dst, aborting if the context is canceled.
// io.Copy alone ignores cancellation, so a transfer over a dead VPN would hang
// until the TCP stack noticed rather than honoring the caller's timeout.
func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return io.Copy(dst, readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return src.Read(p)
	}))
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// PutDir recursively uploads a local directory tree.
//
// The walk, the escape check, the temp-and-rename per file and the
// between-files cancellation all live in go-sftpsync; this is the binding.
// Recursion is not reimplemented here on purpose — see WP-9 in
// docs/plans/connector-work-packages.md for why that library exists.
func (a *APIBackend) PutDir(ctx context.Context, localDir, remoteDir string) (*DirTransferResult, error) {
	client, err := a.sftpSession()
	if err != nil {
		return nil, err
	}
	defer client.Close() //nolint:errcheck

	res, err := sftpsync.Upload(ctx, client, localDir, remoteDir)
	if err != nil {
		return nil, dirTransferError("upload", localDir, remoteDir, res, err)
	}
	return dirTransferResultFrom(res, localDir, remoteDir), nil
}

// GetDir recursively downloads a remote directory tree. It is the same walk as
// PutDir with the ends exchanged, including the refusal to follow a symlink
// out of the tree — a download is where "it is only reading" quietly becomes
// "it wrote outside the destination".
func (a *APIBackend) GetDir(ctx context.Context, remoteDir, localDir string) (*DirTransferResult, error) {
	client, err := a.sftpSession()
	if err != nil {
		return nil, err
	}
	defer client.Close() //nolint:errcheck

	res, err := sftpsync.Download(ctx, client, remoteDir, localDir)
	if err != nil {
		return nil, dirTransferError("download", localDir, remoteDir, res, err)
	}
	return dirTransferResultFrom(res, localDir, remoteDir), nil
}

// dirTransferError folds how far the transfer got into the message.
//
// go-sftpsync returns a partial result alongside its error, but the connector
// service drops the result whenever the error is non-nil, so an operator would
// otherwise be told only that it failed. "Nothing moved" and "half the tree
// moved" are different situations to recover from, and a failed sync is
// exactly when that difference matters.
func dirTransferError(direction, localDir, remoteDir string, res *sftpsync.Result, err error) error {
	prefix := fmt.Sprintf("ssh %s %s <-> %s", direction, localDir, remoteDir)
	if res == nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	if errors.Is(err, sftpsync.ErrSymlinkEscape) {
		// Named rather than left to read as an I/O failure: an escape is a
		// property of the tree, so retrying will not fix it and the operator
		// has to decide what to do about the link.
		return fmt.Errorf("%s: refused after %d files, %d dirs, %d bytes: %w",
			prefix, res.Files, res.Dirs, res.Bytes, err)
	}
	return fmt.Errorf("%s: stopped after %d files, %d dirs, %d bytes: %w",
		prefix, res.Files, res.Dirs, res.Bytes, err)
}
