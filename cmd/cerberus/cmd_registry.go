package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var registerCmd = &cobra.Command{
	Use:   "register <path>",
	Short: "Register an app-owned .cerberus.yaml with Cerberus",
	Long: `Register an app-owned project config (or a bundle manifest) with Cerberus.

The path may be a project config (kind: cerberus-project/v1), a bundle
manifest (kind: cerberus-bundle/v1), or a directory containing a
bundle.cerberus.yaml. Cerberus stores only a pointer to the file —
the app remains the owner of its config. Registration is rejected if
the config fails schema validation.`,
	Args:    cobra.ExactArgs(1),
	GroupID: "resources",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg, err := registry.ForConfig(cfgPath)
		if err != nil {
			return err
		}
		entries, err := reg.Register(args[0])
		if err != nil {
			return fmt.Errorf("register: %w", err)
		}
		var resourceIDs []string
		for _, e := range entries {
			via := ""
			if e.Via != "" {
				via = " (via " + e.Via + ")"
			}
			fmt.Printf("registered: %s [%s]%s -> %s\n", e.Owner, e.Kind, via, e.Path)
			if pc, loadErr := registry.LoadProjectConfig(e.Path); loadErr == nil {
				for _, r := range pc.Resources {
					resourceIDs = append(resourceIDs, r.ID)
				}
			}
		}
		// Registration is declarative — it makes resources visible but
		// builds and activates nothing. Point the operator at the
		// activation step so the two-step flow is obvious.
		if len(resourceIDs) > 0 {
			fmt.Println("\nregistration is declarative — nothing is built or running yet.")
			fmt.Println("build and activate each resource with:")
			for _, id := range resourceIDs {
				fmt.Printf("  cerberus resource deploy %s\n", id)
			}
		}
		return nil
	},
}

var deregisterCmd = &cobra.Command{
	Use:     "deregister <owner>",
	Short:   "Remove a registered app from Cerberus",
	Long:    "Removes an owner's pointer from the registry. The app's own config file is left untouched.",
	Args:    cobra.ExactArgs(1),
	GroupID: "resources",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg, err := registry.ForConfig(cfgPath)
		if err != nil {
			return err
		}
		if err := reg.Deregister(args[0]); err != nil {
			return err
		}
		fmt.Printf("deregistered: %s\n", args[0])
		return nil
	},
}

var registryCmd = &cobra.Command{
	Use:     "registry",
	Short:   "Inspect the resource registry",
	GroupID: "resources",
}

var registryListOutput string

var registryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered project configs",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg, err := registry.ForConfig(cfgPath)
		if err != nil {
			return err
		}
		entries, err := reg.List()
		if err != nil {
			return err
		}
		if registryListOutput == outputFormatJSON {
			if entries == nil {
				entries = []registry.IndexEntry{}
			}
			return printJSON(entries)
		}
		if len(entries) == 0 {
			fmt.Println("No project configs registered.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "OWNER\tNAMESPACE\tKIND\tPATH")
		fmt.Fprintln(w, "-----\t---------\t----\t----")
		for _, e := range entries {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Owner, e.Namespace, e.Kind, e.Path)
		}
		return w.Flush()
	},
}

var registryHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check that every registered config still resolves",
	Long:  "Verifies each registered config file still exists, parses, and passes schema validation. Exits non-zero if any is unhealthy.",
	RunE: func(cmd *cobra.Command, args []string) error {
		reg, err := registry.ForConfig(cfgPath)
		if err != nil {
			return err
		}
		reports, err := reg.Health()
		if err != nil {
			return err
		}
		if len(reports) == 0 {
			fmt.Println("No project configs registered.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "OWNER\tSTATUS\tDETAIL")
		fmt.Fprintln(w, "-----\t------\t------")
		unhealthy := 0
		for _, r := range reports {
			if !r.Healthy() {
				unhealthy++
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Owner, r.Status, r.Detail)
		}
		w.Flush() //nolint:errcheck
		if unhealthy > 0 {
			return fmt.Errorf("%d registered config(s) unhealthy", unhealthy)
		}
		return nil
	},
}

func init() {
	addOutputFlag(registryListCmd, &registryListOutput)
	registryCmd.AddCommand(registryListCmd)
	registryCmd.AddCommand(registryHealthCmd)
}
