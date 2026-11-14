package datadir

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

// RefuseLegacy refuses to start on the old build's state. It runs immediately before the
// database module opens (and migrates) dbPath, and only reads.
//
// Refused: a data directory or database inside home/.cerberus; a data directory holding
// the old XDG layout (a workspaces/ directory, as ~/.local/share/cerberus does) or entries
// of the old state tree; and a database file with the old schema (a schema_migrations
// table, which the old store kept and goose does not).
func RefuseLegacy(d Dirs, dbPath, home string) error {
	if home != "" {
		old := resolved(filepath.Join(home, ".cerberus"))
		for _, p := range []struct{ what, path string }{{"the data directory", d.Data}, {"database.sqlite.path", dbPath}} {
			if p.path != "" && within(old, resolved(p.path)) {
				return fmt.Errorf("%w: %s %s is inside %s: %s", ErrLegacyLayout, p.what, p.path, old, remedy)
			}
		}
	}
	if fi, err := os.Stat(filepath.Join(d.Data, "workspaces")); err == nil && fi.IsDir() {
		return fmt.Errorf("%w: %s holds workspaces/, the old build's database layout: %s", ErrLegacyLayout, d.Data, remedy)
	}
	for _, m := range oldStateMarkers {
		if _, err := os.Lstat(filepath.Join(d.Data, m)); err == nil {
			return fmt.Errorf("%w: %s holds %s, from the old build's state tree: %s", ErrLegacyLayout, d.Data, m, remedy)
		}
	}
	return refuseOldSchema(dbPath)
}

func refuseOldSchema(path string) error {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.Size() == 0) {
		return nil
	} else if err != nil {
		return err
	}
	// Only reads: the guard never changes the file it judges. (A mode=ro open fails on a WAL
	// database whose -shm file is gone, so the open is plain and the statements are reads.)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&n); err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if n > 0 {
		return fmt.Errorf("%w: %s has the old build's schema: %s", ErrLegacyLayout, path, remedy)
	}
	return nil
}

// resolved follows symlinks where the path exists, so a link into ~/.cerberus is caught.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return filepath.Clean(p)
}
