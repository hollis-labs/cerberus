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

// bootOptions are the app's two boot hooks.
//
// After the config module has applied the SUBSTRATE_CONFIG override, the data directory
// (CERBERUS_DATA_DIR, or the XDG data home's cerberus-substrate) is resolved and created
// 0700, a relative database path is joined onto it, and the cerberus block is checked so a
// bad value stops the start naming its key.
//
// Immediately before the database module opens (and migrates) the file, the old build's
// state is refused (datadir.RefuseLegacy), and only then is the database file created 0600.
// The guard reads the path from the config the module will read, so it needs no copy of
// the merge rule.
func bootOptions(getenv func(string) string) boot.Options {
	var dirs datadir.Dirs
	return boot.Options{
		AfterConfig: func(_ context.Context, app *module.App) error {
			var err error
			if dirs, err = datadir.Resolve(getenv); err != nil {
				return err
			}
			if err = dirs.Prepare(); err != nil {
				return fmt.Errorf("prepare the data directory %s: %w", dirs.Data, err)
			}
			sq := nested(app.Config, "database", "sqlite")
			p, _ := sq["path"].(string)
			if sq["path"], err = dirs.Anchor(dirs.Database(), p); err != nil {
				return fmt.Errorf("database.sqlite.path: %w", err)
			}
			_, err = settings.Parse(app.Config[settings.Key], home(getenv))
			return err
		},
		Before: map[string]boot.Hook{
			"database": func(_ context.Context, app *module.App) error {
				db, _ := app.Config["database"].(map[string]any)
				if d, _ := db["driver"].(string); d != "" && d != "sqlite" {
					return fmt.Errorf("database.driver %q: Cerberus is SQLite only (docs/architecture.md)", d)
				}
				p, _ := nested(app.Config, "database", "sqlite")["path"].(string)
				if err := datadir.RefuseLegacy(dirs, p, home(getenv)); err != nil {
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
