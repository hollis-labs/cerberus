package legacyexport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
)

// The standby sidecar (plan section 7.2, F2): at F the old build's ~/.cerberus/standby.d/ is
// archived beside the old tree, and the new build imports it read-only as a sidecar log. On
// main (internal/standby) its layout is sessions.json ({written_at, sessions}, written once) and
// audit/ (YYYY-MM.jsonl, .lock and its own .digest_key). Its chain is never anchored (SetAnchor
// covers only the main directory), so only the hash chain is checked. The standby fence digests
// a verb's arguments with the standby sink's own key, while a read diverted through audit.Switch
// is digested with the main key; plan rev 10 accepts both: each standby verb's digest is
// recomputed under each key and the report says which matched, never refusing on it.

// Sidecar paths, relative to the new data directory.
const (
	SidecarStandbyRel  = "sidecar/standby"
	StandbyReportName  = "import-report.json"
	standbySessions    = "sessions.json"
	standbyAuditDir    = "audit"
	StandbyReportVer   = 1
	sidecarFileMode    = 0o400
	sidecarDirMode     = 0o500
	standbyVerbVia     = "standby"
	standbyVerbConnect = "local"
)

// standbyVerbs are the operations the standby fence runs on the flagged resource.
var standbyVerbs = map[string]bool{"apply": true, "stop": true, "reload": true}

// Key matches.
const (
	MatchMain    = "main"
	MatchStandby = "standby"
	MatchBoth    = "both"
	MatchNeither = "neither"
)

// StandbyOptions are import-standby's inputs. Paths are absolute.
type StandbyOptions struct {
	// From is the archived standby.d directory.
	From string
	// DataDir is the new data directory: the sidecar goes to <DataDir>/sidecar/standby and the
	// main digest key is <DataDir>/audit/.digest_key.
	DataDir string
	Now     func() time.Time
}

// StandbyReport is what import-standby did; it is also written to the sidecar as
// import-report.json.
type StandbyReport struct {
	Version  int       `json:"version"`
	Result   string    `json:"result"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	From     string    `json:"from"`
	Target   string    `json:"target"`

	// SourceFiles are every file of the archive with its sha256, taken before the copy;
	// SourceUnchanged is the same list taken again after.
	SourceFiles     []oldFile `json:"source_files"`
	SourceUnchanged bool      `json:"source_unchanged"`

	Chain    StandbyChain     `json:"chain"`
	Keys     StandbyKeys      `json:"keys"`
	Verbs    []StandbyVerb    `json:"verbs"`
	Matches  map[string]int   `json:"matches"`
	Sessions *StandbySessions `json:"sessions,omitempty"`

	Warnings []string `json:"warnings"`
	Refusals []string `json:"refusals"`
}

type oldFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// StandbyChain is the hash chain's check, with no anchor.
type StandbyChain struct {
	Records  int    `json:"records"`
	Files    int    `json:"files"`
	Anchored bool   `json:"anchored"`
	Verify   string `json:"verify"`
}

// StandbyKeys names the two keys by fingerprint (never their bytes).
type StandbyKeys struct {
	MainFingerprint    string `json:"main_fingerprint"`
	StandbyFingerprint string `json:"standby_fingerprint,omitempty"`
	StandbyKeyAbsent   bool   `json:"standby_key_absent,omitempty"`
	Same               bool   `json:"same"`
}

// StandbyVerb is one standby verb record and which key its ArgsDigest matches.
type StandbyVerb struct {
	Seq         uint64 `json:"seq"`
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id,omitempty"`
	Operation   string `json:"operation"`
	Resource    string `json:"resource"`
	Match       string `json:"match"`
}

// StandbySessions is sessions.json parsed (origin/main standby.Sessions).
type StandbySessions struct {
	WrittenAt time.Time `json:"written_at"`
	IDs       []string  `json:"sessions"`
}

// ImportStandby copies the archived standby.d into <data>/sidecar/standby read-only (files 0400,
// directories 0500), refusing an existing target, hashing every source file and never writing
// the source; checks the hash chain with no anchor; reports, for each standby verb record, which
// key its ArgsDigest was made with; parses sessions.json; and writes import-report.json. A
// refusal leaves nothing behind.
func ImportStandby(o StandbyOptions) (*StandbyReport, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	target := filepath.Join(o.DataDir, filepath.FromSlash(SidecarStandbyRel))
	r := &StandbyReport{Version: StandbyReportVer, Started: now().UTC(), From: o.From, Target: target, Verbs: []StandbyVerb{},
		Matches: map[string]int{}, Warnings: []string{}, Refusals: []string{}}
	finish := func(result string, err error) (*StandbyReport, error) {
		r.Result = result
		if err != nil {
			r.Error = err.Error()
		}
		r.Finished = now().UTC()
		return r, err
	}
	refuse := func(format string, args ...any) (*StandbyReport, error) {
		r.Refusals = append(r.Refusals, fmt.Sprintf(format, args...))
		return finish(ResultRefused, ErrRefused)
	}
	for _, p := range []struct{ name, path string }{{"--from", o.From}, {"the data directory", o.DataDir}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			return refuse("%s must be an absolute path (got %q)", p.name, p.path)
		}
	}
	if fi, err := os.Stat(o.From); err != nil || !fi.IsDir() {
		return refuse("--from %s is not a directory (%v): it is the archived standby.d", o.From, err)
	}
	if fi, err := os.Stat(filepath.Join(o.From, standbyAuditDir)); err != nil || !fi.IsDir() {
		return refuse("--from %s has no audit/ directory: it is not a standby.d archive", o.From)
	}
	if within(resolve(o.DataDir), resolve(o.From)) || within(resolve(o.From), resolve(o.DataDir)) {
		return refuse("--from %s overlaps the data directory %s", o.From, o.DataDir)
	}
	if _, err := os.Lstat(target); err == nil {
		return refuse("%s exists: the standby sidecar is imported once; inspect it and move it aside only if you know it is stale", target)
	}
	partial := target + ".partial"
	if _, err := os.Lstat(partial); err == nil {
		return refuse("%s exists: a previous run left it; inspect it and remove it", partial)
	}
	mainDigester, err := audit.OpenExistingDigester(filepath.Join(o.DataDir, "audit"))
	if err != nil {
		return refuse("the main digest key: %v", err)
	}
	r.Keys.MainFingerprint = mainDigester.Fingerprint()

	// The source, hashed before anything is read into the copy.
	before, err := hashTree(o.From)
	if err != nil {
		return refuse("%v", err)
	}
	r.SourceFiles = before

	// Built in <target>.partial, checked there, then renamed into place and made read-only.
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return finish(ResultFailed, err)
	}
	if err = os.Mkdir(partial, 0o700); err != nil {
		return finish(ResultFailed, err)
	}
	fail := func(err error) (*StandbyReport, error) {
		_ = os.RemoveAll(partial)
		return finish(ResultFailed, err)
	}
	if err = copyArchive(o.From, partial); err != nil {
		return fail(err)
	}
	copied, err := hashTree(partial)
	if err != nil {
		return fail(err)
	}
	if fmt.Sprint(copied) != fmt.Sprint(before) {
		return fail(errors.New("the copy does not hash as the source does"))
	}

	// The hash chain, with no anchor: the standby chain is never anchored.
	auditCopy := filepath.Join(partial, standbyAuditDir)
	audit.SetAnchor("", nil)
	checked, err := audit.Check(auditCopy)
	if err != nil {
		return fail(err)
	}
	months, _ := monthFiles(auditCopy)
	r.Chain = StandbyChain{Records: len(checked.Records), Files: len(months), Verify: "ok"}
	if len(checked.Problems) > 0 {
		r.Chain.Verify = "problems: " + checked.Err().Error()
		_ = os.RemoveAll(partial)
		r.Refusals = append(r.Refusals, "the standby audit chain does not verify: "+checked.Err().Error())
		return finish(ResultRefused, ErrRefused)
	}

	// Which key each standby verb's ArgsDigest was made with.
	standbyDigester, kerr := audit.OpenExistingDigester(auditCopy)
	if kerr != nil {
		r.Keys.StandbyKeyAbsent = true
		r.Warnings = append(r.Warnings, fmt.Sprintf("the standby key: %v; digests are checked against the main key only", kerr))
	} else {
		r.Keys.StandbyFingerprint = standbyDigester.Fingerprint()
		r.Keys.Same = r.Keys.StandbyFingerprint == r.Keys.MainFingerprint
	}
	for _, rec := range checked.Records {
		if rec.Connector != standbyVerbConnect || !standbyVerbs[rec.Operation] || rec.Principal.Via != standbyVerbVia || rec.ArgsDigest == "" {
			continue
		}
		res := rec.Target.Fields["id"]
		if res == "" {
			res = rec.Target.Resource
		}
		args := map[string]any{"id": res}
		onMain := mainDigester.Digest(args) == rec.ArgsDigest
		onStandby := kerr == nil && standbyDigester.Digest(args) == rec.ArgsDigest
		match := MatchNeither
		switch {
		case onMain && onStandby:
			match = MatchBoth
		case onMain:
			match = MatchMain
		case onStandby:
			match = MatchStandby
		}
		r.Matches[match]++
		r.Verbs = append(r.Verbs, StandbyVerb{Seq: rec.Seq, Kind: rec.Kind, OperationID: rec.OperationID, Operation: rec.Operation, Resource: res, Match: match})
	}
	if r.Matches[MatchNeither] > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d standby verb record(s) match neither key: their arguments are not {\"id\": <resource>} under either key", r.Matches[MatchNeither]))
	}

	// sessions.json.
	if data, rerr := os.ReadFile(filepath.Join(partial, standbySessions)); rerr == nil { //nolint:gosec // the copy
		var s StandbySessions
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			_ = os.RemoveAll(partial)
			r.Refusals = append(r.Refusals, fmt.Sprintf("%s does not parse: %v", standbySessions, jerr))
			return finish(ResultRefused, ErrRefused)
		}
		if s.IDs == nil {
			s.IDs = []string{}
		}
		r.Sessions = &s
	} else if errors.Is(rerr, fs.ErrNotExist) {
		r.Warnings = append(r.Warnings, "the archive has no sessions.json: standby stopped no dev session, or the list was cleared")
	} else {
		return fail(rerr)
	}

	// The source must be exactly what it was.
	after, err := hashTree(o.From)
	if err != nil {
		return fail(err)
	}
	r.SourceUnchanged = fmt.Sprint(after) == fmt.Sprint(before)
	if !r.SourceUnchanged {
		return fail(errors.New("the archive changed during the import; the copy is discarded"))
	}

	r.Result = "imported"
	r.Finished = now().UTC()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fail(err)
	}
	if err = writeNew(filepath.Join(partial, StandbyReportName), append(data, '\n')); err != nil {
		return fail(err)
	}
	if err = os.Rename(partial, target); err != nil {
		return fail(err)
	}
	if err = readOnly(target); err != nil {
		return finish(ResultFailed, fmt.Errorf("the sidecar is in place at %s but could not be made read-only: %w", target, err))
	}
	return r, nil
}

// hashTree lists every regular file under dir with its sha256, refusing links and special
// files (the import never follows one out of the archive).
func hashTree(dir string) ([]oldFile, error) {
	var out []oldFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is a %s; the archive holds only files and directories", filepath.Join(filepath.Base(dir), rel), d.Type())
		}
		sum, err := oldtree.FileSHA256(p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, oldFile{Path: filepath.ToSlash(rel), SHA256: sum, Size: fi.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// copyArchive copies src's files and directories into dst (which exists), reading the source
// only.
func copyArchive(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		in, err := os.Open(p) //nolint:gosec // a file of the archive, read only
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the staged sidecar
		if err != nil {
			return err
		}
		_, cerr := io.Copy(out, in)
		return errors.Join(cerr, out.Sync(), out.Close())
	})
}

func writeNew(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the staged sidecar
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	return errors.Join(werr, f.Sync(), f.Close())
}

// readOnly makes every file under dir 0400 and every directory 0500, deepest first, through an
// os.Root on dir so no change can follow a link out of the sidecar.
func readOnly(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var dirs []string
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		return root.Chmod(p, sidecarFileMode)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := root.Chmod(dirs[i], sidecarDirMode); err != nil {
			return err
		}
	}
	return nil
}

// WriteText writes the report for a person.
func (r *StandbyReport) WriteText(w io.Writer) error {
	var lines []string
	p := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	p("import-standby: %s", r.Result)
	if r.Error != "" {
		p("  error: %s", r.Error)
	}
	p("  from:   %s", r.From)
	p("  target: %s", r.Target)
	for _, x := range r.Refusals {
		p("  REFUSED: %s", x)
	}
	if r.Result == "imported" {
		p("  source files: %d, unchanged %v", len(r.SourceFiles), r.SourceUnchanged)
		p("  chain: %d records in %d file(s), no anchor: %s", r.Chain.Records, r.Chain.Files, r.Chain.Verify)
		p("  keys: main %s, standby %s (same %v)", r.Keys.MainFingerprint, r.Keys.StandbyFingerprint, r.Keys.Same)
		for _, v := range r.Verbs {
			p("  seq %d %s %s %s: %s key", v.Seq, v.Kind, v.Operation, v.Resource, v.Match)
		}
		if r.Sessions != nil {
			p("  sessions.json: %v (written %s)", r.Sessions.IDs, r.Sessions.WrittenAt.Format(time.RFC3339))
		}
	}
	for _, x := range r.Warnings {
		p("  warning: %s", x)
	}
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON writes the report as indented JSON.
func (r *StandbyReport) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
