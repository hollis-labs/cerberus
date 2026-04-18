package main

import (
	"fmt"

	"github.com/chrispian/cerberus/internal/app"
	"github.com/chrispian/cerberus/internal/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server",
	Long:  "Starts the MCP server for tool integration over stdio (JSON-RPC 2.0).",
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := app.New(cfgPath)
		if err != nil {
			return fmt.Errorf("init app: %w", err)
		}
		defer a.Close() //nolint:errcheck

		srv := mcp.NewServer("cerberus", "0.1.0")

		// Lifecycle tools — all route through the ServiceRegistry, which
		// re-parses ~/.cerberus/config.yaml on every call. This removes
		// the stale-config trap that required a daemon bounce after
		// every config edit.
		srv.RegisterTool(mcp.NewCerberusStatusTool(a.ServiceRegistry, nil))
		srv.RegisterTool(mcp.NewCerberusStartTool(a.ServiceRegistry))
		srv.RegisterTool(mcp.NewCerberusStopTool(a.ServiceRegistry))
		srv.RegisterTool(mcp.NewCerberusRestartTool(a.ServiceRegistry))
		srv.RegisterTool(mcp.NewCerberusRebuildTool(a.ServiceRegistry))
		srv.RegisterTool(mcp.NewCerberusLogsTool(a.ServiceRegistry))
		srv.RegisterTool(mcp.NewCerberusBuildTool(a.ServiceRegistry))
		srv.RegisterTool(mcp.NewCerberusHealthTool(a.ServiceRegistry, nil))

		// New v2 tools
		srv.RegisterTool(mcp.NewCerberusProjectListTool(a.Config))
		srv.RegisterTool(mcp.NewCerberusResourceListTool(a.Config))
		srv.RegisterTool(mcp.NewCerberusPipelineListTool(a.Config))
		srv.RegisterTool(mcp.NewCerberusPipelineRunTool(a.Config, a.ServiceRegistry, a.Local))
		srv.RegisterTool(mcp.NewCerberusGithubStatusTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusGithubReleasesTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusGithubRunsTool(a.Secrets))

		// SSH tools
		srv.RegisterTool(mcp.NewCerberusSSHExecTool(a.Config, a.Secrets))
		srv.RegisterTool(mcp.NewCerberusSSHStatusTool(a.Config, a.Secrets))

		// Namecheap tools
		srv.RegisterTool(mcp.NewCerberusDomainListTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusDomainStatusTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusDNSListTool(a.Secrets))

		// Forge tools
		srv.RegisterTool(mcp.NewCerberusForgeServersTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusForgeServerTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusForgeSitesTool(a.Secrets))

		// Cloudflare tools
		srv.RegisterTool(mcp.NewCerberusCloudflareZonesTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSListTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusCloudflareDNSCreateTool(a.Secrets))

		// Docker tools
		srv.RegisterTool(mcp.NewCerberusDockerPSTool())
		srv.RegisterTool(mcp.NewCerberusDockerLogsTool())
		srv.RegisterTool(mcp.NewCerberusDockerUpTool())
		srv.RegisterTool(mcp.NewCerberusDockerDownTool())

		return srv.Run()
	},
}
