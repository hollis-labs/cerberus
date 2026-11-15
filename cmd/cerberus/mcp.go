package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/spf13/cobra"

	smcp "github.com/hollis-labs/substrate/modules/mcp"

	"github.com/hollis-labs/cerberus/internal/api"
	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
)

// stdioScopes are the local principal's under `cerberus mcp` (plan section 4.3): read, operate
// and admin, never approve.
var stdioScopes = []string{api.ScopeRead, api.ScopeOperate, api.ScopeAdmin}

// mcpCmd is `cerberus mcp`: MCP over stdio for a locally launched client. It runs no service of
// its own. Every tool call goes to the serving Cerberus's /api over loopback with the CLI key
// file, so one process supervises, as the old stdio client dialed the daemon; with no serving
// Cerberus a call fails naming its address, and nothing runs in-process instead.
func (c *cerberus) mcpCmd(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve Cerberus's MCP tools over stdio, through the serving Cerberus",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			client, err := newAPIClient(c.app, c.home)
			if err != nil {
				return err
			}
			if err := cmcp.Register(c.app, apiBackend{c: client}); err != nil {
				return err
			}
			return smcp.ServeStdio(ctx, c.app, smcp.StdioOptions{LocalTrust: true, LocalScopes: stdioScopes})
		},
	}
}

// apiBackend forwards tool calls to the serving Cerberus with the CLI key file.
type apiBackend struct{ c *apiClient }

// Do calls /api; a refusal comes back as the tool's structured refusal, except a refused key,
// whose message names the key file.
func (b apiBackend) Do(ctx context.Context, method, path string, in, out any) error {
	err := b.c.do(ctx, method, path, in, out)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status != http.StatusUnauthorized {
		return &cmcp.Refusal{Status: ae.Status, Code: ae.Code, Message: ae.Message, Approval: ae.Approval}
	}
	return err
}
