package legacyexport

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ReportVersion is the report format.
const ReportVersion = 1

// Results.
const (
	// ResultExported is a run that left every step done.
	ResultExported = "exported"
	// ResultTelemetryPending is a run that completed every file step and the anchor but not the
	// class B step: not a success (exit 3); `--only telemetry` finishes it.
	ResultTelemetryPending = "telemetry_pending"
	// ResultRefused is a run that refused before writing anything.
	ResultRefused = "refused"
	// ResultFailed is a run that stopped on an error after it began writing; the progress file
	// says which step, and the next run resumes it.
	ResultFailed = "failed"
	// ResultChecked is --check-legacy-files.
	ResultChecked = "checked"
)

// Report is what one run did: per step its status and what it wrote, the verification, what is
// deliberately not reversed, and what awaits a ruling.
type Report struct {
	Version  int       `json:"version"`
	Result   string    `json:"result"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`

	// Handoff is the D3 manifest's body sha256; ManifestPath the copy migrate-from kept.
	Handoff      string `json:"handoff,omitempty"`
	ManifestPath string `json:"manifest_path,omitempty"`
	Only         string `json:"only,omitempty"`

	DataDir          string `json:"data_dir"`
	DatabasePath     string `json:"database_path,omitempty"`
	OldHome          string `json:"old_home"`
	OldDB            string `json:"old_db,omitempty"`
	OldAnchorAccount string `json:"old_anchor_account,omitempty"`
	StatePath        string `json:"state_path,omitempty"`

	// ManifestMatches is the preflight's verdict on the old tree: it matches the manifest, or the
	// post state of the steps already done.
	ManifestMatches bool     `json:"manifest_matches"`
	Mismatches      []string `json:"mismatches,omitempty"`

	Steps     []StepRow        `json:"steps"`
	Audit     *AuditReport     `json:"audit,omitempty"`
	Approvals *ChainFileReport `json:"approvals,omitempty"`
	Brakes    *ChainFileReport `json:"brakes,omitempty"`
	Pause     *PauseReport     `json:"pause,omitempty"`
	Applied   *AppliedReport   `json:"applied,omitempty"`
	Receipts  *ReceiptsReport  `json:"receipts,omitempty"`
	Anchor    *AnchorReport    `json:"anchor,omitempty"`
	Telemetry *TelemetryReport `json:"telemetry,omitempty"`

	LegacyFiles *LegacyFilesReport `json:"legacy_files,omitempty"`

	// NotReversed are the stores a rollback leaves as they are, and why.
	NotReversed []string `json:"not_reversed,omitempty"`
	// AwaitingRuling are the parts of the spec this build does not do yet.
	AwaitingRuling []string `json:"awaiting_ruling,omitempty"`

	Warnings []string `json:"warnings"`
	Refusals []string `json:"refusals"`
}

// StepRow is one step's outcome in this run.
type StepRow struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	// Ran is false for a step done by an earlier run (its post state verified, nothing written).
	Ran    bool   `json:"ran"`
	Detail string `json:"detail,omitempty"`
}

// AuditReport is the audit step (R4/R5).
type AuditReport struct {
	// Appended are month files the suffix was appended to (bytes appended); Created month files
	// the old tree did not have (a rollover), copied whole.
	Appended map[string]int64 `json:"appended"`
	Created  map[string]int64 `json:"created"`
	// KeptInOld are month files new pruned that stay in the old tree.
	KeptInOld []string `json:"kept_in_old,omitempty"`
	// NewVerify is new's chain against new's anchor before writing; OldVerify the old tree's
	// chain against the head after writing.
	NewVerify            string          `json:"new_verify"`
	OldVerify            string          `json:"old_verify,omitempty"`
	Head                 json.RawMessage `json:"head,omitempty"`
	Records              int             `json:"records"`
	DigestKeySHA256Equal bool            `json:"digest_key_sha256_equal"`
}

// ChainFileReport is the approvals or brakes step (R6, R21).
type ChainFileReport struct {
	Appended int64  `json:"appended_bytes"`
	Created  bool   `json:"created,omitempty"`
	Verify   string `json:"verify"`
	// NextSeq is the store's next cursor seq (approvals) after writing.
	NextSeq uint64 `json:"next_seq,omitempty"`
	// Legacy, Verified and Failed are VerifyApprovals' counts on new's store (approvals).
	Legacy   int `json:"legacy,omitempty"`
	Verified int `json:"verified,omitempty"`
}

// PauseReport is the pause step (R10).
type PauseReport struct {
	Targets []string `json:"targets"`
	Created []string `json:"created"`
	Removed []string `json:"removed"`
}

// AppliedReport is the applied-digests step (R9).
type AppliedReport struct {
	KeyFingerprint string   `json:"key_fingerprint,omitempty"`
	Count          int      `json:"count"`
	Added          []string `json:"added"`
	Changed        []string `json:"changed"`
	Written        bool     `json:"written"`
}

// ReceiptsReport is the receipts step (R11).
type ReceiptsReport struct {
	Rows     int      `json:"rows"`
	Written  []string `json:"written"`
	NewDirs  []string `json:"new_dirs"`
	Verified int      `json:"verified"`
}

// AnchorReport is the anchor move.
type AnchorReport struct {
	Account string          `json:"account"`
	Moved   bool            `json:"moved"`
	Head    json.RawMessage `json:"head,omitempty"`
	Verify  string          `json:"verify,omitempty"`
}

// TelemetryReport is the class B step (R1).
type TelemetryReport struct {
	Pending bool   `json:"pending"`
	Error   string `json:"error,omitempty"`
	// Inserted and Updated are rows per table; KeptOld rows where old's row was newer.
	Inserted       map[string]int64 `json:"inserted,omitempty"`
	Updated        map[string]int64 `json:"updated,omitempty"`
	KeptOld        map[string]int64 `json:"kept_old,omitempty"`
	SQLiteSequence int64            `json:"sqlite_sequence_host_events,omitempty"`
	IntegrityCheck string           `json:"integrity_check,omitempty"`
	HostDumpSHA256 string           `json:"host_dump_sha256,omitempty"`
}

// LegacyFilesReport is --check-legacy-files.
type LegacyFilesReport struct {
	File    string `json:"file"`
	D3Copy  string `json:"d3_copy"`
	SHA256  string `json:"sha256,omitempty"`
	D3SHA   string `json:"d3_sha256,omitempty"`
	Changed bool   `json:"changed"`
	// Decoded is whether old's decoder read the changed file; Entries how many entries it holds.
	Decoded bool   `json:"decoded"`
	Entries int    `json:"entries,omitempty"`
	Version int    `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

func newReport(now time.Time) *Report {
	return &Report{Version: ReportVersion, Started: now, Steps: []StepRow{}, Warnings: []string{}, Refusals: []string{}}
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

func (r *Report) refuse(format string, args ...any) {
	r.Refusals = append(r.Refusals, fmt.Sprintf(format, args...))
}

func (r *Report) step(name, status string, ran bool, format string, args ...any) {
	r.Steps = append(r.Steps, StepRow{Step: name, Status: status, Ran: ran, Detail: fmt.Sprintf(format, args...)})
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes the report for a person.
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	p("export-legacy: %s", r.Result)
	if r.Error != "" {
		p("  error: %s", r.Error)
	}
	if r.Handoff != "" {
		p("  handoff:    %s", r.Handoff)
	}
	if r.DataDir != "" {
		p("  data dir:   %s", r.DataDir)
	}
	p("  old home:   %s", r.OldHome)
	if r.OldDB != "" {
		p("  old db:     %s", r.OldDB)
	}
	if r.OldAnchorAccount != "" {
		p("  old anchor: %s", r.OldAnchorAccount)
	}
	for _, x := range r.Refusals {
		p("  REFUSED: %s", x)
	}
	for _, x := range r.Mismatches {
		p("  mismatch: %s", x)
	}
	for _, s := range r.Steps {
		ran := "verified"
		if s.Ran {
			ran = "ran"
		}
		p("  %-10s %-8s %-8s %s", s.Step, s.Status, ran, s.Detail)
	}
	if t := r.Telemetry; t != nil && t.Pending {
		p("  B step pending: run `cerberus export-legacy --since %s --only telemetry` once the Telemetry store is reachable", r.Handoff)
	}
	if lf := r.LegacyFiles; lf != nil {
		p("  %s: changed %v, decoded %v (%d entries)", lf.File, lf.Changed, lf.Decoded, lf.Entries)
		if lf.Error != "" {
			p("    error: %s", lf.Error)
		}
	}
	for _, x := range r.NotReversed {
		p("  not reversed: %s", x)
	}
	for _, x := range r.AwaitingRuling {
		p("  awaiting ruling: %s", x)
	}
	for _, x := range r.Warnings {
		p("  warning: %s", x)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
