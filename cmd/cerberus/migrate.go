package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/legacyimport"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
)

// migrateFromCmd is `cerberus migrate-from` (docs/substrate-port-plan.md, section 5): the old
// build's state, from a snapshot of ~/.cerberus and a .backup of main.db, into a new data
// directory, with a report.
//
// It needs no server and is not a data command (needsData): the root starts only the modules
// that read config, so boot creates no data directory, database or signing key. migrate-from
// resolves the same paths the server's boot would (CERBERUS_DATA_DIR or the XDG default, and
// database.sqlite.path anchored in it), applies the boot guard's refusals and its own, and only
// then creates the data directory and the database itself, atomically (built in <data>.partial
// and renamed into place). A server started afterwards finds the database already migrated.
func (c *cerberus) migrateFromCmd() *cobra.Command {
	var oldHome, oldDB, manifestPath, account, output string
	cmd := &cobra.Command{
		Use:   "migrate-from",
		Short: "Import the old build's state from a snapshot of ~/.cerberus and a backup of main.db",
		Long: "Import the old build's state into a new, empty data directory: --old-home is a copy (cp -a) of ~/.cerberus taken\n" +
			"with old in standby, --old-db a `sqlite3 main.db \".backup <copy>\"`. Neither may be the live tree. Nothing is written\n" +
			"unless every check passes; the report lists every store with its treatment, digests, counts and verification.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != outputText && output != outputJSON {
				return fmt.Errorf("--output must be text or json, got %q", output)
			}
			o, err := c.migrateOptions(oldHome, oldDB, manifestPath, account)
			if err != nil {
				return err
			}
			rep, runErr := legacyimport.Run(cmd.Context(), o)
			if output == outputJSON {
				_ = rep.WriteJSON(cmd.OutOrStdout())
			} else {
				_ = rep.WriteText(cmd.OutOrStdout())
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&oldHome, "old-home", "", "the snapshot of the old ~/.cerberus (required)")
	cmd.Flags().StringVar(&oldDB, "old-db", "", "the .backup of the old main.db (required)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "the D3 manifest the snapshot must match (kept in <data>/migration)")
	cmd.Flags().StringVar(&account, "old-anchor-account", "", "the keychain account of the old audit anchor (default $HOME/.cerberus/audit, the live tree's audit dir)")
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "output format: text|json")
	_ = cmd.MarkFlagRequired("old-home")
	_ = cmd.MarkFlagRequired("old-db")
	return cmd
}

// migrateOptions resolves the run's paths as the server's boot would.
func (c *cerberus) migrateOptions(oldHome, oldDB, manifestPath, account string) (legacyimport.Options, error) {
	var o legacyimport.Options
	dirs, err := datadir.Resolve(os.Getenv)
	if err != nil {
		return o, err
	}
	p, _ := nested(c.app.Config, "database", "sqlite")["path"].(string)
	dbPath, err := dirs.Anchor(dirs.Database(), p)
	if err != nil {
		return o, fmt.Errorf("database.sqlite.path: %w", err)
	}
	if c.home == "" {
		return o, errors.New("the home directory is unknown")
	}
	roots, err := datadir.OldRoots(c.home)
	if err != nil {
		return o, err
	}
	abs := func(p string) (string, error) {
		if p == "" {
			return "", nil
		}
		return filepath.Abs(p)
	}
	if oldHome, err = abs(oldHome); err != nil {
		return o, err
	}
	if oldDB, err = abs(oldDB); err != nil {
		return o, err
	}
	if manifestPath, err = abs(manifestPath); err != nil {
		return o, err
	}
	if account == "" {
		account = filepath.Join(c.home, ".cerberus", "audit")
	} else if account, err = abs(account); err != nil {
		return o, err
	}
	return legacyimport.Options{OldHome: oldHome, OldDB: oldDB, OldAnchorAccount: account, DataDir: dirs.Data, DBPath: dbPath,
		ConfigDir: configDir(c.home), Home: c.home, OldRoots: roots, Manifest: manifestPath}, nil
}

// manifestCmd is `cerberus manifest write|check`: the D3 manifest of the old tree, taken with old
// in standby before the window opens, and checked against it later (by export-legacy before a
// rollback, and here by hand). It reads the live old tree by design, and only reads: the
// database is opened read-only through its WAL and never checkpointed.
func manifestCmd() *cobra.Command {
	group := &cobra.Command{Use: "manifest", Short: "The D3 manifest of the old tree (write, check)"}
	var oldHome, oldDB, out, account string
	write := &cobra.Command{
		Use:   "write",
		Short: "Record the old tree's files, audit anchor and main.db logical state (0600, never over an existing file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := absAll(&oldHome, &oldDB, &out, &account); err != nil {
				return err
			}
			m, err := manifest.Write(cmd.Context(), manifest.Options{OldHome: oldHome, OldDB: oldDB, AnchorAccount: account, Live: true}, out)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "manifest written: %s (%d files, %d reservations, anchor absent %v, host rows %s)\n",
				out, len(m.Files), len(m.Dirs), m.Anchor.Absent, m.DB.HostDumpSHA256)
			return nil
		},
	}
	write.Flags().StringVar(&oldHome, "old-home", "", "the old tree (~/.cerberus) (required)")
	write.Flags().StringVar(&oldDB, "old-db", "", "the old main.db (required)")
	write.Flags().StringVar(&out, "out", "", "the manifest file to create (required)")
	write.Flags().StringVar(&account, "anchor-account", "", "the keychain account of the audit anchor (default <old-home>/audit)")
	for _, f := range []string{"old-home", "old-db", "out"} {
		_ = write.MarkFlagRequired(f)
	}

	var path, cHome, cDB, cAccount string
	check := &cobra.Command{
		Use:   "check",
		Short: "Compare the old tree with a manifest; exit non-zero naming every mismatch",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := absAll(&path, &cHome, &cDB, &cAccount); err != nil {
				return err
			}
			m, sum, err := manifest.Load(path)
			if err != nil {
				return err
			}
			mismatches, err := manifest.Check(cmd.Context(), m, manifest.Options{OldHome: cHome, OldDB: cDB, AnchorAccount: cAccount, Live: true})
			if err != nil {
				return err
			}
			for _, x := range mismatches {
				fmt.Fprintf(cmd.OutOrStdout(), "mismatch: %s\n", x)
			}
			if len(mismatches) > 0 {
				return fmt.Errorf("the old tree does not match the manifest %s (%d mismatch(es))", path, len(mismatches))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "the old tree matches the manifest %s (%s)\n", path, sum)
			return nil
		},
	}
	check.Flags().StringVar(&path, "manifest", "", "the manifest (required)")
	check.Flags().StringVar(&cHome, "old-home", "", "the old tree (required)")
	check.Flags().StringVar(&cDB, "old-db", "", "the old main.db (required)")
	check.Flags().StringVar(&cAccount, "anchor-account", "", "the keychain account of the audit anchor (default: the manifest's)")
	for _, f := range []string{"manifest", "old-home", "old-db"} {
		_ = check.MarkFlagRequired(f)
	}
	group.AddCommand(write, check)
	return group
}

// absAll makes each non-empty path absolute.
func absAll(paths ...*string) error {
	for _, p := range paths {
		if *p == "" {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = abs
	}
	return nil
}
