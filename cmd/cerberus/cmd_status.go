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
)

var statusOutput string

// statusCmd is the state an operator needs at a glance, gathered from the
// commands that own each piece. It reads; it changes nothing, and a part it
// cannot read is shown as unavailable rather than failing the rest.
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Cerberus at a glance: daemon, posture, who you are, plugins, audit, web console",
	Long: `Show the state an operator needs at a glance, read-only:

  daemon    running or not (details: cerberus daemon status)
  posture   the enforcement posture; always "secure" until postures land
  you       the principal the daemon sees for this caller (cerberus whoami)
  plugins   installed managed plugins and how many are review_pending
  audit     whether the hash chain verifies, and the last record's time
            (cerberus audit verify)
  web       the web consoles running on this machine, and where

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

// statusPosture is the placeholder until P2-5 lands postures: Cerberus's
// behavior today is the secure default.
const statusPosture = "secure"

type statusReport struct {
	Daemon  statusDaemon   `json:"daemon"`
	Posture string         `json:"posture"`
	You     statusYou      `json:"you"`
	Plugins statusPlugins  `json:"plugins"`
	Audit   statusAudit    `json:"audit"`
	Web     []statusWebApp `json:"web"`
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
	r := statusReport{Posture: statusPosture, Web: []statusWebApp{}}
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
		r.You.Note, r.Plugins.Note = notRunning, notRunning
	case err != nil:
		r.You.Note, r.Plugins.Note = err.Error(), err.Error()
	default:
		r.You, r.Plugins = statusFromDaemon(ctx, client)
	}
	r.Audit = statusOfAudit()
	r.Web = statusOfWebConsoles()
	return r
}

// statusFromDaemon asks the daemon who this caller is and what it has loaded.
// Neither falls back to reading files in-process: the point is the daemon's
// view.
func statusFromDaemon(ctx context.Context, client statusDaemonClient) (statusYou, statusPlugins) {
	var you statusYou
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
	list, err := client.ListManagedPlugins(ctx)
	if err != nil {
		plugins.Note = unavailable(err)
		return you, plugins
	}
	plugins.Installed = len(list)
	for _, p := range list {
		if p.ReviewPending {
			plugins.ReviewPending = append(plugins.ReviewPending, p.ID)
		}
	}
	return you, plugins
}

// statusDaemonClient is the two daemon calls status makes.
type statusDaemonClient interface {
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
	line("posture", "%s (postures are not configurable yet)", r.Posture)
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
	_, err := io.WriteString(w, b.String())
	return err
}
