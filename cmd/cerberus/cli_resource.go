package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// The resource mutation verbs on the CLI: the old `resource apply|deploy|reload|stop|sync|remove
// <id>`, `resource plan <id> <verb>` and `resource logs <id>` (cmd/cerberus cmd_resource.go on
// main), with the old flags and output, as thin clients over the legacy routes L6, L7 and L8
// (POST /api/resources/{id}/{verb}, GET /api/resources/{id}/logs) with the CLI's key. Runbook
// D7 runs `resource apply <id> --ack`; tachyon and tether sysop call the same verbs.
//
// What the old in-process path did with the daemon down is gone: the verbs need the serving
// Cerberus, which records every attempt. A policy approval is the owner's on the console: the
// first run is answered 409 approval_pending, and the same command with --approval <id> runs it
// once the owner has approved it. Old's terminal confirm (tty_confirm) and --break-glass need a
// person at the CLI, which no API key is (decision 0.1), so they are not carried.

// resourceVerbs are the mutations, with the old fallback line each printed on success.
var resourceVerbs = []struct{ verb, done, short, long string }{
	{"apply", "Applied", "Apply an already-built local process resource",
		"Applies a local process resource using its configured runtime backend. It does not run the build command first; use `cerberus resource deploy` when source changes need to be built."},
	{"deploy", "Deployed", "Build then apply a local process resource",
		"Runs the resource's declared build contract first, then applies it through the configured runtime backend. Use this when the intent is source-to-runtime deployment: make the running service match the current source tree. For already-built artifacts, use `cerberus resource apply`. Pass --no-install-after-build to skip the post-build install step for this invocation (useful when bisecting build vs install failures)."},
	{"reload", "Reloaded", "Kickstart a local process resource through its runtime backend",
		"Asks the configured runtime backend to restart the current installed resource without syncing artifacts or rewriting service definitions. IMPORTANT: reload does NOT rebuild or re-sync — it relaunches the existing (possibly stale) installed artifact. If your source changed, use `cerberus resource deploy`, not reload."},
	{"stop", "Stopped", "Stop a local process resource without removing install state",
		"Stops a local process resource through its configured runtime backend without deleting installed artifacts or unit state. For dev_session resources, this also suppresses auto-restart until the resource is explicitly applied, deployed, or reloaded."},
	{"sync", "Synced", "Sync installed runtime artifacts for a local process resource",
		"Syncs installed runtime artifacts without applying the runtime backend. This is primarily useful for os_service resources using run_from=artifact when you want to copy the artifact now and activate it later."},
	{"remove", "Removed", "Uninstall a local process resource from its runtime backend",
		"Removes a local process resource from its configured runtime backend, uninstalling the service and its installed artifact tree. Use stop, not remove, for non-destructive stop/pause intent."},
}

// mutationBody is the L7/L8 body.
type mutationBody struct {
	Acknowledged              bool   `json:"acknowledged,omitempty"`
	ApprovalID                string `json:"approval_id,omitempty"`
	Plan                      bool   `json:"plan,omitempty"`
	InstallAfterBuildOverride *bool  `json:"install_after_build_override,omitempty"`
}

// resourceMutationCommands are the verbs added under `resource`, against the server client
// returns.
func resourceMutationCommands(client func() (*apiClient, error)) []*cobra.Command {
	var out []*cobra.Command
	for _, v := range resourceVerbs {
		var ack bool
		var approvalID, output string
		var install, noInstall bool
		c := &cobra.Command{
			Use:   v.verb + " <resource-id>",
			Short: v.short,
			Long:  v.long,
			Args:  cobra.ExactArgs(1),
		}
		c.RunE = func(cmd *cobra.Command, args []string) error {
			id := args[0]
			body := mutationBody{Acknowledged: ack, ApprovalID: approvalID}
			if v.verb == "deploy" {
				override, err := installOverride(cmd)
				if err != nil {
					return err
				}
				body.InstallAfterBuildOverride = override
			}
			cl, err := client()
			if err != nil {
				return err
			}
			var res api.OpResult
			err = cl.post(cmd.Context(), "/resources/"+url.PathEscape(id)+"/"+v.verb, body, &res)
			if err != nil {
				return pendingHint(err, approvalID)
			}
			if output == outputJSON {
				if perr := printJSON(cmd.OutOrStdout(), res); perr != nil {
					return perr
				}
				if !res.Success {
					return opFailure(res, fmt.Sprintf("%s resource %s", v.done, id))
				}
				return nil
			}
			return printOpResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), res, fmt.Sprintf("%s resource %s", v.done, id))
		}
		c.Flags().BoolVar(&ack, "ack", false, "acknowledge the operation; every resource mutation requires it")
		c.Flags().StringVar(&approvalID, "approval", "", "run under this approval, once the owner has approved it on the console")
		c.Flags().StringVarP(&output, "output", "o", outputText, "output format: text|json")
		if v.verb == "deploy" {
			c.Flags().BoolVar(&install, "install-after-build", true, "force-run `make install` after `make build` (overrides resource + global config)")
			c.Flags().BoolVar(&noInstall, "no-install-after-build", false, "skip `make install` after `make build` for this invocation only")
		}
		out = append(out, c)
	}

	var planAck bool
	plan := &cobra.Command{
		Use:   "plan <resource-id> <deploy|apply|reload|stop|sync|remove>",
		Short: "Show the plan an approval of a resource verb would bind to",
		Long: `Compute and print the plan for one resource verb, and its hash, without
running it: the resource's definition (as a keyed digest), the checkout's
commit and dirty flag for deploy, the build output apply and sync would
install, the unit apply and deploy would write (as a keyed digest), and the
resource's observed state.

This is the same function an approval is asked for and used with, so the hash
printed is the one an approval of the verb would record. It is recorded like a
dry run, and writes nothing.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, verb := args[0], args[1]
			known := false
			for _, v := range resourceVerbs {
				known = known || v.verb == verb
			}
			if !known {
				return fmt.Errorf("resource plan: unknown verb %q; want deploy, apply, reload, stop, sync or remove", verb)
			}
			cl, err := client()
			if err != nil {
				return err
			}
			var res api.OpResult
			if err = cl.post(cmd.Context(), "/resources/"+url.PathEscape(id)+"/"+verb, mutationBody{Acknowledged: planAck, Plan: true}, &res); err != nil {
				return err
			}
			if len(res.Plan) == 0 {
				return fmt.Errorf("resource plan %s %s: the serving Cerberus returned no plan", id, verb)
			}
			var shown any
			if err = json.Unmarshal(res.Plan, &shown); err != nil {
				return fmt.Errorf("resource plan %s %s: the plan could not be read: %w", id, verb, err)
			}
			return printJSON(cmd.OutOrStdout(), shown)
		},
	}
	plan.Flags().BoolVar(&planAck, "ack", false, "plan the verb as it would be sent with --ack")

	var lines int
	var stream, logsApproval string
	logs := &cobra.Command{
		Use:   "logs <resource-id>",
		Short: "Show local process resource logs",
		Long:  "Shows recent logs for a local process resource. For os_service resources, use --stream stdout or --stream stderr to select the stream the service writes under its install root (the same files under launchd and systemd).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			q := url.Values{"lines": {strconv.Itoa(lines)}, "stream": {stream}}
			if logsApproval != "" {
				q.Set("approval_id", logsApproval)
			}
			var res api.LogLines
			if err = cl.get(cmd.Context(), "/resources/"+url.PathEscape(args[0])+"/logs?"+q.Encode(), &res); err != nil {
				return pendingHint(err, logsApproval)
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprint(w, res.Content)
			if !strings.HasSuffix(res.Content, "\n") {
				_, _ = fmt.Fprintln(w)
			}
			return nil
		},
	}
	logs.Flags().IntVarP(&lines, "lines", "n", 50, "number of log lines to return")
	logs.Flags().StringVar(&stream, "stream", "stdout", "log stream to read: stdout or stderr")
	logs.Flags().StringVar(&logsApproval, "approval", "", "read under this approval, once the owner has approved it on the console")
	return append(out, plan, logs)
}

// installOverride is old's resolveDeployFlags: --install-after-build[=bool] passes its value,
// --no-install-after-build forces false, both is an error, neither is no override.
func installOverride(cmd *cobra.Command) (*bool, error) {
	yesSet, noSet := cmd.Flags().Changed("install-after-build"), cmd.Flags().Changed("no-install-after-build")
	switch {
	case yesSet && noSet:
		return nil, errors.New("--install-after-build and --no-install-after-build are mutually exclusive")
	case yesSet:
		v, err := cmd.Flags().GetBool("install-after-build")
		if err != nil {
			return nil, fmt.Errorf("read --install-after-build: %w", err)
		}
		return &v, nil
	case noSet:
		v := false
		return &v, nil
	}
	return nil, nil
}

// printOpResult is old's printResourceOpResult: warnings and, for a failed run, the captured
// build and install output and the build log on stderr; the message or the fallback on stdout.
// A failed run is an error, so the command exits 1.
func printOpResult(out, errOut io.Writer, res api.OpResult, fallback string) error {
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintf(errOut, "Warning: %s\n", w)
	}
	if !res.Success {
		if res.BuildOutput != "" {
			_, _ = fmt.Fprintf(errOut, "--- build output ---\n%s\n--------------------\n", res.BuildOutput)
		}
		if res.InstallOutput != "" {
			_, _ = fmt.Fprintf(errOut, "--- install output ---\n%s\n----------------------\n", res.InstallOutput)
		}
		if res.BuildLogPath != "" {
			_, _ = fmt.Fprintf(errOut, "build log: %s\n", res.BuildLogPath)
		}
		return opFailure(res, fallback)
	}
	msg := res.Message
	if msg == "" {
		msg = fallback
	}
	_, err := fmt.Fprintln(out, msg)
	return err
}

// opFailure is the error a failed run exits with: its error, or "<fallback> failed".
func opFailure(res api.OpResult, fallback string) error {
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return fmt.Errorf("%s failed", fallback)
}

// pendingHint adds, to a refusal that names a pending approval, what the owner does on the
// console and the command to run after; the command still fails (exit 1), as old's did, so a
// script never takes a pending call for done. Named with --approval and still pending, it says
// that approval is not approved yet.
func pendingHint(err error, named string) error {
	var refused *apiError
	if !errors.As(err, &refused) || refused.Code != string(gate.CodeApprovalPending) || refused.Approval == nil {
		return err
	}
	if named != "" {
		return fmt.Errorf("%w\napproval %s is not approved yet: the owner approves it on the console, signed in; then run this again", err, named)
	}
	return fmt.Errorf("%w\nApprove %s on the console, signed in as the owner, then run this again with --approval %s", err, refused.Approval.ID, refused.Approval.ID)
}
