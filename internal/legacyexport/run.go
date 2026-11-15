// Package legacyexport is `cerberus export-legacy` (docs/substrate-port-plan.md, section 5, the
// reverse path): the rollback of `migrate-from`. It writes what the new build recorded after the
// handoff back into the old build's tree, in the old build's formats, so the old binary takes
// over where the new one stopped:
//
//   - the three append-only chains (audit, approvals, brakes): new's file must continue old's
//     byte for byte (a prefix proof against the D3 manifest), and the suffix is appended; the
//     audit anchor moves to the new head only after the appended chain verifies against it;
//   - the pause set, the applied digests and the credential-issuance receipts, rendered exactly
//     as the old writers rendered them, and state/*.json copied back whole;
//   - the class B host tables, merged into old's main.db in one transaction (B last; if the
//     Telemetry store is unreachable the run records it pending and exits 3).
//
// It holds old's daemon lock for the whole run (refusing while the old daemon holds it), and, before writing anything, unless the old tree matches the D3 manifest migrate-from
// kept, or the post-write state of the steps an earlier run did (manifest.Expect), so a partial
// run resumes and a finished one is a no-op. It never writes new's data directory except its
// progress file (StateRel).
package legacyexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/legacyimport"
	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
)

// ErrRefused is a run that refused before writing anything; the report names why.
var ErrRefused = errors.New("export-legacy refused; nothing was written")

// ExitTelemetryPending is the exit code of a run that finished every step but the class B one.
const ExitTelemetryPending = 3

// OnlyTelemetry is --only telemetry: the class B step alone, once every other step is done.
const OnlyTelemetry = "telemetry"

// PendingError is a run whose class B step is pending: not a success.
type PendingError struct {
	Handoff string
	Cause   string
}

func (e *PendingError) Error() string {
	return fmt.Sprintf("B step pending (%s): run `cerberus export-legacy --since %s --only telemetry` once that is resolved; the rollback is not complete", e.Cause, e.Handoff)
}

// ExitCode is ExitTelemetryPending.
func (e *PendingError) ExitCode() int { return ExitTelemetryPending }

// TelemetryOpener opens the Telemetry store's host tables for reading. Today that is the new
// SQLite database read-only through its WAL (hostdump.OpenLive); after class B moves to Postgres
// it becomes that store's reader.
type TelemetryOpener func(ctx context.Context) (hostdump.Querier, func() error, error)

// Options are one run's inputs. Paths are absolute.
type Options struct {
	// DataDir is the new data directory migrate-from wrote; DBPath its database (the Local
	// store, and today the Telemetry store); ConfigDir the new config dir (read for the policy
	// report only).
	DataDir   string
	DBPath    string
	ConfigDir string
	// Since names the handoff: the D3 manifest's body sha256, or a path to the manifest. It must
	// be the manifest migrate-from kept.
	Since string
	// OldHome, OldDB and OldAnchorAccount are the old tree to write to; empty is the manifest's
	// own (the live tree it was taken of). A rehearsal points them at a second copy.
	OldHome          string
	OldDB            string
	OldAnchorAccount string
	// Only is "" (every step) or OnlyTelemetry.
	Only string
	// Telemetry opens the Telemetry store; nil reads DBPath through hostdump.OpenLive.
	Telemetry TelemetryOpener
	Now       func() time.Time
}

// export is one run's state.
type export struct {
	o     Options
	r     *Report
	now   func() time.Time
	m     *manifest.Manifest
	state *State
	dirty bool
	local *store.Store

	newAudit, oldAudit string
	newHead            audit.Head
	plans              map[string]*plan
	// releaseLock drops old's daemon lock, held from preflight to the end of the run.
	releaseLock func()
}

// plan is one file step: the post state it leaves the old tree in, how to get there from the
// manifest state (or from a partial write of it), and how to check it got there.
type plan struct {
	post    Post
	apply   func(ctx context.Context) error
	verify  func(ctx context.Context) error
	summary string
}

func noop(context.Context) error { return nil }

// Run exports. The report is returned in every case; the error is ErrRefused (nothing written),
// a *PendingError (every step done but class B: exit 3), or what stopped the run (the progress
// file records the step, and the next run resumes it).
func Run(ctx context.Context, o Options) (*Report, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	r := newReport(now().UTC())
	r.DataDir, r.DatabasePath, r.Only = o.DataDir, o.DBPath, o.Only
	x := &export{o: o, r: r, now: now, plans: map[string]*plan{}}
	defer audit.SetAnchor("", nil)
	defer x.close()
	x.notReversed()

	finish := func(result string, err error) (*Report, error) {
		r.Result = result
		if err != nil {
			r.Error = err.Error()
		}
		r.Finished = now().UTC()
		return r, err
	}
	if err := x.preflight(ctx); err != nil {
		return finish(ResultFailed, err)
	}
	if len(r.Refusals) > 0 {
		return finish(ResultRefused, ErrRefused)
	}
	if o.Only == "" {
		for _, name := range append(append([]string(nil), fileSteps...), StepAnchor) {
			if err := x.run(ctx, name); err != nil {
				x.state.Error = err.Error()
				_ = x.save()
				return finish(ResultFailed, err)
			}
		}
	}
	if err := x.telemetry(ctx); err != nil {
		var pending *PendingError
		if errors.As(err, &pending) {
			return finish(ResultTelemetryPending, err)
		}
		x.state.Error = err.Error()
		_ = x.save()
		return finish(ResultFailed, err)
	}
	if x.dirty {
		x.state.Error = ""
		if err := x.save(); err != nil {
			return finish(ResultFailed, err)
		}
	}
	return finish(ResultExported, nil)
}

func (x *export) close() {
	if x.local != nil {
		_ = x.local.Close()
	}
	if x.releaseLock != nil {
		x.releaseLock()
	}
}

func (x *export) save() error {
	x.dirty = true
	return x.state.save(x.o.DataDir)
}

// run takes one file step (or the anchor): skipped when an earlier run did it (its post state
// was verified in preflight), otherwise recorded as started with its post state, applied,
// verified, and recorded done.
func (x *export) run(ctx context.Context, name string) error {
	p := x.plans[name]
	if x.state.Steps[name].done() {
		x.r.step(name, StatusDone, false, "done by an earlier run; the old tree is in its post state")
		return nil
	}
	x.state.Steps[name] = &Step{Status: StatusStarted, At: x.now().UTC(), Post: clonePost(&p.post)}
	if err := x.save(); err != nil {
		return err
	}
	err := p.apply(ctx)
	if err == nil {
		err = p.verify(ctx)
	}
	if err != nil {
		x.state.Steps[name].Status, x.state.Steps[name].Error = StatusFailed, err.Error()
		_ = x.save()
		x.r.step(name, StatusFailed, true, "%v", err)
		return fmt.Errorf("%s: %w", name, err)
	}
	x.state.Steps[name].Status, x.state.Steps[name].At = StatusDone, x.now().UTC()
	if err := x.save(); err != nil {
		return err
	}
	x.r.step(name, StatusDone, true, "%s", p.summary)
	return nil
}

// preflight checks everything before anything is written: the handoff, the paths, new's side
// (every chain verifies, every row is writable in the old format), and the old tree against the
// manifest or the steps already done. Refusals go on the report. It writes nothing.
func (x *export) preflight(ctx context.Context) error {
	o, r := &x.o, x.r
	for _, p := range []struct{ name, path string }{{"the data directory", o.DataDir}, {"database.sqlite.path", o.DBPath}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			r.refuse("%s must be an absolute path (got %q)", p.name, p.path)
		}
	}
	if o.Only != "" && o.Only != OnlyTelemetry {
		r.refuse("--only takes %q, got %q", OnlyTelemetry, o.Only)
	}
	if o.Since == "" {
		r.refuse("--since is required: the D3 manifest's body sha256 (or its path) that migrate-from kept in %s", filepath.Join(o.DataDir, filepath.FromSlash(manifest.KeptRel)))
	}
	if len(r.Refusals) > 0 {
		return nil
	}
	if fi, err := os.Stat(o.DataDir); err != nil || !fi.IsDir() {
		r.refuse("the data directory %s is not a directory (%v)", o.DataDir, err)
		return nil
	}

	// The handoff: the manifest migrate-from kept, named exactly by --since, and the migration's
	// own report saying it migrated against it.
	m, fileSum, err := manifest.LoadKept(o.DataDir)
	if err != nil {
		r.refuse("%v", err)
		return nil
	}
	x.m = m
	r.Handoff, r.ManifestPath = m.BodySHA256, filepath.Join(o.DataDir, filepath.FromSlash(manifest.KeptRel))
	if !x.sinceMatches(m) {
		return nil
	}
	x.checkMigrationReport(fileSum)

	if o.OldHome == "" {
		o.OldHome = m.OldHome
	}
	if o.OldDB == "" {
		o.OldDB = m.OldDB
	}
	if o.OldAnchorAccount == "" {
		o.OldAnchorAccount = m.Anchor.Account
	}
	if o.OldAnchorAccount == "" && o.OldHome != "" {
		o.OldAnchorAccount = filepath.Join(o.OldHome, "audit")
	}
	r.OldHome, r.OldDB, r.OldAnchorAccount = o.OldHome, o.OldDB, o.OldAnchorAccount
	for _, p := range []struct{ name, path string }{{"--old-home", o.OldHome}, {"--old-db", o.OldDB}, {"--old-anchor-account", o.OldAnchorAccount}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			r.refuse("%s must be an absolute path (got %q)", p.name, p.path)
		}
	}
	if len(r.Refusals) > 0 {
		return nil
	}
	if fi, serr := os.Stat(o.OldHome); serr != nil || !fi.IsDir() {
		r.refuse("--old-home %s is not a directory (%v)", o.OldHome, serr)
	}
	if fi, serr := os.Lstat(o.OldDB); serr != nil || !fi.Mode().IsRegular() {
		r.refuse("--old-db %s is not a regular file (%v)", o.OldDB, serr)
	}
	if fi, serr := os.Lstat(o.DBPath); serr != nil || !fi.Mode().IsRegular() {
		r.refuse("the new database %s is not a regular file (%v)", o.DBPath, serr)
	}
	for _, src := range []string{o.OldHome, o.OldDB} {
		if within(resolve(src), resolve(o.DataDir)) || within(resolve(o.DataDir), resolve(src)) {
			r.refuse("the data directory %s overlaps the old tree %s", o.DataDir, src)
		}
	}
	if len(r.Refusals) > 0 {
		return nil
	}
	x.newAudit, x.oldAudit = filepath.Join(o.DataDir, "audit"), filepath.Join(o.OldHome, "audit")

	// The old daemon must not be running (plan rev 10): its single-instance lock is taken here and
	// held until the run ends (close), so it cannot start mid-export. --only telemetry too.
	lock, release, lockErr := acquireDaemonLock(o.OldHome)
	r.OldDaemonLock = lock
	if lockErr != nil {
		r.refuse("%v", lockErr)
		return nil
	}
	x.releaseLock = release
	if lock == "absent" {
		r.warn("old's daemon lock %s does not exist: the run holds no lock against an old daemon starting", filepath.Join(o.OldHome, DaemonLockName))
	}

	// The progress of an earlier run belongs to this handoff and this tree, or it is refused.
	st, ok, err := loadState(o.DataDir)
	switch {
	case err != nil:
		r.refuse("the progress file: %v", err)
		return nil
	case !ok:
		st = &State{Version: StateVersion, Handoff: m.BodySHA256, OldHome: o.OldHome, OldDB: o.OldDB, OldAnchorAccount: o.OldAnchorAccount, Steps: map[string]*Step{}}
	case st.Handoff != m.BodySHA256:
		r.refuse("the progress file %s belongs to handoff %s, not %s: it is another window's rollback", statePath(o.DataDir), st.Handoff, m.BodySHA256)
	case st.OldHome != o.OldHome || st.OldDB != o.OldDB || st.OldAnchorAccount != o.OldAnchorAccount:
		r.refuse("the progress file %s records a rollback into %s (%s, anchor %s); this run targets %s (%s, anchor %s): finish that one, or move the progress file aside only if you know it is stale",
			statePath(o.DataDir), st.OldHome, st.OldDB, st.OldAnchorAccount, o.OldHome, o.OldDB, o.OldAnchorAccount)
	}
	if len(r.Refusals) > 0 {
		return nil
	}
	x.state = st
	r.StatePath = statePath(o.DataDir)
	if o.Only == OnlyTelemetry {
		for _, name := range append(append([]string(nil), fileSteps...), StepAnchor) {
			if !st.Steps[name].done() {
				r.refuse("--only telemetry needs every other step done; %s is not (%s): run export-legacy --since %s without --only", name, statusOf(st.Steps[name]), m.BodySHA256)
			}
		}
		if len(r.Refusals) > 0 {
			return nil
		}
	}

	x.local, err = store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: o.DBPath}})
	if err != nil {
		r.refuse("open the new database %s: %v", o.DBPath, err)
		return nil
	}

	// New's side, and what each step will leave in the old tree.
	planners := []struct {
		name string
		fn   func(context.Context) (*plan, error)
	}{
		{StepAudit, x.planAudit}, {StepApprovals, x.planApprovals}, {StepBrakes, x.planBrakes},
		{StepPause, x.planPause}, {StepApplied, x.planApplied}, {StepReceipts, x.planReceipts}, {StepState, x.planState},
	}
	for _, p := range planners {
		pl, perr := p.fn(ctx)
		if perr != nil {
			return fmt.Errorf("%s: %w", p.name, perr)
		}
		x.plans[p.name] = pl
	}
	if len(r.Refusals) > 0 {
		return nil
	}
	x.plans[StepAnchor] = x.planAnchor()

	// A step done by an earlier run must still be what new's side gives now: new changed since
	// (it ran again after the export) is refused rather than half carried.
	for name, pl := range x.plans {
		if s := st.Steps[name]; s.done() && !samePost(s.Post, &pl.post) {
			r.refuse("the %s step was done by an earlier run, but new's side no longer gives the state it wrote: the new build wrote after the export; stop it, and start the rollback over from a fresh copy of the old tree", name)
		}
	}
	if len(r.Refusals) > 0 {
		return nil
	}

	x.policyReport()

	// The old tree, as it is now, against the manifest and the steps done.
	now, err := manifest.Take(ctx, manifest.Options{OldHome: o.OldHome, OldDB: o.OldDB, AnchorAccount: o.OldAnchorAccount, Live: true})
	if err != nil {
		return fmt.Errorf("read the old tree: %w", err)
	}
	x.checkTree(now)
	return nil
}

func statusOf(s *Step) string {
	if s == nil {
		return "not started"
	}
	return s.Status
}

// sinceMatches checks --since against the kept manifest.
func (x *export) sinceMatches(m *manifest.Manifest) bool {
	since := x.o.Since
	if strings.HasPrefix(since, "sha256:") {
		if since != m.BodySHA256 {
			x.r.refuse("--since %s is not the handoff migrate-from kept (%s)", since, m.BodySHA256)
			return false
		}
		return true
	}
	given, _, err := manifest.Load(since)
	if err != nil {
		x.r.refuse("--since: %v", err)
		return false
	}
	if given.BodySHA256 != m.BodySHA256 {
		x.r.refuse("--since %s is manifest %s; migrate-from kept %s: name the window migrate-from ran against", since, given.BodySHA256, m.BodySHA256)
		return false
	}
	return true
}

// checkMigrationReport requires the migration's own report: it migrated, against this manifest.
func (x *export) checkMigrationReport(manifestFileSum string) {
	p := filepath.Join(x.o.DataDir, "migration", "report.json")
	data, err := os.ReadFile(p) //nolint:gosec // the data directory's own file
	if err != nil {
		x.r.refuse("the migration's report %s: %v", p, err)
		return
	}
	var rep legacyimport.Report
	if err = json.Unmarshal(data, &rep); err != nil {
		x.r.refuse("the migration's report %s does not parse: %v", p, err)
		return
	}
	switch {
	case rep.Result != legacyimport.ResultMigrated:
		x.r.refuse("the migration's report %s says %q, not %q", p, rep.Result, legacyimport.ResultMigrated)
	case rep.Manifest == nil || rep.Manifest.SHA256 != manifestFileSum:
		x.r.refuse("the migration's report %s does not record the kept manifest (%s)", p, manifestFileSum)
	}
}

// checkTree compares the old tree with what it should be: the manifest, with each done step's
// post state over it. A file of a step a crash or an error interrupted may be in either its
// manifest state or its post state. Anything else refuses, naming the first mismatch.
func (x *export) checkTree(now *manifest.Manifest) {
	base := manifest.Overrides{Files: map[string]manifest.File{}}
	alt := manifest.Overrides{Files: map[string]manifest.File{}}
	inFlight := map[string]bool{}
	for _, name := range append(append([]string(nil), fileSteps...), StepAnchor) {
		s := x.state.Steps[name]
		switch {
		case s.done():
			addPost(&base, s.Post, nil)
			addPost(&alt, s.Post, nil)
		case s.inFlight():
			addPost(&alt, &x.plans[name].post, inFlight)
		}
	}
	tel := x.state.Steps[StepTelemetry]
	if tel.done() && tel.Post != nil && tel.Post.DBState != nil {
		base.DB, alt.DB = tel.Post.DBState, tel.Post.DBState
	}
	altBad := map[string]bool{}
	for _, d := range manifest.Diff(manifest.Expect(x.m, alt), now) {
		altBad[d.Key] = true
	}
	var bad []string
	for _, d := range manifest.Diff(manifest.Expect(x.m, base), now) {
		switch {
		case d.Key == manifest.KeyDB && tel.inFlight():
			// The class B transaction may have committed before the crash; the merge is
			// idempotent under its own conflict rules and runs again.
			x.r.warn("main.db is not in its manifest state, and the class B step was interrupted: it runs again, refusing any conflicting row")
		case inFlight[d.Key] && !altBad[d.Key]:
		default:
			bad = append(bad, d.Detail)
		}
	}
	x.r.Mismatches = bad
	x.r.ManifestMatches = len(bad) == 0
	if len(bad) > 0 {
		x.r.refuse("the old tree does not match the D3 manifest %s (or the post state of the steps already done): first mismatch: %s", x.m.BodySHA256, bad[0])
	}
}

// addPost overrides o with p, marking the keys it covers in keys (when set).
func addPost(o *manifest.Overrides, p *Post, keys map[string]bool) {
	if p == nil {
		return
	}
	for k, f := range p.Files {
		o.Files[k] = f
		if keys != nil {
			keys[k] = true
		}
	}
	if p.Dirs != nil {
		o.Dirs = p.Dirs
		if keys != nil {
			keys[manifest.KeyDirs] = true
		}
	}
	if p.Anchor != nil {
		o.Anchor = p.Anchor
		if keys != nil {
			keys[manifest.KeyAnchor] = true
		}
	}
}

func clonePost(p *Post) *Post {
	if p == nil {
		return nil
	}
	out := &Post{Dirs: append([]string(nil), p.Dirs...)}
	if p.Dirs == nil {
		out.Dirs = nil
	}
	if len(p.Files) > 0 {
		out.Files = make(map[string]manifest.File, len(p.Files))
		for k, v := range p.Files {
			out.Files[k] = v
		}
	}
	if p.Anchor != nil {
		a := *p.Anchor
		a.Head = append(json.RawMessage(nil), p.Anchor.Head...)
		out.Anchor = &a
	}
	if p.DBState != nil {
		s := *p.DBState
		out.DBState = &s
	}
	return out
}

// samePost reports whether a recorded post state is the one planned now (files, reservations and
// anchor head; the database state is the class B step's own).
func samePost(a, b *Post) bool {
	if a == nil {
		a = &Post{}
	}
	if b == nil {
		b = &Post{}
	}
	if len(a.Files) != len(b.Files) {
		return false
	}
	for k, v := range a.Files {
		if w, ok := b.Files[k]; !ok || w != v {
			return false
		}
	}
	if !reflect.DeepEqual(append([]string{}, a.Dirs...), append([]string{}, b.Dirs...)) {
		return false
	}
	if (a.Anchor == nil) != (b.Anchor == nil) {
		return false
	}
	if a.Anchor != nil {
		var x, y audit.Head
		if json.Unmarshal(a.Anchor.Head, &x) != nil || json.Unmarshal(b.Anchor.Head, &y) != nil || x != y || a.Anchor.Account != b.Anchor.Account {
			return false
		}
	}
	return true
}

// fileState is a file's manifest.File as it is now (Absent when it does not exist).
func fileState(p string) (manifest.File, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return manifest.File{Absent: true}, nil
	}
	if err != nil {
		return manifest.File{}, err
	}
	if !fi.Mode().IsRegular() {
		return manifest.File{}, fmt.Errorf("%s is a %s, not a file", p, fi.Mode().Type())
	}
	sum, err := oldtree.FileSHA256(p)
	if err != nil {
		return manifest.File{}, err
	}
	return manifest.File{SHA256: sum, Size: fi.Size()}, nil
}

// manifestFile is the manifest's record of rel (Absent when it has none).
func (x *export) manifestFile(rel string) manifest.File {
	if f, ok := x.m.Files[rel]; ok {
		return f
	}
	return manifest.File{Absent: true}
}

// notReversed fills what a rollback leaves as it is, and what awaits a ruling.
func (x *export) notReversed() {
	x.r.NotReversed = []string{
		"policy/ in the config dir and the config fragment (R13): not in the manifest; old's policy/ and config files are left as they are",
		"oauth/, alerts/, events/: carried inert by migrate-from; the old tree's own copies are unchanged",
		"registry.yaml, hosts.yaml, hosts.history.jsonl*, plugin-connectors.json: in place in the adopted root; a rollback keeps new's edits (10a rulings R8, R15, R18)",
		"credential-issuance/secrets/issued: in place; nothing to do",
		"standby.d/: old's own record of the window; nothing to do",
	}
}

// resolve follows symlinks where the path (or its parent) exists.
func resolve(p string) string {
	if res, err := filepath.EvalSymlinks(p); err == nil {
		return res
	}
	if res, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(res, filepath.Base(p))
	}
	return filepath.Clean(p)
}

// within reports whether path is root or below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
