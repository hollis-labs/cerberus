package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sftpsync "github.com/hollis-labs/go-sftpsync"
	"github.com/pkg/sftp"
)

// newLoopbackSFTPClient wires an *sftp.Client to pkg/sftp's own server over a
// net.Pipe. That server serves the real filesystem, so the "remote" side is an
// ordinary temp directory — which is the point: the walk under test runs over
// the SFTP protocol rather than over a mock of it, with no network, no sshd
// and no external sftp-server binary.
func newLoopbackSFTPClient(t *testing.T) *sftp.Client {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatalf("sftp.NewServer: %v", err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		if serveErr := server.Serve(); serveErr != nil && !errors.Is(serveErr, io.EOF) {
			t.Logf("sftp server stopped: %v", serveErr)
		}
	}()

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("sftp.NewClientPipe: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		_ = serverConn.Close()
		<-served
	})
	return client
}

// writeSampleTree lays down the shape WP-9 names: nested subdirectories, an
// executable script, an empty directory and a symlink that stays inside.
func writeSampleTree(t *testing.T, root string) {
	t.Helper()
	mkdir := func(rel string, mode os.FileMode) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		// Created tight and chmodded to the intended mode below: the mode
		// is what these tests assert, so it is set explicitly rather than
		// left to MkdirAll and the umask.
		if err := os.MkdirAll(full, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatalf("chmod %s: %v", rel, err)
		}
	}
	write := func(rel, content string, mode os.FileMode) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir parent of %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatalf("chmod %s: %v", rel, err)
		}
	}

	write("compose.yml", "services: {}\n", 0o644)
	write("bin/deploy.sh", "#!/bin/sh\necho deploying\n", 0o755)
	write("conf/app/settings.toml", "debug = false\n", 0o640)
	mkdir("conf/empty", 0o755)
	if err := os.Symlink("../compose.yml", filepath.Join(root, "conf", "compose.yml")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

// The preview cannot run go-sftpsync's dry run — it has no client at the point
// a preview is produced — so it walks the tree itself. This is the test that
// keeps the two walks reporting the same thing: change one rule in either
// place and the counts stop matching.
func TestPreviewDirUploadMatchesLibraryDryRun(t *testing.T) {
	src := t.TempDir()
	writeSampleTree(t, src)
	dst := filepath.Join(t.TempDir(), "target")

	res, err := sftpsync.Upload(context.Background(), newLoopbackSFTPClient(t), src, dst, sftpsync.WithDryRun(true))
	if err != nil {
		t.Fatalf("library dry run: %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("dry run created %s", dst)
	}

	preview := PreviewDirUpload(src)
	if len(preview.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", preview.Warnings)
	}
	if preview.Files != res.Files || preview.Dirs != res.Dirs ||
		preview.Symlinks != res.Symlinks || preview.Skipped != res.Skipped ||
		preview.Bytes != res.Bytes {
		t.Fatalf("preview %+v disagrees with library files=%d dirs=%d symlinks=%d skipped=%d bytes=%d",
			preview, res.Files, res.Dirs, res.Symlinks, res.Skipped, res.Bytes)
	}
	if preview.Files != 3 || preview.Dirs != 5 || preview.Symlinks != 1 {
		t.Fatalf("preview = %+v, want 3 files, 5 dirs, 1 symlink", preview)
	}
}

// A preview that stayed silent about a link the transfer is going to refuse
// would send an operator to acknowledge a sync that cannot finish.
func TestPreviewDirUploadWarnsWhereTheTransferRefuses(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "app.env"), []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "leak")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	preview := PreviewDirUpload(src)
	if preview.Symlinks != 0 {
		t.Fatalf("Symlinks = %d, want 0 — an escaping link is not transferred", preview.Symlinks)
	}
	if len(preview.Warnings) != 1 || !strings.Contains(preview.Warnings[0], "leak") ||
		!strings.Contains(preview.Warnings[0], "/etc/passwd") {
		t.Fatalf("Warnings = %v, want one naming the link and its target", preview.Warnings)
	}

	_, err := sftpsync.Upload(context.Background(), newLoopbackSFTPClient(t), src,
		filepath.Join(t.TempDir(), "target"))
	if !errors.Is(err, sftpsync.ErrSymlinkEscape) {
		t.Fatalf("transfer err = %v, want ErrSymlinkEscape — the preview promised a refusal", err)
	}
}

func TestPreviewDirUploadRejectsANonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	preview := PreviewDirUpload(file)
	if preview.Files != 0 || preview.Dirs != 0 {
		t.Fatalf("preview = %+v, want nothing counted", preview)
	}
	if len(preview.Warnings) != 1 || !strings.Contains(preview.Warnings[0], "ssh put") {
		t.Fatalf("Warnings = %v, want one naming the single-file command", preview.Warnings)
	}
}

// The DTO is mapped from a real transfer rather than a hand-built Result, so
// this covers what the library actually produces.
func TestDirTransferResultFromDescribesARealTransfer(t *testing.T) {
	src := t.TempDir()
	writeSampleTree(t, src)
	dst := filepath.Join(t.TempDir(), "target")

	res, err := sftpsync.Upload(context.Background(), newLoopbackSFTPClient(t), src, dst)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	dto := dirTransferResultFrom(res, src, dst)
	if dto.Direction != "upload" || dto.LocalPath != src || dto.RemotePath != dst {
		t.Fatalf("dto ends = %q %q -> %q", dto.Direction, dto.LocalPath, dto.RemotePath)
	}
	if dto.DryRun {
		t.Fatal("DryRun set on a real transfer")
	}
	if dto.Files != 3 || dto.Dirs != 5 || dto.Symlinks != 1 {
		t.Fatalf("dto = %+v, want 3 files, 5 dirs, 1 symlink", dto)
	}
	if len(dto.Entries) != res.Files+res.Dirs+res.Symlinks+res.Skipped {
		t.Fatalf("Entries = %d, want one per counted entry", len(dto.Entries))
	}

	byPath := map[string]DirTransferEntry{}
	for _, e := range dto.Entries {
		byPath[e.Path] = e
	}
	// Permission bits are rendered, not emitted as the integer os.FileMode
	// marshals to — an operator reading CLI or MCP output needs "-rwxr-xr-x".
	if got := byPath["bin/deploy.sh"]; got.Action != "file" || got.Mode != "-rwxr-xr-x" {
		t.Fatalf("bin/deploy.sh = %+v, want an executable file", got)
	}
	if got := byPath["conf/app/settings.toml"]; got.Mode != "-rw-r-----" || got.Size == 0 {
		t.Fatalf("conf/app/settings.toml = %+v", got)
	}
	// A symlink's own bits are not portable and nothing chmods them, so the
	// DTO must not claim a mode it never applied.
	if got := byPath["conf/compose.yml"]; got.Action != "symlink" || got.Target != "../compose.yml" || got.Mode != "" {
		t.Fatalf("conf/compose.yml = %+v, want a symlink with no mode", got)
	}

	// The transfer really happened: the executable arrived executable.
	info, err := os.Stat(filepath.Join(dst, "bin", "deploy.sh"))
	if err != nil {
		t.Fatalf("stat uploaded script: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("uploaded script mode = %v, want 0755", info.Mode().Perm())
	}
}

// go-sftpsync returns a partial result alongside its error and the connector
// service drops the result when the error is non-nil, so how far the transfer
// got has to survive in the message.
func TestDirTransferErrorCarriesHowFarItGot(t *testing.T) {
	res := &sftpsync.Result{Files: 4, Dirs: 2, Bytes: 8192}

	escape := dirTransferError("upload", "/local/deploy", "/opt/deploy", res,
		&sftpsync.PathError{Op: "symlink", Side: sftpsync.Local, Path: "/local/deploy/leak", Err: sftpsync.ErrSymlinkEscape})
	if !errors.Is(escape, sftpsync.ErrSymlinkEscape) {
		t.Fatalf("escape no longer answers to ErrSymlinkEscape: %v", escape)
	}
	for _, want := range []string{"refused", "4 files", "2 dirs", "8192 bytes", "/opt/deploy"} {
		if !strings.Contains(escape.Error(), want) {
			t.Fatalf("error %q missing %q", escape, want)
		}
	}

	stopped := dirTransferError("download", "/local/conf", "/opt/conf", res, errors.New("connection lost"))
	if !strings.Contains(stopped.Error(), "stopped after 4 files") {
		t.Fatalf("error = %q", stopped)
	}

	// A failure before the walk started has no result to report from.
	none := dirTransferError("upload", "/local", "/opt", nil, errors.New("not connected"))
	if strings.Contains(none.Error(), "files") {
		t.Fatalf("error = %q, want no invented counts", none)
	}
}
