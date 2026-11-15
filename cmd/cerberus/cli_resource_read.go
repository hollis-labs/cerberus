package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/registry"
)

// The resource read family on the CLI: the old `resource show|inspect|doctor|ensure-fresh <id>`
// (cmd/cerberus cmd_resource.go on main), with the old flags and output.
//
//   - show is old's config view, local as it was: the resource's definition from the registry
//     in the adopted root (the same resolve the serving Cerberus does), its Config redacted.
//   - inspect and doctor are thin clients over GET /api/resources/{id}/inspect and /doctor with
//     the CLI's key. Until supervision answers the full read, they print what the server answers
//     (legacy-routes.md section 6, note 3: inspect's status fields; doctor's summary and no
//     checks). A resource the local lane does not supervise is old's error.
//   - ensure-fresh is old's client-side composition (cerbapi.EnsureFresh): --force deploys;
//     otherwise it reads the status's recommended action and runs that verb (deploy, apply or
//     sync) over POST /api/resources/{id}/{verb}, passing its --ack, --approval and the install
//     override to that one call. This build's status does not compute built-binary drift advice
//     yet (it arrives with supervision's status advice, slice 8b), so a status with no
//     recommended action is answered as pending, with exit 1 and nothing changed, rather than
//     old's "no built-binary drift detected", which this build cannot claim.

// errNoDriftAdvice is ensure-fresh's answer while the status carries no drift advice.
const errNoDriftAdvice = "ensure-fresh %s: pending (slice 8b): this Cerberus does not compute built-binary drift advice yet, so it cannot tell whether %s is fresh; nothing was changed. After source changes run `cerberus resource deploy %s --ack` (or ensure-fresh --force --ack)"

// resourceReadCommands are show, inspect, doctor and ensure-fresh, against the server client
// returns and the registry reg returns.
func resourceReadCommands(client func() (*apiClient, error), reg func() (*registry.Registry, error)) []*cobra.Command {
	show := &cobra.Command{
		Use:   "show <resource-id>",
		Short: "Show resource details",
		Long:  "Shows detailed information about a specific resource.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := reg()
			if err != nil {
				return err
			}
			resolved, err := r.Resolve()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			id := args[0]
			for _, res := range resolved.Config.Resources {
				if res.ID == id {
					return printResourceShow(cmd.OutOrStdout(), res.ID, res.Name, res.Type, res.Project, res.Connector, res.Tags, res.DependsOn, res.Config)
				}
			}
			return fmt.Errorf("resource %q not found in config; run `cerberus resource list` to see available resources", id)
		},
	}

	inspect := &cobra.Command{
		Use:   "inspect <resource-id>",
		Short: "Inspect local process resource runtime details",
		Long:  "Shows detailed runtime, install, and log-path details for a local process resource. For os_service resources on macOS, this includes launchd label, plist path, artifact install layout, and log files.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var st api.ResourceInspect
			if err = cl.get(cmd.Context(), "/resources/"+url.PathEscape(args[0])+"/inspect", &st); err != nil {
				return err
			}
			if st.Status == unsupervisedStatus {
				return unsupervisedError("inspect", args[0], st.RecommendedReason, st.RecommendedNextStep)
			}
			printResourceInspect(cmd.OutOrStdout(), st)
			return nil
		},
	}

	doctor := &cobra.Command{
		Use:   "doctor <resource-id>",
		Short: "Run explicit runtime checks for a local process resource",
		Long:  "Runs pass/warn/fail checks against the runtime and install surface of a local process resource. For os_service resources on macOS, this validates launchd/plist, install paths, artifact state, and log paths.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			path := "/resources/" + url.PathEscape(args[0])
			var st api.ResourceRuntimeStatus
			if err = cl.get(cmd.Context(), path+"/status", &st); err != nil {
				return err
			}
			if st.Status == unsupervisedStatus {
				return unsupervisedError("doctor", args[0], st.RecommendedReason, st.RecommendedNextStep)
			}
			var out api.ResourceDoctor
			if err = cl.get(cmd.Context(), path+"/doctor", &out); err != nil {
				return err
			}
			printResourceDoctor(cmd.OutOrStdout(), out)
			return nil
		},
	}

	var force, ack bool
	var approvalID string
	ensure := &cobra.Command{
		Use:   "ensure-fresh <resource-id>",
		Short: "Reconcile built-binary drift; --force rebuilds source",
		Long: `Without --force, reconciles the built binaries with installed/running
resources using status advice. It does not check unbuilt source edits.
If activation is unconfirmed, it stops without changing the service and asks
you to verify the running image before retrying activation.
After editing code, use resource deploy or ensure-fresh --force to build,
install and activate the new binary. Apply activates existing build output;
sync only copies it; reload only restarts the current installed binary.

This build does not compute built-binary drift advice yet (slice 8b): without
--force, a resource with no advice is answered as pending, exit 1, nothing
changed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			override, err := installOverride(cmd)
			if err != nil {
				return err
			}
			cl, err := client()
			if err != nil {
				return err
			}
			body := mutationBody{Acknowledged: ack, ApprovalID: approvalID, InstallAfterBuildOverride: override}
			res, err := ensureFresh(cmd.Context(), cl, id, force, body)
			if err != nil {
				return pendingHint(err, approvalID)
			}
			return printEnsureFreshResult(cmd.OutOrStdout(), res)
		},
	}
	ensure.Flags().BoolVar(&force, "force", false, "always deploy (rebuild); use for dev_session resources, which have no staleness detection")
	ensure.Flags().Bool("install-after-build", true, "force-run `make install` after `make build` (overrides resource + global config)")
	ensure.Flags().Bool("no-install-after-build", false, "skip `make install` after `make build` for this invocation only")
	ensure.Flags().BoolVar(&ack, "ack", false, "acknowledge the operation; every resource mutation requires it")
	ensure.Flags().StringVar(&approvalID, "approval", "", "run under this approval, once the owner has approved it on the console")
	return []*cobra.Command{show, inspect, doctor, ensure}
}

// unsupervisedStatus is the status the server reports for a resource the local lane does not
// supervise.
const unsupervisedStatus = "unsupervised"

// unsupervisedError is old's UnsupervisedOperationError, from the server's reason and next step.
func unsupervisedError(verb, id, reason, next string) error {
	return fmt.Errorf("%s does not apply to %q: %s; try: %s", verb, id, reason, next)
}

// ensureFreshResult is old's cerbapi.EnsureFreshResult.
type ensureFreshResult struct {
	ServiceID string        `json:"service_id"`
	Result    *api.OpResult `json:"result,omitempty"`
	Action    string        `json:"action"`
	Reason    string        `json:"reason,omitempty"`
	Message   string        `json:"message,omitempty"`
	Success   bool          `json:"success"`
}

// ensureFresh is old's cerbapi.EnsureFresh over the routes: body goes to whichever mutation it
// chooses, so the caller's acknowledgment covers exactly that one call.
func ensureFresh(ctx context.Context, cl *apiClient, id string, force bool, body mutationBody) (*ensureFreshResult, error) {
	run := func(verb, reason string) (*ensureFreshResult, error) {
		var op api.OpResult
		if err := cl.post(ctx, "/resources/"+url.PathEscape(id)+"/"+verb, body, &op); err != nil {
			return nil, err
		}
		r := &ensureFreshResult{ServiceID: id, Action: verb, Reason: reason, Result: &op, Success: op.Success, Message: op.Message}
		if !op.Success && op.Error != "" {
			r.Message = op.Error
		}
		return r, nil
	}
	if force {
		return run("deploy", "forced rebuild")
	}
	var st api.ResourceRuntimeStatus
	if err := cl.get(ctx, "/resources/"+url.PathEscape(id)+"/status", &st); err != nil {
		return nil, err
	}
	switch st.RecommendedAction {
	case "deploy", "apply", "sync":
		return run(st.RecommendedAction, st.RecommendedReason)
	case "":
		if st.Status == unsupervisedStatus {
			return nil, unsupervisedError("ensure-fresh", id, st.RecommendedReason, st.RecommendedNextStep)
		}
		return &ensureFreshResult{ServiceID: id, Action: "noop", Message: fmt.Sprintf(errNoDriftAdvice, id, id, id)}, nil
	case "inspect":
		return &ensureFreshResult{ServiceID: id, Action: "noop", Reason: st.RecommendedReason,
			Message: localconn.RecommendedNextStep("inspect", st.RecommendedReason)}, nil
	default:
		return &ensureFreshResult{ServiceID: id, Action: "noop", Reason: st.RecommendedReason,
			Message: fmt.Sprintf("unsupported recommended action %q; inspect the resource before changing it", st.RecommendedAction)}, nil
	}
}

// printEnsureFreshResult is old's: a failed or refused run is an error (exit 1).
func printEnsureFreshResult(w io.Writer, res *ensureFreshResult) error {
	if !res.Success {
		if res.Message != "" {
			return errors.New(res.Message)
		}
		return fmt.Errorf("ensure-fresh %s: %s failed", res.ServiceID, res.Action)
	}
	if res.Action == "noop" {
		_, err := fmt.Fprintf(w, "%s: %s\n", res.ServiceID, res.Message)
		return err
	}
	reason := res.Reason
	if reason != "" {
		reason = " (" + reason + ")"
	}
	if _, err := fmt.Fprintf(w, "%s: %s%s\n", res.ServiceID, res.Action, reason); err != nil {
		return err
	}
	if res.Message != "" {
		_, err := fmt.Fprintln(w, res.Message)
		return err
	}
	return nil
}

// printResourceShow is old's show, line for line.
func printResourceShow(w io.Writer, id, name, typ, project, connector string, tags, dependsOn []string, cfg map[string]any) error {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("ID:        %s\n", id)
	p("Name:      %s\n", name)
	p("Type:      %s\n", typ)
	p("Project:   %s\n", project)
	p("Connector: %s\n", connector)
	if len(tags) > 0 {
		p("Tags:      %s\n", strings.Join(tags, ", "))
	}
	if len(dependsOn) > 0 {
		p("Depends:   %s\n", strings.Join(dependsOn, ", "))
	}
	if typ == string(domain.ResourceProcess) && connector == "local" {
		if spec, err := localconn.SpecFromResourceConfig(cfg); err == nil {
			mode, supervisor, runFrom := string(spec.Mode), string(spec.Supervisor), string(spec.RunFrom)
			if mode == "" {
				mode = string(localconn.ProcessModeDevSession)
			}
			if supervisor == "" {
				supervisor = string(localconn.ProcessSupervisorAuto)
			}
			if runFrom == "" {
				runFrom = string(localconn.ProcessRunFromWorkspace)
			}
			p("Mode:      %s\n", mode)
			p("Supervisor:%s\n", supervisor)
			p("Run From:  %s\n", runFrom)
			for _, l := range []struct{ label, value string }{{"Service:   ", spec.ServiceName}, {"Artifact:  ", spec.ArtifactPath}, {"Install:   ", spec.InstallRoot}} {
				if l.value != "" {
					p("%s%s\n", l.label, l.value)
				}
			}
		}
	}
	if len(cfg) > 0 {
		p("Config:\n")
		data, err := redact.MarshalIndent(cfg, "  ", "  ")
		if err != nil {
			return err
		}
		p("  %s\n", data)
	}
	return nil
}

// printResourceInspect is old's inspect record, line for line.
func printResourceInspect(w io.Writer, st api.ResourceInspect) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	for _, warning := range st.DependencyWarnings {
		p("Warning: %s\n", warning)
	}
	for _, warning := range st.ConfigWarnings {
		p("Warning: %s\n", warning)
	}
	p("Resource:    %s\n", st.ID)
	p("Name:        %s\n", st.Name)
	p("Status:      %s\n", st.Status)
	p("Type:        %s\n", st.Type)
	p("Project:     %s\n", st.Project)
	p("Connector:   %s\n", st.Connector)
	line := func(label, value string) {
		if value != "" {
			p("%s%s\n", label, value)
		}
	}
	line("Mode:        ", st.Mode)
	line("Supervisor:  ", st.Supervisor)
	line("Run From:    ", st.RunFrom)
	line("Workspace:   ", st.WorkspaceDir)
	line("Working Dir: ", st.WorkingDir)
	line("Command:     ", strings.Join(st.Command, " "))
	line("Build:       ", st.BuildStrategy)
	line("Service:     ", st.ServiceName)
	line("Plist:       ", st.PlistPath)
	line("Unit:        ", st.UnitPath)
	line("Install:     ", st.InstallRoot)
	line("Current:     ", st.InstallWorkDir)
	line("Bin Dir:     ", st.BinDir)
	line("Artifact:    ", st.ArtifactPath)
	if st.ArtifactInstalled {
		p("Installed:   true\n")
	}
	if st.ArtifactStale {
		p("Stale:       true\n")
	}
	line("Drift:       ", st.ArtifactStaleReason)
	line("Source:      ", st.ArtifactSource)
	line("Synced At:   ", st.ArtifactSyncedAt)
	line("Stdout Log:  ", st.StdoutLogPath)
	line("Stderr Log:  ", st.StderrLogPath)
	line("Recommend:   ", st.RecommendedAction)
	line("Context:     ", st.RecommendedReason)
	line("Next Step:   ", st.RecommendedNextStep)
	if st.OperatorStopped {
		p("Stopped By:  operator stop (apply, deploy, or reload resumes dev_session auto-restart)\n")
	}
	if st.LaunchdLoaded {
		p("Loaded:      true\n")
	}
	line("Launchd:     ", st.LaunchdState)
	if st.LaunchdPID > 0 {
		p("Launchd PID: %d\n", st.LaunchdPID)
	}
	if st.LaunchdLastExitCode != nil {
		p("Last Exit:   %d\n", *st.LaunchdLastExitCode)
	}
	if st.LaunchdThrottled {
		p("Throttled:   true\n")
	}
	line("Reason:      ", st.LaunchdReason)
	line("Diagnosis:   ", st.LaunchdDiagnosis)
	if len(st.LaunchdHighlights) > 0 {
		p("Highlights:  %s\n", strings.Join(st.LaunchdHighlights, " | "))
	}
	block := func(title, text string) {
		if text == "" {
			return
		}
		p("%s\n", title)
		p("%s", text)
		if !strings.HasSuffix(text, "\n") {
			p("\n")
		}
	}
	block("Launchctl:", st.LaunchdRaw)
	printSystemdSummary(w, st.SystemdActiveState, st.SystemdSubState, st.SystemdUnitFileState, st.SystemdPID, st.SystemdExitStatus, st.SystemdRestarts, st.SystemdDiagnosis, st.SystemdHighlights)
	block("Systemctl:", st.SystemdRaw)
	block("Journal:", st.SystemdJournal)
}

// printSystemdSummary is old's systemd record; it prints nothing for a resource not under
// systemd.
func printSystemdSummary(w io.Writer, active, sub, unitFileState string, pid int, exitStatus *int, restarts int, diagnosis string, highlights []string) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	if active != "" {
		p("Systemd:     %s/%s\n", active, sub)
	}
	if unitFileState != "" {
		p("Unit File:   %s\n", unitFileState)
	}
	if pid > 0 {
		p("Main PID:    %d\n", pid)
	}
	if exitStatus != nil {
		p("Last Exit:   %d\n", *exitStatus)
	}
	if restarts > 0 {
		p("Restarts:    %d\n", restarts)
	}
	if diagnosis != "" {
		p("Diagnosis:   %s\n", diagnosis)
	}
	if len(highlights) > 0 {
		p("Highlights:  %s\n", strings.Join(highlights, " | "))
	}
}

// printResourceDoctor is old's doctor output.
func printResourceDoctor(w io.Writer, out api.ResourceDoctor) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("Resource:    %s\n", out.ResourceID)
	if out.Status != "" {
		p("Status:      %s\n", out.Status)
	}
	p("Summary:     %s\n", out.Summary)
	for _, l := range []struct{ label, value string }{
		{"Recommend:   ", out.RecommendedAction}, {"Context:     ", out.RecommendedReason}, {"Next Step:   ", out.RecommendedNextStep},
	} {
		if l.value != "" {
			p("%s%s\n", l.label, l.value)
		}
	}
	if out.OperatorStopped {
		p("Stopped By:  operator stop\n")
	}
	p("Checks:\n")
	for _, c := range out.Checks {
		p("  [%s] %s: %s\n", strings.ToUpper(c.Status), c.Name, c.Message)
	}
}
