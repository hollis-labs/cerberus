package credentialbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	shared "github.com/hollis-labs/libs/security/secrets"
)

func binding() Binding {
	return Binding{ID: strings.Repeat("a", 32), Connector: "postgres", Operation: "provision", Digest: "hmac-sha256:bound-arguments"}
}
func TestPreparedValuesPersistBeforeDispatchAndNeverEnterReceipts(t *testing.T) {
	b := New(filepath.Join(t.TempDir(), "broker"))
	d, err := b.Prepare(context.Background(), binding(), []string{"runtime", "migration"})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range d.Values() {
		r, resolveErr := shared.New().Resolve(context.Background(), d.Receipt().References[name])
		if resolveErr != nil || r.Secret.Reveal() != v {
			t.Fatal("issued credential is not readable through shared private-file resolver")
		}
		data, _ := json.Marshal(d)
		if strings.Contains(string(data), v) || strings.Contains(fmt.Sprintf("%+v %#v", d, d), v) {
			t.Fatal("receipt contains secret")
		}
		if len(v) != 43 {
			t.Fatal("credential entropy encoding changed")
		}
	}
	if d.Receipt().State != "prepared" {
		t.Fatal("not prepared")
	}
	if err = d.Start(); err != nil {
		t.Fatal(err)
	}
	if err = d.Finish(true); err != nil {
		t.Fatal(err)
	}
	if d.Receipt().State != "completed" || len(d.Values()) != 0 {
		t.Fatal("dispatch not closed")
	}
	// A newly constructed broker after a restart cannot replay an issuance.
	if _, err = New(b.root).Prepare(context.Background(), binding(), []string{"runtime"}); err == nil {
		t.Fatal("completed issuance replayed")
	}
}
func TestSameIDCannotRaceOrRebind(t *testing.T) {
	root := filepath.Join(t.TempDir(), "broker")
	var success atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := New(root).Prepare(context.Background(), binding(), []string{"runtime"}); err == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal("concurrent issuance was not exclusive")
	}
	b := binding()
	b.Connector = "another"
	b.Digest = "different"
	if _, err := New(root).Prepare(context.Background(), b, []string{"runtime"}); err == nil {
		t.Fatal("existing ID was rebound")
	}
}

type brokenRandom struct{}

func (brokenRandom) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
func TestInterruptedPreparationAndDispatchFailClosed(t *testing.T) {
	for _, phase := range []string{"generate", "persist", "dispatch"} {
		t.Run(phase, func(t *testing.T) {
			b := New(filepath.Join(t.TempDir(), "broker"))
			if phase == "generate" {
				b.random = brokenRandom{}
			}
			if phase == "persist" {
				if err := os.MkdirAll(b.root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(b.root, "secrets"), []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d, err := b.Prepare(context.Background(), binding(), []string{"runtime"})
			if phase == "dispatch" {
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(filepath.Join(d.dir, "dispatching.json"), 0o700); err != nil {
					t.Fatal(err)
				}
				if d.Start() == nil {
					t.Fatal("dispatch without durable marker")
				}
			} else if err == nil || d != nil {
				t.Fatal("failed preparation produced a dispatch")
			}
			if _, err = New(b.root).Prepare(context.Background(), binding(), []string{"runtime"}); err == nil {
				t.Fatal("uncertain issuance reused")
			}
		})
	}
}
func TestUncertainResultRetainsCredentialForReconciliation(t *testing.T) {
	b := New(filepath.Join(t.TempDir(), "broker"))
	d, err := b.Prepare(context.Background(), binding(), []string{"runtime"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Start() != nil || d.Finish(false) != nil {
		t.Fatal("lifecycle failed")
	}
	if d.Receipt().State != "uncertain" {
		t.Fatal("uncertainty lost")
	}
	if _, err = shared.New().Resolve(context.Background(), d.Receipt().References["runtime"]); err != nil {
		t.Fatal("uncertain credential was deleted")
	}
}

func TestFailedOutcomePersistenceCannotReplay(t *testing.T) {
	b := New(filepath.Join(t.TempDir(), "broker"))
	d, err := b.Prepare(context.Background(), binding(), []string{"runtime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d.dir, "completed.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if d.Finish(true) == nil {
		t.Fatal("outcome persistence failure ignored")
	}
	if d.Receipt().State != "dispatching" || len(d.Values()) != 0 {
		t.Fatal("failed finish lost uncertainty or retained dispatch values")
	}
	if _, err := New(b.root).Prepare(context.Background(), binding(), []string{"runtime"}); err == nil {
		t.Fatal("failed outcome was replayed")
	}
}
