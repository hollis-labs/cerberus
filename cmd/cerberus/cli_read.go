package main

import (
	"bytes"
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

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/gate"
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
	// header, when set, adds headers to every request (`cerberus mcp`'s client label).
	header func(ctx context.Context) http.Header
}

// newAPIClient reads the key file named by the settings and the listen address from the
// config. A missing key file is an error naming the path and how to create it.
func newAPIClient(app *module.App, home string) (*apiClient, error) {
	st, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return nil, err
	}
	return newKeyedClient(app, st.CLIKeyFile, fmt.Sprintf("cerberus identity key create --name %s --scope cerberus.read --out %s", api.CLIKeyName, st.CLIKeyFile),
		"cerberus.cli_key_file")
}

// newKeyedClient is a client presenting the key in keyFile; create is the command that makes one,
// setting the settings key that names the file.
func newKeyedClient(app *module.App, keyFile, create, setting string) (*apiClient, error) {
	// The key travels as a bearer over plain http, so only to a loopback address.
	if lerr := requireLoopback(app); lerr != nil {
		return nil, lerr
	}
	hh, _ := app.Config["httphost"].(map[string]any)
	addr, _ := hh["addr"].(string)
	fi, err := os.Stat(keyFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no API key at %s: create one with `%s` (or set %s)", keyFile, create, setting)
	}
	if err != nil {
		return nil, fmt.Errorf("read the API key %s: %w", keyFile, err)
	}
	if perr := privateKeyFile(keyFile, fi); perr != nil {
		return nil, perr
	}
	raw, err := os.ReadFile(keyFile) //nolint:gosec // the operator's configured key file, checked above
	if err != nil {
		return nil, fmt.Errorf("read the API key %s: %w", keyFile, err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return nil, fmt.Errorf("the API key file %s is empty", keyFile)
	}
	return &apiClient{base: "http://" + addr + "/api", key: key, keyFile: keyFile,
		http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// apiError is a refusal from the server, with its flat body.
type apiError struct {
	Status        int
	Code, Message string
	// Approval is the approval an approval_pending refusal names under details.
	Approval *gate.ApprovalRef
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the server answered %d", e.Status)
	}
	return fmt.Sprintf("the server answered %d %s: %s", e.Status, e.Code, e.Message)
}

// get decodes the JSON answer to GET path into out.
func (c *apiClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// post sends in as the JSON body of POST path and decodes the answer into out.
func (c *apiClient) post(ctx context.Context, path string, in, out any) error {
	return c.do(ctx, http.MethodPost, path, in, out)
}

func (c *apiClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if c.header != nil {
		for k, vs := range c.header(ctx) {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the serving Cerberus at %s is not answering (is `cerberus serve` running?): %w", c.base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var flat struct {
			Code, Message string
			Details       struct {
				Approval *gate.ApprovalRef `json:"approval"`
			}
		}
		_ = json.Unmarshal(data, &flat)
		e := &apiError{Status: resp.StatusCode, Code: flat.Code, Message: flat.Message, Approval: flat.Details.Approval}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w (the key in %s was refused: revoked, expired or not a key of this Cerberus)", e, c.keyFile)
		}
		return e
	}
	return json.Unmarshal(data, out)
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
		{"plugins", pluginsLine(ctx, c)},
		{"audit", "unavailable (slice 6)"},
		{"web", "unavailable (slice 11)"},
		{"passkeys", "not used: approvals come from the owner's console session (decision 0.1)"},
		{"presence", "not used: no passkey enrollment (decision 0.1)"},
		{"enforce", "unavailable (slice 6)"},
		{"grants", "unavailable (slice 6)"},
	}
}

// pluginsLine is the old plugins section: how many managed plugins are installed and which are
// review_pending, from the serving Cerberus (observe mode runs none: unavailable).
func pluginsLine(ctx context.Context, c *apiClient) string {
	list, err := managedPlugins(ctx, c)
	if err != nil {
		return "unavailable (" + err.Error() + ")"
	}
	var pending []string
	for _, p := range list {
		if p.ReviewPending {
			pending = append(pending, p.ID)
		}
	}
	if len(pending) > 0 {
		return fmt.Sprintf("%d installed, %d review pending: %s", len(list), len(pending), strings.Join(pending, ", "))
	}
	return fmt.Sprintf("%d installed, 0 review pending", len(list))
}

// managedPlugins reads GET /plugins/connectors.
func managedPlugins(ctx context.Context, c *apiClient) ([]admin.PluginState, error) {
	var list []admin.PluginState
	err := c.get(ctx, "/plugins/connectors", &list)
	return list, err
}

// pluginBanners are the old status's lines for a plugin the host stopped restarting.
func pluginBanners(ctx context.Context, c *apiClient) []string {
	list, err := managedPlugins(ctx, c)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range list {
		if p.GaveUp != "" {
			out = append(out, fmt.Sprintf("!!! PLUGIN %s stopped after 3 restarts in 10m (%s). Load it again from the console (POST /api/plugins/connectors/%s/load)", p.ID, p.GaveUp, p.ID))
		}
	}
	return out
}

// withPluginBanners puts the plugin lines where the old status had them: after the lockdown and
// freezes, before the breaker's.
func withPluginBanners(brakes, plugins []string) []string {
	out := make([]string, 0, len(brakes)+len(plugins))
	i := 0
	for ; i < len(brakes) && !strings.HasPrefix(brakes[i], "!!! BREAKER"); i++ {
		out = append(out, brakes[i])
	}
	out = append(out, plugins...)
	return append(out, brakes[i:]...)
}

// writeStatus is the old layout: the brake banners, a title, then "  %-8s %s" per section.
func writeStatus(w io.Writer, banners []string, sections []statusSection) {
	for _, b := range banners {
		_, _ = fmt.Fprintln(w, b)
	}
	_, _ = fmt.Fprintln(w, "Cerberus status")
	for _, s := range sections {
		_, _ = fmt.Fprintf(w, "  %-8s %s\n", s.label, s.value)
	}
}

// readCommands are `resource list`, `resource status`, the resource verbs (cli_resource.go) and
// `status`.
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

	resource := &cobra.Command{Use: "resource", Short: "V2 resource management",
		Long: "Manages v2 resources through the serving Cerberus. After editing source, use deploy to build and activate it. Lower-level verbs: apply for already-built activation, reload for restart-only (NO rebuild), sync for artifact-copy only, stop for non-destructive stop/pause intent, and remove only for uninstalling runtime state. Every mutation needs --ack."}
	resource.AddCommand(list, status)
	resource.AddCommand(resourceMutationCommands(client)...)

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
			writeStatus(cmd.OutOrStdout(), withPluginBanners(statusBanners(cmd.Context(), cl), pluginBanners(cmd.Context(), cl)), sections)
			return nil
		},
	}
	overall.Flags().StringVarP(&overallOut, "output", "o", outputText, "output format: text|json")
	return []*cobra.Command{resource, overall}
}

// privateKeyFile refuses a key file that is not a regular file readable by its owner alone, or
// that another account owns: a key others can read is a key others can use.
func privateKeyFile(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("the API key %s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("the API key %s is mode %#o, readable by other accounts; run `chmod 600 %s`", path, perm, path)
	}
	if uid, ok := fileOwner(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("the API key %s belongs to uid %d, not to this account (uid %d)", path, uid, os.Getuid())
	}
	return nil
}
