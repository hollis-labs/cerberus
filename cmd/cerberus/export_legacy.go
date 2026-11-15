package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/legacyexport"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
)

// exportLegacyCmd is `cerberus export-legacy` (docs/substrate-port-plan.md, section 5, the reverse
// path): the rollback of migrate-from. It writes what this build recorded after the handoff back
// into the old tree in the old build's formats, and refuses before writing anything unless the
// old tree still matches the D3 manifest migrate-from kept (or the post state of the steps an
// earlier run did). Like migrate-from it needs no server and is not a data command (needsData):
// it resolves the data directory and database the server's boot would, and opens them itself.
//
// Exit status: 0 when every step is done; 3 when every step but the class B one is done (the
// Telemetry store was unreachable or a host row conflicted): run it again with --only telemetry;
// 1 for a refusal or an error.
func (c *cerberus) exportLegacyCmd() *cobra.Command {
	var since, oldHome, oldDB, account, only, d3Copy, output string
	var checkFiles bool
	cmd := &cobra.Command{
		Use:   "export-legacy",
		Short: "Roll back to the old build: write what this build recorded since the handoff into the old tree",
		Long: "Write the audit, approvals and brake records, the pause set, the applied digests, the credential-issuance receipts\n" +
			"and the host monitor rows recorded since the handoff back into the old tree, in the old build's formats, and move\n" +
			"the old audit anchor to the new head once the appended chain verifies. --since names the handoff (the D3 manifest's\n" +
			"body sha256, or its path) and must be the manifest migrate-from kept. Nothing is written unless the old tree still\n" +
			"matches it. If the Telemetry store is unreachable the run exits 3 with the class B step pending; finish it with\n" +
			"--only telemetry. --check-legacy-files compares plugin-connectors.json with the D3 copy and parses a changed one\n" +
			"with the old build's decoder (runbook step 0).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != outputText && output != outputJSON {
				return fmt.Errorf("--output must be text or json, got %q", output)
			}
			if err := absAll(&oldHome, &oldDB, &account, &d3Copy); err != nil {
				return err
			}
			if !strings.HasPrefix(since, "sha256:") {
				// A path to the manifest; a body digest is kept as given.
				if err := absAll(&since); err != nil {
					return err
				}
			}
			var rep *legacyexport.Report
			var runErr error
			if checkFiles {
				if d3Copy == "" {
					return errors.New("--check-legacy-files needs --d3-copy <dir>: the directory the D3 cp -a snapshot was taken into")
				}
				if oldHome == "" {
					dirs, err := datadir.Resolve(os.Getenv)
					if err != nil {
						return err
					}
					m, _, err := manifest.LoadKept(dirs.Data)
					if err != nil {
						return fmt.Errorf("--old-home is not given and %w", err)
					}
					oldHome = m.OldHome
				}
				rep, runErr = legacyexport.CheckLegacyFiles(legacyexport.LegacyFilesOptions{OldHome: oldHome, D3Copy: d3Copy})
			} else {
				o, err := c.exportOptions()
				if err != nil {
					return err
				}
				o.Since, o.OldHome, o.OldDB, o.OldAnchorAccount, o.Only = since, oldHome, oldDB, account, only
				rep, runErr = legacyexport.Run(cmd.Context(), o)
			}
			if output == outputJSON {
				_ = rep.WriteJSON(cmd.OutOrStdout())
			} else {
				_ = rep.WriteText(cmd.OutOrStdout())
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "the handoff: the D3 manifest's body sha256 (sha256:...) or its path (required unless --check-legacy-files)")
	cmd.Flags().StringVar(&oldHome, "old-home", "", "the old tree (default: the one the manifest was taken of)")
	cmd.Flags().StringVar(&oldDB, "old-db", "", "the old main.db (default: the manifest's)")
	cmd.Flags().StringVar(&account, "old-anchor-account", "", "the keychain account of the old audit anchor (default: the manifest's)")
	cmd.Flags().StringVar(&only, "only", "", "run one step alone: telemetry (the class B step, once every other step is done)")
	cmd.Flags().BoolVar(&checkFiles, "check-legacy-files", false, "compare plugin-connectors.json with the D3 copy and parse it with the old decoder if it changed (runbook step 0)")
	cmd.Flags().StringVar(&d3Copy, "d3-copy", "", "the D3 copy's directory (its cerberus-home is the cp -a snapshot), for --check-legacy-files")
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "output format: text|json")
	return cmd
}

// exportOptions resolves the data directory, database and config dir as the server's boot would.
func (c *cerberus) exportOptions() (legacyexport.Options, error) {
	var o legacyexport.Options
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
	return legacyexport.Options{DataDir: dirs.Data, DBPath: dbPath, ConfigDir: configDir(c.home)}, nil
}
