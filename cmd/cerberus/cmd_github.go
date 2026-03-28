package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/chrispian/cerberus/internal/app"
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

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		gh, err := ghconn.New(a.Secrets)
		if err != nil {
			return err
		}

		status, err := gh.RepoStatus(context.Background(), owner, repo)
		if err != nil {
			return err
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

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		gh, err := ghconn.New(a.Secrets)
		if err != nil {
			return err
		}

		releases, err := gh.ListReleases(context.Background(), owner, repo, githubReleasesLimit)
		if err != nil {
			return err
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

		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		gh, err := ghconn.New(a.Secrets)
		if err != nil {
			return err
		}

		runs, err := gh.ListWorkflowRuns(context.Background(), owner, repo, githubRunsLimit)
		if err != nil {
			return err
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

func init() {
	githubReleasesCmd.Flags().IntVar(&githubReleasesLimit, "limit", 10, "max releases to show")
	githubRunsCmd.Flags().IntVar(&githubRunsLimit, "limit", 10, "max runs to show")
	githubCmd.AddCommand(githubStatusCmd)
	githubCmd.AddCommand(githubReleasesCmd)
	githubCmd.AddCommand(githubRunsCmd)
}
