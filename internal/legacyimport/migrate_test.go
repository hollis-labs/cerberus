package legacyimport_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/legacyimport"
	"github.com/hollis-labs/cerberus/internal/legacyimport/hostdump"
	"github.com/hollis-labs/cerberus/internal/legacyimport/legacyimporttest"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) {
	// The anchor lives in the OS credential store: mocked, never the operator's.
	secrets.MockStoreForTests()
	testhome.Main(m)
}

func options(f legacyimporttest.Fixture) legacyimport.Options {
	return legacyimport.Options{
		OldHome: f.OldHome, OldDB: f.OldDB, OldAnchorAccount: f.Account,
		DataDir: f.DataDir, DBPath: f.DBPath, ConfigDir: f.ConfigDir, Home: f.Home,
		OldRoots: []string{filepath.Join(f.Home, ".cerberus"), filepath.Join(f.Home, ".local", "share", "cerberus")},
	}
}

func migrate(t *testing.T) (legacyimporttest.Fixture, *legacyimport.Report) {
	t.Helper()
	f := legacyimporttest.Materialize(t, t.TempDir())
	rep, err := legacyimport.Run(context.Background(), options(f))
	if err != nil {
		var buf bytes.Buffer
		_ = rep.WriteText(&buf)
		t.Fatalf("migrate-from: %v\n%s", err, buf.String())
	}
	return f, rep
}

// openStore opens the migrated database as the database module will.
func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Treatment coverage: the whole fixture migrates, every row is accounted for, both chains verify,
// and the source is byte for byte what it was. The key fields are checked against the fixture.
func TestTheFixtureMigrates(t *testing.T) {
	f, rep := migrate(t)
	if rep.Result != legacyimport.ResultMigrated || !rep.SourceUnchanged || rep.SourceDigestBefore == "" || rep.SourceDigestBefore != rep.SourceDigestAfter {
		t.Fatalf("result %s, source_unchanged %v (%s -> %s)", rep.Result, rep.SourceUnchanged, rep.SourceDigestBefore, rep.SourceDigestAfter)
	}
	if len(rep.Refusals) != 0 {
		t.Fatalf("refusals: %v", rep.Refusals)
	}
	rows := map[string]int{}
	for _, s := range rep.Stores {
		rows[s.Row]++
		if s.CountBefore != nil && s.CountAfter != nil && *s.CountBefore != *s.CountAfter {
			t.Errorf("%s %s: %d before, %d after", s.Row, s.Store, *s.CountBefore, *s.CountAfter)
		}
	}
	for _, want := range []string{"R1", "R2", "R3", "R4", "R6", "R8", "R9", "R10", "R11", "R12", "R13", "R16", "R21"} {
		if rows[want] == 0 {
			t.Errorf("no store row for %s", want)
		}
	}

	// The golden-ish fields: what the fixture holds.
	db := rep.MainDB
	if db.IntegrityCheck != "ok" || !reflect.DeepEqual(db.SchemaVersions, []int{1, 2, 3, 5, 6, 7, 8}) {
		t.Errorf("main_db: integrity %q, versions %v", db.IntegrityCheck, db.SchemaVersions)
	}
	wantCounts := map[string]int64{"host_samples": 120, "host_status": 2, "host_sample_rollups": 78, "host_rollup_state": 4, "host_events": 3}
	for name, n := range wantCounts {
		tr := db.Tables[name]
		if tr.Before != n || tr.After != n || tr.ExceptOldNew != 0 || tr.ExceptNewOld != 0 {
			t.Errorf("%s: %+v, want %d rows and no difference", name, tr, n)
		}
	}
	if db.SQLiteSequence.Old["host_events"] != 5 || db.SQLiteSequence.New["host_events"] != 5 || db.MaxID == nil || *db.MaxID != 3 {
		t.Errorf("sqlite_sequence %v -> %v, max id %v", db.SQLiteSequence.Old, db.SQLiteSequence.New, db.MaxID)
	}
	if db.HostDumpSHA256.Old == "" || db.HostDumpSHA256.Old != db.HostDumpSHA256.New {
		t.Errorf("host dump %s -> %s", db.HostDumpSHA256.Old, db.HostDumpSHA256.New)
	}
	a := rep.Audit
	if a.Records != 10 || a.OldAnchorVerify != "ok" || a.CopyVerify != "ok" || a.NewAnchorVerify != "ok" || !a.DigestKeySHA256Equal || !a.NewAnchorWritten ||
		a.NewAnchorAccount != filepath.Join(f.DataDir, "audit") || string(a.Head) != f.AnchorRaw {
		t.Errorf("audit: %+v", a)
	}
	if ap := rep.Approvals; ap.Events != 5 || ap.Lines != 5 || len(ap.Legacy) != 1 || ap.Verified != 0 || ap.Failed != 0 {
		t.Errorf("approvals: %+v", ap)
	}
	if ap := rep.Applied; ap.Count != 2 || len(ap.Changed) != 0 || len(ap.Unrecorded) != 1 || ap.Unrecorded[0] != "box" || ap.KeyFingerprint == "" {
		t.Errorf("applied: %+v", ap)
	}
	if p := rep.Pause; !reflect.DeepEqual(p.Targets, []string{"*", "ghost.x", "web"}) || !reflect.DeepEqual(p.NoResource, []string{"ghost.x"}) {
		t.Errorf("pause: %+v", p)
	}
	if rc := rep.Receipts; rc.Files != 10 || rc.Rows != 10 || rc.Issuances != 4 || !reflect.DeepEqual(rc.ReservationsWithoutReceipt, []string{strings.Repeat("d", 32)}) {
		t.Errorf("receipts: %+v", rc)
	}
	if c := rep.Config; c.Fragment != filepath.Join(f.DataDir, "migration", "config-fragment.yaml") || !c.EventExportWasEnabled ||
		len(c.Unmapped) != 1 || !strings.HasPrefix(c.Unmapped[0], "web.public_url") || c.ResourcesListed != 0 {
		t.Errorf("config: %+v", c)
	}
	if rep.Registry.Entries != 1 || rep.Registry.Resources != 3 || rep.Hosts.Hosts != 1 {
		t.Errorf("registry %+v, hosts %+v", rep.Registry, rep.Hosts)
	}
	if rep.Brakes == nil || rep.Brakes.Freezes != 1 || len(rep.Brakes.Problems) != 0 {
		t.Errorf("brakes: %+v", rep.Brakes)
	}
	if rep.Policy == nil || !rep.Policy.AppliedHashOK || rep.Policy.SourceStatus != rep.Policy.DestStatus || strings.HasPrefix(rep.Policy.SourceStatus, "mismatch") {
		t.Errorf("policy: %+v", rep.Policy)
	}
	listed := map[string]string{}
	for _, group := range [][]legacyimport.Entry{rep.Listed.InPlace, rep.Listed.NotMigrated, rep.Listed.Archive, rep.Listed.Runtime} {
		for _, e := range group {
			listed[e.Path] = e.Row
		}
	}
	for path, row := range map[string]string{"registry.yaml": "R14", "hosts.yaml": "R15", "hosts.history.jsonl": "R8", "approvals/passkeys/keys.json": "R7",
		"pids/web.pid": "R19", "web/login-fixture.key": "R22", "cerberus.log": "R23", "daemon.lock": "R25", "apps": "R18", "plugin-connectors.json": "R18",
		"policy/posture.yaml.bak-20261101": "R12", "credential-issuance/secrets": "R18", "keyring service cerberus": "R17"} {
		if listed[path] != row {
			t.Errorf("listed %s as %q, want %s", path, listed[path], row)
		}
	}
	for _, absent := range []string{"oauth/", "alerts/"} {
		if !contains(rep.Listed.Absent, absent) {
			t.Errorf("%s not reported absent: %v", absent, rep.Listed.Absent)
		}
	}
	if len(rep.Warnings) == 0 {
		t.Error("expected warnings for the pause target with no resource and the empty reservation")
	}

	// What is on disk.
	for p, mode := range map[string]os.FileMode{
		f.DataDir: 0o700, filepath.Join(f.DataDir, "audit"): 0o700, filepath.Join(f.DataDir, "audit", ".digest_key"): 0o600,
		filepath.Join(f.DataDir, "approvals", "events.jsonl"): 0o600, f.DBPath: 0o600, filepath.Join(f.ConfigDir, "connector-secrets.yaml"): 0o600,
		filepath.Join(f.ConfigDir, "policy", "applied.yaml"): 0o600, filepath.Join(f.DataDir, "migration", "config-fragment.yaml"): 0o600,
		filepath.Join(f.DataDir, "migration", "report.json"): 0o600, filepath.Join(f.DataDir, "brakes", "events.jsonl"): 0o600,
	} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want %#o", p, fi, err, mode)
		}
	}
	for _, p := range []string{f.DataDir + ".partial", filepath.Join(f.ConfigDir, ".migrate-from.partial"), filepath.Join(f.ConfigDir, "policy", "posture.yaml.bak-20261101")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind (%v)", p, err)
		}
	}
	frag, err := os.ReadFile(filepath.Join(f.DataDir, "migration", "config-fragment.yaml")) //nolint:gosec // the test's own output
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"host_monitor:", "event_export:", "enabled: false", "grace_seconds: 90", "connectors:", "fixture-echo:"} {
		if !bytes.Contains(frag, []byte(want)) {
			t.Errorf("config fragment lacks %q:\n%s", want, frag)
		}
	}

	// The sample report for the PR, paths made relative to the scratch root.
	if out := os.Getenv("CERBERUS_SAMPLE_REPORT_OUT"); out != "" {
		var buf bytes.Buffer
		if err := rep.WriteJSON(&buf); err != nil {
			t.Fatal(err)
		}
		sample := bytes.ReplaceAll(buf.Bytes(), []byte(f.Root), []byte("<scratch>"))
		if err := os.WriteFile(out, sample, 0o600); err != nil {
			t.Fatal(err)
		}
		var text bytes.Buffer
		_ = rep.WriteText(&text)
		if err := os.WriteFile(strings.TrimSuffix(out, ".json")+".txt", bytes.ReplaceAll(text.Bytes(), []byte(f.Root), []byte("<scratch>")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The sample report under testdata is this fixture's: its key fields match a run now.
func TestTheSampleReportIsTheFixtures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sample-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sample legacyimport.Report
	if err := json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	_, rep := migrate(t)
	if sample.Result != rep.Result || !sample.SourceUnchanged || sample.MainDB.HostDumpSHA256.Old != rep.MainDB.HostDumpSHA256.Old ||
		!reflect.DeepEqual(sample.MainDB.Tables, rep.MainDB.Tables) || !reflect.DeepEqual(sample.Pause, rep.Pause) ||
		sample.Applied.Count != rep.Applied.Count || sample.Audit.Records != rep.Audit.Records || sample.Receipts.Rows != rep.Receipts.Rows {
		t.Fatalf("testdata/sample-report.json is stale; regenerate it with CERBERUS_SAMPLE_REPORT_OUT (TestTheFixtureMigrates)")
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

// The host tables, checked independently of the import's own verify: EXCEPT both ways is empty on
// all five tables, the ids are the old ids, and host_events' sqlite_sequence is the old value,
// which is past max(id) (the newest events were deleted on the old side).
func TestHostTablesAreTheOldRows(t *testing.T) {
	f, _ := migrate(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", hostdump.URI(f.DBPath, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "ATTACH DATABASE ? AS old", hostdump.URI(f.OldDB, "mode=ro&immutable=1")); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range hostdump.Tables {
		cols := tbl.ColumnList()
		for _, q := range []string{
			"SELECT COUNT(*) FROM (SELECT " + cols + " FROM main." + tbl.Name + " EXCEPT SELECT " + cols + " FROM old." + tbl.Name + ")",
			"SELECT COUNT(*) FROM (SELECT " + cols + " FROM old." + tbl.Name + " EXCEPT SELECT " + cols + " FROM main." + tbl.Name + ")",
		} {
			var n int
			if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 0 {
				t.Errorf("%s: %d rows differ (%v)", tbl.Name, n, err)
			}
		}
		var a, b int
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM main."+tbl.Name).Scan(&a)
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM old."+tbl.Name).Scan(&b)
		if a != b || a == 0 {
			t.Errorf("%s: %d rows, old %d", tbl.Name, a, b)
		}
	}
	var ids []string
	rows, err := db.QueryContext(ctx, "SELECT group_concat(id) FROM main.host_events UNION ALL SELECT group_concat(id) FROM old.host_events")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		ids = append(ids, s)
	}
	_ = rows.Close()
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] != "1,2,3" {
		t.Errorf("host_events ids new/old = %v", ids)
	}
	var seqNew, seqOld, maxID int64
	_ = db.QueryRowContext(ctx, "SELECT seq FROM main.sqlite_sequence WHERE name = 'host_events'").Scan(&seqNew)
	_ = db.QueryRowContext(ctx, "SELECT seq FROM old.sqlite_sequence WHERE name = 'host_events'").Scan(&seqOld)
	_ = db.QueryRowContext(ctx, "SELECT MAX(id) FROM main.host_events").Scan(&maxID)
	if seqNew != seqOld || seqNew <= maxID {
		t.Errorf("sqlite_sequence %d (old %d), max(id) %d: the sequence must be the old one, past max(id)", seqNew, seqOld, maxID)
	}

	// The next event the new build writes takes the id after the old sequence, never a reused one.
	st := openStore(t, f.DBPath)
	var next int64
	if err := st.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO host_events (ts, host, kind, from_status, to_status) VALUES (1, 'box-1', 'status', 'ok', 'offline')`)
		if err != nil {
			return err
		}
		next, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if next != seqOld+1 {
		t.Fatalf("the next host event got id %d, want %d", next, seqOld+1)
	}
}

// Applied digests equal per resource, and the result in observe mode reports nothing changed:
// supervise mode's own key check accepts the migrated key, and every definition the registry
// resolves over the snapshot digests to what was imported.
func TestAppliedDigestsAndNothingChanged(t *testing.T) {
	f, _ := migrate(t)
	ctx := context.Background()
	st := openStore(t, f.DBPath)
	raw, err := os.ReadFile(filepath.Join(f.OldHome, "runtime", "applied.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got, err := storage.AppliedAll(ctx, st)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("applied digests %v (%v), want %v", got, err, want)
	}
	digester, err := supervise.OpenAppliedKey(ctx, st, filepath.Join(f.DataDir, "audit"), time.Now())
	if err != nil {
		t.Fatalf("supervise mode refuses the migrated key: %v", err)
	}
	resolved, err := registry.ForRoot(f.OldHome).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	lane, err := supervise.New(supervise.Options{Mode: settings.ModeObserve, Home: f.Home, StateDir: f.DataDir,
		Resources: func() ([]config.ResourceDef, error) { return resolved.Config.Resources, nil }, Runner: noSystemctl{}})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := lane.List(ctx)
	if err != nil || len(statuses) != 3 {
		t.Fatalf("observe lane lists %d resources (%v)", len(statuses), err)
	}
	var changed []string
	for _, res := range resolved.Config.Resources {
		d, ok := got[res.ID]
		if ok && digester.Digest(res) != d {
			changed = append(changed, res.ID)
		}
	}
	if len(changed) > 0 {
		t.Fatalf("resources reported changed after the migration: %v", changed)
	}
	// Paused as the markers said.
	targets, err := storage.PausedTargets(ctx, st)
	if err != nil || !reflect.DeepEqual(targets, []string{"*", "ghost.x", "web"}) {
		t.Fatalf("pause targets %v %v", targets, err)
	}
}

// The audit chain verifies on the snapshot against the old anchor and on the copy against the new
// one, and the copy holds the old records byte for byte.
func TestTheChainVerifiesBeforeAndAfter(t *testing.T) {
	f, _ := migrate(t)
	check := func(dir, account string) audit.Checked {
		audit.SetAnchor(dir, auditchain.KeychainAnchor{Dir: account})
		defer audit.SetAnchor("", nil)
		c, err := audit.Check(dir)
		if err != nil || len(c.Problems) > 0 || !c.TailTrusted() {
			t.Fatalf("%s: %v %v", dir, err, c.Problems)
		}
		return c
	}
	before := check(filepath.Join(f.OldHome, "audit"), f.Account)
	after := check(filepath.Join(f.DataDir, "audit"), filepath.Join(f.DataDir, "audit"))
	if len(before.Records) != len(after.Records) || len(before.Records) == 0 {
		t.Fatalf("%d records before, %d after", len(before.Records), len(after.Records))
	}
	for _, name := range []string{"2026-11.jsonl", ".digest_key"} {
		a, _ := os.ReadFile(filepath.Join(f.OldHome, "audit", name))
		b, _ := os.ReadFile(filepath.Join(f.DataDir, "audit", name))
		if !bytes.Equal(a, b) || len(a) == 0 {
			t.Errorf("audit/%s differs in the copy", name)
		}
	}
	got, err := secrets.KeyringGet(auditchain.AnchorService, filepath.Join(f.DataDir, "audit"))
	if err != nil || got != f.AnchorRaw {
		t.Fatalf("new anchor %q %v, want the old head verbatim", got, err)
	}
	if old, _ := secrets.KeyringGet(auditchain.AnchorService, f.Account); old != f.AnchorRaw {
		t.Fatal("the old anchor entry was changed")
	}
}

// VerifyApprovals never runs on the source: the source approvals directory is byte for byte,
// mode for mode, what it was after a run, and a damaged source chain is refused by the read-only
// check (approval.Open would have appended a reanchor and chmodded the directory).
func TestVerifyApprovalsNeverRunsOnTheSource(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "damaged"}[damaged], func(t *testing.T) {
			f := legacyimporttest.Materialize(t, t.TempDir())
			dir := filepath.Join(f.OldHome, "approvals")
			// 0750 so a chmod to 0700 (approval.Open's) would show.
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			if damaged {
				p := filepath.Join(dir, approval.FileName)
				data, _ := os.ReadFile(p)
				lines := bytes.SplitAfter(data, []byte("\n"))
				lines[0] = bytes.Replace(lines[0], []byte(`"deploy"`), []byte(`"deplox"`), 1)
				if err := os.WriteFile(p, bytes.Join(lines, nil), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := fingerprintDir(t, dir)
			rep, err := legacyimport.Run(context.Background(), options(f))
			if after := fingerprintDir(t, dir); after != before {
				t.Fatalf("the source approvals directory changed:\n%s\n%s", before, after)
			}
			if damaged {
				if !errors.Is(err, legacyimport.ErrRefused) || !anyContains(rep.Refusals, "does not read whole") {
					t.Fatalf("damaged source: %v %v", err, rep.Refusals)
				}
				assertNothingWritten(t, f)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fingerprintDir(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		data := []byte{}
		if fi.Mode().IsRegular() {
			data, _ = os.ReadFile(p) //nolint:gosec // the test's own dir
		}
		b.WriteString(p + " " + fi.Mode().String() + " " + fi.ModTime().String() + " " + string(data) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// assertNothingWritten: no data dir, no staging, no config items, no new anchor.
func assertNothingWritten(t *testing.T, f legacyimporttest.Fixture) {
	t.Helper()
	for _, p := range []string{f.DataDir, f.DataDir + ".partial", filepath.Join(f.ConfigDir, "policy"), filepath.Join(f.ConfigDir, "connector-secrets.yaml"),
		filepath.Join(f.ConfigDir, ".migrate-from.partial")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after a refusal (%v)", p, err)
		}
	}
	if _, err := secrets.KeyringGet(auditchain.AnchorService, filepath.Join(f.DataDir, "audit")); err == nil {
		t.Error("a refusal wrote the new anchor")
	}
}

// noSystemctl answers no unit: the fixture's processes are dev sessions, observed from pid files.
type noSystemctl struct{}

func (noSystemctl) CombinedOutput(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("no systemctl in this test")
}

// With --manifest, the snapshot and backup must match the D3 manifest; the manifest is kept in
// the data dir with its sha256 in the report. A snapshot that differs is refused.
func TestMigrateChecksTheManifest(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "matches", true: "drifted"}[drift], func(t *testing.T) {
			f := legacyimporttest.Materialize(t, t.TempDir())
			o := options(f)
			o.Manifest = filepath.Join(f.Root, "manifest.json")
			if _, err := manifest.Write(context.Background(), manifest.Options{OldHome: f.OldHome, OldDB: f.OldDB, AnchorAccount: f.Account}, o.Manifest); err != nil {
				t.Fatal(err)
			}
			if drift {
				if err := os.WriteFile(filepath.Join(f.OldHome, "paused.api"), []byte("paused\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			rep, err := legacyimport.Run(context.Background(), o)
			if drift {
				if !errors.Is(err, legacyimport.ErrRefused) || rep.Manifest == nil || rep.Manifest.SnapshotMatches || !anyContains(rep.Refusals, "paused.api") {
					t.Fatalf("a drifted snapshot: %v %+v %v", err, rep.Manifest, rep.Refusals)
				}
				assertNothingWritten(t, f)
				return
			}
			if err != nil || !rep.Manifest.SnapshotMatches || rep.Manifest.SHA256 == "" {
				t.Fatalf("run: %v %+v", err, rep.Manifest)
			}
			kept, err := os.ReadFile(filepath.Join(f.DataDir, "migration", "d3-manifest.json"))
			orig, _ := os.ReadFile(o.Manifest)
			if err != nil || !bytes.Equal(kept, orig) {
				t.Fatalf("the manifest was not kept in the data dir: %v", err)
			}
		})
	}
}

// A config.yaml that still lists resources or projects is reported (never carried), and the run
// goes on: the registry's descriptors are what the new build reads.
func TestConfigListingResourcesIsReported(t *testing.T) {
	f := legacyimporttest.Materialize(t, t.TempDir())
	appendFile(t, filepath.Join(f.OldHome, "config.yaml"), "resources:\n  - id: legacy\n    type: process\n    connector: local\nprojects:\n  - id: old\n")
	rep, err := legacyimport.Run(context.Background(), options(f))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Config.ResourcesListed != 1 || rep.Config.ProjectsListed != 1 || !anyContains(rep.Warnings, "still lists 1 resources") {
		t.Fatalf("config: %+v, warnings %v", rep.Config, rep.Warnings)
	}
	frag, _ := os.ReadFile(rep.Config.Fragment)
	if bytes.Contains(frag, []byte("legacy")) {
		t.Fatal("a listed resource was carried into the fragment")
	}
}
