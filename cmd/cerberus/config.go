package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hollis-labs/substrate/boot"
	"github.com/hollis-labs/substrate/module"

	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// bootOptions are the app's two boot hooks. The legacy guard (internal/datadir) runs in
// both, and nothing is created or changed until the checks that can run at that point pass.
//
// After the config module has applied the SUBSTRATE_CONFIG override, the data directory
// (CERBERUS_DATA_DIR, or the XDG data home's cerberus-substrate) is resolved, a relative
// database path is joined onto it, and the cerberus block is checked so a bad value stops
// the start naming its key. Then the data directory and the database path are refused if
// they lie in either of the old build's homes (~/.cerberus, the old XDG data directory),
// and the data directory if it holds the old layout. Only then is it created 0700.
//
// Immediately before the database module opens (and migrates) the file, the paths are
// checked again (the database module reads exactly this config), an existing file is
// refused if it has the old schema (read-only and immutable: it changes nothing), and only
// then is the database file created 0600.
func bootOptions(getenv func(string) string) boot.Options {
	var dirs datadir.Dirs
	var roots []string
	dbPath := func(app *module.App) string {
		p, _ := nested(app.Config, "database", "sqlite")["path"].(string)
		return p
	}
	return boot.Options{
		AfterConfig: func(_ context.Context, app *module.App) error {
			var err error
			if dirs, err = datadir.Resolve(getenv); err != nil {
				return err
			}
			sq := nested(app.Config, "database", "sqlite")
			p, _ := sq["path"].(string)
			if sq["path"], err = dirs.Anchor(dirs.Database(), p); err != nil {
				return fmt.Errorf("database.sqlite.path: %w", err)
			}
			if _, err = settings.Parse(app.Config[settings.Key], home(getenv)); err != nil {
				return err
			}
			if roots, err = datadir.OldRoots(home(getenv)); err != nil {
				return err
			}
			if err = datadir.RefuseOldPaths(dirs, dbPath(app), roots); err != nil {
				return err
			}
			if err = datadir.RefuseOldContents(dirs); err != nil {
				return err
			}
			if err = dirs.Prepare(); err != nil {
				return fmt.Errorf("prepare the data directory: %w", err)
			}
			return nil
		},
		Before: map[string]boot.Hook{
			"database": func(_ context.Context, app *module.App) error {
				db, _ := app.Config["database"].(map[string]any)
				if d, _ := db["driver"].(string); d != "" && d != "sqlite" {
					return fmt.Errorf("database.driver %q: Cerberus is SQLite only (docs/architecture.md)", d)
				}
				p := dbPath(app)
				if err := datadir.RefuseOldPaths(dirs, p, roots); err != nil {
					return err
				}
				if err := datadir.RefuseOldSchema(p); err != nil {
					return err
				}
				if err := dirs.EnsureParent(p); err != nil {
					return fmt.Errorf("database.sqlite.path: %w", err)
				}
				if err := datadir.PrivateFile(p); err != nil {
					return fmt.Errorf("database.sqlite.path: %w", err)
				}
				return nil
			},
		},
	}
}

// nested returns cfg[keys[0]][keys[1]]..., creating missing maps.
func nested(cfg map[string]any, keys ...string) map[string]any {
	m := cfg
	for _, k := range keys {
		child, _ := m[k].(map[string]any)
		if child == nil {
			child = map[string]any{}
			m[k] = child
		}
		m = child
	}
	return m
}

// home is the user's home directory: HOME from the environment the app was given, else the
// OS answer.
func home(getenv func(string) string) string {
	if h := getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}
