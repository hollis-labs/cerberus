package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/substrate/module"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// The read verbs of the CLI. They talk to the serving Cerberus over HTTP, presenting the API key
// from cerberus.cli_key_file as a bearer, and print what the old CLI printed for the same verb
// (cmd/cerberus on main: printResourceList, printResourceRuntimeStatus, writeStatus).

const (
	outputText = "text"
	outputJSON = "json"
)

// apiClient calls the serving Cerberus's /api routes with the CLI's key.
type apiClient struct {
	base, key, keyFile string
	http               *http.Client
}

// newAPIClient reads the key file named by the settings and the listen address from the
// config. A missing key file is an error naming the path and how to create it.
func newAPIClient(app *module.App, home string) (*apiClient, error) {
	st, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return nil, err
	}
	hh, _ := app.Config["httphost"].(map[string]any)
	addr, _ := hh["addr"].(string)
	if addr == "" {
		return nil, errors.New("httphost.addr is empty; the CLI needs the serving Cerberus's address")
	}
	raw, err := os.ReadFile(st.CLIKeyFile) //nolint:gosec // the operator's configured key file
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no API key at %s: create one with `cerberus identity key create --name cli --scope cerberus.read --out %s` "+
			"(or set cerberus.cli_key_file)", st.CLIKeyFile, st.CLIKeyFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the API key %s: %w", st.CLIKeyFile, err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return nil, fmt.Errorf("the API key file %s is empty", st.CLIKeyFile)
	}
	return &apiClient{base: "http://" + addr + "/api", key: key, keyFile: st.CLIKeyFile,
		http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// apiError is a refusal from the server, with its flat body.
type apiError struct {
	Status        int
	Code, Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the server answered %d", e.Status)
	}
	return fmt.Sprintf("the server answered %d %s: %s", e.Status, e.Code, e.Message)
}

// get decodes the JSON answer to GET path into out.
func (c *apiClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the serving Cerberus at %s is not answering (is `cerberus serve` running?): %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var flat struct{ Code, Message string }
		_ = json.Unmarshal(body, &flat)
		e := &apiError{Status: resp.StatusCode, Code: flat.Code, Message: flat.Message}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w (the key in %s was refused: revoked, expired or not a key of this Cerberus)", e, c.keyFile)
		}
		return e
	}
	return json.Unmarshal(body, out)
}

// printJSON is the old CLI's -o json: indented, through the redactor.
func printJSON(w io.Writer, v any) error {
	data, err := redact.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func valueOrDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// printResourceList is the old table.
func printResourceList(w io.Writer, list []api.ResourceInfo) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tTYPE\tPROJECT\tCONNECTOR\tMODE\tSUPERVISOR\tRUN FROM\tSTATUS\tARTIFACT\tNEXT\tENV\tOWNER\tADMIN\tTAGS")
	_, _ = fmt.Fprintln(tw, "--\t----\t----\t-------\t---------\t----\t----------\t--------\t------\t--------\t----\t---\t-----\t-----\t----")
	for _, r := range list {
		tags := "-"
		if len(r.Tags) > 0 {
			tags = strings.Join(r.Tags, ", ")
		}
		artifact := "-"
		if r.ArtifactInstalled {
			artifact = "installed"
		}
		if r.ArtifactStale {
			artifact = "stale"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Name, r.Type, r.Project, r.Connector,
			valueOrDash(r.Mode), valueOrDash(r.Supervisor), valueOrDash(r.RunFrom),
			valueOrDash(r.Status), artifact, valueOrDash(r.RecommendedAction), valueOrDash(r.Env), valueOrDash(r.Owner), valueOrDash(r.Admin), tags)
	}
	_ = tw.Flush()
	if len(list) == 0 {
		_, _ = fmt.Fprintln(w, "No resources found.")
	}
}

// printResourceRuntimeStatus is the old status record, line for line.
func printResourceRuntimeStatus(w io.Writer, st api.ResourceRuntimeStatus) {
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
	lines := []struct {
		label, value string
	}{
		{"Mode:        ", st.Mode}, {"Supervisor:  ", st.Supervisor}, {"Run From:    ", st.RunFrom},
		{"Service:     ", st.ServiceName}, {"Install:     ", st.InstallRoot}, {"Artifact:    ", st.ArtifactPath},
	}
	for _, l := range lines {
		if l.value != "" {
			p("%s%s\n", l.label, l.value)
		}
	}
	if st.ArtifactInstalled {
		p("Installed:   true\n")
	}
	if st.ArtifactStale {
		p("Stale:       true\n")
	}
	for _, l := range []struct{ label, value string }{
		{"Drift:       ", st.ArtifactStaleReason}, {"Source:      ", st.ArtifactSource}, {"Synced At:   ", st.ArtifactSyncedAt},
		{"Recommend:   ", st.RecommendedAction}, {"Context:     ", st.RecommendedReason}, {"Next Step:   ", st.RecommendedNextStep},
	} {
		if l.value != "" {
			p("%s%s\n", l.label, l.value)
		}
	}
	if st.OperatorStopped {
		p("Stopped By:  operator stop (apply, deploy, or reload resumes dev_session auto-restart)\n")
	}
	if st.LaunchdLoaded {
		p("Loaded:      true\n")
	}
	if st.LaunchdState != "" {
		p("Launchd:     %s\n", st.LaunchdState)
	}
	if st.LaunchdPID > 0 {
		p("Launchd PID: %d\n", st.LaunchdPID)
	}
	if st.LaunchdLastExitCode != nil {
		p("Last Exit:   %d\n", *st.LaunchdLastExitCode)
	}
	if st.LaunchdThrottled {
		p("Throttled:   true\n")
	}
	if st.LaunchdReason != "" {
		p("Reason:      %s\n", st.LaunchdReason)
	}
	if st.LaunchdDiagnosis != "" {
		p("Diagnosis:   %s\n", st.LaunchdDiagnosis)
	}
	if len(st.LaunchdHighlights) > 0 {
		p("Highlights:  %s\n", strings.Join(st.LaunchdHighlights, " | "))
	}
	if st.SystemdActiveState != "" {
		p("Systemd:     %s/%s\n", st.SystemdActiveState, st.SystemdSubState)
	}
	if st.SystemdUnitFileState != "" {
		p("Unit File:   %s\n", st.SystemdUnitFileState)
	}
	if st.SystemdPID > 0 {
		p("Main PID:    %d\n", st.SystemdPID)
	}
	if st.SystemdExitStatus != nil {
		p("Last Exit:   %d\n", *st.SystemdExitStatus)
	}
	if st.SystemdRestarts > 0 {
		p("Restarts:    %d\n", st.SystemdRestarts)
	}
	if st.SystemdDiagnosis != "" {
		p("Diagnosis:   %s\n", st.SystemdDiagnosis)
	}
	if len(st.SystemdHighlights) > 0 {
		p("Highlights:  %s\n", strings.Join(st.SystemdHighlights, " | "))
	}
}

// statusSection is one line of `cerberus status`: the old labels, in the old order.
type statusSection struct{ label, value string }

// statusSections reads what the serving Cerberus can say. The sections later slices own are
// reported unavailable, as the old command reported a part it could not read.
func statusSections(ctx context.Context, c *apiClient) []statusSection {
	daemon := "running at " + strings.TrimSuffix(c.base, "/api") + " (observe mode)"
	var h api.DaemonHealth
	if err := c.get(ctx, "/health", &h); err != nil {
		daemon = "not running (" + err.Error() + ")"
	}
	return []statusSection{
		{"daemon", daemon},
		{"posture", "unavailable (slice 6)"},
		{"you", "unavailable (slice 6)"},
		{"plugins", "unavailable (slice 7)"},
		{"audit", "unavailable (slice 6)"},
		{"web", "unavailable (slice 11)"},
		{"passkeys", "not used: approvals come from the owner's console session (decision 0.1)"},
		{"presence", "not used: no passkey enrollment (decision 0.1)"},
		{"enforce", "unavailable (slice 6)"},
		{"grants", "unavailable (slice 6)"},
	}
}

// writeStatus is the old layout: a title, then "  %-8s %s" per section.
func writeStatus(w io.Writer, sections []statusSection) {
	_, _ = fmt.Fprintln(w, "Cerberus status")
	for _, s := range sections {
		_, _ = fmt.Fprintf(w, "  %-8s %s\n", s.label, s.value)
	}
}

// readCommands are `resource list`, `resource status` and `status`.
func (c *cerberus) readCommands() []*cobra.Command {
	client := func() (*apiClient, error) { return newAPIClient(c.app, c.home) }

	var listOut, listProject, statusOut, overallOut string
	list := &cobra.Command{
		Use:   "list",
		Short: "List resources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			path := "/resources"
			if listProject != "" {
				path += "?project_id=" + url.QueryEscape(listProject)
			}
			var rows []api.ResourceInfo
			if err := cl.get(cmd.Context(), path, &rows); err != nil {
				return err
			}
			if listOut == outputJSON {
				if rows == nil {
					rows = []api.ResourceInfo{}
				}
				return printJSON(cmd.OutOrStdout(), rows)
			}
			printResourceList(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	list.Flags().StringVar(&listProject, "project", "", "filter by project ID")
	list.Flags().StringVarP(&listOut, "output", "o", outputText, "output format: text|json")

	status := &cobra.Command{
		Use:   "status <resource-id>",
		Short: "Show the runtime status of a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			var st api.ResourceRuntimeStatus
			if err := cl.get(cmd.Context(), "/resources/"+url.PathEscape(args[0])+"/status", &st); err != nil {
				return err
			}
			if statusOut == outputJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			printResourceRuntimeStatus(cmd.OutOrStdout(), st)
			return nil
		},
	}
	status.Flags().StringVarP(&statusOut, "output", "o", outputText, "output format: text|json")

	resource := &cobra.Command{Use: "resource", Short: "Read resources (observe mode: mutations arrive with slice 5)"}
	resource.AddCommand(list, status)

	overall := &cobra.Command{
		Use:   "status",
		Short: "Show Cerberus at a glance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := client()
			if err != nil {
				return err
			}
			sections := statusSections(cmd.Context(), cl)
			if overallOut == outputJSON {
				m := map[string]string{}
				for _, s := range sections {
					m[s.label] = s.value
				}
				return printJSON(cmd.OutOrStdout(), m)
			}
			writeStatus(cmd.OutOrStdout(), sections)
			return nil
		},
	}
	overall.Flags().StringVarP(&overallOut, "output", "o", outputText, "output format: text|json")
	return []*cobra.Command{resource, overall}
}
