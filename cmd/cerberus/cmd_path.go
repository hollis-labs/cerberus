package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/hollis-labs/go-apppaths/paths"
	"github.com/spf13/cobra"

	"github.com/chrispian/cerberus/internal/config"
)

// pathCommand prints Cerberus's resolved on-disk layout — the go-apppaths XDG
// roots, the active workspace, and the main database — plus the legacy
// ~/.cerberus/ dotdir paths that are deliberately NOT migrated (config.yaml,
// registry.yaml). It is the introspection surface operators use to confirm
// where the daemon reads and writes before a deploy.
//
// Only the main database is resolved through go-apppaths (CW-20260517-0065);
// the dotdir paths shown below it are the unchanged hand-made layout.
//
// It resolves through config.ResolveLayout, so the printed main-db path
// reflects CERBERUS_DB_PATH / CERBERUS_WORKSPACE / $XDG_* and the --db flag.
// paths.WithoutMaterialize() keeps introspection side-effect free.
var pathCommand = &cobra.Command{
	Use:   "path",
	Short: "Print Cerberus's resolved on-disk layout (go-apppaths)",
	Long: "Print the data/state/cache/config roots, active workspace, and main\n" +
		"database path Cerberus resolves via go-apppaths, plus the legacy\n" +
		"~/.cerberus/ dotdir paths (config.yaml, registry.yaml) that are kept\n" +
		"in place by design — only the main database moves onto the XDG layout.",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := []paths.Option{paths.WithoutMaterialize()}
		if dbPath != "" {
			opts = append(opts, paths.WithDBOverride(dbPath))
		}
		layout, err := config.ResolveLayout(opts...)
		if err != nil {
			return fmt.Errorf("resolve layout: %w", err)
		}

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		for _, e := range layout.Describe() {
			fmt.Fprintf(w, "%s\t%s\n", e.Label, e.Value)
		}
		// The legacy dotdir paths are NOT go-apppaths-resolved; they are the
		// hand-made ~/.cerberus/ layout kept in place by design.
		fmt.Fprintf(w, "config-yaml\t%s\n", config.DefaultPath())
		return w.Flush()
	},
}
