package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func enforceReq(env, kind string, effect contract.Effect) Request {
	return Request{Connector: "local", Operation: "stop", Effect: effect, Principal: Principal{Kind: kind},
		Target: target.Target{Kind: "local.resource", ID: "web", Labels: target.Labels{Env: target.Env(env), Owner: "self"}}}
}

// An enforce entry covers the calls its target match, principal and effects
// select; mode enforce covers everything; nothing is enforced by default.
func TestEnforcementScopes(t *testing.T) {
	e := Enforcement{Enforce: []EnforceEntry{
		{ID: "agents-prod", Match: TargetMatch{Env: "prod"}, Principal: "agent"},
		{ID: "not-human-destructive", Principal: "!human", Effect: []contract.Effect{contract.EffectDestructive}},
	}}
	for name, c := range map[string]struct {
		req  Request
		want bool
	}{
		"an agent on prod":             {enforceReq("prod", "agent", contract.EffectLifecycle), true},
		"a human on prod":              {enforceReq("prod", "human", contract.EffectLifecycle), false},
		"an agent on dev, lifecycle":   {enforceReq("dev", "agent", contract.EffectLifecycle), false},
		"an agent on dev, destructive": {enforceReq("dev", "agent", contract.EffectDestructive), true},
		"a human on dev, destructive":  {enforceReq("dev", "human", contract.EffectDestructive), false},
	} {
		if got, _ := e.Enforced(c.req); got != c.want {
			t.Errorf("%s: enforced %v, want %v", name, got, c.want)
		}
	}
	if got, _ := (File{}).EnforcementOf().Enforced(enforceReq("prod", "agent", contract.EffectDestructive)); got {
		t.Error("an empty snapshot enforced something")
	}
	if got, by := (Enforcement{Mode: EnforceAll}).Enforced(enforceReq("dev", "human", contract.EffectRead)); !got || by != "mode: enforce" {
		t.Errorf("mode enforce: %v %q", got, by)
	}
	bad := File{Version: FileVersion, Enforcement: &Enforcement{Mode: "on", Enforce: []EnforceEntry{{Principal: "robot", Effect: []contract.Effect{"explode"}}}}}
	problems := strings.Join(bad.Validate(), "\n")
	for _, want := range []string{`mode "on"`, `principal "robot"`, `effect "explode"`} {
		if !strings.Contains(problems, want) {
			t.Errorf("validation missed %s:\n%s", want, problems)
		}
	}
}

// Merging working files carries the enforcement scopes and the break-glass
// limit into the snapshot (the limit used to be dropped).
func TestMergeCarriesEnforcementAndBreakGlass(t *testing.T) {
	merged := Merge(
		File{Enforcement: &Enforcement{Enforce: []EnforceEntry{{ID: "a"}}}, BreakGlass: &BreakGlass{PerTarget: 5}},
		File{Enforcement: &Enforcement{Mode: EnforceAll, Enforce: []EnforceEntry{{ID: "b"}}}},
	)
	if merged.Enforcement == nil || merged.Enforcement.Mode != EnforceAll || len(merged.Enforcement.Enforce) != 2 {
		t.Fatalf("enforcement %+v", merged.Enforcement)
	}
	if merged.BreakGlassLimits().PerTarget != 5 {
		t.Fatalf("break glass %+v", merged.BreakGlass)
	}
}

// The installed decision point is a Reloading: the break-glass limit is read
// through it, not only through an *Evaluator.
func TestBreakGlassLimitsThroughReloading(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if _, err := store.Apply(File{Version: FileVersion, BreakGlass: &BreakGlass{PerTarget: 1, Window: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if got := BreakGlassLimitsOf(NewReloading(store, nil)); got.PerTarget != 1 || got.Window != time.Hour {
		t.Fatalf("limits through Reloading %+v", got)
	}
}

// applyRecorded applies f and records the apply as the daemon does, with its
// enforcement.
func applyRecorded(t *testing.T, store Store, sink *audit.FileSink, f File) {
	t.Helper()
	if _, err := store.Apply(f); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write(audit.Record{Kind: audit.KindOutcome, Connector: "policy", Operation: "apply", Decision: audit.DecisionAllowed,
		OutcomeCode: "ok", Enforcement: f.EnforcementOf().Record()}); err != nil {
		t.Fatal(err)
	}
}

func tamperFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("\n# edited\n")...), 0o600); err != nil { //nolint:gosec // the test's own file
		t.Fatal(err)
	}
}

// A snapshot that fails its hash check is enforced as it was last verified,
// from the hash-chained audit log, across a restart; with the log's chain
// broken as well, everything is enforced.
func TestMismatchEnforcesTheLastVerified(t *testing.T) {
	agentProd := enforceReq("prod", "agent", contract.EffectLifecycle)
	humanDev := enforceReq("dev", "human", contract.EffectLifecycle)
	setup := func(t *testing.T, f File) (Store, string) {
		t.Helper()
		auditDir := filepath.Join(t.TempDir(), "audit")
		sink, err := audit.OpenFileSink(auditDir)
		if err != nil {
			t.Fatal(err)
		}
		store := Store{Dir: t.TempDir(), AuditDir: auditDir}
		applyRecorded(t, store, sink, f)
		tamperFile(t, filepath.Join(store.Dir, appliedName))
		return store, auditDir
	}
	t.Run("shadow stays shadow", func(t *testing.T) {
		store, _ := setup(t, File{Version: FileVersion})
		ev, status := store.LoadVerified()
		if !status.Mismatch() || !strings.Contains(status.Enforcement, "last verified") {
			t.Fatalf("status %+v", status)
		}
		if on, _ := ev.Enforced(enforceReq("dev", "agent", contract.EffectLifecycle)); on {
			t.Fatal("a mismatch after shadow enforced something")
		}
		// Except what is always enforced (B2): an agent's change to prod.
		if on, by := ev.Enforced(agentProd); !on || !strings.Contains(by, "built-in") {
			t.Fatalf("an agent's change to prod: %v %q", on, by)
		}
	})
	t.Run("scopes survive a restart", func(t *testing.T) {
		store, _ := setup(t, File{Version: FileVersion, Enforcement: &Enforcement{Enforce: []EnforceEntry{{ID: "agents-prod", Match: TargetMatch{Env: "prod"}, Principal: "agent"}}}})
		for i := 0; i < 2; i++ { // a fresh Reloading is a daemon restart
			r := NewReloading(store, nil)
			if on, by := r.Enforced(agentProd); !on || !strings.Contains(by, "snapshot mismatch") || !strings.Contains(by, "agents-prod") {
				t.Fatalf("restart %d: agent on prod %v %q", i, on, by)
			}
			if on, _ := r.Enforced(humanDev); on {
				t.Fatalf("restart %d: enforced beyond the verified scopes", i)
			}
		}
	})
	t.Run("a broken chain enforces everything", func(t *testing.T) {
		store, auditDir := setup(t, File{Version: FileVersion})
		files, _ := filepath.Glob(filepath.Join(auditDir, "*.jsonl"))
		data, _ := os.ReadFile(files[0]) //nolint:gosec // the test's own file
		_ = os.WriteFile(files[0], bytes.Replace(data, []byte(`"apply"`), []byte(`"applY"`), 1), 0o600)
		ev, status := store.LoadVerified()
		if on, _ := ev.Enforced(humanDev); !on || !strings.Contains(status.Enforcement, "enforcing everything") {
			t.Fatalf("broken chain: enforced %v, %q", on, status.Enforcement)
		}
	})
	t.Run("no recorded apply enforces everything", func(t *testing.T) {
		store := Store{Dir: t.TempDir(), AuditDir: filepath.Join(t.TempDir(), "audit")}
		if _, err := store.Apply(File{Version: FileVersion}); err != nil {
			t.Fatal(err)
		}
		tamperFile(t, filepath.Join(store.Dir, appliedName))
		if ev, _ := store.LoadVerified(); !func() bool { on, _ := ev.Enforced(humanDev); return on }() {
			t.Fatal("an unrecorded snapshot's mismatch did not fail closed")
		}
	})
}
