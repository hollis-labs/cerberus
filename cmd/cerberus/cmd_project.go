package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/cerbapi"
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

// projectSurface is the shared runtime service, reached either through
// the daemon socket or in-process. Both list commands take everything
// from it rather than resolving the config themselves: a second
// construction of the project set is a second thing to keep in step.
type projectSurface interface {
	resolveDiagnoser
	ListProjects(ctx context.Context) ([]cerbapi.ProjectInfo, error)
	ListResources(ctx context.Context, args cerbapi.ResourceListArgs) ([]cerbapi.ResourceInfo, error)
}

// projectClient prefers the daemon and falls back in-process, matching
// the resource commands. The returned surface is also the diagnostics
// source, so a trailing skip notice describes the same view the rows
// came from.
func projectClient(ctx context.Context) (projectSurface, []cerbapi.ProjectInfo, error) {
	if client, err := newResourceSocketClient(); err == nil {
		list, listErr := client.ListProjects(ctx)
		if listErr == nil {
			return client, list, nil
		}
		var dErr *cerbapi.DaemonUnreachableError
		if !errors.As(listErr, &dErr) {
			return nil, nil, listErr
		}
	}
	svc := newResourceRuntimeService()
	list, err := svc.ListProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	return svc, list, nil
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List projects",
	Long:  "Lists all projects defined in the config, with their capabilities.",
	RunE: func(cmd *cobra.Command, args []string) error {
		surface, list, err := projectClient(cmd.Context())
		if err != nil {
			return err
		}

		if projectListOutput == outputFormatJSON {
			if list == nil {
				list = []cerbapi.ProjectInfo{}
			}
			return printJSON(list)
		}

		if len(list) == 0 {
			fmt.Println("No projects defined.")
			reportResolveNotice(cmd.Context(), surface)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tRESOURCES\tCAPABILITIES\tDESCRIPTION")
		fmt.Fprintln(w, "--\t----\t---------\t------------\t-----------")
		for _, p := range list {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
				p.ID, p.Name, p.Resources, valueOrDash(strings.Join(p.Capabilities, ", ")), p.Description)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		reportResolveNotice(cmd.Context(), surface)
		return nil
	},
}

var projectShowCmd = &cobra.Command{
	Use:   "show <project-id>",
	Short: "Show project details",
	Long:  "Shows a project's metadata, capabilities, links and resources.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		surface, list, err := projectClient(cmd.Context())
		if err != nil {
			return err
		}

		projectID := args[0]
		var found *cerbapi.ProjectInfo
		for i := range list {
			if list[i].ID == projectID {
				found = &list[i]
				break
			}
		}
		if found == nil {
			// A project can be missing because its config was skipped,
			// not because it was never declared. Say so rather than
			// leaving the operator to guess which.
			fmt.Fprintf(os.Stderr, "project %q not found\n", projectID)
			reportResolveNotice(cmd.Context(), surface)
			return fmt.Errorf("project %q not found", projectID)
		}

		fmt.Printf("Project: %s\n", found.Name)
		fmt.Printf("Slug:    %s\n", found.ID)
		if found.Description != "" {
			fmt.Printf("Description: %s\n", found.Description)
		}
		if len(found.Capabilities) > 0 {
			fmt.Printf("Capabilities: %s\n", strings.Join(found.Capabilities, ", "))
		}
		if len(found.Links) > 0 {
			fmt.Println("Links:")
			for _, link := range found.Links {
				fmt.Printf("  %-16s %s\n", link.Kind, link.Target)
			}
		}

		resources, err := surface.ListResources(cmd.Context(), cerbapi.ResourceListArgs{ProjectID: projectID})
		if err != nil {
			return err
		}

		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tCONNECTOR")
		fmt.Fprintln(w, "--\t----\t----\t---------")
		for _, r := range resources {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Type, r.Connector)
		}
		w.Flush() //nolint:errcheck

		if len(resources) == 0 {
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
