package datadir

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	paths "github.com/hollis-labs/libs/core/util/apppaths"
	_ "modernc.org/sqlite" // the guard reads a database file it finds, only to recognize the old schema
)

// ErrLegacyLayout is returned when the configured data directory or database belongs to
// the old (pre-Substrate) build.
var ErrLegacyLayout = errors.New("this is the old Cerberus build's data")

// remedy is the operator's next step, in every refusal.
const remedy = "point " + Env + " at a new, empty directory (the default is ~/.local/share/cerberus-substrate). " +
	"The old build keeps ~/.cerberus and ~/.local/share/cerberus; its data reaches the new build only through " +
	"`cerberus migrate-from` (docs/conversion-notes.md), never by opening it in place"

// oldStateMarkers are entries only the old build's state tree (~/.cerberus) holds.
var oldStateMarkers = []string{"daemon.lock", "cerberus.sock", "cerberus.pid", "registry.yaml", "plugin-connectors.json", "audit", "approvals"}

// OldRoots are the old build's two homes: its state tree (home/.cerberus) and its XDG data
// directory (the XDG data home's "cerberus", which holds workspaces/<ws>/main.db). Nothing
// under either may be the new build's data directory or database.
func OldRoots(home string) ([]string, error) {
	layout, err := paths.Resolve("cerberus", paths.WithoutMaterialize())
	if err != nil {
		return nil, fmt.Errorf("resolve the old build's data directory: %w", err)
	}
	roots := []string{layout.DataDir()}
	if home != "" {
		roots = append([]string{filepath.Join(home, ".cerberus")}, roots...)
	}
	return roots, nil
}

// RefuseOldPaths refuses a data directory or database path that is, or is inside, one of
// the old roots (symlinks followed). It only resolves paths: it runs before anything is
// created or changed, so a refused start leaves the old tree exactly as it was.
func RefuseOldPaths(d Dirs, dbPath string, roots []string) error {
	for _, root := range roots {
		r := resolved(root)
		for _, p := range []struct{ what, path string }{{"the data directory", d.Data}, {"database.sqlite.path", dbPath}} {
			if p.path != "" && within(r, resolved(p.path)) {
				return fmt.Errorf("%w: %s %s is inside %s: %s", ErrLegacyLayout, p.what, p.path, root, remedy)
			}
		}
	}
	return nil
}

// RefuseOldContents refuses a data directory that holds the old XDG layout (workspaces/)
// or an entry of the old state tree. It only stats; a missing directory passes.
func RefuseOldContents(d Dirs) error {
	if fi, err := os.Stat(filepath.Join(d.Data, "workspaces")); err == nil && fi.IsDir() {
		return fmt.Errorf("%w: %s holds workspaces/, the old build's database layout: %s", ErrLegacyLayout, d.Data, remedy)
	}
	for _, m := range oldStateMarkers {
		if _, err := os.Lstat(filepath.Join(d.Data, m)); err == nil {
			return fmt.Errorf("%w: %s holds %s, from the old build's state tree: %s", ErrLegacyLayout, d.Data, m, remedy)
		}
	}
	return nil
}

// RefuseOldSchema refuses a database file with the old schema (a schema_migrations table,
// which the old store kept and goose never creates). It is the last line, behind
// RefuseOldPaths.
//
// It changes nothing: the file is opened read-only and immutable (no locks, no WAL replay,
// no checkpoint, no -wal or -shm created or removed). An immutable read sees the main file
// only; the old database keeps its schema there, and a table that exists only in an
// un-checkpointed WAL is not the old build's long-lived database.
func RefuseOldSchema(path string) error {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.Size() == 0) {
		return nil
	} else if err != nil {
		return err
	}
	dsn := (&url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("inspect %s read-only: %w", path, err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&n); err != nil {
		return fmt.Errorf("cannot inspect %s without writing to it (%w); refusing to open it: %s", path, err, remedy)
	}
	if n > 0 {
		return fmt.Errorf("%w: %s has the old build's schema: %s", ErrLegacyLayout, path, remedy)
	}
	return nil
}

// resolved follows symlinks where the path (or its parent) exists, so a link into an old
// root is caught.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return filepath.Clean(p)
}
