package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// The audit commands read ~/.cerberus/audit directly rather than through
// the daemon: the log is the operator's own files, and reading it must keep
// working when the daemon is down. Tests swap these.
var (
	auditDir        = app.AuditDir
	auditSink       = app.AuditSink
	auditIsTerminal = func() bool {
		return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
	}
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Read, verify and prune the operation audit log",
	Long: `Every operation that reaches a connector or the resource runtime is recorded
in ~/.cerberus/audit as an intent before it runs and an outcome after, in
hash-chained monthly files. These commands read that log directly; they do not
need the daemon.

JSON output is the records exactly as written, one per line. The log never
holds a credential value — only names and a keyed digest of the arguments —
so it is not passed through redaction, which would rewrite those names.`,
}

var auditTailCount int
var auditOutput string

var auditTailCmd = &cobra.Command{
	Use:   "tail",
	Short: "Show the most recent audit records",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		recs, err := readAuditRecords()
		if err != nil {
			return err
		}
		if auditTailCount > 0 && len(recs) > auditTailCount {
			recs = recs[len(recs)-auditTailCount:]
		}
		return writeAuditRecords(cmd.OutOrStdout(), recs, auditOutput)
	},
}

var auditQuery struct {
	connector, operation, surface, outcome, target, since, until string
	limit                                                        int
}

var auditQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "Find audit records by connector, operation, surface, outcome, target or time",
	Long: `Find audit records. Every filter given must match.

  --outcome matches an outcome record's code (ok, or the error code the caller
            received, such as acknowledgment_required) or its decision
            (allowed, refused). Intents have no outcome, so it drops them.
  --target  matches any identifying field of the target: a resource id, a
            host, a droplet id.
  --since / --until take a date (2026-09-01) or an RFC 3339 time; --until is
            exclusive.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		f := audit.Filter{
			Connector: auditQuery.connector, Operation: auditQuery.operation,
			Surface: auditQuery.surface, Outcome: auditQuery.outcome, Target: auditQuery.target,
		}
		var err error
		if f.Since, err = parseAuditTime(auditQuery.since); err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		if f.Until, err = parseAuditTime(auditQuery.until); err != nil {
			return fmt.Errorf("--until: %w", err)
		}
		recs, err := readAuditRecords()
		if err != nil {
			return err
		}
		var out []audit.Record
		for _, rec := range recs {
			if f.Match(rec) {
				out = append(out, rec)
			}
		}
		if auditQuery.limit > 0 && len(out) > auditQuery.limit {
			out = out[len(out)-auditQuery.limit:]
		}
		return writeAuditRecords(cmd.OutOrStdout(), out, auditOutput)
	},
}

var auditVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Check the audit hash chain across every month file",
	Long: `Check the audit log's hash chain across every month file: contiguous
sequence numbers, each record chained to the one before, each hash matching its
content, and each month file continuing from the last.

A torn write followed by the chain_break record Cerberus writes when it finds
one is not a break. Month files removed by a recorded 'cerberus audit prune'
are not a break either. Anything else exits non-zero.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := auditDir()
		if err != nil {
			return err
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if len(files) == 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "no audit records in %s\n", dir)
			return err
		}
		if err = audit.Verify(dir); err != nil {
			return err
		}
		recs, err := audit.ReadRecords(dir)
		if err != nil {
			return err
		}
		breaks := 0
		for _, rec := range recs {
			if rec.Kind == audit.KindChainBreak {
				breaks++
			}
		}
		msg := fmt.Sprintf("audit chain intact: %d records in %d month file(s)", len(recs), len(files))
		if breaks > 0 {
			msg += fmt.Sprintf("; %d recorded chain_break(s)", breaks)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), msg)
		return err
	},
}

var auditPruneBefore string

var auditPruneCmd = &cobra.Command{
	Use:   "prune --before <date>",
	Short: "Remove whole audit month files older than a date (interactive only)",
	Long: `Remove the audit month files that lie wholly before a date. Only whole month
files are ever removed, never the newest one, and never part of a file.

This is an admin operation. It runs only from an interactive terminal, asks you
to type a confirmation, and is itself recorded in the log before anything is
removed — if that record cannot be written, nothing is removed. 'cerberus audit
verify' accepts a chain whose earlier files a recorded prune removed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !auditIsTerminal() {
			return errAuditPruneNotInteractive
		}
		if auditPruneBefore == "" {
			return errors.New("--before is required, as a date such as 2026-06-01")
		}
		before, err := parseAuditTime(auditPruneBefore)
		if err != nil {
			return fmt.Errorf("--before: %w", err)
		}
		dir, err := auditDir()
		if err != nil {
			return err
		}
		plan, err := audit.PrunePlan(dir, before)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(plan) == 0 {
			_, err = fmt.Fprintf(out, "no audit month file lies wholly before %s; nothing to prune\n", before.Format("2006-01-02"))
			return err
		}
		phrase := auditPrunePhrase(before)
		_, _ = fmt.Fprintf(out, "This removes %d audit month file(s) from %s:\n", len(plan), dir)
		for _, file := range plan {
			_, _ = fmt.Fprintf(out, "  %s\n", filepath.Base(file))
		}
		_, _ = fmt.Fprintf(out, "Type %q to confirm: ", phrase)
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.TrimSpace(line) != phrase {
			return errors.New("confirmation did not match; nothing was removed")
		}
		principal := audit.Principal{Surface: string(cerbapi.SurfaceInProcess), SelfReported: true}
		removed, err := audit.Prune(auditSink(), dir, before, principal)
		for _, name := range removed {
			_, _ = fmt.Fprintf(out, "removed %s\n", name)
		}
		return err
	},
}

var errAuditPruneNotInteractive = errors.New("audit prune runs only from an interactive terminal, where it asks for a typed confirmation; run it from a terminal, not a script or an agent")

func auditPrunePhrase(before time.Time) string {
	return "prune before " + before.Format("2006-01-02")
}

func readAuditRecords() ([]audit.Record, error) {
	dir, err := auditDir()
	if err != nil {
		return nil, err
	}
	recs, err := audit.ReadRecords(dir)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Seq < recs[j].Seq })
	return recs, nil
}

// parseAuditTime reads a date (UTC midnight), a month (its first day) or an
// RFC 3339 time. Empty is the zero time: no bound.
func parseAuditTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02", "2006-01"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date (2026-09-01), a month (2026-09) or an RFC 3339 time", s)
}

func writeAuditRecords(w io.Writer, recs []audit.Record, format string) error {
	if format == outputFormatJSON {
		enc := json.NewEncoder(w)
		for _, rec := range recs {
			if err := enc.Encode(rec); err != nil {
				return err
			}
		}
		return nil
	}
	for _, rec := range recs {
		if _, err := fmt.Fprintln(w, auditLine(rec)); err != nil {
			return err
		}
	}
	return nil
}

// auditLine is one record as a line: when, where from, what, and how it went.
func auditLine(rec audit.Record) string {
	parts := []string{rec.Time.UTC().Format(time.RFC3339), fmt.Sprintf("#%d", rec.Seq), rec.Kind}
	switch rec.Kind {
	case audit.KindIntent, audit.KindOutcome:
	default:
		if rec.Note != "" {
			parts = append(parts, rec.Note)
		}
		return strings.Join(parts, "  ")
	}
	who := rec.Principal.Surface
	if rec.Principal.Kind != "" {
		who = rec.Principal.Kind + "/" + who
	}
	parts = append(parts, who, rec.Connector+" "+rec.Operation, rec.Effect)
	if rec.Kind == audit.KindOutcome {
		status := rec.OutcomeCode
		if rec.Decision == audit.DecisionRefused {
			status = "refused:" + status
		}
		parts = append(parts, status)
	}
	if target := auditTargetText(rec.Target); target != "" {
		parts = append(parts, target)
	}
	if rec.DryRun {
		parts = append(parts, "dry-run")
	}
	if rec.Reason != "" {
		parts = append(parts, "reason="+rec.Reason)
	}
	return strings.Join(parts, "  ")
}

func auditTargetText(t audit.Target) string {
	keys := make([]string, 0, len(t.Fields))
	for k := range t.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]string, 0, len(keys))
	for _, k := range keys {
		fields = append(fields, k+"="+t.Fields[k])
	}
	text := t.Kind
	if len(fields) > 0 {
		text += "(" + strings.Join(fields, ",") + ")"
	}
	return text
}

func init() {
	auditCmd.GroupID = "runtime"
	auditCmd.PersistentFlags().StringVarP(&auditOutput, "output", "o", outputFormatText, "output format: text|json (JSON is one record per line, as written)")

	auditTailCmd.Flags().IntVarP(&auditTailCount, "lines", "n", 20, "number of records to show (0 for all)")

	f := auditQueryCmd.Flags()
	f.StringVar(&auditQuery.connector, "connector", "", "connector id (local, ssh, docker, a plugin id, audit)")
	f.StringVar(&auditQuery.operation, "op", "", "operation name (deploy, exec, list_zones, prune)")
	f.StringVar(&auditQuery.surface, "surface", "", "caller surface (in_process, socket, web, monitor, unknown)")
	f.StringVar(&auditQuery.outcome, "outcome", "", "outcome code or decision (ok, refused, acknowledgment_required)")
	f.StringVar(&auditQuery.target, "target", "", "a target id: resource id, host, droplet id")
	f.StringVar(&auditQuery.since, "since", "", "records at or after this date or time")
	f.StringVar(&auditQuery.until, "until", "", "records before this date or time")
	f.IntVar(&auditQuery.limit, "limit", 0, "show at most this many of the most recent matches (0 for all)")

	auditPruneCmd.Flags().StringVar(&auditPruneBefore, "before", "", "remove month files wholly before this date (required)")

	auditCmd.AddCommand(auditTailCmd, auditQueryCmd, auditVerifyCmd, auditPruneCmd)
	rootCmd.AddCommand(auditCmd)
}
