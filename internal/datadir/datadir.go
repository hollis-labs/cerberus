// Package datadir resolves where the Substrate build of Cerberus keeps its own data, and
// refuses to start on the old build's state.
//
// The data directory is CERBERUS_DATA_DIR when set, otherwise the XDG data home joined with
// "cerberus-substrate" (~/.local/share/cerberus-substrate). It is created 0700. It never
// overlaps the old build: ~/.cerberus (its state tree) and ~/.local/share/cerberus (its
// workspace database) stay the old build's until the import (docs/substrate-port-plan.md,
// section 5), so both builds can run side by side.
package datadir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	paths "github.com/hollis-labs/libs/core/util/apppaths"
)

const (
	// appName names the XDG directory. It differs from the old build's "cerberus" on purpose.
	appName = "cerberus-substrate"
	// Env overrides the data directory.
	Env = "CERBERUS_DATA_DIR"
	// DatabaseFile is the default database name under the data directory.
	DatabaseFile = "cerberus.db"
)

// Dirs is the resolved data directory.
type Dirs struct {
	Data string
}

// Resolve finds the data directory without creating anything.
func Resolve(getenv func(string) string) (Dirs, error) {
	if v := getenv(Env); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return Dirs{}, fmt.Errorf("resolve %s: %w", Env, err)
		}
		return Dirs{Data: abs}, nil
	}
	layout, err := paths.Resolve(appName, paths.WithoutMaterialize())
	if err != nil {
		return Dirs{}, fmt.Errorf("resolve application directories: %w", err)
	}
	return Dirs{Data: layout.DataDir()}, nil
}

// Database is the default database path.
func (d Dirs) Database() string { return filepath.Join(d.Data, DatabaseFile) }

// Prepare creates the data directory 0700 if it is missing. An existing directory is never
// changed: one that others can read or enter is refused, with the fix, so the guard's
// promise that a refused start changes nothing holds for whatever the path points at.
func (d Dirs) Prepare() error {
	fi, err := os.Stat(d.Data)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if mkErr := os.MkdirAll(d.Data, 0o700); mkErr != nil {
			return mkErr
		}
		return os.Chmod(d.Data, 0o700) //nolint:gosec // 0700 on a directory we just created: owner-only, whatever the umask
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", d.Data)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is mode %#o and must be private: run `chmod 700 %s`, or point %s at a new directory", d.Data, fi.Mode().Perm(), d.Data, Env)
	}
	return nil
}

// Within reports whether path is the data directory or below it.
func (d Dirs) Within(path string) bool { return within(d.Data, path) }

// EnsureParent creates the parent directories (0700) of a path inside the data directory.
// A path outside it is the operator's explicit choice and gets nothing.
func (d Dirs) EnsureParent(path string) error {
	if !d.Within(path) {
		return nil
	}
	return os.MkdirAll(filepath.Dir(path), 0o700)
}

// Anchor places a configured path under the data directory. An empty value is def; an
// absolute path is kept as the operator's choice; a relative path is joined onto the data
// directory, and refused if it climbs out of it through "..".
func (d Dirs) Anchor(def, p string) (string, error) {
	switch {
	case p == "":
		return def, nil
	case filepath.IsAbs(p):
		return p, nil
	}
	joined := filepath.Join(d.Data, p)
	if !d.Within(joined) {
		return "", fmt.Errorf("relative path %q leaves the data directory %s; use an absolute path to put it elsewhere", p, d.Data)
	}
	return joined, nil
}

// PrivateFile creates the file 0600 if it is missing. An existing file is never changed:
// one that others can read is refused, with the fix.
func PrivateFile(path string) error {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		f, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path is operator configuration
		if openErr != nil {
			return openErr
		}
		if chErr := f.Chmod(0o600); chErr != nil {
			_ = f.Close()
			return chErr
		}
		return f.Close()
	case err != nil:
		return err
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is mode %#o and must be private: run `chmod 600 %s`", path, fi.Mode().Perm(), path)
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
