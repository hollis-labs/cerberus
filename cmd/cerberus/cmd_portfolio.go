package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/spf13/cobra"
)

var portfolioCmd = &cobra.Command{
	Use:   "portfolio",
	Short: "Combined portfolio health and cross-fleet observation",
	Long: `Read-only portfolio views across hosts, projects, deployments, shared
Docker containers, and connectors.

Subcommands:
  health   combined portfolio health projection (cerberus.portfolio.health.v1)`,
}

var (
	portfolioJSONFlag bool
	portfolioOutput   string
	portfolioQuery    struct {
		mode                string
		project             string
		host                string
		kind                string
		rollupStatus        string
		includeUnsupervised bool
	}
)

func portfolioWantsJSON() bool {
	return portfolioJSONFlag || portfolioOutput == outputFormatJSON
}

func newPortfolioClient(cmd *cobra.Command) (cerbapi.PortfolioHealthClient, error) {
	if flag := cmd.Flag("config"); flag == nil || !flag.Changed {
		client, err := commandSocket(cmd.Context())
		if err != nil {
			return nil, err
		}
		if client != nil {
			return client, nil
		}
	}
	cfg, err := loadUnifiedForTools(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("init app: %w", err)
	}
	runtimeSvc := app.NewResourceRuntimeService(cfgPath, cfg)
	hostSvc := app.NewLocalHostService(cfgPath)
	extSvc := app.NewExternalConnectorService(cfgPath)
	svc := cerbapi.NewPortfolioHealthService(
		app.AuditSink(),
		runtimeSvc,
		hostSvc,
		extSvc,
		nil,
		cerbapi.PortfolioHealthOptions{},
	)
	return localPortfolioClient{svc: svc}, nil
}

type localPortfolioClient struct {
	svc *cerbapi.PortfolioHealthService
}

func (l localPortfolioClient) PortfolioHealth(ctx context.Context, q cerbapi.PortfolioHealthQuery) (*cerbapi.PortfolioHealthProjection, error) {
	return l.svc.Project(inProcessContext(ctx), q)
}

var portfolioHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Combined portfolio health projection across hosts, projects, and deployments",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newPortfolioClient(cmd)
		if err != nil {
			return err
		}
		inc := portfolioQuery.includeUnsupervised
		q := cerbapi.PortfolioHealthQuery{
			Mode:                portfolioQuery.mode,
			Project:             portfolioQuery.project,
			Host:                portfolioQuery.host,
			Kind:                portfolioQuery.kind,
			RollupStatus:        portfolioQuery.rollupStatus,
			IncludeUnsupervised: &inc,
		}
		proj, err := client.PortfolioHealth(cmd.Context(), q)
		if err != nil {
			return err
		}
		if portfolioWantsJSON() {
			return printJSON(proj)
		}
		return writePortfolioHealth(cmd.OutOrStdout(), proj)
	},
}

func writePortfolioHealth(w io.Writer, p *cerbapi.PortfolioHealthProjection) error {
	if p == nil {
		return nil
	}
	_, _ = fmt.Fprintf(w, "Portfolio Health (%s) — controller=%s overall=%s generated=%s\n",
		p.Contract, p.ControllerID, p.Summary.OverallStatus, p.GeneratedAt)
	_, _ = fmt.Fprintf(w, "Hosts: %d | Projects: %d | Deployments: %d (healthy=%d degraded=%d down=%d expected_stopped=%d unsupervised=%d unknown=%d)\n\n",
		len(p.Hosts),
		len(p.Projects),
		len(p.Deployments),
		p.Summary.ByStatus[cerbapi.RollupHealthy],
		p.Summary.ByStatus[cerbapi.RollupDegraded],
		p.Summary.ByStatus[cerbapi.RollupDown],
		p.Summary.ByStatus[cerbapi.RollupExpectedStopped],
		p.Summary.ByStatus[cerbapi.RollupUnsupervised],
		p.Summary.ByStatus[cerbapi.RollupUnknown],
	)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if len(p.Hosts) > 0 {
		_, _ = fmt.Fprintln(tw, "HOST\tLIFECYCLE\tREACHABILITY\tREADINESS\tROLLUP\tTRANSPORT")
		for _, h := range p.Hosts {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s (%s)\t%s\t%s\n",
				h.Name, h.Lifecycle, h.Reachability, h.Readiness, h.ReadinessSource, h.RollupStatus, valueOrDash(h.Transport))
		}
		_, _ = fmt.Fprintln(tw)
	}
	if len(p.Projects) > 0 {
		_, _ = fmt.Fprintln(tw, "PROJECT\tROLLUP\tHEALTHY\tDEGRADED\tDOWN\tEXP_STOPPED\tUNSUPERVISED")
		for _, pr := range p.Projects {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
				pr.ID,
				pr.RollupStatus,
				pr.ResourceCounts[cerbapi.RollupHealthy],
				pr.ResourceCounts[cerbapi.RollupDegraded],
				pr.ResourceCounts[cerbapi.RollupDown],
				pr.ResourceCounts[cerbapi.RollupExpectedStopped],
				pr.ResourceCounts[cerbapi.RollupUnsupervised],
			)
		}
		_, _ = fmt.Fprintln(tw)
	}
	if len(p.Deployments) > 0 {
		_, _ = fmt.Fprintln(tw, "DEPLOYMENT\tPROJECT\tHOST\tKIND\tDESIRED\tOBSERVED\tREADINESS\tROLLUP")
		for _, d := range p.Deployments {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				d.ID, valueOrDash(d.ProjectID), d.Host, d.Kind, d.DesiredState, d.ObservedState, d.Readiness, d.RollupStatus)
		}
	}
	return tw.Flush()
}

func init() {
	portfolioCmd.GroupID = "resources"
	rootCmd.AddCommand(portfolioCmd)
	portfolioCmd.AddCommand(portfolioHealthCmd)

	addOutputFlag(portfolioHealthCmd, &portfolioOutput)
	portfolioHealthCmd.Flags().BoolVar(&portfolioJSONFlag, "json", false, "output JSON")
	portfolioHealthCmd.Flags().StringVar(&portfolioQuery.mode, "mode", cerbapi.PortfolioModeCached, "observation mode: cached or live_bounded")
	portfolioHealthCmd.Flags().StringVar(&portfolioQuery.project, "project", "", "filter to one project ID")
	portfolioHealthCmd.Flags().StringVar(&portfolioQuery.host, "host", "", "filter to one host name")
	portfolioHealthCmd.Flags().StringVar(&portfolioQuery.kind, "kind", "", "filter by deployment kind (local_service, remote_service, shared_infra, connector_handle)")
	portfolioHealthCmd.Flags().StringVar(&portfolioQuery.rollupStatus, "rollup-status", "", "filter by rollup status")
	portfolioHealthCmd.Flags().BoolVar(&portfolioQuery.includeUnsupervised, "include-unsupervised", true, "include unsupervised connector handles")
}
