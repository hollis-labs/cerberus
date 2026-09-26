package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/webui"
)

var approvalsDecideFlags struct {
	reason  string
	browser bool
}

// approvalsIsTerminal is whether this is an interactive terminal. Tests swap
// it.
var approvalsIsTerminal = policyIsTerminal

// approvalsClient is the daemon's approvals, as these commands use them.
// Tests swap newApprovalsClient.
type approvalsClient interface {
	GetApproval(ctx context.Context, id string) (approval.Approval, error)
	DecideApproval(ctx context.Context, id string, args cerbapi.ApprovalDecisionArgs) (approval.Approval, error)
	RevokeApproval(ctx context.Context, id string, args cerbapi.ApprovalRevokeArgs) (approval.Approval, error)
}

var newApprovalsClient = func() (approvalsClient, error) { return newResourceSocketClient() }

// consoleApprovalURL is a one-time sign-in link to the running console's
// page for an approval, or an error naming how to start the console. Tests
// swap it.
var consoleApprovalURL = func(id string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return webui.MintLoginURLTo(webui.LoginKeyPath(home, webListenAddr), "/approvals?id="+id)
}

var errApprovalsNotInteractive = errors.New("approvals are decided, and passkeys enrolled, only from an interactive terminal or the console's approvals page, where the request is shown in full; run this in your terminal, not from a script or an agent")

var approvalsApproveCmd = &cobra.Command{
	Use:   "approve <approval-id>",
	Short: "Approve a pending request (interactive)",
	Long: `Show a pending approval request in full and approve it.

A request that has to be approved out of band — on a production, shared or
not-ours target — is approved on the console's approvals page with a passkey
(Touch ID or a security key), which is what an agent running as you cannot do.
For those, this prints and opens a one-time link to that page, and the decision
is made there. Any other request is approved here by typing its target.

The request's own surface can never approve it: an approval asked for over MCP
is approved here or on the console, never from MCP.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return decideApproval(cmd, args[0], true)
	},
}

var approvalsDenyCmd = &cobra.Command{
	Use:   "deny <approval-id>",
	Short: "Deny a pending request (interactive)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return decideApproval(cmd, args[0], false)
	},
}

var approvalsRevokeCmd = &cobra.Command{
	Use:   "revoke <approval-id>",
	Short: "Withdraw an approval before it is used (interactive)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !approvalsIsTerminal() {
			return errApprovalsNotInteractive
		}
		client, err := newApprovalsClient()
		if err != nil {
			return err
		}
		a, err := client.RevokeApproval(inProcessContext(cmd.Context()), args[0], cerbapi.ApprovalRevokeArgs{Reason: approvalsDecideFlags.reason})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s; it can no longer be used.\n", a.ID)
		return err
	},
}

// decideApproval is approve and deny: show the request, then decide it here
// or, for an out-of-band approve, send the operator to the console.
func decideApproval(cmd *cobra.Command, id string, approve bool) error {
	if !approvalsIsTerminal() {
		return errApprovalsNotInteractive
	}
	client, err := newApprovalsClient()
	if err != nil {
		return err
	}
	ctx := inProcessContext(cmd.Context())
	a, err := client.GetApproval(ctx, id)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if err = writeApproval(out, a, "the daemon"); err != nil {
		return err
	}
	if a.Status != approval.Pending {
		return fmt.Errorf("approval %s is %s, not pending; there is nothing to decide", a.ID, a.Status)
	}
	if approve && a.Channel == approval.ChannelOutOfBand {
		return sendToConsole(out, a)
	}
	if approve {
		name := approvalTargetName(a)
		fmt.Fprintf(out, "\nType the target (%s) to approve: ", name)
		line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if strings.TrimSpace(line) != name {
			return errors.New("the confirmation did not match the target; nothing was approved")
		}
	}
	decided, err := client.DecideApproval(ctx, id, cerbapi.ApprovalDecisionArgs{Approve: approve, Reason: approvalsDecideFlags.reason})
	if err != nil {
		return err
	}
	verb := "Denied"
	if approve {
		verb = "Approved"
	}
	_, err = fmt.Fprintf(out, "%s %s. The requester retries the operation with the approval id.\n", verb, decided.ID)
	return err
}

// sendToConsole prints, and opens, the console page where an out-of-band
// approval is made with a passkey.
func sendToConsole(out io.Writer, a approval.Approval) error {
	link, err := consoleApprovalURL(a.ID)
	if err != nil {
		return fmt.Errorf("approval %s is approved out of band, on the console's approvals page with a passkey; start the console with `cerberus web`, then run this again: %w", a.ID, err)
	}
	fmt.Fprintf(out, "\nThis request is approved out of band, with a passkey, on the console:\n  %s\n", link)
	if approvalsDecideFlags.browser {
		if err := openBrowser(link); err != nil {
			fmt.Fprintf(out, "Open it in your browser (%v)\n", err)
		}
	}
	return nil
}

// approvalTargetName is what an approver types: the resource the target was
// resolved through, or its kind.
func approvalTargetName(a approval.Approval) string {
	if a.Target.Resource != "" {
		return a.Target.Resource
	}
	return a.Target.Kind
}

func init() {
	for _, c := range []*cobra.Command{approvalsApproveCmd, approvalsDenyCmd, approvalsRevokeCmd} {
		c.Flags().StringVar(&approvalsDecideFlags.reason, "reason", "", "why, for the record")
	}
	approvalsApproveCmd.Flags().BoolVar(&approvalsDecideFlags.browser, "browser", true, "for an out-of-band approval, open the console page as well as printing it")
	approvalsCmd.AddCommand(approvalsApproveCmd, approvalsDenyCmd, approvalsRevokeCmd)
}
