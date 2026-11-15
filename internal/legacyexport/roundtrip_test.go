package legacyexport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/legacyimport"
	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
	"github.com/hollis-labs/cerberus/internal/legacyimport/legacyimporttest"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// roundTrip is the rehearsal's setup: the old-tree fixture materialized twice (copy1, copy2,
// each with its anchor seeded under its own account), the D3 manifest taken of copy1, migrate-from
// from copy1 into a data dir with that manifest, and a window of new activity there. export-legacy
// then targets copy2, the "live" tree.
type roundTrip struct {
	c1, c2   legacyimporttest.Fixture
	manifest string
	handoff  string
	act      activity
}

func setup(t *testing.T) *roundTrip {
	t.Helper()
	ctx := context.Background()
	rt := &roundTrip{c1: legacyimporttest.Materialize(t, t.TempDir()), c2: legacyimporttest.Materialize(t, t.TempDir())}
	rt.manifest = filepath.Join(rt.c1.Root, "d3-manifest.json")
	m, err := manifest.Write(ctx, manifest.Options{OldHome: rt.c1.OldHome, OldDB: rt.c1.OldDB, AnchorAccount: rt.c1.Account, Live: true}, rt.manifest)
	if err != nil {
		t.Fatal(err)
	}
	rt.handoff = m.BodySHA256
	f := rt.c1
	rep, err := legacyimport.Run(ctx, legacyimport.Options{OldHome: f.OldHome, OldDB: f.OldDB, OldAnchorAccount: f.Account,
		DataDir: f.DataDir, DBPath: f.DBPath, ConfigDir: f.ConfigDir, Home: f.Home, Manifest: rt.manifest,
		OldRoots: []string{filepath.Join(f.Home, ".cerberus"), filepath.Join(f.Home, ".local", "share", "cerberus")}})
	if err != nil {
		var buf bytes.Buffer
		_ = rep.WriteText(&buf)
		t.Fatalf("migrate-from: %v\n%s", err, buf.String())
	}
	// copy2 is what the manifest recorded, by relative path.
	if got, err := manifest.Check(ctx, m, manifest.Options{OldHome: rt.c2.OldHome, OldDB: rt.c2.OldDB, AnchorAccount: rt.c2.Account, Live: true}); err != nil || len(got) > 0 {
		t.Fatalf("copy2 does not match the manifest of copy1: %v %v", got, err)
	}
	rt.act = driveNewActivity(t, f.DataDir, f.DBPath)
	return rt
}

func (rt *roundTrip) options() Options {
	return Options{DataDir: rt.c1.DataDir, DBPath: rt.c1.DBPath, ConfigDir: rt.c1.ConfigDir, Since: rt.handoff,
		OldHome: rt.c2.OldHome, OldDB: rt.c2.OldDB, OldAnchorAccount: rt.c2.Account}
}

func (rt *roundTrip) export(t *testing.T, o Options) (*Report, error) {
	t.Helper()
	r, err := Run(context.Background(), o)
	if r == nil {
		t.Fatalf("no report (%v)", err)
	}
	return r, err
}

func mustExport(t *testing.T, rt *roundTrip, o Options) *Report {
	t.Helper()
	r, err := rt.export(t, o)
	if err != nil {
		var buf bytes.Buffer
		_ = r.WriteText(&buf)
		t.Fatalf("export-legacy: %v\n%s", err, buf.String())
	}
	return r
}

// fingerprint is the old tree and database as they stand (legacyimport's fingerprint: every
// entry's path, mode, size, mtime and sha256).
func fingerprint(t *testing.T, rt *roundTrip) string {
	t.Helper()
	fp, err := legacyimport.TakeFingerprint(rt.c2.OldHome, rt.c2.OldDB)
	if err != nil {
		t.Fatal(err)
	}
	anchor, _ := secrets.KeyringGet(auditchain.AnchorService, rt.c2.Account)
	return fp.Digest + " " + anchor
}

func readBytes(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p) //nolint:gosec // the test's own tree
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The round trip: every record and row new wrote after the handoff is in the old tree in the old
// format, the anchor is the new head, the old chain verifies against it, and a second run is a
// no-op.
func TestExportLegacyRoundTrip(t *testing.T) {
	rt := setup(t)
	ctx := context.Background()
	r := mustExport(t, rt, rt.options())
	if r.Result != ResultExported || !r.ManifestMatches || len(r.Refusals) != 0 {
		t.Fatalf("result %s, matches %v, refusals %v", r.Result, r.ManifestMatches, r.Refusals)
	}
	for _, s := range r.Steps {
		if s.Status != StatusDone || !s.Ran {
			t.Errorf("step %+v", s)
		}
	}
	data, old := rt.c1.DataDir, rt.c2.OldHome

	// Audit: November appended, December created, both byte for byte new's; .digest_key untouched.
	if r.Audit.Appended["audit/2026-11.jsonl"] == 0 || r.Audit.Created["audit/2026-12.jsonl"] == 0 || !r.Audit.DigestKeySHA256Equal {
		t.Fatalf("audit: %+v", r.Audit)
	}
	for _, rel := range []string{"audit/2026-11.jsonl", "audit/2026-12.jsonl", "approvals/events.jsonl", "brakes/events.jsonl"} {
		if !bytes.Equal(readBytes(t, filepath.Join(old, rel)), readBytes(t, filepath.Join(data, rel))) {
			t.Errorf("%s: the old tree's file is not new's", rel)
		}
	}
	if !bytes.Equal(readBytes(t, filepath.Join(old, "audit", ".digest_key")), readBytes(t, filepath.Join(legacyimporttest.Testdata(), "oldtree", "audit", ".digest_key"))) {
		t.Error("audit/.digest_key changed")
	}
	if fi, err := os.Stat(filepath.Join(old, "audit", "2026-12.jsonl")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the created month file: %v %v", fi, err)
	}
	// The anchor is new's last record, and the old chain verifies against it, as old will.
	head, err := lastHead(filepath.Join(data, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	if _, got, ok, err := oldtree.ReadAnchor(rt.c2.Account); err != nil || !ok || got != head || head.File != "2026-12.jsonl" {
		t.Fatalf("anchor %+v %v, want %+v", got, err, head)
	}
	if _, verdict := oldtree.CheckChain(filepath.Join(old, "audit"), auditchain.KeychainAnchor{Dir: rt.c2.Account}); verdict != "ok" {
		t.Fatalf("the old chain against the moved anchor: %s", verdict)
	}
	if fresh, _ := secrets.KeyringGet(auditchain.AnchorService, rt.c1.Account); fresh != rt.c1.AnchorRaw {
		t.Error("copy1's anchor moved")
	}

	// Approvals: old's store reads to new's seq and holds the consumed and the pending approval;
	// brakes fold to new's state.
	nb, _ := approval.ReadFrom(filepath.Join(data, "approvals"), approval.Cursor{}, 0)
	ob, _ := approval.ReadFrom(filepath.Join(old, "approvals"), approval.Cursor{}, 0)
	if ob.Stop != nil || ob.Next.Seq != nb.Next.Seq || ob.Next.Seq == 0 {
		t.Fatalf("approvals: old %+v, new %+v", ob.Next, nb.Next)
	}
	ns, _ := brake.Store{Dir: filepath.Join(data, "brakes")}.Load()
	os2, _ := brake.Store{Dir: filepath.Join(old, "brakes")}.Load()
	if !reflect.DeepEqual(ns, os2) || os2.Lockdown == nil {
		t.Fatalf("brakes: old %+v, new %+v", os2, ns)
	}

	// The pause set is new's: api created with the old writer's bytes and mode, web removed.
	st := openStore(t, rt.c1.DBPath)
	targets, _ := storage.PausedTargets(ctx, st)
	var pp oldtree.Problems
	var got []string
	for _, mk := range oldtree.ReadPause(old, &pp) {
		got = append(got, mk.Target)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, targets) || !contains(targets, rt.act.Paused) || contains(targets, rt.act.Resumed) {
		t.Fatalf("pause markers %v, new's set %v", got, targets)
	}
	if b := readBytes(t, filepath.Join(old, "paused."+rt.act.Paused)); string(b) != "paused\n" {
		t.Errorf("paused.%s is %q", rt.act.Paused, b)
	}
	if fi, _ := os.Stat(filepath.Join(old, "paused."+rt.act.Paused)); fi.Mode().Perm()&^0o022 != 0o644&^0o022 {
		t.Errorf("paused.%s mode %v", rt.act.Paused, fi.Mode())
	}

	// applied.json: old's map overlaid with new's, in the old writer's bytes.
	want := map[string]string{}
	if err := json.Unmarshal(readBytes(t, filepath.Join(legacyimporttest.Testdata(), "oldtree", "runtime", "applied.json")), &want); err != nil {
		t.Fatal(err)
	}
	want[rt.act.AppliedID] = rt.act.AppliedDigest
	render, _ := json.MarshalIndent(want, "", "  ")
	if b := readBytes(t, filepath.Join(old, "runtime", "applied.json")); !bytes.Equal(b, render) || bytes.HasSuffix(b, []byte("\n")) {
		t.Fatalf("applied.json:\n%s\nwant\n%s", b, render)
	}
	if fi, _ := os.Stat(filepath.Join(old, "runtime", "applied.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("applied.json mode %v", fi.Mode())
	}

	// Receipts: new's issuance as the old broker's files, byte for byte its rendering.
	rows, _ := storage.Receipts(ctx, st, rt.act.Issuance)
	if len(rows) != 4 {
		t.Fatalf("%d receipts for the new issuance", len(rows))
	}
	for _, rc := range rows {
		p := filepath.Join(old, "credential-issuance", rc.IssuanceID, rc.State+".json")
		b := readBytes(t, p)
		var parsed map[string]any
		if err := json.Unmarshal(b, &parsed); err != nil || parsed["state"] != rc.State {
			t.Fatalf("%s: %s", p, b)
		}
		keys := []string{`"id"`, `"connector"`, `"operation"`, `"binding_digest"`, `"state"`, `"references"`, `"at"`}
		last := -1
		for _, k := range keys {
			i := bytes.Index(b, []byte(k))
			if i <= last {
				t.Errorf("%s: field %s out of the old order: %s", p, k, b)
			}
			last = i
		}
		if rc.State == "preparing" && !bytes.Contains(b, []byte(`"references":{}`)) || bytes.HasSuffix(b, []byte("\n")) || !bytes.Contains(b, []byte(`Z"}`)) {
			t.Errorf("%s: %s", p, b)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", p, fi.Mode())
		}
	}
	if fi, err := os.Stat(filepath.Join(old, "credential-issuance", rt.act.Issuance)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the new reservation directory: %v %v", fi, err)
	}

	// The host tables: new EXCEPT old is empty on all five; old keeps what new's janitor removed.
	checkHostUnion(t, rt.c1.DBPath, rt.c2.OldDB)

	// A second run is a no-op: nothing in the old tree or the anchor changes.
	before := fingerprint(t, rt)
	again := mustExport(t, rt, rt.options())
	if again.Result != ResultExported || fingerprint(t, rt) != before {
		t.Fatalf("the second run: %s; changed the old tree %v", again.Result, fingerprint(t, rt) != before)
	}
	for _, s := range again.Steps {
		if s.Ran {
			t.Errorf("the second run ran %s", s.Step)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// checkHostUnion: every new host row is in old (samples, rollups, events as they are; status and
// rollup state per key, new being newer here), the sequence is new's, and integrity_check passes.
func checkHostUnion(t *testing.T, newDB, oldDB string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", hostdump.URI(oldDB, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "ATTACH DATABASE ? AS new", hostdump.URI(newDB, "mode=ro")); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range hostdump.Tables {
		cols := tbl.ColumnList()
		var n, newCount, oldCount int
		q := "SELECT COUNT(*) FROM (SELECT " + cols + " FROM new." + tbl.Name + " EXCEPT SELECT " + cols + " FROM main." + tbl.Name + ")"
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d of new's rows are not in old (%v)", tbl.Name, n, err)
		}
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM new."+tbl.Name).Scan(&newCount)
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM main."+tbl.Name).Scan(&oldCount)
		if oldCount < newCount {
			t.Errorf("%s: old %d rows, new %d", tbl.Name, oldCount, newCount)
		}
	}
	var oldSeq, newSeq int64
	_ = db.QueryRowContext(ctx, "SELECT seq FROM main.sqlite_sequence WHERE name = 'host_events'").Scan(&oldSeq)
	_ = db.QueryRowContext(ctx, "SELECT seq FROM new.sqlite_sequence WHERE name = 'host_events'").Scan(&newSeq)
	if oldSeq != newSeq || oldSeq <= 5 {
		t.Errorf("host_events sqlite_sequence old %d, new %d", oldSeq, newSeq)
	}
	var ok string
	if err := db.QueryRowContext(ctx, "PRAGMA main.integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Errorf("integrity_check %q %v", ok, err)
	}
}

// A tampered old tree is refused before anything is written: no file, no anchor, no progress.
func TestExportLegacyRefusesATamperedTree(t *testing.T) {
	rt := setup(t)
	f := filepath.Join(rt.c2.OldHome, "state", "hosts.json")
	data := readBytes(t, f)
	if err := os.WriteFile(f, append(data, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	before := fingerprint(t, rt)
	r, err := rt.export(t, rt.options())
	if !errors.Is(err, ErrRefused) || r.Result != ResultRefused || len(r.Mismatches) == 0 || !strings.Contains(r.Refusals[0], "state/hosts.json") {
		t.Fatalf("a tampered tree: %s %v %v", r.Result, err, r.Refusals)
	}
	if fingerprint(t, rt) != before {
		t.Fatal("a refused run changed the old tree or its anchor")
	}
	if _, err := os.Stat(statePath(rt.c1.DataDir)); !os.IsNotExist(err) {
		t.Fatalf("a refused run left a progress file (%v)", err)
	}

	// An edited new chain is refused: it no longer verifies against new's anchor.
	if err := os.WriteFile(f, data, 0o600); err != nil {
		t.Fatal(err)
	}
	nov := filepath.Join(rt.c1.DataDir, "audit", "2026-11.jsonl")
	b := readBytes(t, nov)
	b[10] ^= 1
	if err := os.WriteFile(nov, b, 0o600); err != nil {
		t.Fatal(err)
	}
	before = fingerprint(t, rt)
	if r, err := rt.export(t, rt.options()); !errors.Is(err, ErrRefused) || fingerprint(t, rt) != before || !strings.Contains(strings.Join(r.Refusals, " "), "new's audit chain does not verify") {
		t.Fatalf("an edited new chain: %s %v %v", r.Result, err, r.Refusals)
	}

	// A --since that is not the kept handoff is refused.
	b[10] ^= 1
	if err := os.WriteFile(nov, b, 0o600); err != nil {
		t.Fatal(err)
	}
	o := rt.options()
	o.Since = "sha256:" + strings.Repeat("0", 64)
	if r, err := rt.export(t, o); !errors.Is(err, ErrRefused) || !strings.Contains(r.Refusals[0], "not the handoff") {
		t.Fatalf("another handoff: %v %v", err, r.Refusals)
	}
	o.Since = rt.manifest
	if r := mustExport(t, rt, o); r.Result != ResultExported {
		t.Fatalf("--since by path: %s", r.Result)
	}
}

// With the Telemetry store unreachable, every file step and the anchor are done, the host tables
// are not touched, the run exits 3 with B pending; --only telemetry finishes it, and is then a
// no-op.
func TestExportLegacyTelemetryPending(t *testing.T) {
	rt := setup(t)
	ctx := context.Background()
	o := rt.options()
	o.Telemetry = func(context.Context) (hostdump.Querier, func() error, error) {
		return nil, nil, errors.New("connection refused")
	}
	dbBefore := dbState(t, rt.c2.OldDB)
	r, err := rt.export(t, o)
	var pending *PendingError
	if !errors.As(err, &pending) || pending.ExitCode() != ExitTelemetryPending || r.Result != ResultTelemetryPending || !r.Telemetry.Pending {
		t.Fatalf("unreachable Telemetry: %s %v", r.Result, err)
	}
	if !strings.Contains(err.Error(), "--only telemetry") || !strings.Contains(err.Error(), rt.handoff) {
		t.Fatalf("the pending error does not say what to run: %v", err)
	}
	if got := dbState(t, rt.c2.OldDB); got.Diff(dbBefore) != "" {
		t.Fatalf("the host tables changed: %s", got.Diff(dbBefore))
	}
	st, _, err := loadState(rt.c1.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(append([]string(nil), fileSteps...), StepAnchor) {
		if !st.Steps[name].done() {
			t.Errorf("%s: %+v", name, st.Steps[name])
		}
	}
	if st.Steps[StepTelemetry].Status != StatusPending {
		t.Fatalf("telemetry: %+v", st.Steps[StepTelemetry])
	}

	// --only telemetry while it is still down: still pending.
	o.Only = OnlyTelemetry
	if _, err := rt.export(t, o); !errors.As(err, &pending) {
		t.Fatalf("--only telemetry, still down: %v", err)
	}
	// Back up.
	o.Telemetry = nil
	r = mustExport(t, rt, o)
	if r.Result != ResultExported || r.Telemetry == nil || r.Telemetry.Inserted["host_samples"] != 30 || r.Telemetry.Updated["host_status"] != 1 || r.Telemetry.Inserted["host_events"] != 1 {
		t.Fatalf("--only telemetry: %s %+v", r.Result, r.Telemetry)
	}
	checkHostUnion(t, rt.c1.DBPath, rt.c2.OldDB)
	before := fingerprint(t, rt)
	if r := mustExport(t, rt, o); r.Result != ResultExported || fingerprint(t, rt) != before {
		t.Fatalf("--only telemetry again: %s", r.Result)
	}
	_ = ctx
}

func dbState(t *testing.T, path string) hostdump.State {
	t.Helper()
	db, err := hostdump.OpenLive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s, err := hostdump.ReadState(context.Background(), db, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// --only telemetry before the file steps are done is refused, writing nothing.
func TestOnlyTelemetryNeedsTheOtherSteps(t *testing.T) {
	rt := setup(t)
	o := rt.options()
	o.Only = OnlyTelemetry
	before := fingerprint(t, rt)
	r, err := rt.export(t, o)
	if !errors.Is(err, ErrRefused) || !strings.Contains(r.Refusals[0], "needs every other step done") || fingerprint(t, rt) != before {
		t.Fatalf("--only telemetry first: %v %v", err, r.Refusals)
	}
}

// A step a crash interrupted resumes: its files may be in their manifest state or their post
// state. Here the anchor step failed after the files were written; the old anchor is still the
// manifest's. The next run moves it. The tree in its post state with no progress recorded is
// refused (nothing says those writes were export-legacy's).
func TestExportLegacyResumesAnInterruptedStep(t *testing.T) {
	rt := setup(t)
	mustExport(t, rt, rt.options())
	st, _, err := loadState(rt.c1.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	st.Steps[StepAnchor].Status = StatusFailed
	if err := st.save(rt.c1.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := secrets.KeyringSet(auditchain.AnchorService, rt.c2.Account, rt.c2.AnchorRaw); err != nil {
		t.Fatal(err)
	}
	r := mustExport(t, rt, rt.options())
	if r.Result != ResultExported || r.Anchor == nil || !r.Anchor.Moved {
		t.Fatalf("resume: %s %+v", r.Result, r.Anchor)
	}

	if err := os.Remove(statePath(rt.c1.DataDir)); err != nil {
		t.Fatal(err)
	}
	if r, err := rt.export(t, rt.options()); !errors.Is(err, ErrRefused) || len(r.Mismatches) == 0 {
		t.Fatalf("a written tree with no progress file: %v", err)
	}
}

// A class B conflict (a host row both sides hold with different values) leaves B pending and
// the host tables untouched.
func TestExportLegacyTelemetryConflictIsPending(t *testing.T) {
	rt := setup(t)
	ctx := context.Background()
	st := openStore(t, rt.c1.DBPath)
	var host string
	var ts int64
	if err := st.Reader().QueryRowContext(ctx, "SELECT host, ts FROM host_samples ORDER BY ts LIMIT 1").Scan(&host, &ts); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, "UPDATE host_samples SET probe_ms = probe_ms + 1 WHERE host = ? AND ts = ?", host, ts); err != nil {
		t.Fatal(err)
	}
	before := dbState(t, rt.c2.OldDB)
	r, err := rt.export(t, rt.options())
	var pending *PendingError
	if !errors.As(err, &pending) || !strings.Contains(r.Telemetry.Error, "conflict") {
		t.Fatalf("a conflicting row: %v %+v", err, r.Telemetry)
	}
	if got := dbState(t, rt.c2.OldDB); got.Diff(before) != "" {
		t.Fatalf("a conflicting merge changed old's main.db: %s", got.Diff(before))
	}
}

// The prefix proof: new's file continues the manifest's only when its first size bytes hash to
// the recorded sha256; a shorter file or another prefix is refused; a file the manifest never
// had is created whole.
func TestPlanAppendableProvesThePrefix(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "new"), filepath.Join(dir, "old")
	writeFile(t, src, "abcdef\n")
	x := &export{m: &manifest.Manifest{Files: map[string]manifest.File{
		"ok":      {SHA256: oldtree.BytesSHA256([]byte("abc")), Size: 3},
		"other":   {SHA256: oldtree.BytesSHA256([]byte("abd")), Size: 3},
		"longer":  {SHA256: oldtree.BytesSHA256([]byte("abcdefgh\n")), Size: 9},
		"missing": {Absent: true},
	}}}
	for rel, want := range map[string]string{"ok": "", "other": "does not continue", "longer": "shorter than", "missing": "", "unlisted": ""} {
		a, refusal, err := x.planAppendable(rel, src, dst)
		if err != nil || (want == "" && refusal != "") || !strings.Contains(refusal, want) {
			t.Errorf("%s: %q %v, want %q", rel, refusal, err, want)
			continue
		}
		if rel == "ok" && a.suffix() != 4 {
			t.Errorf("ok: suffix %d", a.suffix())
		}
		if (rel == "missing" || rel == "unlisted") && (!a.base.Absent || a.suffix() != 7) {
			t.Errorf("%s: %+v", rel, a)
		}
	}
	// Applying: the old file at the manifest state gains the suffix; at the post state it is
	// left; at anything else it is refused.
	a, _, _ := x.planAppendable("ok", src, dst)
	writeFile(t, dst, "abc")
	if err := a.apply(nil); err != nil || string(readBytes(t, dst)) != "abcdef\n" {
		t.Fatalf("append: %v %q", err, readBytes(t, dst))
	}
	if err := a.apply(nil); err != nil || string(readBytes(t, dst)) != "abcdef\n" {
		t.Fatalf("again: %v %q", err, readBytes(t, dst))
	}
	writeFile(t, dst, "abX")
	if err := a.apply(nil); err == nil || string(readBytes(t, dst)) != "abX" {
		t.Fatalf("a file in neither state: %v %q", err, readBytes(t, dst))
	}
}
