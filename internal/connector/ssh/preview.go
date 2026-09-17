package ssh

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// previewMaxDepth matches go-sftpsync's default WithMaxDepth. A tree deeper
// than this is one the transfer will refuse, so the preview has to say so
// rather than quietly counting past it.
const previewMaxDepth = 64

// DirUploadPreview describes the local tree a put_dir would send.
//
// The counts come from a second walk rather than from go-sftpsync's dry-run
// mode, because a preview runs before the connector is resolved and so has no
// SFTP client to hand the library. The walk therefore mirrors the library's:
// same root handling, same per-entry lstat, same symlink rule, same depth
// bound. TestPreviewMatchesLibraryDryRun is the guard against the two drifting
// apart.
type DirUploadPreview struct {
	Files    int
	Dirs     int
	Symlinks int
	Skipped  int
	Bytes    int64
	Warnings []string
}

// PreviewDirUpload walks localDir and reports what a put_dir would move.
//
// It returns no error. Everything it cannot read becomes a warning: an
// operator reading a preview is deciding whether to proceed, and a walk that
// failed on one unreadable subdirectory is still worth seeing the rest of.
func PreviewDirUpload(localDir string) DirUploadPreview {
	p := DirUploadPreview{}

	// stat, not lstat: the operator named this path, so a symlinked root is
	// their choice. Only links found inside the tree face the escape check.
	info, err := os.Stat(localDir)
	switch {
	case err != nil:
		p.warnf("local directory %s cannot be read: %v", localDir, err)
		return p
	case !info.IsDir():
		p.warnf("%s is not a directory; ssh put-dir transfers a tree, ssh put transfers a file", localDir)
		return p
	}

	// The root counts as a directory, as it does in the library's result.
	p.Dirs++
	p.walk(localDir, "", 0)
	return p
}

func (p *DirUploadPreview) walk(root, rel string, depth int) {
	if depth >= previewMaxDepth {
		p.warnf("%s is deeper than %d levels; the transfer will refuse it", displayRel(rel), previewMaxDepth)
		return
	}

	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		p.warnf("%s cannot be listed: %v", displayRel(rel), err)
		return
	}

	for _, entry := range entries {
		childRel := path.Join(rel, entry.Name())
		full := filepath.Join(root, filepath.FromSlash(childRel))

		// lstat rather than the directory entry's own type, so a symlink
		// cannot present itself as a regular file and walk past the check
		// below.
		info, statErr := os.Lstat(full)
		if statErr != nil {
			p.warnf("%s cannot be read: %v", childRel, statErr)
			continue
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, linkErr := os.Readlink(full)
			if linkErr != nil {
				p.warnf("symlink %s cannot be read: %v", childRel, linkErr)
				continue
			}
			if escapesSyncRoot(childRel, target) {
				p.warnf("symlink %s targets %q outside the tree; the transfer will refuse it and stop there", childRel, target)
				continue
			}
			p.Symlinks++
		case info.IsDir():
			p.Dirs++
			p.walk(root, childRel, depth+1)
		case info.Mode().IsRegular():
			p.Files++
			p.Bytes += info.Size()
		default:
			// Sockets, devices, FIFOs — reported and left behind, as the
			// library leaves them behind.
			p.Skipped++
		}
	}
}

func (p *DirUploadPreview) warnf(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

func displayRel(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

// escapesSyncRoot reports whether a symlink at rel, pointing at target,
// resolves outside the transfer root.
//
// The rule is go-sftpsync's escapesRoot, restated here because the preview
// runs without a client and so cannot ask the library. It is lexical: it needs
// no I/O, cannot be raced by a target that changes between the check and the
// transfer, and gives the same answer on both ends. An absolute target counts
// as an escape even when it happens to sit inside the root, because the same
// absolute path on the other machine is a different place.
func escapesSyncRoot(rel, target string) bool {
	if target == "" {
		return true
	}
	if path.IsAbs(target) || filepath.IsAbs(target) {
		return true
	}
	resolved := path.Join(path.Dir(rel), filepath.ToSlash(target))
	return resolved == ".." || strings.HasPrefix(resolved, "../")
}
