package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/oauth"
)

// The built-in issuer's tokens (WP-S8): minted and revoked by a person on a
// terminal, with a typed confirmation the daemon checks.

var mcpHTTPTokenFlags struct {
	client string
	scopes []string
	ttl    string
}

// tokenClient is the daemon's token routes. Tests swap newTokenClient.
type tokenClient interface {
	IssueToken(ctx context.Context, args cerbapi.TokenIssueArgs) (cerbapi.IssuedToken, error)
	ListTokens(ctx context.Context) ([]oauth.TokenRecord, error)
	RevokeToken(ctx context.Context, id string, args cerbapi.TokenRevokeArgs) (oauth.TokenRecord, error)
}

var newTokenClient = func() (tokenClient, error) { return newResourceSocketClient() }

// inProcessAuth is the auth an in-process CLI builds when the daemon is
// down: the same config, key and token records. Tests swap it.
var inProcessAuth = func(ctx context.Context) (*cerbapi.Auth, error) {
	return newAuth(ctx, app.AuditSink(), app.SecretStore())
}

var mcpHTTPTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Mint, list and revoke mcp-http bearer tokens (the built-in issuer)",
	Long: `The built-in issuer mints tokens for mcp-http on your say-so: it authenticates
nobody, and Cerberus is not an identity provider. A token is bound to the
resource URL in ~/.cerberus/mcp-http.yaml, names one client and its scopes, and
expires. Give it to that client as an Authorization: Bearer header.

Scopes: cerberus:read (plain reads), cerberus:read_sensitive (adds logs and
command output), cerberus:operate (any effect, each still decided by policy).
A token's caller is an agent whatever its scopes, and cannot approve anything.`,
}

var mcpHTTPTokenIssueCmd = &cobra.Command{
	Use:   "issue --client <name> --scope <scopes> [--ttl 7d]",
	Short: "Mint a token for one client (interactive)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ttl, err := parseTTL(mcpHTTPTokenFlags.ttl)
		if err != nil {
			return err
		}
		client := strings.TrimSpace(mcpHTTPTokenFlags.client)
		scopes, unknown := oauth.ParseScopes(mcpHTTPTokenFlags.scopes)
		if len(unknown) > 0 || len(scopes) == 0 {
			return fmt.Errorf("--scope takes %s", strings.Join(oauth.Supported, ", "))
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Mint a token for %q with %s, valid %s. Whoever holds it can call mcp-http as that client.\n", client, strings.Join(scopes, " "), ttl)
		typed, err := typedConfirmation(cmd, "issue "+client)
		if err != nil {
			return err
		}
		args := cerbapi.TokenIssueArgs{Client: client, Scopes: scopes, TTL: ttl, Typed: typed}
		issued, err := withTokenClient(cmd.Context(), func(c tokenClient) (cerbapi.IssuedToken, error) { return c.IssueToken(cmd.Context(), args) },
			func(a *cerbapi.Auth) (cerbapi.IssuedToken, error) {
				return cerbapi.IssueToken(inProcessContext(cmd.Context()), a, args)
			})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\nMinted %s for %s, expiring %s. It is shown once; Cerberus keeps only its id.\n\n%s\n\n",
			issued.Record.ID, issued.Record.Client, issued.Record.ExpiresAt.Local().Format(time.RFC3339), issued.Token)
		fmt.Fprintf(out, "In the client's HTTP MCP config:\n  \"headers\": {\"Authorization\": \"Bearer <the token above>\"}\nRevoke it with: cerberus mcp-http token revoke %s\n", issued.Record.ID)
		return nil
	},
}

var mcpHTTPTokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List minted tokens (never their text)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		list, err := withTokenClient(cmd.Context(), func(c tokenClient) ([]oauth.TokenRecord, error) { return c.ListTokens(cmd.Context()) },
			func(a *cerbapi.Auth) ([]oauth.TokenRecord, error) {
				if a.Issuer == nil {
					return nil, errors.New("the built-in issuer is not enabled in " + oauth.ConfigFilename)
				}
				return a.Issuer.Store.List()
			})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No tokens have been minted.")
			return err
		}
		for _, t := range list {
			fmt.Fprintln(cmd.OutOrStdout(), cerbapi.DescribeToken(t))
		}
		return nil
	},
}

var mcpHTTPTokenRevokeCmd = &cobra.Command{
	Use:   "revoke <token-id>",
	Short: "Revoke a token (interactive)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		typed, err := typedConfirmation(cmd, "revoke "+id)
		if err != nil {
			return err
		}
		rec, err := withTokenClient(cmd.Context(), func(c tokenClient) (oauth.TokenRecord, error) {
			return c.RevokeToken(cmd.Context(), id, cerbapi.TokenRevokeArgs{Typed: typed})
		}, func(a *cerbapi.Auth) (oauth.TokenRecord, error) {
			return cerbapi.RevokeToken(inProcessContext(cmd.Context()), a, id, cerbapi.TokenRevokeArgs{Typed: typed})
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Revoked. %s\n", cerbapi.DescribeToken(rec))
		return err
	},
}

// withTokenClient runs through the daemon, or in-process when the daemon
// was never reached.
func withTokenClient[T any](ctx context.Context, daemon func(tokenClient) (T, error), local func(*cerbapi.Auth) (T, error)) (T, error) {
	var zero T
	client, err := newTokenClient()
	if err == nil {
		var out T
		if out, err = daemon(client); err == nil {
			return out, nil
		}
	}
	var unreachable *cerbapi.DaemonUnreachableError
	if !errors.As(err, &unreachable) {
		return zero, err
	}
	a, aerr := inProcessAuth(ctx)
	if aerr != nil {
		return zero, aerr
	}
	if a == nil {
		return zero, fmt.Errorf("mcp-http auth is not configured; write ~/.cerberus/%s (docs/mcp-http.md)", oauth.ConfigFilename)
	}
	return local(a)
}

func typedConfirmation(cmd *cobra.Command, phrase string) (string, error) {
	if !policyIsTerminal() {
		return "", errors.New("mcp-http tokens are minted and revoked only from an interactive terminal, by a person, with a typed phrase")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Type %q to confirm: ", phrase)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	typed := strings.TrimSpace(line)
	if typed != phrase {
		return "", errors.New("the confirmation did not match; nothing changed")
	}
	return typed, nil
}

// parseTTL reads a duration, with d for days.
func parseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return oauth.DefaultBuiltinTTL, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--ttl %q is not a duration like 7d or 12h", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--ttl %q is not a duration like 7d or 12h", s)
	}
	return d, nil
}

func init() {
	mcpHTTPTokenIssueCmd.Flags().StringVar(&mcpHTTPTokenFlags.client, "client", "", "the client the token is for (its name in the audit log)")
	mcpHTTPTokenIssueCmd.Flags().StringSliceVar(&mcpHTTPTokenFlags.scopes, "scope", nil, "scopes: cerberus:read, cerberus:read_sensitive, cerberus:operate")
	mcpHTTPTokenIssueCmd.Flags().StringVar(&mcpHTTPTokenFlags.ttl, "ttl", "7d", "lifetime, like 7d or 12h (at most 90d)")
	mcpHTTPTokenCmd.AddCommand(mcpHTTPTokenIssueCmd, mcpHTTPTokenListCmd, mcpHTTPTokenRevokeCmd)
	mcpHTTPCmd.AddCommand(mcpHTTPTokenCmd)
}
