package legacyexport

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/overview"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// activity is what driveNewActivity did, for the assertions.
type activity struct {
	// ConsumedApproval was requested, decided by the owner's console session and consumed;
	// PendingApproval was requested in the next month.
	ConsumedApproval, PendingApproval string
	Issuance                          string
	AppliedID, AppliedDigest          string
	// Paused and Resumed are pause targets set and cleared.
	Paused, Resumed string
}

// The timeline: everything in November, after the fixture's newest host row and rollup watermark,
// then one approval request in December, so the chain rolls over to a month the manifest never
// saw.
var (
	november = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	december = time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC)
)

// tick is a clock that advances a second a call from start.
func tick(start time.Time) func() time.Time {
	var mu sync.Mutex
	at := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(time.Second)
		return at
	}
}

// driveNewActivity writes, through the new build's own code paths, what a window of the new build
// records after the handoff: audit records through the gate, an approval the owner's console
// session decides and the caller consumes, a pause and a resume, an applied digest, host
// samples, a status change, an event and rollups, a credential-issuance receipt through the
// broker's database recorder, a brake engaged, and a record in the next month.
func driveNewActivity(t testing.TB, dataDir, dbPath string) activity {
	t.Helper()
	ctx := context.Background()
	var act activity
	auditDir := filepath.Join(dataDir, "audit")
	audit.SetAnchor(auditDir, auditchain.KeychainAnchor{Dir: auditDir})
	defer audit.SetAnchor("", nil)

	st, err := store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: dbPath}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	// The gate: an out-of-band approval asked by an agent, approved by the owner on the console,
	// consumed by the agent's call. Every transition is an audit record.
	sink, err := audit.OpenFileSinkClock(auditDir, tick(november))
	if err != nil {
		t.Fatal(err)
	}
	broker, err := gate.NewBroker(sink, filepath.Join(dataDir, "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	requester := audit.Principal{Kind: "agent", Surface: "socket", Via: gate.ViaMCPStdio, Client: "claude-code", Session: "s1"}
	intent := audit.Record{Kind: audit.KindIntent, OperationID: audit.NewID(), Principal: requester, Connector: "docker", Operation: "stop",
		Effect: "lifecycle", Target: audit.Target{Kind: "docker.container", Resource: "web", Env: "prod", Owner: "self"}, ArgsDigest: sink.Digest(map[string]any{"container": "web"})}
	a, err := broker.Request(ctx, intent, policy.Result{Decision: policy.Approve}, approval.ChannelOutOfBand, approval.ScopeOnce, time.Hour, gate.PlanSnapshot{Hash: "sha256:plan"})
	if err != nil {
		t.Fatal(err)
	}
	owner := gate.WithPrincipal(gate.BeginRequest(ctx, gate.SurfaceWeb), gate.WebSessionPrincipal("sess_owner"))
	a, err = broker.DecideAs(owner, a.ID, gate.ApprovalDecisionArgs{Approve: true, Reason: "rehearsal"}, gate.OwnerSession{ConsolePerson: true, SessionID: "sess_owner"})
	if err != nil || a.Status != approval.Approved {
		t.Fatalf("the owner's approve: %+v %v", a, err)
	}
	if a, err = broker.Consume(ctx, a.ID, approval.ConsumeCheck{Connector: "docker", Operation: "stop", Principal: requester, Target: intent.Target,
		ArgsDigest: intent.ArgsDigest, PlanHash: "sha256:plan", OperationID: intent.OperationID, RequireOutOfBand: true}); err != nil || a.Status != approval.Consumed {
		t.Fatalf("consume: %+v %v", a, err)
	}
	act.ConsumedApproval = a.ID

	// A brake engaged (the brake store; the gate's own record of it is the audit's).
	if _, _, e := (brake.Store{Dir: filepath.Join(dataDir, "brakes"), Now: tick(november)}).EngageLockdown(audit.Principal{Kind: "human", Via: gate.ViaWeb}, "rehearsal"); e != nil {
		t.Fatal(e)
	}
	if _, e := sink.Write(audit.Record{Kind: audit.KindBrakeChanged, Principal: audit.Principal{Kind: "human", Via: gate.ViaWeb}, Note: "lockdown engaged (rehearsal)"}); e != nil {
		t.Fatal(e)
	}

	// Pause one target and resume one the old tree had paused (supervise's pause state).
	act.Paused, act.Resumed = "api", "web"
	if e := storage.SetPaused(ctx, st, act.Paused, true, november); e != nil {
		t.Fatal(e)
	}
	if e := storage.SetPaused(ctx, st, act.Resumed, false, november); e != nil {
		t.Fatal(e)
	}

	// An apply records the digest of the definition it applied, keyed with the audit key.
	digester, err := audit.OpenExistingDigester(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	act.AppliedID, act.AppliedDigest = "box", digester.Digest(map[string]any{"id": "box", "rehearsal": true})
	if e := storage.SetApplied(ctx, st, act.AppliedID, act.AppliedDigest, november); e != nil {
		t.Fatal(e)
	}

	// Host monitoring: samples past the watermark, a status change and its event, then rollups.
	samples := hosts.NewSampleStore(st)
	load := 0.5
	for i := 0; i < 30; i++ {
		at := november.Add(time.Duration(i) * time.Minute)
		if e := samples.AppendSample(ctx, hosts.Sample{Host: "box-1", At: at, Load1: &load, CPU: 4, MemoryTotal: 8 << 30, MemoryAvailable: 4 << 30,
			DiskTotal: 100 << 30, DiskFree: 50 << 30, UptimeSeconds: 3600, ProbeMS: 12}); e != nil {
			t.Fatal(e)
		}
	}
	last := november.Add(29 * time.Minute)
	if e := samples.PutStatus(ctx, hosts.HostStatus{Host: "box-2", Status: "ok", Since: last, LastOK: &last, LastAttempt: &last}); e != nil {
		t.Fatal(e)
	}
	if _, e := samples.AppendEvent(ctx, hosts.Event{At: last, Host: "box-2", Kind: "status", From: "degraded", To: "ok"}); e != nil {
		t.Fatal(e)
	}
	if _, e := samples.Compact(ctx, november.Add(2*time.Hour), hosts.Retention{Raw: 24 * 365 * time.Hour, Rollup5m: 24 * 365 * time.Hour, Rollup1h: 24 * 365 * time.Hour, Events: 24 * 365 * time.Hour}); e != nil {
		t.Fatal(e)
	}

	// A credential issuance through the broker's database recorder (the issued value stays a file
	// under the broker's root; only the receipts are the store's).
	act.Issuance = strings.Repeat("e", 32)
	root, err := os.MkdirTemp("", "issuance-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	cb := credentialbroker.New(root, credentialbroker.WithRecorder(admin.StoreRecorder{Store: st}))
	d, err := cb.Prepare(ctx, credentialbroker.Binding{ID: act.Issuance, Connector: "postgres", Operation: "provision", Digest: "hmac-sha256:binding"}, []string{"password"})
	if err != nil {
		t.Fatal(err)
	}
	if e := d.Start(); e != nil {
		t.Fatal(e)
	}
	if e := d.Finish(true); e != nil {
		t.Fatal(e)
	}

	// The state files, through their own writers: a host's state, and an overview snapshot the
	// recorder persists on its first tick.
	if e := hosts.NewStore(filepath.Join(dataDir, "state", "hosts.json")).Put("box-1", hosts.State{State: "online", CheckedAt: &last}); e != nil {
		t.Fatal(e)
	}
	ticked := make(chan struct{}, 1)
	rec := overview.NewSnapshotRecorder(overview.SnapshotRecorderConfig{Interval: time.Hour, Retention: 30 * 24 * time.Hour, Path: filepath.Join(dataDir, "state", "overview_snapshots.json")},
		func(context.Context) (overview.OverviewSnapshot, error) {
			defer func() { ticked <- struct{}{} }()
			return overview.OverviewSnapshot{Timestamp: time.Now(), Resources: 3, Running: 1}, nil
		}, nil)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = rec.Run(rctx); close(done) }()
	<-ticked
	cancel()
	<-done

	// December: the chain rolls over to a new month file.
	decSink, err := audit.OpenFileSinkClock(auditDir, tick(december))
	if err != nil {
		t.Fatal(err)
	}
	decBroker, err := gate.NewBroker(decSink, filepath.Join(dataDir, "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	intent.OperationID = audit.NewID()
	p, err := decBroker.Request(ctx, intent, policy.Result{Decision: policy.Approve}, approval.ChannelOutOfBand, approval.ScopeOnce, time.Hour, gate.PlanSnapshot{Hash: "sha256:plan2"})
	if err != nil {
		t.Fatal(err)
	}
	act.PendingApproval = p.ID
	return act
}

// The rehearsal script's driver: with CERBERUS_EXPORT_DRIVE_DATA set to a data directory
// migrate-from wrote (and CERBERUS_EXPORT_DRIVE_DB to its database, default <data>/cerberus.db),
// it drives the same activity there for scripts/verify-exported-chain.sh. Skipped otherwise.
func TestDriveActivityForTheRehearsal(t *testing.T) {
	data := os.Getenv("CERBERUS_EXPORT_DRIVE_DATA")
	if data == "" {
		t.Skip("CERBERUS_EXPORT_DRIVE_DATA is not set (scripts/verify-exported-chain.sh sets it)")
	}
	db := os.Getenv("CERBERUS_EXPORT_DRIVE_DB")
	if db == "" {
		db = filepath.Join(data, "cerberus.db")
	}
	act := driveNewActivity(t, data, db)
	t.Logf("drove: approval %s consumed, %s pending; issuance %s; paused %s, resumed %s", act.ConsumedApproval, act.PendingApproval, act.Issuance, act.Paused, act.Resumed)
}

// The rehearsal script's check of the exported copy, which the old binary has no verb for: with
// CERBERUS_EXPORT_CHECK_OLD_HOME (the copy) and CERBERUS_EXPORT_DRIVE_DATA (new's data dir) set,
// the copy's approvals store reads whole to new's next seq and its brake store folds to new's
// state. Skipped otherwise.
func TestCheckTheExportedCopyForTheRehearsal(t *testing.T) {
	old, data := os.Getenv("CERBERUS_EXPORT_CHECK_OLD_HOME"), os.Getenv("CERBERUS_EXPORT_DRIVE_DATA")
	if old == "" || data == "" {
		t.Skip("CERBERUS_EXPORT_CHECK_OLD_HOME is not set (scripts/verify-exported-chain.sh sets it)")
	}
	nb, err := approval.ReadFrom(filepath.Join(data, "approvals"), approval.Cursor{}, 0)
	if err != nil || nb.Stop != nil {
		t.Fatalf("new's approvals: %v %v", err, nb.Stop)
	}
	ob, err := approval.ReadFrom(filepath.Join(old, "approvals"), approval.Cursor{}, 0)
	if err != nil || ob.Stop != nil || ob.Next.Seq != nb.Next.Seq || len(ob.Damaged) != 0 {
		t.Fatalf("the copy's approvals: %v %v, next seq %d (new %d), damaged %v", err, ob.Stop, ob.Next.Seq, nb.Next.Seq, ob.Damaged)
	}
	ns, np := brake.Store{Dir: filepath.Join(data, "brakes")}.Load()
	os2, op := brake.Store{Dir: filepath.Join(old, "brakes")}.Load()
	if !reflect.DeepEqual(ns, os2) || len(op) != len(np) {
		t.Fatalf("the copy's brakes fold to %+v (%v), new's %+v (%v)", os2, op, ns, np)
	}
	t.Logf("approvals: next seq %d on both; brakes: engaged %v on both", ob.Next.Seq, os2.Engaged())
}
