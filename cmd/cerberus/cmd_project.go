package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/registry"
	"github.com/spf13/cobra"
)

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Project management",
	Long: `Project inventory commands.

A project is a logical grouping of v2 resources under 'projects:' in the
config. These commands read the registered config tree — to add/remove
projects, edit the config or use 'cerberus register' / 'cerberus deregister'.

Subcommands:
  list   list all registered projects
  show   show a project's metadata and resources`,
}

var projectListOutput string

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List projects",
	Long:  "Lists all projects defined in the config.",
	RunE: func(cmd *cobra.Command, args []string) error {
		v2, err := registry.ResolveConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		if projectListOutput == outputFormatJSON {
			// Marshal via cerbapi.ProjectInfo so the JSON shape matches the
			// MCP / socket project_list contract (lowercase keys, resource_count).
			resourceCounts := map[string]int{}
			for _, r := range v2.Resources {
				resourceCounts[r.Project]++
			}
			out := make([]cerbapi.ProjectInfo, 0, len(v2.Projects))
			for _, p := range v2.Projects {
				out = append(out, cerbapi.ProjectInfo{
					ID:          p.ID,
					Name:        p.Name,
					Description: p.Description,
					Resources:   resourceCounts[p.ID],
				})
			}
			return printJSON(out)
		}

		if len(v2.Projects) == 0 {
			fmt.Println("No projects defined.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tDESCRIPTION")
		fmt.Fprintln(w, "--\t----\t-----------")
		for _, p := range v2.Projects {
			fmt.Fprintf(w, "%s\t%s\t%s\n", p.ID, p.Name, p.Description)
		}
		return w.Flush()
	},
}

var projectShowCmd = &cobra.Command{
	Use:   "show <project-id>",
	Short: "Show project details",
	Long:  "Shows a project and its resources.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		v2, err := registry.ResolveConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		projectID := args[0]

		// Find project
		var found bool
		for _, p := range v2.Projects {
			if p.ID == projectID {
				fmt.Printf("Project: %s\n", p.Name)
				if p.Description != "" {
					fmt.Printf("Description: %s\n", p.Description)
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("project %q not found", projectID)
		}

		// Show resources in this project
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tCONNECTOR")
		fmt.Fprintln(w, "--\t----\t----\t---------")
		count := 0
		for _, r := range v2.Resources {
			if r.Project == projectID {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Type, r.Connector)
				count++
			}
		}
		w.Flush() //nolint:errcheck

		if count == 0 {
			fmt.Println("No resources in this project.")
		}
		return nil
	},
}

func init() {
	addOutputFlag(projectListCmd, &projectListOutput)
	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectShowCmd)
}
