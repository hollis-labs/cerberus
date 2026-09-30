package brake

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

var operator = audit.Principal{Kind: "human", Via: "cli"}

func prodAPI() target.Target {
	return target.Target{Kind: "local.resource", ID: "api", Resource: "api", Labels: target.Labels{Env: target.EnvProd, Owner: "self"}}
}

// A lockdown refuses every effect but a plain read, read_sensitive
// included, and survives a reopen until lifted.
func TestLockdown(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_, l, err := s.EngageLockdown(operator, "incident")
	if err != nil {
		t.Fatal(err)
	}
	if _, again, _ := s.EngageLockdown(operator, "twice"); again.ID != l.ID {
		t.Fatal("engaging twice made a second lockdown")
	}
	st, problems := (Store{Dir: s.Dir}).Load()
	if len(problems) != 0 || st.Lockdown == nil || st.Lockdown.Reason != "incident" {
		t.Fatalf("reopened %+v %v", st, problems)
	}
	for effect, want := range map[contract.Effect]bool{contract.EffectRead: false, contract.EffectReadSensitive: true, contract.EffectWrite: true, contract.EffectDestructive: true} {
		if got, _, _ := st.Blocks("local", effect, prodAPI()); got != want {
			t.Errorf("%s: blocked %v, want %v", effect, got, want)
		}
	}
	if _, err = s.LiftLockdown("ldn_other", operator, "tty"); !errors.Is(err, ErrNotEngaged) {
		t.Fatalf("lifted another id: %v", err)
	}
	if st, err = s.LiftLockdown(l.ID, operator, "tty"); err != nil || st.Lockdown != nil {
		t.Fatalf("lift: %v %+v", err, st)
	}
}

// A freeze refuses only the targets its match selects.
func TestFreeze(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	st, f, err := s.EngageFreeze(policy.TargetMatch{Env: "prod"}, operator, "release freeze")
	if err != nil {
		t.Fatal(err)
	}
	if blocked, _, by := st.Blocks("local", contract.EffectLifecycle, prodAPI()); !blocked || by == nil || by.ID != f.ID {
		t.Fatal("a prod target was not frozen")
	}
	dev := prodAPI()
	dev.Env = target.EnvDev
	if blocked, _, _ := st.Blocks("local", contract.EffectLifecycle, dev); blocked {
		t.Fatal("a dev target was frozen")
	}
	if st, err = s.LiftFreeze(f.ID, operator, "tty"); err != nil || len(st.Freezes) != 0 {
		t.Fatalf("lift: %v %+v", err, st)
	}
}

// Appends from many writers chain: the store folds with no problems.
func TestConcurrentAppendsChain(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = (Store{Dir: dir}).EngageFreeze(policy.TargetMatch{ID: fmt.Sprintf("t%d", i)}, operator, "")
		}(i)
	}
	wg.Wait()
	st, problems := (Store{Dir: dir}).Load()
	if len(problems) != 0 || len(st.Freezes) != 8 {
		t.Fatalf("%d freezes, problems %v", len(st.Freezes), problems)
	}
}

// Deleting the store cannot lift a lockdown the verified audit log recorded.
func TestEffectiveIsTheMoreRestrictive(t *testing.T) {
	auditDir := filepath.Join(t.TempDir(), "audit")
	sink, err := audit.OpenFileSink(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: t.TempDir()}
	st, _, _ := s.EngageLockdown(operator, "incident")
	data, _ := json.Marshal(st)
	if _, err := sink.Write(audit.Record{Kind: audit.KindBrakeChanged, Brakes: data}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.Dir, FileName)); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Load()
	recorded, ok := Recorded(auditDir)
	if !ok || stored.Lockdown != nil || Effective(stored, recorded).Lockdown == nil {
		t.Fatalf("stored %+v recorded %+v (%v)", stored, recorded, ok)
	}
}
