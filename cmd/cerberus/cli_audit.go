package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// The audit chain's reads on the CLI: the old `audit tail` and `audit query` (cmd/cerberus
// cmd_audit.go on main), with the old flags and output. As `audit verify` does here, and as old
// did, they read the chain's files in <data>/audit directly, read-only: the log is the
// operator's own files, and reading it keeps working when the server is down. Nothing here
// writes the chain; `audit prune` and `audit reanchor` write it and wait for a served lane
// (cli_pending.go), so the server stays its only writer.

// auditReadCommands are tail and query over the chain in the directory dir returns; output is
// the group's -o.
func auditReadCommands(dir func() (string, error), output *string) []*cobra.Command {
	var lines int
	tail := &cobra.Command{
		Use:   "tail",
		Short: "Show the most recent audit records",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			recs, err := readAuditRecords(dir)
			if err != nil {
				return err
			}
			if lines > 0 && len(recs) > lines {
				recs = recs[len(recs)-lines:]
			}
			return writeAuditRecords(cmd.OutOrStdout(), recs, *output)
		},
	}
	tail.Flags().IntVarP(&lines, "lines", "n", 20, "number of records to show (0 for all)")

	var q struct {
		connector, operation, surface, outcome, target, since, until string
		limit                                                        int
	}
	query := &cobra.Command{
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
			f := audit.Filter{Connector: q.connector, Operation: q.operation, Surface: q.surface, Outcome: q.outcome, Target: q.target}
			var err error
			if f.Since, err = parseAuditTime(q.since); err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			if f.Until, err = parseAuditTime(q.until); err != nil {
				return fmt.Errorf("--until: %w", err)
			}
			recs, err := readAuditRecords(dir)
			if err != nil {
				return err
			}
			var out []audit.Record
			for _, rec := range recs {
				if f.Match(rec) {
					out = append(out, rec)
				}
			}
			if q.limit > 0 && len(out) > q.limit {
				out = out[len(out)-q.limit:]
			}
			return writeAuditRecords(cmd.OutOrStdout(), out, *output)
		},
	}
	fl := query.Flags()
	fl.StringVar(&q.connector, "connector", "", "connector id (local, ssh, docker, a plugin id, audit)")
	fl.StringVar(&q.operation, "op", "", "operation name (deploy, exec, list_zones, prune)")
	fl.StringVar(&q.surface, "surface", "", "caller surface (in_process, socket, web, monitor, unknown)")
	fl.StringVar(&q.outcome, "outcome", "", "outcome code or decision (ok, refused, acknowledgment_required)")
	fl.StringVar(&q.target, "target", "", "a target id: resource id, host, droplet id")
	fl.StringVar(&q.since, "since", "", "records at or after this date or time")
	fl.StringVar(&q.until, "until", "", "records before this date or time")
	fl.IntVar(&q.limit, "limit", 0, "show at most this many of the most recent matches (0 for all)")
	return []*cobra.Command{tail, query}
}

// auditDataDir is <data>/audit for the data directory data returns.
func auditDataDir(data func() (string, error)) func() (string, error) {
	return func() (string, error) {
		d, err := data()
		if err != nil {
			return "", err
		}
		return filepath.Join(d, "audit"), nil
	}
}

func readAuditRecords(dir func() (string, error)) ([]audit.Record, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	recs, err := audit.ReadRecords(d)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Seq < recs[j].Seq })
	return recs, nil
}

// parseAuditTime reads a date (UTC midnight), a month (its first day) or an RFC 3339 time.
// Empty is the zero time: no bound.
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

// writeAuditRecords is old's: JSON is the records exactly as written, one per line, not passed
// through redaction (the log holds names and keyed digests, never a value); text is a line each.
func writeAuditRecords(w io.Writer, recs []audit.Record, format string) error {
	if format == outputJSON {
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
