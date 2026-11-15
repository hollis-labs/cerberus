package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/legacyexport"
)

// importStandbyCmd is `cerberus import-standby --from <archive>` (plan section 7.2, F2): the old
// build's standby.d, archived beside the old tree at F, imported read-only as a sidecar into
// <data>/sidecar/standby. Not a data command (needsData): it resolves the data directory as boot
// would and writes only the sidecar.
func importStandbyCmd() *cobra.Command {
	var from, output string
	cmd := &cobra.Command{
		Use:   "import-standby",
		Short: "Import the old build's archived standby.d as a read-only sidecar log",
		Long: "Copy the archived standby.d into <data>/sidecar/standby (files 0400, directories 0500; an existing sidecar is refused),\n" +
			"hashing every source file and never writing it; check its audit hash chain (it was never anchored); report, for each\n" +
			"standby verb record, whether its argument digest was made with the main key or the standby key; parse sessions.json;\n" +
			"and write import-report.json beside the copy.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != outputText && output != outputJSON {
				return fmt.Errorf("--output must be text or json, got %q", output)
			}
			if err := absAll(&from); err != nil {
				return err
			}
			dirs, err := datadir.Resolve(os.Getenv)
			if err != nil {
				return err
			}
			rep, runErr := legacyexport.ImportStandby(legacyexport.StandbyOptions{From: from, DataDir: dirs.Data})
			if output == outputJSON {
				_ = rep.WriteJSON(cmd.OutOrStdout())
			} else {
				_ = rep.WriteText(cmd.OutOrStdout())
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the archived standby.d directory (required)")
	cmd.Flags().StringVarP(&output, "output", "o", outputText, "output format: text|json")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}
