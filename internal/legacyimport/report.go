package legacyimport

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ReportVersion is the report format.
const ReportVersion = 1

// Results.
const (
	ResultMigrated = "migrated"
	ResultRefused  = "refused"
	ResultFailed   = "failed"
)

// Report is what one run did, for the operator and the PR: per store the treatment, source and
// destination digests, counts before and after and the verification, and whether the source was
// left exactly as it was. A refused run reports the refusals and wrote nothing.
type Report struct {
	Version  int       `json:"version"`
	Result   string    `json:"result"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`

	OldHome          string `json:"old_home"`
	OldDB            string `json:"old_db"`
	OldAnchorAccount string `json:"old_anchor_account"`
	DataDir          string `json:"data_dir"`
	DatabasePath     string `json:"database_path"`
	ConfigDir        string `json:"config_dir"`

	Manifest *ManifestReport `json:"manifest,omitempty"`

	SourceUnchanged    bool   `json:"source_unchanged"`
	SourceDigestBefore string `json:"source_digest_before,omitempty"`
	SourceDigestAfter  string `json:"source_digest_after,omitempty"`
	SourceEntries      int    `json:"source_entries,omitempty"`

	Stores    []StoreRow       `json:"stores"`
	MainDB    *MainDBReport    `json:"main_db,omitempty"`
	Audit     *AuditReport     `json:"audit,omitempty"`
	Approvals *ApprovalsReport `json:"approvals,omitempty"`
	Applied   *AppliedReport   `json:"applied,omitempty"`
	Pause     *PauseReport     `json:"pause,omitempty"`
	Receipts  *ReceiptsReport  `json:"receipts,omitempty"`
	Policy    *PolicyReport    `json:"policy,omitempty"`
	Brakes    *BrakesReport    `json:"brakes,omitempty"`
	Config    *ConfigReport    `json:"config,omitempty"`
	Registry  *RegistryReport  `json:"registry,omitempty"`
	Hosts     *HostsReport     `json:"hosts,omitempty"`
	// PluginDigests are the bundle digests plugin-connectors.json records (R18, in place).
	PluginDigests map[string]string `json:"plugin_digests,omitempty"`
	Listed        Listed            `json:"listed"`

	// Decisions are the pending-decision defaults this run applied (decisions.go).
	Decisions map[string]string `json:"decisions"`
	Warnings  []string          `json:"warnings"`
	Refusals  []string          `json:"refusals"`
}

// ManifestReport is the D3 manifest the snapshot was checked against.
type ManifestReport struct {
	Path            string   `json:"path"`
	SHA256          string   `json:"sha256"`
	SnapshotMatches bool     `json:"snapshot_matches"`
	Mismatches      []string `json:"mismatches,omitempty"`
	CopiedTo        string   `json:"copied_to,omitempty"`
}

// StoreRow is one inventory row's treatment.
type StoreRow struct {
	Row          string `json:"row"`
	Store        string `json:"store"`
	Treatment    string `json:"treatment"`
	Dest         string `json:"dest,omitempty"`
	SourceSHA256 string `json:"source_sha256,omitempty"`
	CountBefore  *int64 `json:"count_before,omitempty"`
	CountAfter   *int64 `json:"count_after,omitempty"`
	DestSHA256   string `json:"dest_sha256,omitempty"`
	Verify       string `json:"verify"`
}

// TableReport is one host table before and after.
type TableReport struct {
	Before       int64  `json:"before"`
	After        int64  `json:"after"`
	ExceptOldNew int64  `json:"except_old_new"`
	ExceptNewOld int64  `json:"except_new_old"`
	MaxTime      *int64 `json:"max_time"`
}

// MainDBReport is R1 to R3.
type MainDBReport struct {
	IntegrityCheck string                 `json:"integrity_check"`
	SchemaVersions []int                  `json:"schema_versions"`
	Tables         map[string]TableReport `json:"tables"`
	MaxID          *int64                 `json:"host_events_max_id"`
	SQLiteSequence struct {
		Old map[string]int64 `json:"old"`
		New map[string]int64 `json:"new"`
	} `json:"sqlite_sequence"`
	HostDumpSHA256 struct {
		Old string `json:"old"`
		New string `json:"new"`
	} `json:"host_dump_sha256"`
	AssertedEmpty map[string]int64 `json:"asserted_empty"`
}

// FileDigest is one file and its sha256.
type FileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// AuditReport is R4 and R5.
type AuditReport struct {
	Records              int             `json:"records"`
	Files                []FileDigest    `json:"files"`
	Head                 json.RawMessage `json:"head"`
	OldAnchorVerify      string          `json:"old_anchor_verify"`
	CopyVerify           string          `json:"copy_verify"`
	NewAnchorVerify      string          `json:"new_anchor_verify"`
	NewAnchorAccount     string          `json:"new_anchor_account"`
	NewAnchorWritten     bool            `json:"new_anchor_written"`
	DigestKeySHA256Equal bool            `json:"digest_key_sha256_equal"`
	KeyFingerprint       string          `json:"key_fingerprint"`
}

// ApprovalsReport is R6 and R7.
type ApprovalsReport struct {
	Events   int      `json:"events"`
	Lines    int      `json:"lines"`
	NextSeq  uint64   `json:"next_seq"`
	Damaged  int      `json:"damaged"`
	Legacy   []string `json:"legacy"`
	Verified int      `json:"verified"`
	Failed   int      `json:"failed"`
}

// AppliedReport is R9.
type AppliedReport struct {
	Count           int       `json:"count"`
	Changed         []string  `json:"changed"`
	Unrecorded      []string  `json:"unrecorded,omitempty"`
	NoResource      []string  `json:"no_resource,omitempty"`
	AppliedAtSource time.Time `json:"applied_at_source"`
	KeyFingerprint  string    `json:"key_fingerprint"`
}

// PauseReport is R10.
type PauseReport struct {
	Targets    []string `json:"targets"`
	NoResource []string `json:"no_resource"`
}

// ReceiptsReport is R11.
type ReceiptsReport struct {
	Issuances                  int      `json:"issuances"`
	Files                      int      `json:"files"`
	Rows                       int      `json:"rows"`
	ReservationsWithoutReceipt []string `json:"reservations_without_receipt"`
}

// PolicyReport is R12.
type PolicyReport struct {
	Files          []FileDigest `json:"files"`
	AppliedHashOK  bool         `json:"applied_hash_ok"`
	SourceStatus   string       `json:"source_status"`
	DestStatus     string       `json:"dest_status"`
	LeftBehind     []string     `json:"left_behind,omitempty"`
	ConfigDirStage string       `json:"-"`
}

// BrakesReport is R21's brakes.
type BrakesReport struct {
	Engaged  bool     `json:"engaged"`
	Freezes  int      `json:"freezes"`
	Problems []string `json:"problems"`
}

// ConfigReport is R13.
type ConfigReport struct {
	Output                string   `json:"output"`
	Fragment              string   `json:"fragment"`
	FragmentSHA256        string   `json:"fragment_sha256"`
	Mapped                []string `json:"mapped"`
	Unmapped              []string `json:"unmapped"`
	ResourcesListed       int      `json:"resources_listed"`
	ProjectsListed        int      `json:"projects_listed"`
	PipelinesListed       int      `json:"pipelines_listed"`
	EventExportWasEnabled bool     `json:"event_export_was_enabled"`
}

// RegistryReport is R14.
type RegistryReport struct {
	Entries   int      `json:"entries"`
	Resources int      `json:"resources"`
	Warnings  []string `json:"warnings,omitempty"`
}

// HostsReport is R15.
type HostsReport struct {
	SHA256 string `json:"sha256"`
	Hosts  int    `json:"hosts"`
}

// Entry is a listed path inside the snapshot (names and sizes only: never opened unless the row
// says so).
type Entry struct {
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
	Dir  bool   `json:"dir,omitempty"`
	Row  string `json:"row"`
	Note string `json:"note,omitempty"`
}

// Listed are the rows that stay where they are.
type Listed struct {
	InPlace     []Entry  `json:"in_place"`
	NotMigrated []Entry  `json:"not_migrated"`
	Archive     []Entry  `json:"archive"`
	Runtime     []Entry  `json:"runtime"`
	Absent      []string `json:"absent"`
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

func (r *Report) refuse(format string, args ...any) {
	r.Refusals = append(r.Refusals, fmt.Sprintf(format, args...))
}

func i64(n int) *int64 { v := int64(n); return &v }

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
	p("migrate-from: %s", r.Result)
	if r.Error != "" {
		p("  error: %s", r.Error)
	}
	p("  old home:   %s", r.OldHome)
	p("  old db:     %s", r.OldDB)
	p("  data dir:   %s", r.DataDir)
	p("  config dir: %s", r.ConfigDir)
	for _, x := range r.Refusals {
		p("  REFUSED: %s", x)
	}
	if r.Manifest != nil {
		p("  manifest %s (%s): snapshot matches %v", r.Manifest.Path, r.Manifest.SHA256, r.Manifest.SnapshotMatches)
		for _, m := range r.Manifest.Mismatches {
			p("    mismatch: %s", m)
		}
	}
	if r.SourceDigestBefore != "" {
		p("  source_unchanged: %v (%d entries, %s)", r.SourceUnchanged, r.SourceEntries, r.SourceDigestBefore)
	}
	if len(r.Stores) > 0 {
		p("")
		p("  %-5s %-62s %-13s %-7s %-7s %s", "row", "store", "treatment", "before", "after", "verify")
		for _, s := range r.Stores {
			p("  %-5s %-62s %-13s %-7s %-7s %s", s.Row, s.Store, s.Treatment, count(s.CountBefore), count(s.CountAfter), s.Verify)
		}
	}
	if m := r.MainDB; m != nil {
		p("")
		p("  main.db: integrity %s, schema %v, host rows %s -> %s", m.IntegrityCheck, m.SchemaVersions, m.HostDumpSHA256.Old, m.HostDumpSHA256.New)
		names := make([]string, 0, len(m.Tables))
		for n := range m.Tables {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			t := m.Tables[n]
			p("    %-20s %6d -> %6d  except old-new %d, new-old %d", n, t.Before, t.After, t.ExceptOldNew, t.ExceptNewOld)
		}
		p("    sqlite_sequence %v -> %v", m.SQLiteSequence.Old, m.SQLiteSequence.New)
	}
	if a := r.Audit; a != nil {
		p("  audit: %d records; old anchor %s; copy %s; new anchor %s (%s)", a.Records, a.OldAnchorVerify, a.CopyVerify, a.NewAnchorVerify, a.NewAnchorAccount)
	}
	if a := r.Approvals; a != nil {
		p("  approvals: %d events, %d lines; %d legacy, %d verified, %d failed", a.Events, a.Lines, len(a.Legacy), a.Verified, a.Failed)
	}
	if a := r.Applied; a != nil {
		p("  applied: %d digests, changed %v", a.Count, a.Changed)
	}
	if a := r.Pause; a != nil {
		p("  pause: %v; naming no resource: %v", a.Targets, a.NoResource)
	}
	if a := r.Receipts; a != nil {
		p("  receipts: %d files -> %d rows; reservations without a receipt: %v", a.Files, a.Rows, a.ReservationsWithoutReceipt)
	}
	if c := r.Config; c != nil {
		p("  config: fragment %s; unmapped %v; resources listed %d, projects listed %d", c.Fragment, c.Unmapped, c.ResourcesListed, c.ProjectsListed)
	}
	for _, group := range []struct {
		name    string
		entries []Entry
	}{{"in place", r.Listed.InPlace}, {"not migrated", r.Listed.NotMigrated}, {"archive", r.Listed.Archive}, {"runtime", r.Listed.Runtime}} {
		if len(group.entries) == 0 {
			continue
		}
		names := make([]string, len(group.entries))
		for i, e := range group.entries {
			names[i] = e.Path
		}
		p("  %s: %s", group.name, strings.Join(names, ", "))
	}
	if len(r.Listed.Absent) > 0 {
		p("  absent: %s", strings.Join(r.Listed.Absent, ", "))
	}
	for _, x := range r.Warnings {
		p("  warning: %s", x)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func count(p *int64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprint(*p)
}
