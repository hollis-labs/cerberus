package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReadRecords returns every parseable record in dir's month files, oldest
// first. A torn line is skipped; Verify is what reports it.
func ReadRecords(dir string) ([]Record, error) {
	files, err := monthFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, file := range files {
		err := scanFile(file, func(rec Record, ok bool) {
			if ok {
				out = append(out, rec)
			}
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanFile calls fn for each non-blank line of file: the record, or ok false
// for a line that does not parse.
func scanFile(file string, fn func(rec Record, ok bool)) error {
	f, err := os.Open(file) //nolint:gosec // the audit directory's own file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			fn(Record{}, false)
			continue
		}
		fn(rec, true)
	}
	return scanner.Err()
}

// Filter selects records. A zero field matches everything; a set field
// matches only records that carry that value, so filtering on an operation
// field drops the chain's own bookkeeping records.
type Filter struct {
	Connector string
	Operation string
	Surface   string
	// Outcome matches an outcome record's code (ok, or the error code the
	// caller received) or its decision (allowed, refused).
	Outcome string
	// Target matches a record whose target carries this value in any field
	// (a resource id, a host, a droplet id).
	Target string
	Since  time.Time
	Until  time.Time
}

// Match reports whether rec passes f.
func (f Filter) Match(rec Record) bool {
	switch {
	case f.Connector != "" && rec.Connector != f.Connector,
		f.Operation != "" && rec.Operation != f.Operation,
		f.Surface != "" && rec.Principal.Surface != f.Surface,
		f.Outcome != "" && (rec.Kind != KindOutcome || (rec.OutcomeCode != f.Outcome && rec.Decision != f.Outcome)),
		!f.Since.IsZero() && rec.Time.Before(f.Since),
		!f.Until.IsZero() && !rec.Time.Before(f.Until):
		return false
	}
	if f.Target != "" {
		for _, v := range rec.Target.Fields {
			if v == f.Target {
				return true
			}
		}
		return false
	}
	return true
}

// Prune names. A prune is recorded in the chain it shortens: connector
// "audit", operation "prune", and the removed files listed in the target, so
// Verify can tell a recorded prune from files that went missing.
const (
	PruneConnector = "audit"
	PruneOperation = "prune"
	PruneTarget    = "audit.log"
	pruneRemoved   = "removed"
	pruneBefore    = "before"
)

// PrunePlan lists the month files in dir that lie wholly before before: the
// month ends on or before it. The newest file is never listed — it holds the
// chain's live tail, and the prune's own record goes there.
func PrunePlan(dir string, before time.Time) ([]string, error) {
	files, err := monthFiles(dir)
	if err != nil {
		return nil, err
	}
	var plan []string
	for i, file := range files {
		if i == len(files)-1 {
			break
		}
		// A name that is not a real month (2026-13) ends the plan, which
		// keeps it a prefix of the chain.
		month, ok := fileMonth(file)
		if !ok || month.AddDate(0, 1, 0).After(before) {
			break
		}
		plan = append(plan, file)
	}
	return plan, nil
}

func fileMonth(file string) (time.Time, bool) {
	month, err := time.ParseInLocation("2006-01", strings.TrimSuffix(filepath.Base(file), ".jsonl"), time.UTC)
	return month, err == nil
}

// ErrNothingToPrune is returned when no month file lies wholly before the
// requested date.
var ErrNothingToPrune = errors.New("audit: no month file lies wholly before that date")

// Prune removes the month files PrunePlan lists, recording the prune in the
// chain first. The intent must be durable before anything is removed — an
// unwritable log refuses the prune — and the outcome follows. It only ever
// removes whole month files.
func Prune(sink Sink, dir string, before time.Time, principal Principal) ([]string, error) {
	plan, err := PrunePlan(dir, before)
	if err != nil {
		return nil, err
	}
	if len(plan) == 0 {
		return nil, ErrNothingToPrune
	}
	names := make([]string, len(plan))
	for i, file := range plan {
		names[i] = filepath.Base(file)
	}
	intent := Record{
		Kind:        KindIntent,
		OperationID: NewID(),
		Principal:   principal,
		Connector:   PruneConnector,
		Operation:   PruneOperation,
		Effect:      "admin",
		Target: Target{Kind: PruneTarget, Fields: map[string]string{
			pruneBefore:  before.UTC().Format("2006-01-02"),
			pruneRemoved: strings.Join(names, ","),
		}},
		Acknowledged: true,
		Posture:      PostureSecure,
	}
	if _, err := sink.Write(intent); err != nil {
		return nil, fmt.Errorf("%w: the prune was not recorded, so nothing was removed: %w", ErrUnavailable, err)
	}
	start := time.Now()
	outcome := intent
	outcome.Kind = KindOutcome
	outcome.Decision = DecisionAllowed
	outcome.OutcomeCode = OutcomeOK
	var removed []string
	var removeErr error
	for _, file := range plan {
		if err := os.Remove(file); err != nil {
			removeErr = err
			outcome.OutcomeCode = "operation_failed"
			break
		}
		removed = append(removed, filepath.Base(file))
	}
	syncDir(dir)
	outcome.Target.Fields = map[string]string{
		pruneBefore:  intent.Target.Fields[pruneBefore],
		pruneRemoved: strings.Join(removed, ","),
	}
	outcome.DurationMS = time.Since(start).Milliseconds()
	if _, err := sink.Write(outcome); err != nil && removeErr == nil {
		removeErr = fmt.Errorf("audit: files were removed but the outcome was not recorded: %w", err)
	}
	return removed, removeErr
}

// prunedFiles are the file names a successful prune in recs removed.
func prunedFiles(recs []Record) map[string]bool {
	out := map[string]bool{}
	for _, rec := range recs {
		if rec.Kind != KindOutcome || rec.Connector != PruneConnector || rec.Operation != PruneOperation {
			continue
		}
		for _, name := range strings.Split(rec.Target.Fields[pruneRemoved], ",") {
			if name != "" {
				out[name] = true
			}
		}
	}
	return out
}
