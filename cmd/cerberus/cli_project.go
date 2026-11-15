package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
)

// `cerberus project list|show` (old cmd_project.go), with the old flags and output, over
// GET /api/projects and GET /api/resources?project_id= with the CLI's key: the serving Cerberus
// resolves the registry, as old's daemon did, so the rows are one construction of the project
// set.

// projectCommand is `project` against the server client returns.
func projectCommand(client func() (*apiClient, error)) *cobra.Command {
	group := &cobra.Command{
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
	var output string
	list := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Long:  "Lists all projects defined in the config, with their capabilities.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var projects []api.ProjectInfo
			if err = cl.get(cmd.Context(), "/projects", &projects); err != nil {
				return err
			}
			if output == outputJSON {
				if projects == nil {
					projects = []api.ProjectInfo{}
				}
				return printJSON(cmd.OutOrStdout(), projects)
			}
			return printProjectList(cmd.OutOrStdout(), projects)
		},
	}
	list.Flags().StringVarP(&output, "output", "o", outputText, "output format: text|json")

	show := &cobra.Command{
		Use:   "show <project-id>",
		Short: "Show project details",
		Long:  "Shows a project's metadata, capabilities, links and resources.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var projects []api.ProjectInfo
			if err = cl.get(cmd.Context(), "/projects", &projects); err != nil {
				return err
			}
			id := args[0]
			for _, p := range projects {
				if p.ID != id {
					continue
				}
				var resources []api.ResourceInfo
				if err = cl.get(cmd.Context(), "/resources?project_id="+url.QueryEscape(id), &resources); err != nil {
					return err
				}
				return printProjectShow(cmd.OutOrStdout(), p, resources)
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "project %q not found\n", id)
			return fmt.Errorf("project %q not found", id)
		},
	}
	group.AddCommand(list, show)
	return group
}

// printProjectList is old's table.
func printProjectList(w io.Writer, projects []api.ProjectInfo) error {
	if len(projects) == 0 {
		_, err := fmt.Fprintln(w, "No projects defined.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tRESOURCES\tCAPABILITIES\tDESCRIPTION")
	_, _ = fmt.Fprintln(tw, "--\t----\t---------\t------------\t-----------")
	for _, p := range projects {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", p.ID, p.Name, p.Resources, valueOrDash(strings.Join(p.Capabilities, ", ")), p.Description)
	}
	return tw.Flush()
}

// printProjectShow is old's record and resource table.
func printProjectShow(w io.Writer, p api.ProjectInfo, resources []api.ResourceInfo) error {
	out := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	out("Project: %s\n", p.Name)
	out("Slug:    %s\n", p.ID)
	if p.Description != "" {
		out("Description: %s\n", p.Description)
	}
	if len(p.Capabilities) > 0 {
		out("Capabilities: %s\n", strings.Join(p.Capabilities, ", "))
	}
	if len(p.Links) > 0 {
		out("Links:\n")
		for _, link := range p.Links {
			out("  %-16s %s\n", link.Kind, link.Target)
		}
	}
	out("\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tTYPE\tCONNECTOR")
	_, _ = fmt.Fprintln(tw, "--\t----\t----\t---------")
	for _, r := range resources {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Type, r.Connector)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(resources) == 0 {
		out("No resources in this project.\n")
	}
	return nil
}
