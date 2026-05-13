package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/cerbapi"
	ghconn "github.com/chrispian/cerberus/internal/connector/github"
	"github.com/spf13/cobra"
)

var githubCmd = &cobra.Command{
	Use:   "github",
	Short: "GitHub operations",
}

var githubStatusCmd = &cobra.Command{
	Use:   "status <owner/repo>",
	Short: "Show repository status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		owner, repo, err := splitOwnerRepo(args[0])
		if err != nil {
			return err
		}

		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "github",
			Operation: "status",
			Config:    githubRepoConfig(owner, repo, 0),
		})
		if err != nil {
			return err
		}
		status, ok := result.Data.(*ghconn.RepoStatus)
		if !ok {
			return fmt.Errorf("github status: unexpected result type %T", result.Data)
		}

		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(data))
		return nil
	},
}

var githubReleasesLimit int

var githubReleasesCmd = &cobra.Command{
	Use:   "releases <owner/repo>",
	Short: "List releases",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		owner, repo, err := splitOwnerRepo(args[0])
		if err != nil {
			return err
		}

		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "github",
			Operation: "list_releases",
			Config:    githubRepoConfig(owner, repo, githubReleasesLimit),
		})
		if err != nil {
			return err
		}
		releases, ok := result.Data.([]ghconn.Release)
		if !ok {
			return fmt.Errorf("github releases: unexpected result type %T", result.Data)
		}

		if len(releases) == 0 {
			fmt.Println("No releases found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TAG\tNAME\tDRAFT\tPRERELEASE\tPUBLISHED")
		fmt.Fprintln(w, "---\t----\t-----\t----------\t---------")
		for _, r := range releases {
			fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%s\n",
				r.TagName, r.Name, r.Draft, r.Prerelease,
				r.PublishedAt.Format("2006-01-02"))
		}
		return w.Flush()
	},
}

var githubRunsLimit int

var githubRunsCmd = &cobra.Command{
	Use:   "runs <owner/repo>",
	Short: "List workflow runs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		owner, repo, err := splitOwnerRepo(args[0])
		if err != nil {
			return err
		}

		svc, closeFn, err := newExternalConnectorService()
		if err != nil {
			return err
		}
		defer closeFn()

		result, err := svc.Execute(cmd.Context(), cerbapi.ExternalConnectorOperationArgs{
			Connector: "github",
			Operation: "list_workflow_runs",
			Config:    githubRepoConfig(owner, repo, githubRunsLimit),
		})
		if err != nil {
			return err
		}
		runs, ok := result.Data.([]ghconn.WorkflowRun)
		if !ok {
			return fmt.Errorf("github runs: unexpected result type %T", result.Data)
		}

		if len(runs) == 0 {
			fmt.Println("No workflow runs found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCONCLUSION\tBRANCH\tEVENT")
		fmt.Fprintln(w, "--\t----\t------\t----------\t------\t-----")
		for _, r := range runs {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.Name, r.Status, r.Conclusion, r.Branch, r.Event)
		}
		return w.Flush()
	},
}

func splitOwnerRepo(s string) (string, string, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected owner/repo format, got %q", s)
	}
	return parts[0], parts[1], nil
}

func githubRepoConfig(owner, repo string, limit int) map[string]any {
	cfg := map[string]any{
		"owner": owner,
		"repo":  repo,
	}
	if limit > 0 {
		cfg["limit"] = limit
	}
	return cfg
}

func init() {
	githubReleasesCmd.Flags().IntVar(&githubReleasesLimit, "limit", 10, "max releases to show")
	githubRunsCmd.Flags().IntVar(&githubRunsLimit, "limit", 10, "max runs to show")
	githubCmd.AddCommand(githubStatusCmd)
	githubCmd.AddCommand(githubReleasesCmd)
	githubCmd.AddCommand(githubRunsCmd)
}
