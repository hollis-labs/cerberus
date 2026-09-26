package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/webui"
)

// passkeysClient is the daemon's passkey registry, as these commands use it.
// Tests swap newPasskeysClient.
type passkeysClient interface {
	PasskeyStatus(ctx context.Context) (presence.Status, error)
	PasskeyAllowEnrollment(ctx context.Context, args cerbapi.EnrollAllowArgs) error
}

var newPasskeysClient = func() (passkeysClient, error) { return newResourceSocketClient() }

// consolePageURL is a one-time sign-in link to a page on the running
// console. Tests swap it.
var consolePageURL = func(next string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return webui.MintLoginURLTo(webui.LoginKeyPath(home, webListenAddr), next)
}

var approvalsEnrollFlags struct {
	label   string
	browser bool
}

var approvalsEnrollCmd = &cobra.Command{
	Use:   "enroll",
	Short: "Enroll a passkey for out-of-band approval (interactive)",
	Long: `Enroll a passkey (Touch ID, or a security key) that approves out-of-band
requests on the console.

This prints, and opens, a one-time console link, valid for ten minutes, where
the browser creates the passkey. The first key is trusted on first use; every
key after it has to be authorized by one already enrolled. Each enrollment is
recorded in the audit log, raises a notification, and shows in ` + "`cerberus status`" + `
for a day, so an enrollment you did not make is seen.

The registry is checked against the audit log. If it is changed any other way,
out-of-band approvals are refused for 24 hours and ` + "`cerberus status`" + ` says so.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !approvalsIsTerminal() {
			return errApprovalsNotInteractive
		}
		client, err := newPasskeysClient()
		if err != nil {
			return err
		}
		token, digest := presence.NewEnrollToken()
		if err = client.PasskeyAllowEnrollment(inProcessContext(cmd.Context()), cerbapi.EnrollAllowArgs{Digest: digest}); err != nil {
			return err
		}
		next := "/approvals?enroll=" + url.QueryEscape(token)
		if approvalsEnrollFlags.label != "" {
			next += "&label=" + url.QueryEscape(approvalsEnrollFlags.label)
		}
		return printConsoleLink(cmd.OutOrStdout(), next, approvalsEnrollFlags.browser,
			"Create the passkey on the console (the link works once, for ten minutes):")
	},
}

var approvalsKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "List the passkeys enrolled for out-of-band approval",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newPasskeysClient()
		if err != nil {
			return err
		}
		st, err := client.PasskeyStatus(inProcessContext(cmd.Context()))
		if err != nil {
			return err
		}
		return writePasskeys(cmd.OutOrStdout(), st, time.Now())
	},
}

var approvalsKeysRemoveCmd = &cobra.Command{
	Use:   "remove <fingerprint>",
	Short: "Remove an enrolled passkey (interactive; confirmed with an enrolled passkey)",
	Long: `Remove an enrolled passkey. Removing one is confirmed on the console with
an enrolled passkey (the one being removed, or another), so this prints and
opens the console page where that is done.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !approvalsIsTerminal() {
			return errApprovalsNotInteractive
		}
		return printConsoleLink(cmd.OutOrStdout(), "/approvals?remove="+url.QueryEscape(args[0]), approvalsEnrollFlags.browser,
			"Confirm the removal with an enrolled passkey on the console:")
	},
}

func printConsoleLink(out io.Writer, next string, browser bool, lead string) error {
	link, err := consolePageURL(next)
	if err != nil {
		return fmt.Errorf("passkeys are managed on the console; start it with `cerberus web`, then run this again: %w", err)
	}
	fmt.Fprintf(out, "%s\n  %s\n", lead, link)
	if browser {
		if err := openBrowser(link); err != nil {
			fmt.Fprintf(out, "Open it in your browser (%v)\n", err)
		}
	}
	return nil
}

func writePasskeys(out io.Writer, st presence.Status, now time.Time) error {
	summary, _ := st.Summary(now)
	fmt.Fprintln(out, summary)
	if len(st.Keys) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nFINGERPRINT\tLABEL\tENROLLED")
	for _, k := range st.Keys {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", k.Fingerprint, strings.TrimSpace(k.Label), k.EnrolledAt.Local().Format(time.RFC3339))
	}
	return tw.Flush()
}

func init() {
	approvalsEnrollCmd.Flags().StringVar(&approvalsEnrollFlags.label, "label", "", "a name for the key, such as \"laptop touch id\"")
	for _, c := range []*cobra.Command{approvalsEnrollCmd, approvalsKeysRemoveCmd} {
		c.Flags().BoolVar(&approvalsEnrollFlags.browser, "browser", true, "open the console page as well as printing it")
	}
	approvalsCmd.PersistentFlags().StringVar(&webListenAddr, "listen", webListenAddr, "the listen address of the `cerberus web` console the approvals and passkey links point at")
	approvalsKeysCmd.AddCommand(approvalsKeysRemoveCmd)
	approvalsCmd.AddCommand(approvalsEnrollCmd, approvalsKeysCmd)
}
