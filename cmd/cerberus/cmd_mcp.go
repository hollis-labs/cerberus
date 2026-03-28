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

		// Existing tools (backward compatible — same names, same schemas)
		srv.RegisterTool(mcp.NewCerberusStatusTool(a.Services, nil))
		srv.RegisterTool(mcp.NewCerberusStartTool(a.Services))
		srv.RegisterTool(mcp.NewCerberusStopTool(a.Services))
		srv.RegisterTool(mcp.NewCerberusRestartTool(a.Services))
		srv.RegisterTool(mcp.NewCerberusRebuildTool(a.Services))
		srv.RegisterTool(mcp.NewCerberusLogsTool(a.Services))
		srv.RegisterTool(mcp.NewCerberusBuildTool(a.Services))
		srv.RegisterTool(mcp.NewCerberusHealthTool(a.Services, nil))

		// New v2 tools
		srv.RegisterTool(mcp.NewCerberusProjectListTool(a.Config))
		srv.RegisterTool(mcp.NewCerberusResourceListTool(a.Config))
		srv.RegisterTool(mcp.NewCerberusPipelineListTool(a.Config))
		srv.RegisterTool(mcp.NewCerberusPipelineRunTool(a.Config, a.Services, a.Local))
		srv.RegisterTool(mcp.NewCerberusGithubStatusTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusGithubReleasesTool(a.Secrets))
		srv.RegisterTool(mcp.NewCerberusGithubRunsTool(a.Secrets))

		return srv.Run()
	},
}
