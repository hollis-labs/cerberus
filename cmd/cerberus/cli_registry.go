package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// The registry verbs: `register <path>`, `deregister <owner>`, `registry list` and `registry
// health` (old cmd/cerberus cmd_registry.go), with the old arguments and output. Tether sysop
// runs `cerberus register <tmp.yaml>`.
//
// They read and write <adopt_root>/registry.yaml in place, directly, as the old CLI did, not
// through the server: the index is a pointer file shared with the old build and tether sysop
// (adopted in place, old's format), the serving Cerberus reads it fresh on every request, and
// old's CLI wrote it in-process with no gate and no audit record, whether or not the daemon ran.
// Keeping that keeps old's semantics: registering works with the server down (declaring a
// resource builds and starts nothing), and the write is old's atomic temp-file rename. Old had
// no lock on the index; registering is done from one build at a time (plan section 7). The
// console's gated register route is the console's (slice 11).

// registryFor is the registry in the configured adopted root.
func registryFor(c *cerberus) (*registry.Registry, error) {
	st, err := settings.Parse(c.app.Config[settings.Key], c.home)
	if err != nil {
		return nil, err
	}
	return registry.ForRoot(st.AdoptRoot), nil
}

// registryCommands are the registry verbs over reg.
func registryCommands(reg func() (*registry.Registry, error)) []*cobra.Command {
	register := &cobra.Command{
		Use:   "register <path>",
		Short: "Register an app-owned .cerberus.yaml with Cerberus",
		Long: `Register an app-owned project config (or a bundle manifest) with Cerberus.

The path may be a project config (kind: cerberus-project/v1), a bundle
manifest (kind: cerberus-bundle/v1), or a directory containing a
bundle.cerberus.yaml. Cerberus stores only a pointer to the file —
the app remains the owner of its config. Registration is rejected if
the config fails schema validation.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := reg()
			if err != nil {
				return err
			}
			entries, err := r.Register(args[0])
			if err != nil {
				return fmt.Errorf("register: %w", err)
			}
			out := cmd.OutOrStdout()
			var resourceIDs []string
			for _, e := range entries {
				via := ""
				if e.Via != "" {
					via = " (via " + e.Via + ")"
				}
				_, _ = fmt.Fprintf(out, "registered: %s [%s]%s -> %s\n", e.Owner, e.Kind, via, e.Path)
				if pc, loadErr := registry.LoadProjectConfig(e.Path); loadErr == nil {
					for _, res := range pc.Resources {
						resourceIDs = append(resourceIDs, res.ID)
					}
				}
			}
			// Registration is declarative: it builds and activates nothing.
			if len(resourceIDs) > 0 {
				_, _ = fmt.Fprintln(out, "\nregistration is declarative — nothing is built or running yet.")
				_, _ = fmt.Fprintln(out, "build and activate each resource with:")
				for _, id := range resourceIDs {
					_, _ = fmt.Fprintf(out, "  cerberus resource deploy %s --ack\n", id)
				}
			}
			return nil
		},
	}
	deregister := &cobra.Command{
		Use:   "deregister <owner>",
		Short: "Remove a registered app from Cerberus",
		Long:  "Removes an owner's pointer from the registry. The app's own config file is left untouched.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := reg()
			if err != nil {
				return err
			}
			if err := r.Deregister(args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "deregistered: %s\n", args[0])
			return err
		},
	}
	var listOut string
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered project configs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := reg()
			if err != nil {
				return err
			}
			entries, err := r.List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if listOut == outputJSON {
				if entries == nil {
					entries = []registry.IndexEntry{}
				}
				return printJSON(out, entries)
			}
			if len(entries) == 0 {
				_, err = fmt.Fprintln(out, "No project configs registered.")
				return err
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "OWNER\tNAMESPACE\tKIND\tPATH")
			_, _ = fmt.Fprintln(w, "-----\t---------\t----\t----")
			for _, e := range entries {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Owner, e.Namespace, e.Kind, e.Path)
			}
			return w.Flush()
		},
	}
	list.Flags().StringVarP(&listOut, "output", "o", outputText, "output format: text|json")
	health := &cobra.Command{
		Use:   "health",
		Short: "Check that every registered config still resolves",
		Long:  "Verifies each registered config file still exists, parses, and passes schema validation. Exits non-zero if any is unhealthy.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := reg()
			if err != nil {
				return err
			}
			reports, err := r.Health()
			if err != nil {
				return err
			}
			return writeRegistryHealth(cmd.OutOrStdout(), reports)
		},
	}
	registryCmd := &cobra.Command{Use: "registry", Short: "Inspect the resource registry"}
	registryCmd.AddCommand(list, health)
	return []*cobra.Command{register, deregister, registryCmd}
}

// writeRegistryHealth is old's table; any unhealthy config is an error (exit 1).
func writeRegistryHealth(out io.Writer, reports []registry.HealthReport) error {
	if len(reports) == 0 {
		_, err := fmt.Fprintln(out, "No project configs registered.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "OWNER\tSTATUS\tDETAIL")
	_, _ = fmt.Fprintln(w, "-----\t------\t------")
	unhealthy := 0
	for _, r := range reports {
		if !r.Healthy() {
			unhealthy++
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", r.Owner, r.Status, r.Detail)
	}
	_ = w.Flush()
	if unhealthy > 0 {
		return fmt.Errorf("%d registered config(s) unhealthy", unhealthy)
	}
	return nil
}

// registryCommands are the registry verbs on the configured adopted root.
func (c *cerberus) registryCommands() []*cobra.Command {
	return registryCommands(func() (*registry.Registry, error) { return registryFor(c) })
}
