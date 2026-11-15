// Package legacyimport is `cerberus migrate-from` (docs/substrate-port-plan.md, section 5): it
// reads a snapshot of the old build's ~/.cerberus and a .backup of its main.db, and writes the
// new build's data directory in one run, refusing a non-empty target, never reading the live
// tree and never writing to the source. Every row of the old tree's inventory is migrated
// (copied, or transformed into the class A and B stores), listed as staying in place, or
// refused; the report says which, with digests and counts before and after.
//
// The run is preflight, build, commit:
//   - preflight checks everything it can before writing anything (refuse.go); a refused run
//     leaves no file and no keychain item behind;
//   - build writes <data>.partial (and the config dir's items under a staging directory) and
//     verifies every row there;
//   - commit writes the new anchor, renames the staged config items and <data>.partial into
//     place, checks both anchors and that the source is byte for byte what it was, and undoes
//     the commit if any of that fails.
package legacyimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/legacyimport/manifest"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// ErrRefused is a run that refused before writing anything; the report names why.
var ErrRefused = errors.New("migrate-from refused; nothing was written")

// Options are one run's inputs. Paths are absolute.
type Options struct {
	// OldHome is the snapshot of the old ~/.cerberus (cp -a, taken with old in standby).
	OldHome string
	// OldDB is the .backup of the old main.db.
	OldDB string
	// OldAnchorAccount is the keychain account the old chain's anchor is under: the old audit
	// directory's path, $HOME/.cerberus/audit on the live host (the snapshot is a copy, so its
	// own path is not the account).
	OldAnchorAccount string
	// DataDir is the new data directory (datadir.Dirs.Data); DBPath database.sqlite.path, which
	// must lie inside it; ConfigDir the new config dir (policy/, connector-secrets.yaml).
	DataDir   string
	DBPath    string
	ConfigDir string
	// Home is the user's home directory; OldRoots the old build's homes (datadir.OldRoots), which
	// neither the source nor the target may be in.
	Home     string
	OldRoots []string
	// Manifest, when set, is the D3 manifest the snapshot and backup must match; it is copied to
	// <data>/migration/d3-manifest.json.
	Manifest string
	Now      func() time.Time
	// TestHookAfterBuild, when set, runs after the build is verified and before the commit (a
	// test changes the source there to prove the final check undoes the commit).
	TestHookAfterBuild func()
}

// migration is one run's state.
type migration struct {
	o Options
	r *Report

	partial  string // <data>.partial
	cfgStage string // <config dir>/.migrate-from.partial
	stagedDB string

	oldAnchorRaw  string
	oldHead       audit.Head
	newAnchorSame bool

	applied   appliedFile
	pause     []pauseMarker
	receipts  receipts
	fragment  []byte
	resolved  *registry.ResolvedConfig
	sourceFP  Fingerprint
	createdCD bool
	// createdPartial and createdStage: discard removes only what this run created.
	createdPartial, createdStage bool

	// commit state, for the undo.
	wroteAnchor  bool
	movedPolicy  bool
	movedSecrets bool
	movedData    bool
	removedEmpty bool
}

// Run migrates. The report is returned in every case; the error is ErrRefused for a refusal
// (nothing written) or what failed (nothing left behind: the staged directory and any committed
// step are undone).
func Run(ctx context.Context, o Options) (*Report, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	r := &Report{Version: ReportVersion, Started: now().UTC(), OldHome: o.OldHome, OldDB: o.OldDB, OldAnchorAccount: o.OldAnchorAccount,
		DataDir: o.DataDir, DatabasePath: o.DBPath, ConfigDir: o.ConfigDir, Stores: []StoreRow{}, Rulings: rulings(),
		Warnings: []string{}, Refusals: []string{}}
	m := &migration{o: o, r: r, partial: o.DataDir + ".partial", cfgStage: filepath.Join(o.ConfigDir, ".migrate-from.partial")}
	defer audit.SetAnchor("", nil)

	finish := func(result string, err error) (*Report, error) {
		r.Result = result
		if err != nil {
			r.Error = err.Error()
		}
		r.Finished = now().UTC()
		return r, err
	}
	if err := m.preflight(ctx); err != nil {
		return finish(ResultFailed, err)
	}
	if len(r.Refusals) > 0 {
		return finish(ResultRefused, ErrRefused)
	}
	fp, err := TakeFingerprint(o.OldHome, o.OldDB)
	if err != nil {
		return finish(ResultFailed, err)
	}
	m.sourceFP = fp
	r.SourceDigestBefore, r.SourceEntries = fp.Digest, fp.Entries

	if err = m.build(ctx); err != nil {
		m.discard()
		return finish(ResultFailed, err)
	}
	if o.TestHookAfterBuild != nil {
		o.TestHookAfterBuild()
	}
	if err = m.commit(); err != nil {
		m.undo()
		return finish(ResultFailed, err)
	}
	if err = m.after(); err != nil {
		m.undo()
		return finish(ResultFailed, err)
	}
	r.Result = ResultMigrated
	r.Finished = now().UTC()
	var buf bytes.Buffer
	if err = r.WriteJSON(&buf); err == nil {
		err = writePrivate(filepath.Join(o.DataDir, "migration", "report.json"), buf.Bytes())
	}
	if err != nil {
		r.warn("the report could not be saved in the data directory: %v", err)
	}
	return r, nil
}

func (m *migration) row(s StoreRow) { m.r.Stores = append(m.r.Stores, s) }

// build writes and verifies everything under the staged directories.
func (m *migration) build(ctx context.Context) error {
	o, r := m.o, m.r
	if err := os.MkdirAll(filepath.Dir(o.DataDir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(m.partial, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", m.partial, err)
	}
	m.createdPartial = true
	if err := os.Chmod(m.partial, 0o700); err != nil { //nolint:gosec // a directory: owner-only
		return err
	}
	if !exists(o.ConfigDir) {
		if err := os.MkdirAll(o.ConfigDir, 0o700); err != nil {
			return err
		}
		m.createdCD = true
	}
	if err := os.Mkdir(m.cfgStage, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", m.cfgStage, err)
	}
	m.createdStage = true
	rel := func(p string) string {
		return filepath.Join("<data>", strings.TrimPrefix(p, m.partial+string(filepath.Separator)))
	}

	// R4: the audit chain and its digest key, byte for byte.
	auditSrc, auditDst := filepath.Join(o.OldHome, "audit"), filepath.Join(m.partial, "audit")
	files, err := copyTree(auditSrc, auditDst, func(rel string, d fs.DirEntry) bool {
		chain := filepath.Dir(rel) == "." && strings.HasSuffix(rel, ".jsonl") && d.Type().IsRegular()
		return rel != ".digest_key" && !chain
	})
	if err != nil {
		return fmt.Errorf("copy the audit chain: %w", err)
	}
	copied, err := treeDigests(auditDst)
	if err != nil {
		return err
	}
	if diff := sameDigests(files, copied); diff != "" {
		return fmt.Errorf("audit copy: %s", diff)
	}
	ar := &AuditReport{Files: files, Head: []byte(m.oldAnchorRaw), NewAnchorAccount: filepath.Join(o.DataDir, "audit")}
	r.Audit = ar
	srcKey, _ := fileSHA256(filepath.Join(auditSrc, ".digest_key"))
	dstKey, _ := fileSHA256(filepath.Join(auditDst, ".digest_key"))
	ar.DigestKeySHA256Equal = srcKey != "" && srcKey == dstKey
	if !ar.DigestKeySHA256Equal {
		return errors.New("audit/.digest_key: the copy's sha256 is not the source's")
	}
	checked, verdict := checkChain(auditDst, memoryAnchor{Head: m.oldHead})
	ar.CopyVerify, ar.Records = verdict, len(checked.Records)
	if verdict != "ok" {
		return fmt.Errorf("the copied audit chain does not verify against the old anchor's head: %s", verdict)
	}
	digester, err := audit.OpenExistingDigester(auditDst)
	if err != nil {
		return err
	}
	ar.KeyFingerprint = digester.Fingerprint()
	var chainSum string
	for _, f := range files {
		if f.Path != ".digest_key" {
			chainSum += f.SHA256 + " "
		}
	}
	m.row(StoreRow{Row: "R4", Store: "audit/*.jsonl, audit/.digest_key", Treatment: "copy", Dest: rel(auditDst), SourceSHA256: strings.TrimSpace(chainSum),
		CountBefore: i64(len(checked.Records)), CountAfter: i64(len(checked.Records)), DestSHA256: strings.TrimSpace(chainSum), Verify: "audit.Check ok on source (old anchor) and copy (old head); key sha256 equal"})

	// R6: the approvals store, byte for byte; verified on the source by reading only.
	if err = m.approvals(digester); err != nil {
		return err
	}

	// R8: host state and overview snapshots.
	for _, f := range []string{"hosts.json", "overview_snapshots.json"} {
		src := filepath.Join(o.OldHome, "state", f)
		if !exists(src) {
			r.Listed.Absent = append(r.Listed.Absent, "state/"+f)
			m.row(StoreRow{Row: "R8", Store: "state/" + f, Treatment: "copy", Verify: "absent in the snapshot"})
			continue
		}
		dst := filepath.Join(m.partial, "state", f)
		sum, cerr := copyFile(src, dst)
		if cerr != nil {
			return cerr
		}
		m.row(StoreRow{Row: "R8", Store: "state/" + f, Treatment: "copy", Dest: rel(dst), SourceSHA256: sum.SHA256, DestSHA256: sum.SHA256, Verify: "sha256 equal"})
	}
	// hosts.history.jsonl and its rotations stay in place in the adopted root (listed by listTree).

	// The database: R1 host tables (Telemetry) and the class A state (Local).
	if err = m.database(ctx, digester); err != nil {
		return err
	}

	// R12: policy into the config dir (staged), its hash and load status equal on both sides.
	if err = m.policy(); err != nil {
		return err
	}

	// R16: connector-secrets.yaml at 0600. Its contents are never read or printed.
	if src := filepath.Join(o.OldHome, "connector-secrets.yaml"); exists(src) {
		sum, cerr := copyFile(src, filepath.Join(m.cfgStage, "connector-secrets.yaml"))
		if cerr != nil {
			return cerr
		}
		m.row(StoreRow{Row: "R16", Store: "connector-secrets.yaml", Treatment: "copy", Dest: filepath.Join(o.ConfigDir, "connector-secrets.yaml"),
			SourceSHA256: sum.SHA256, DestSHA256: sum.SHA256, Verify: "sha256 equal; mode 0600"})
	} else {
		r.Listed.Absent = append(r.Listed.Absent, "connector-secrets.yaml")
	}

	// R21: brakes (state and problems equal; events never rewritten), the export outbox, and
	// oauth/ and alerts/, carried inert (nothing in the new build reads them yet).
	if err = m.brakes(); err != nil {
		return err
	}
	for _, dir := range []string{"events", "oauth", "alerts"} {
		src := filepath.Join(o.OldHome, dir)
		if !exists(src) {
			r.Listed.Absent = append(r.Listed.Absent, dir+"/")
			continue
		}
		dst := filepath.Join(m.partial, dir)
		files, cerr := copyTree(src, dst, nil)
		if cerr != nil {
			return cerr
		}
		got, _ := treeDigests(dst)
		if diff := sameDigests(files, got); diff != "" {
			return fmt.Errorf("%s copy: %s", dir, diff)
		}
		m.row(StoreRow{Row: "R21", Store: dir + "/", Treatment: "copy", Dest: rel(dst), CountBefore: i64(len(files)), CountAfter: i64(len(got)), Verify: "sha256 equal per file"})
	}

	// R13: the config fragment.
	if m.fragment != nil {
		if cerr := writePrivate(filepath.Join(m.partial, fragmentRel), m.fragment); cerr != nil {
			return cerr
		}
		r.Config.Output = fragmentOutput
		r.Config.Fragment = filepath.Join(o.DataDir, fragmentRel)
		m.row(StoreRow{Row: "R13", Store: "config.yaml, events.yaml, connector-config.yaml", Treatment: "transform", Dest: filepath.Join("<data>", fragmentRel),
			DestSHA256: r.Config.FragmentSHA256, Verify: "parses as the cerberus block (settings.Parse)"})
	}

	// The D3 manifest, kept with the migration.
	if r.Manifest != nil {
		dst := filepath.Join(m.partial, filepath.FromSlash(manifest.KeptRel))
		if _, cerr := copyFile(o.Manifest, dst); cerr != nil {
			return cerr
		}
		r.Manifest.CopiedTo = filepath.Join(o.DataDir, filepath.FromSlash(manifest.KeptRel))
	}
	return nil
}

// approvals is R6.
func (m *migration) approvals(digester audit.Digester) error {
	o, r := m.o, m.r
	src := filepath.Join(o.OldHome, "approvals", approval.FileName)
	if !exists(src) {
		r.Listed.Absent = append(r.Listed.Absent, "approvals/"+approval.FileName)
		return nil
	}
	dstDir := filepath.Join(m.partial, "approvals")
	if err := os.Mkdir(dstDir, 0o700); err != nil {
		return err
	}
	srcBatch, err := approval.ReadFrom(filepath.Dir(src), approval.Cursor{}, 0)
	if err != nil || srcBatch.Stop != nil {
		if srcBatch.Stop != nil {
			err = errors.Join(err, srcBatch.Stop)
		}
		return fmt.Errorf("approvals: the source no longer reads whole: %w", err)
	}
	sum, err := copyFile(src, filepath.Join(dstDir, approval.FileName))
	if err != nil {
		return err
	}
	dstBatch, err := approval.ReadFrom(dstDir, approval.Cursor{}, 0)
	if err != nil || dstBatch.Stop != nil || len(dstBatch.Events) != len(srcBatch.Events) || dstBatch.Next != srcBatch.Next {
		return fmt.Errorf("approvals: the copy does not read as the source does (%d events, source %d)", len(dstBatch.Events), len(srcBatch.Events))
	}
	data, err := os.ReadFile(filepath.Join(dstDir, approval.FileName)) //nolint:gosec // the staged copy
	if err != nil {
		return err
	}
	// The owner-session seals are checked on the copy only, after it was hashed: VerifyApprovals
	// opens the store, which may write. The copy must hash the same afterwards.
	report, err := gate.VerifyApprovals(dstDir, digester)
	if err != nil {
		return fmt.Errorf("approvals: verify the copy: %w", err)
	}
	after, err := fileSHA256(filepath.Join(dstDir, approval.FileName))
	if err != nil {
		return err
	}
	if after != sum.SHA256 {
		return errors.New("approvals: verifying the copy changed it; the source store's chain is not intact")
	}
	ap := &ApprovalsReport{Events: len(srcBatch.Events), Lines: bytes.Count(data, []byte("\n")), NextSeq: srcBatch.Next.Seq, Damaged: len(srcBatch.Damaged),
		Legacy: append([]string{}, report.Legacy...), Verified: len(report.Verified), Failed: len(report.Failed)}
	r.Approvals = ap
	if err = report.Err(); err != nil {
		return fmt.Errorf("approvals: %w", err)
	}
	m.row(StoreRow{Row: "R6", Store: "approvals/events.jsonl", Treatment: "copy", Dest: "<data>/approvals/events.jsonl", SourceSHA256: sum.SHA256,
		CountBefore: i64(len(srcBatch.Events)), CountAfter: i64(len(dstBatch.Events)), DestSHA256: after,
		Verify: fmt.Sprintf("ReadFrom on source and copy (next seq %d); VerifyApprovals on the copy: %d legacy, %d verified, 0 failed", srcBatch.Next.Seq, len(report.Legacy), len(report.Verified))})
	return nil
}

// database is R1, R9, R10 and R11: one SQLite file, created and migrated as the database module
// does, the host tables in the Telemetry store, the rest in the Local store.
func (m *migration) database(ctx context.Context, digester audit.Digester) error {
	o, r := m.o, m.r
	if err := os.MkdirAll(filepath.Dir(m.stagedDB), 0o700); err != nil {
		return err
	}
	if err := datadir.PrivateFile(m.stagedDB); err != nil {
		return err
	}
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: m.stagedDB}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create the database: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = st.Close()
		}
	}()
	fsys, root := storage.Migrations()
	if err = migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		return fmt.Errorf("migrate the new database: %w", err)
	}
	stores := storage.Stores{Local: st, Telemetry: st}

	// R1.
	if err = importHostTables(ctx, stores.Telemetry, o.OldDB, r.MainDB.SQLiteSequence.Old); err != nil {
		return err
	}

	// R9: the applied digests and the key's fingerprint, in one transaction.
	ap := &AppliedReport{Changed: []string{}, KeyFingerprint: digester.Fingerprint()}
	r.Applied = ap
	if m.applied.Present {
		ap.AppliedAtSource = m.applied.MTime.UTC()
		if err = storage.ImportApplied(ctx, stores.Local, m.applied.Digests, digester.Fingerprint(), m.applied.MTime); err != nil {
			return fmt.Errorf("import the applied digests: %w", err)
		}
		got, aerr := storage.AppliedAll(ctx, stores.Local)
		if aerr != nil {
			return aerr
		}
		if !reflect.DeepEqual(got, m.applied.Digests) {
			return errors.New("applied digests: the store does not hold exactly the file's digests")
		}
		fp, ok, kerr := storage.AppliedKey(ctx, stores.Local)
		if kerr != nil || !ok || fp != digester.Fingerprint() {
			return errors.Join(errors.New("applied digests: the key fingerprint was not recorded with them"), kerr)
		}
		ap.Count = len(got)
		seen := map[string]bool{}
		if m.resolved != nil {
			for _, res := range m.resolved.Config.Resources {
				seen[res.ID] = true
				d, ok := got[res.ID]
				switch {
				case !ok:
					ap.Unrecorded = append(ap.Unrecorded, res.ID)
				case digester.Digest(res) != d:
					ap.Changed = append(ap.Changed, res.ID)
				}
			}
		}
		for id := range got {
			if !seen[id] {
				ap.NoResource = append(ap.NoResource, id)
			}
		}
		sort.Strings(ap.NoResource)
		if len(ap.Changed) > 0 {
			r.warn("applied digests: %v differ from their definition as the registry resolves it now; the monitor will not restart them until they are applied", ap.Changed)
		}
		m.row(StoreRow{Row: "R9", Store: "runtime/applied.json", Treatment: "transform", Dest: "<data>/" + filepath.Base(m.stagedDB) + " applied_definitions, applied_digest_key",
			SourceSHA256: m.applied.SHA256, CountBefore: i64(len(m.applied.Digests)), CountAfter: i64(len(got)),
			Verify: fmt.Sprintf("digest for digest equal; key fingerprint recorded; changed %v", ap.Changed)})
	}

	// R10.
	pr := &PauseReport{Targets: []string{}, NoResource: []string{}}
	r.Pause = pr
	for _, p := range m.pause {
		if err = storage.SetPaused(ctx, stores.Local, p.Target, true, p.MTime); err != nil {
			return fmt.Errorf("pause %s: %w", p.Target, err)
		}
		pr.Targets = append(pr.Targets, p.Target)
		if p.Target != storage.AllTargets && !resourceKnown(m.resolved, p.Target) {
			pr.NoResource = append(pr.NoResource, p.Target)
		}
	}
	gotPaused, err := storage.PausedTargets(ctx, stores.Local)
	if err != nil {
		return err
	}
	if len(gotPaused) != len(pr.Targets) || fmt.Sprint(gotPaused) != fmt.Sprint(pr.Targets) {
		return fmt.Errorf("pause state %v is not the markers' set %v", gotPaused, pr.Targets)
	}
	if len(pr.NoResource) > 0 {
		r.warn("pause targets name no resource the registry lists: %v (carried; the owner decides)", pr.NoResource)
	}
	m.row(StoreRow{Row: "R10", Store: "paused, paused.<id>", Treatment: "transform", Dest: "<data>/" + filepath.Base(m.stagedDB) + " pause_state",
		CountBefore: i64(len(m.pause)), CountAfter: i64(len(gotPaused)), Verify: "set equal"})

	// R11.
	if err = importReceipts(ctx, stores.Local, m.receipts); err != nil {
		return err
	}
	rows, err := verifyReceipts(ctx, stores.Local, m.receipts)
	if err != nil {
		return err
	}
	r.Receipts = &ReceiptsReport{Issuances: m.receipts.IDs, Files: len(m.receipts.ByFile), Rows: rows, ReservationsWithoutReceipt: append([]string{}, m.receipts.Empty...)}
	if len(m.receipts.Empty) > 0 {
		r.warn("credential-issuance reservations without a receipt (left in place): %v", m.receipts.Empty)
	}
	m.row(StoreRow{Row: "R11", Store: "credential-issuance/<id>/<state>.json", Treatment: "transform", Dest: "<data>/" + filepath.Base(m.stagedDB) + " credential_issuance_receipts",
		CountBefore: i64(len(m.receipts.ByFile)), CountAfter: i64(rows), Verify: "row count equals file count; every row equals its file"})

	if err = verifyHostTables(ctx, m.stagedDB, o.OldDB, r.MainDB); err != nil {
		return err
	}
	// One clean database file: everything checkpointed into it and the WAL truncated before the
	// store closes, so the data directory renamed into place carries no -wal holding committed
	// rows. Checked afterwards by reading the file alone (immutable ignores any WAL).
	if err = checkpoint(ctx, st); err != nil {
		return err
	}
	closed = true
	if err = st.Close(); err != nil {
		return err
	}
	if err = cleanDatabaseFile(ctx, m.stagedDB, r.MainDB.HostDumpSHA256.New); err != nil {
		return err
	}
	var before, after int64
	for _, t := range r.MainDB.Tables {
		before += t.Before
		after += t.After
	}
	m.row(StoreRow{Row: "R1", Store: "main.db host_* (5 tables) + sqlite_sequence", Treatment: "transform", Dest: "<data>/" + filepath.Base(m.stagedDB) + " (Telemetry)",
		SourceSHA256: r.MainDB.HostDumpSHA256.Old, CountBefore: &before, CountAfter: &after, DestSHA256: r.MainDB.HostDumpSHA256.New,
		Verify: "integrity_check ok; counts equal; EXCEPT both ways 0; ordered digest equal; sqlite_sequence kept"})
	var empty int64
	for _, n := range r.MainDB.AssertedEmpty {
		empty += n
	}
	m.row(StoreRow{Row: "R2", Store: "main.db projects, resources, cerberus_schedule_*, gosched_*", Treatment: "assert empty", CountBefore: &empty, Verify: "0 rows; regenerated by the new build"})
	m.row(StoreRow{Row: "R3", Store: "main.db schema_migrations", Treatment: "regenerate", Verify: fmt.Sprintf("old set %v accepted; goose migrations applied", r.MainDB.SchemaVersions)})
	return nil
}

func resourceKnown(resolved *registry.ResolvedConfig, id string) bool {
	if resolved == nil {
		return false
	}
	for _, res := range resolved.Config.Resources {
		if res.ID == id {
			return true
		}
	}
	return false
}

// policy is R12.
func (m *migration) policy() error {
	o, r := m.o, m.r
	src := filepath.Join(o.OldHome, "policy")
	if !exists(src) {
		r.Listed.Absent = append(r.Listed.Absent, "policy/")
		return nil
	}
	dst := filepath.Join(m.cfgStage, "policy")
	files, err := copyTree(src, dst, func(rel string, _ fs.DirEntry) bool {
		return strings.HasPrefix(filepath.Base(rel), "posture.yaml.bak-")
	})
	if err != nil {
		return fmt.Errorf("copy policy: %w", err)
	}
	got, err := treeDigests(dst)
	if err != nil {
		return err
	}
	if diff := sameDigests(files, got); diff != "" {
		return fmt.Errorf("policy copy: %s", diff)
	}
	pr := &PolicyReport{Files: files}
	r.Policy = pr
	srcOK, dstOK := appliedHashOK(src), appliedHashOK(dst)
	pr.AppliedHashOK = srcOK && dstOK
	if srcOK != dstOK {
		return errors.New("policy: applied.sha256 checks against applied.yaml on one side only")
	}
	if !srcOK && exists(filepath.Join(src, "applied.yaml")) {
		r.warn("policy/applied.sha256 does not match applied.yaml in the snapshot; the copy carries the same mismatch")
	}
	audit.SetAnchor("", nil)
	_, srcStatus := policy.Store{Dir: src, AuditDir: filepath.Join(o.OldHome, "audit")}.LoadVerified()
	_, dstStatus := policy.Store{Dir: dst, AuditDir: filepath.Join(m.partial, "audit")}.LoadVerified()
	pr.SourceStatus, pr.DestStatus = statusString(srcStatus), statusString(dstStatus)
	if srcStatus != dstStatus {
		return fmt.Errorf("policy: the copy loads as %q, the source as %q", pr.DestStatus, pr.SourceStatus)
	}
	m.row(StoreRow{Row: "R12", Store: "policy/", Treatment: "copy", Dest: filepath.Join(o.ConfigDir, "policy"), CountBefore: i64(len(files)), CountAfter: i64(len(got)),
		Verify: "sha256 per file; applied.sha256 checked on both; LoadVerified status equal (" + pr.SourceStatus + ")"})
	return nil
}

func appliedHashOK(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "applied.yaml")) //nolint:gosec // the policy directory
	if err != nil {
		return false
	}
	want, err := os.ReadFile(filepath.Join(dir, "applied.sha256")) //nolint:gosec // as above
	return err == nil && strings.TrimSpace(string(want)) == policy.Hash(data)
}

func statusString(s policy.LoadStatus) string {
	out := s.Snapshot
	if s.Problem != "" {
		out += ": " + s.Problem
	}
	return out
}

// brakes is R21's brakes/.
func (m *migration) brakes() error {
	o, r := m.o, m.r
	src := filepath.Join(o.OldHome, "brakes")
	if !exists(src) {
		r.Listed.Absent = append(r.Listed.Absent, "brakes/")
		return nil
	}
	dst := filepath.Join(m.partial, "brakes")
	files, err := copyTree(src, dst, func(rel string, _ fs.DirEntry) bool { return strings.HasSuffix(rel, ".lock") })
	if err != nil {
		return err
	}
	got, _ := treeDigests(dst)
	if diff := sameDigests(files, got); diff != "" {
		return fmt.Errorf("brakes copy: %s", diff)
	}
	srcState, srcProblems := brake.Store{Dir: src}.Load()
	dstState, dstProblems := brake.Store{Dir: dst}.Load()
	if !reflect.DeepEqual(srcState, dstState) || fmt.Sprint(srcProblems) != fmt.Sprint(dstProblems) {
		return errors.New("brakes: the copy folds to another state than the source")
	}
	r.Brakes = &BrakesReport{Engaged: srcState.Engaged(), Freezes: len(srcState.Freezes), Problems: append([]string{}, srcProblems...)}
	m.row(StoreRow{Row: "R21", Store: "brakes/", Treatment: "copy", Dest: "<data>/brakes", CountBefore: i64(len(files)), CountAfter: i64(len(got)),
		Verify: "brake.Store.Load state and problems equal (legacy v1 resets applied as before)"})
	return nil
}

// commit puts the staged result in place: the new anchor, the config dir's items, the data dir.
func (m *migration) commit() error {
	o := m.o
	account := filepath.Join(o.DataDir, "audit")
	if !m.newAnchorSame {
		if err := secrets.KeyringSet(auditchain.AnchorService, account, m.oldAnchorRaw); err != nil {
			return fmt.Errorf("write the audit anchor for %s: %w", account, err)
		}
		m.wroteAnchor = true
	}
	m.r.Audit.NewAnchorWritten = m.wroteAnchor
	if exists(filepath.Join(m.cfgStage, "policy")) {
		if err := os.Rename(filepath.Join(m.cfgStage, "policy"), filepath.Join(o.ConfigDir, "policy")); err != nil {
			return err
		}
		m.movedPolicy = true
	}
	if exists(filepath.Join(m.cfgStage, "connector-secrets.yaml")) {
		if err := os.Rename(filepath.Join(m.cfgStage, "connector-secrets.yaml"), filepath.Join(o.ConfigDir, "connector-secrets.yaml")); err != nil {
			return err
		}
		m.movedSecrets = true
	}
	if exists(o.DataDir) {
		// Preflight allowed only an empty directory here.
		if err := os.Remove(o.DataDir); err != nil {
			return fmt.Errorf("the data directory %s is no longer empty: %w", o.DataDir, err)
		}
		m.removedEmpty = true
	}
	if err := os.Rename(m.partial, o.DataDir); err != nil {
		return err
	}
	m.movedData = true
	_ = os.Remove(m.cfgStage)
	return nil
}

// after checks the committed result: both anchors, and the source untouched.
func (m *migration) after() error {
	o, r := m.o, m.r
	newDir := filepath.Join(o.DataDir, "audit")
	_, r.Audit.NewAnchorVerify = checkChain(newDir, auditchain.KeychainAnchor{Dir: newDir})
	_, r.Audit.OldAnchorVerify = checkChain(filepath.Join(o.OldHome, "audit"), auditchain.KeychainAnchor{Dir: o.OldAnchorAccount})
	if r.Audit.NewAnchorVerify != "ok" {
		return fmt.Errorf("the migrated chain does not verify against its new anchor: %s", r.Audit.NewAnchorVerify)
	}
	if r.Audit.OldAnchorVerify != "ok" {
		return fmt.Errorf("the snapshot's chain no longer verifies against the old anchor: %s", r.Audit.OldAnchorVerify)
	}
	fp, err := TakeFingerprint(o.OldHome, o.OldDB)
	if err != nil {
		return err
	}
	r.SourceDigestAfter = fp.Digest
	r.SourceUnchanged = fp == m.sourceFP
	if !r.SourceUnchanged {
		return errors.New("the source changed during the run (snapshot or backup); the result is discarded")
	}
	return nil
}

// undo reverses whatever commit did, then discards the staged result.
func (m *migration) undo() {
	o := m.o
	if m.movedData {
		_ = os.Rename(o.DataDir, m.partial)
		m.movedData = false
	}
	if m.removedEmpty {
		_ = os.Mkdir(o.DataDir, 0o700)
	}
	_ = os.MkdirAll(m.cfgStage, 0o700)
	if m.movedSecrets {
		_ = os.Rename(filepath.Join(o.ConfigDir, "connector-secrets.yaml"), filepath.Join(m.cfgStage, "connector-secrets.yaml"))
	}
	if m.movedPolicy {
		_ = os.Rename(filepath.Join(o.ConfigDir, "policy"), filepath.Join(m.cfgStage, "policy"))
	}
	if m.wroteAnchor {
		_ = secrets.KeyringDelete(auditchain.AnchorService, filepath.Join(o.DataDir, "audit"))
	}
	m.discard()
}

// discard removes the staged directories (never anything else).
func (m *migration) discard() {
	if m.createdPartial {
		_ = os.RemoveAll(m.partial)
	}
	if m.createdStage {
		_ = os.RemoveAll(m.cfgStage)
	}
	if m.createdCD {
		_ = os.Remove(m.o.ConfigDir) // only when empty
	}
}
