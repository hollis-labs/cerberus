package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// The plugin install review on the CLI: the old `connectors plugin managed install <dir>` and
// `review <id>` (cmd/cerberus cmd_connectors_plugin.go on main), with the old flags and output,
// over the serving Cerberus's /api/plugins/connectors routes with the CLI's key.
//
// The old build reviewed in-process and took the typed plugin id on the operator's terminal.
// Accepting is now the owner's (the lead's ruling (a) on decision 0.1): on the console, or from
// here naming an approval the owner decided there. This CLI calls with an API key, so it shows
// the review, and asking is still the old interactive act (a terminal and the typed plugin id);
// the server answers 409 approval_pending naming an out-of-band approval bound to that review,
// the owner approves it on the console, signed in, and the same command with --approval <id>
// accepts it, with no terminal needed. --yes (the old permissive-posture shortcut) is gone: no
// path skips the owner.

// pluginCommands are `connectors plugin managed install|review` and, from cli_admin.go, the
// managed plugin lifecycle and `connectors exec|plan`, against the server client returns.
func pluginCommands(client func() (*apiClient, error), isTerminal func() bool) []*cobra.Command {
	var dev bool
	var approvalID, reviewApproval string
	install := &cobra.Command{
		Use:   "install <plugin-dir>",
		Short: "Review a plugin and install it into the plugin store (the owner accepts)",
		Long: `Review a plugin directory and install it. It prints what the plugin declares it
can do and where the declaration has gaps. Accepting is the owner's: on the
console, typing the plugin id; or here. Here, the first run (on a terminal, typing
the plugin id) asks for the owner's approval of this review; the owner approves
it on the console, signed in, and install <dir> --approval <id> installs it.

The reviewed bundle is copied into the adopted root's plugins/<id>/<digest>/ and
runs from there, so rebuilding the source directory changes nothing that runs
until it is installed again. Installing a plugin that is already installed is an
upgrade, shown as a diff against the review accepted before. Cerberus then
reloads the plugin by id.

Every load compares the installed bundle with the one accepted and refuses a
plugin that changed (plugin_changed); 'review <id>' accepts the change.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			return runPluginReview(cmd, client, isTerminal, "/plugins/connectors/install", admin.ReviewRequest{PluginDir: dir, Dev: dev}, approvalID)
		},
	}
	install.Flags().BoolVar(&dev, "dev", false, "development install: destructive operations are refused (requires a devmode build)")
	install.Flags().StringVar(&approvalID, "approval", "", "the approval the owner decided on the console that accepts this review")

	review := &cobra.Command{
		Use:   "review <plugin-id>",
		Short: "Review an installed plugin again: one installed before reviews existed, or a changed bundle (the owner accepts)",
		Long: `Review an installed plugin. A plugin installed before install review existed
keeps loading, reported as review_pending, until this is run once for it: the
review is the same one install shows, and accepting it copies the plugin into
the plugin store. A plugin whose bundle no longer matches its accepted review is
shown as a diff, and accepting it accepts the change. Accepting is the owner's,
as for install: on the console, or here with --approval <id>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginReview(cmd, client, isTerminal, "/plugins/connectors/"+url.PathEscape(args[0])+"/review", admin.ReviewRequest{}, reviewApproval)
		},
	}
	review.Flags().StringVar(&reviewApproval, "approval", "", "the approval the owner decided on the console that accepts this review")

	managed := &cobra.Command{Use: "managed", Short: "The managed plugins of the serving Cerberus"}
	managed.AddCommand(install, review)
	managed.AddCommand(managedPluginCommands(client)...)
	plugin := &cobra.Command{Use: "plugin", Short: "Connector plugins"}
	plugin.AddCommand(managed)
	connectors := &cobra.Command{Use: "connectors", Short: "Connectors and their plugins"}
	connectors.AddCommand(plugin)
	connectors.AddCommand(connectorExecCommands(client)...)
	return []*cobra.Command{connectors}
}

// errPluginReviewNotInteractive is asking without a terminal.
var errPluginReviewNotInteractive = errors.New("a plugin install or review is accepted by the owner: asking for that is done from an interactive terminal, where it shows what the plugin can do and asks you to type its id, or on the console; with an approval the owner decided on the console, run it with --approval <id>")

// runPluginReview shows the review (the route's plan form), then asks for the owner's approval
// or, with --approval, accepts the review under it.
func runPluginReview(cmd *cobra.Command, client func() (*apiClient, error), isTerminal func() bool, path string, req admin.ReviewRequest, approvalID string) error {
	if approvalID == "" && !isTerminal() {
		return errPluginReviewNotInteractive
	}
	cl, err := client()
	if err != nil {
		return err
	}
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	var shown gate.ConnectorPlan
	err = cl.post(ctx, path+"/plan", req, &shown)
	var refused *apiError
	if errors.As(err, &refused) && refused.Code == "nothing_to_review" {
		_, err = fmt.Fprintln(out, refused.Message)
		return err
	}
	if err != nil {
		return err
	}
	var preview admin.ReviewPreview
	if err = json.Unmarshal(shown.Plan.Preview, &preview); err != nil {
		return fmt.Errorf("the server's review could not be read: %w", err)
	}
	if preview.ID == "" {
		return errors.New("the server answered no review")
	}
	_, _ = fmt.Fprint(out, preview.Text)
	if approvalID == "" {
		typed, askErr := askPhrase(cmd, fmt.Sprintf("\nAccept? Type the plugin id (%s) to confirm: ", preview.ID))
		if askErr != nil {
			return askErr
		}
		if typed != preview.ID {
			return fmt.Errorf("the confirmation did not match the plugin id %q; nothing was installed", preview.ID)
		}
	}
	var res admin.ReviewResult
	err = cl.post(ctx, path, struct {
		admin.ReviewRequest
		ApprovalID string `json:"approval_id,omitempty"`
	}{req, approvalID}, &res)
	if pending, perr := approvalPending(out, err, approvalID); pending {
		return perr
	}
	if err != nil {
		return pluginOwnerHint(err, cl)
	}
	return writeReviewAccepted(out, res)
}

// pluginOwnerHint adds, to the server's 428 refusal of an accept from this CLI's key, the two
// ways the owner does it.
func pluginOwnerHint(err error, cl *apiClient) error {
	var refused *apiError
	if errors.As(err, &refused) && refused.Status == http.StatusPreconditionRequired && refused.Code == string(gate.CodeApprovalRequired) {
		return fmt.Errorf("%w\na plugin review is accepted by the owner: on the console, signed in, typing the plugin id; or from this CLI (the API key in %s) with --approval <id> once the owner has approved that request on the console (run it without --approval to ask)", err, cl.keyFile)
	}
	return err
}

// writeReviewAccepted is the old output after an accepted review, with the daemon's reload now
// the serving Cerberus's own.
func writeReviewAccepted(w io.Writer, res admin.ReviewResult) error {
	st := res.State
	if _, err := fmt.Fprintf(w, "Accepted %s %s (%s); it runs from %s\n", st.ID, st.Version, st.BundleDigest, st.Path); err != nil {
		return err
	}
	var err error
	switch {
	case res.Warning != "":
		_, err = fmt.Fprintln(w, res.Warning)
	case st.Loaded:
		_, err = fmt.Fprintf(w, "Cerberus reloaded %s.\n", st.ID)
	default:
		_, err = fmt.Fprintf(w, "Cerberus has registered %s; load it with the console, or POST /api/plugins/connectors/%s/load.\n", st.ID, st.ID)
	}
	return err
}

// pluginCommands are the plugin review verbs against the serving Cerberus.
func (c *cerberus) pluginCommands() []*cobra.Command {
	return pluginCommands(func() (*apiClient, error) { return newAPIClient(c.app, c.home) }, stdIsTerminal)
}
