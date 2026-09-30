package cerbapi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// A file transfer names a path on this machine (`local_path`): the file
// `ssh get` writes, or the file `ssh put` reads. Only a person at their own
// terminal may name any path. Every other caller (MCP, the HTTP API, the
// console, an agent at the CLI, a caller of unknown kind) reads and writes
// only under the transfer root, symlinks resolved. Otherwise an agent that
// can reach a host could write ~/.zshrc, a LaunchAgent or Cerberus's own
// policy, or read any file the operator can (B3, CERB-GAP-850).
//
// The root is the operator's, from the global config (`transfers.root`,
// default ~/.cerberus/transfers), never from a call.

// localPathField is the input every local file transfer names its path by.
const localPathField = "local_path"

var transferRootPoint atomic.Pointer[string]

// SetTransferRoot installs the process's transfer root. Empty is the default.
func SetTransferRoot(root string) {
	if root == "" {
		transferRootPoint.Store(nil)
		return
	}
	transferRootPoint.Store(&root)
}

// TransferRoot is the installed transfer root, or the default.
func TransferRoot() string {
	if root := transferRootPoint.Load(); root != nil {
		return *root
	}
	return config.ExpandHomePath(config.DefaultTransferRoot)
}

// confinedToTransferRoot is whether p's local paths are held to the root:
// everyone but a human at the CLI.
func confinedToTransferRoot(p audit.Principal) bool {
	return p.Kind != string(PrincipalHuman) || p.Via != ViaCLI
}

// confineLocalPath holds args' local_path to the transfer root for a caller
// that is confined, for an operation that touches local files, and rewrites
// it to the resolved path the operation then uses. Other calls pass
// unchanged.
func confineLocalPath(args ExternalConnectorOperationArgs, op contract.Operation, p audit.Principal) (ExternalConnectorOperationArgs, error) {
	if op.LocalFS == contract.LocalFSNone || op.LocalFS == "" || !confinedToTransferRoot(p) {
		return args, nil
	}
	raw, present := args.Config[localPathField]
	if !present {
		return args, nil
	}
	requested, ok := raw.(string)
	if !ok || strings.TrimSpace(requested) == "" {
		return args, nil // the key table refuses it
	}
	root := TransferRoot()
	resolved, err := ConfineToRoot(root, requested)
	if err != nil {
		return args, externalConnectorError(args, ExternalConnectorInvalidArgs, redact.GuidanceWrap(err,
			"%s from %s reads and writes local files only under the transfer root %s; name a path inside it (a relative path is taken from it), or run the transfer from your own terminal",
			localPathField, callerWords(p), root))
	}
	cfg := make(map[string]any, len(args.Config))
	for k, v := range args.Config {
		cfg[k] = v
	}
	cfg[localPathField] = resolved
	args.Config = cfg
	return args, nil
}

// callerWords names a caller for a refusal: "agent over mcp_stdio", or "a
// caller of unknown kind".
func callerWords(p audit.Principal) string {
	kind, via := p.Kind, p.Via
	if kind == "" {
		kind = "a caller of unknown kind"
	}
	if via == "" || via == ViaUnknown {
		return kind
	}
	return kind + " over " + via
}

// errOutsideRoot is a path that resolves outside the transfer root.
var errOutsideRoot = errors.New("the path resolves outside the transfer root")

// ConfineToRoot resolves path against root and returns it if it stays inside
// root once every symlink on the way is followed. A relative path is taken
// from root. A component that does not exist yet ends the walk: what follows
// it cannot be a symlink. A dangling symlink is refused, since creating the
// file would follow it. root is created, private, if it is missing.
func ConfineToRoot(root, path string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("the transfer root %q is not an absolute path", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create the transfer root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve the transfer root: %w", err)
	}
	path = config.ExpandHomePath(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(realRoot, path)
	}
	path = filepath.Clean(path)
	// Name the path relative to the root it was written against, so a
	// root reached through a symlink (/tmp on macOS) still matches.
	rel, ok := within(realRoot, path)
	if !ok {
		if rel, ok = within(filepath.Clean(root), path); !ok {
			return "", errOutsideRoot
		}
	}
	cur := realRoot
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			cur = filepath.Join(append([]string{cur}, parts[i:]...)...)
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("%s is a symlink that cannot be resolved, and writing it would follow it: %w", part, errOutsideRoot)
			}
			next = target
		}
		if _, ok := within(realRoot, next); !ok {
			return "", errOutsideRoot
		}
		cur = next
	}
	if _, ok := within(realRoot, cur); !ok {
		return "", errOutsideRoot
	}
	return cur, nil
}

// within reports whether path is root or under it, and path relative to it.
func within(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}
