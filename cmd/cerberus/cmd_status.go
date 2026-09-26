package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/presence"
)

var statusOutput string

// statusCmd is the state an operator needs at a glance, gathered from the
// commands that own each piece. It reads; it changes nothing, and a part it
// cannot read is shown as unavailable rather than failing the rest.
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Cerberus at a glance: daemon, posture, who you are, plugins, audit, web console, passkeys",
	Long: `Show the state an operator needs at a glance, read-only:

  daemon    running or not (details: cerberus daemon status)
  posture   secure or permissive, and any per-target posture rules
  you       the principal the daemon sees for this caller (cerberus whoami)
  plugins   installed managed plugins and how many are review_pending
  audit     whether the hash chain verifies, and the last record's time
            (cerberus audit verify)
  web       the web consoles running on this machine, and where
  passkeys  out-of-band approval: not set up, how many keys, a recent
            enrollment, or a cool-down after the key registry changed by
            other means (cerberus approvals keys)

A part that cannot be read is reported as unavailable; the rest still shows.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		report := gatherStatus(cmd.Context())
		if statusOutput == outputFormatJSON {
			return printJSON(report)
		}
		return writeStatus(cmd.OutOrStdout(), report)
	},
}

func init() {
	addOutputFlag(statusCmd, &statusOutput)
	rootCmd.AddCommand(statusCmd)
}

// currentPosture is the applied posture, from the hash-checked snapshot
// (policy.Store.CurrentPosture); secure when nothing is applied or the
// snapshot fails its check. Tests swap it.
var currentPosture = func() policy.PostureSummary {
	store, err := policyStore()
	if err != nil {
		return policy.PostureSummary{Global: policy.PostureSecure, Snapshot: policy.SnapshotBaseline}
	}
	return store.CurrentPosture()
}

type statusReport struct {
	Daemon   statusDaemon          `json:"daemon"`
	Posture  policy.PostureSummary `json:"posture"`
	You      statusYou             `json:"you"`
	Plugins  statusPlugins         `json:"plugins"`
	Audit    statusAudit           `json:"audit"`
	Web      []statusWebApp        `json:"web"`
	Passkeys statusPasskeys        `json:"passkeys"`
}

type statusPasskeys struct {
	Status  *presence.Status `json:"status,omitempty"`
	Summary string           `json:"summary,omitempty"`
	Alert   bool             `json:"alert"`
	Note    string           `json:"note,omitempty"`
}

type statusDaemon struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	Note    string `json:"note,omitempty"`
}

type statusYou struct {
	Principal *cerbapi.Principal `json:"principal,omitempty"`
	Note      string             `json:"note,omitempty"`
}

type statusPlugins struct {
	Installed     int      `json:"installed"`
	ReviewPending []string `json:"review_pending"`
	Note          string   `json:"note,omitempty"`
}

type statusAudit struct {
	Intact     bool       `json:"intact"`
	Records    int        `json:"records"`
	LastRecord *time.Time `json:"last_record,omitempty"`
	Note       string     `json:"note,omitempty"`
}

type statusWebApp struct {
	URL     string `json:"url"`
	Running bool   `json:"running"`
}

func gatherStatus(ctx context.Context) statusReport {
	r := statusReport{Posture: currentPosture(), Web: []statusWebApp{}}
	r.Plugins.ReviewPending = []string{}

	ready := false
	if d, err := currentDaemonStatus(ctx); err != nil {
		r.Daemon.Note = err.Error()
	} else {
		r.Daemon = statusDaemon{Running: d.Running, PID: d.PID, Note: d.Error}
		ready = d.SocketReady
	}

	// Ask the daemon only when its socket answers, so a stopped daemon is a
	// line of status and not a page of dial warnings.
	const notRunning = "the daemon is not running"
	client, err := newResourceSocketClient()
	switch {
	case !ready:
		r.You.Note, r.Plugins.Note, r.Passkeys.Note = notRunning, notRunning, notRunning
	case err != nil:
		r.You.Note, r.Plugins.Note, r.Passkeys.Note = err.Error(), err.Error(), err.Error()
	default:
		r.You, r.Plugins, r.Passkeys = statusFromDaemon(ctx, client)
	}
	r.Audit = statusOfAudit()
	r.Web = statusOfWebConsoles()
	return r
}

// statusFromDaemon asks the daemon who this caller is and what it has loaded.
// Neither falls back to reading files in-process: the point is the daemon's
// view.
func statusFromDaemon(ctx context.Context, client statusDaemonClient) (statusYou, statusPlugins, statusPasskeys) {
	var you statusYou
	var passkeys statusPasskeys
	plugins := statusPlugins{ReviewPending: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	unavailable := func(err error) string {
		var unreachable *cerbapi.DaemonUnreachableError
		if errors.As(err, &unreachable) {
			return "the daemon is not running"
		}
		return strings.TrimSpace(err.Error())
	}
	if p, err := client.WhoAmI(ctx); err != nil {
		you.Note = unavailable(err)
	} else {
		you.Principal = &p
	}
	if st, err := client.PasskeyStatus(ctx); err != nil {
		passkeys.Note = unavailable(err)
	} else {
		passkeys.Status = &st
		passkeys.Summary, passkeys.Alert = st.Summary(time.Now())
	}
	list, err := client.ListManagedPlugins(ctx)
	if err != nil {
		plugins.Note = unavailable(err)
		return you, plugins, passkeys
	}
	plugins.Installed = len(list)
	for _, p := range list {
		if p.ReviewPending {
			plugins.ReviewPending = append(plugins.ReviewPending, p.ID)
		}
	}
	return you, plugins, passkeys
}

// statusDaemonClient is the daemon calls status makes.
type statusDaemonClient interface {
	PasskeyStatus(context.Context) (presence.Status, error)
	WhoAmI(context.Context) (cerbapi.Principal, error)
	ListManagedPlugins(context.Context) ([]cerbapi.ManagedPluginConnectorState, error)
}

func statusOfAudit() statusAudit {
	var out statusAudit
	dir, err := auditDir()
	if err != nil {
		out.Note = err.Error()
		return out
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(files) == 0 {
		out.Intact, out.Note = true, "no audit records yet"
		return out
	}
	if err = audit.Verify(dir); err != nil {
		out.Note = "chain does not verify: " + err.Error() + " (see cerberus audit verify)"
	} else {
		out.Intact = true
	}
	recs, err := audit.ReadRecords(dir)
	if err != nil {
		if out.Note == "" {
			out.Note = err.Error()
		}
		return out
	}
	out.Records = len(recs)
	if len(recs) > 0 {
		last := recs[len(recs)-1].Time
		out.LastRecord = &last
	}
	return out
}

// statusOfWebConsoles lists the consoles running on this machine. A running
// `cerberus web` keeps a login key file under ~/.cerberus/web and removes it
// on shutdown; only the file's address is read, never its key, and the
// address is probed, because a console that crashed leaves its file behind.
func statusOfWebConsoles() []statusWebApp {
	out := []statusWebApp{}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	files, _ := filepath.Glob(filepath.Join(home, ".cerberus", "web", "login-*.key"))
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // the operator's own files under ~/.cerberus/web
		if err != nil {
			continue
		}
		var file struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(data, &file) != nil || file.URL == "" {
			continue
		}
		out = append(out, statusWebApp{URL: file.URL, Running: listening(file.URL)})
	}
	return out
}

func listening(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", u.Host, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func writeStatus(w io.Writer, r statusReport) error {
	var b strings.Builder
	line := func(label, format string, args ...any) {
		fmt.Fprintf(&b, "  %-8s %s\n", label, fmt.Sprintf(format, args...))
	}
	b.WriteString("Cerberus status\n")
	switch {
	case r.Daemon.Running:
		line("daemon", "running (pid %d)", r.Daemon.PID)
	case r.Daemon.Note != "":
		line("daemon", "not running (%s)", r.Daemon.Note)
	default:
		line("daemon", "not running")
	}
	line("posture", "%s", r.Posture)
	if p := r.You.Principal; p != nil {
		uid := "uid unverified"
		if p.UIDVerified {
			uid = fmt.Sprintf("uid %d, verified", p.UID)
		}
		line("you", "%s via %s (%s; kind is self-reported)", p.Kind, p.Via, uid)
	} else {
		line("you", "unavailable (%s)", r.You.Note)
	}
	switch {
	case r.Plugins.Note != "":
		line("plugins", "unavailable (%s)", r.Plugins.Note)
	case len(r.Plugins.ReviewPending) > 0:
		line("plugins", "%d installed, %d review pending: %s", r.Plugins.Installed, len(r.Plugins.ReviewPending), strings.Join(r.Plugins.ReviewPending, ", "))
	default:
		line("plugins", "%d installed, 0 review pending", r.Plugins.Installed)
	}
	switch {
	case !r.Audit.Intact:
		line("audit", "NOT INTACT: %s", r.Audit.Note)
	case r.Audit.LastRecord != nil:
		line("audit", "chain intact, %d records, last %s", r.Audit.Records, r.Audit.LastRecord.Local().Format(time.RFC3339))
	default:
		line("audit", "chain intact (%s)", r.Audit.Note)
	}
	if len(r.Web) == 0 {
		line("web", "no console running")
	}
	for _, app := range r.Web {
		if app.Running {
			line("web", "running at %s", app.URL)
		} else {
			line("web", "not answering at %s (a console that exited without cleaning up)", app.URL)
		}
	}
	switch {
	case r.Passkeys.Note != "":
		line("passkeys", "unavailable (%s)", r.Passkeys.Note)
	case r.Passkeys.Alert:
		line("passkeys", "! %s", r.Passkeys.Summary)
	default:
		line("passkeys", "%s", r.Passkeys.Summary)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
