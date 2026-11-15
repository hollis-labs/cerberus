package admin_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/hollis-labs/libs/core/store/storetest"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// envPostgres names the server the Postgres run uses, as internal/storage's tests do.
const envPostgres = "SUBSTRATE_TEST_POSTGRES_DSN"

// receiptDialects runs fn on a migrated SQLite store and, when envPostgres is set, on a
// migrated private Postgres schema (internal/storage's eachDialect, which is not exported).
func receiptDialects(t *testing.T, fn func(t *testing.T, st *store.Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, openStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv(envPostgres)
		if dsn == "" && os.Getenv(storetest.EnvDSN) == "" {
			t.Skipf("%s not set; skipping the Postgres run of this test", envPostgres)
		}
		if dsn != "" && os.Getenv(storetest.EnvDSN) == "" {
			t.Setenv(storetest.EnvDSN, dsn)
		}
		cfg := storetest.PGConfig(t)
		if cfg == nil {
			return
		}
		cfg.SchemaMode = store.SchemaAuto
		ctx := context.Background()
		st, err := store.Open(ctx, *cfg)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		fsys, root := storage.Migrations()
		if err := migrate.Ensure(ctx, *cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
			t.Fatalf("migrate postgres: %v", err)
		}
		fn(t, st)
	})
}

// countingRecorder wraps a recorder and counts what reached it: reservations granted and
// refused, and every later receipt.
type countingRecorder struct {
	inner              credentialbroker.Recorder
	reserved, refused  atomic.Int32
	recordedAfterwards atomic.Int32
}

func (c *countingRecorder) Reserve(ctx context.Context, r credentialbroker.Receipt) error {
	err := c.inner.Reserve(ctx, r)
	if err != nil {
		c.refused.Add(1)
	} else {
		c.reserved.Add(1)
	}
	return err
}

func (c *countingRecorder) Record(ctx context.Context, r credentialbroker.Receipt) error {
	c.recordedAfterwards.Add(1)
	return c.inner.Record(ctx, r)
}

// Two issuances racing for the same id through the class A store's recorder: the preparing
// insert is the exclusive reservation, so exactly one reserves and proceeds to dispatch; the
// other is refused at the reservation, before any secret is generated, written or dispatched;
// and the store holds exactly one preparing receipt for the id.
func TestConcurrentIssuanceReservesTheStoreReceiptOnce(t *testing.T) {
	receiptDialects(t, func(t *testing.T, st *store.Store) {
		const racers = 2
		root := filepath.Join(t.TempDir(), "credential-issuance")
		rec := &countingRecorder{inner: admin.StoreRecorder{Store: st}}
		id := strings.Repeat("d", 32)
		bind := credentialbroker.Binding{ID: id, Connector: "echo", Operation: "provision", Digest: "hmac-sha256:bound"}

		start := make(chan struct{})
		var wg sync.WaitGroup
		dispatches := make([]*credentialbroker.Dispatch, racers)
		errs := make([]error, racers)
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b := credentialbroker.New(root, credentialbroker.WithRecorder(rec))
				<-start
				dispatches[i], errs[i] = b.Prepare(context.Background(), bind, []string{"runtime"})
			}()
		}
		close(start)
		wg.Wait()

		var winner *credentialbroker.Dispatch
		for i := range racers {
			switch {
			case errs[i] == nil:
				if winner != nil {
					t.Fatal("two concurrent issuances both reserved the same id")
				}
				winner = dispatches[i]
			case dispatches[i] != nil:
				t.Fatalf("a refused issuance came back with a dispatch: %v", errs[i])
			case !strings.Contains(errs[i].Error(), "already reserved"):
				t.Fatalf("the losing issuance was refused for another reason than the reservation: %v", errs[i])
			}
		}
		if winner == nil {
			t.Fatalf("no issuance reserved the id: %v", errs)
		}
		if rec.reserved.Load() != 1 || rec.refused.Load() != racers-1 {
			t.Fatalf("reservations granted %d, refused %d; want 1 and %d", rec.reserved.Load(), rec.refused.Load(), racers-1)
		}
		// Only the winner went past the reservation: its prepared receipt is the only later one.
		if rec.recordedAfterwards.Load() != 1 {
			t.Fatalf("%d receipts after the reservation, want the winner's prepared alone", rec.recordedAfterwards.Load())
		}
		// The one secret on disk is the winner's: the loser wrote nothing over it.
		issued := filepath.Join(root, "secrets", "issued", id)
		entries, err := os.ReadDir(issued)
		if err != nil || len(entries) != 1 || entries[0].Name() != "runtime" {
			t.Fatalf("issued secrets = %v, %v; want the winner's runtime alone", entries, err)
		}
		data, err := os.ReadFile(filepath.Join(issued, "runtime")) //nolint:gosec // the test's own file
		if err != nil || strings.TrimSpace(string(data)) != winner.Values()["runtime"] {
			t.Fatalf("the issued secret is not the winner's (%v)", err)
		}
		if err = winner.Start(); err != nil {
			t.Fatalf("the winner could not dispatch: %v", err)
		}
		if err = winner.Finish(true); err != nil {
			t.Fatal(err)
		}

		got, err := storage.Receipts(context.Background(), st, id)
		if err != nil {
			t.Fatal(err)
		}
		var states []string
		preparing := 0
		for _, r := range got {
			states = append(states, r.State)
			if r.State == "preparing" {
				preparing++
			}
		}
		if preparing != 1 || strings.Join(states, ",") != "preparing,prepared,dispatching,completed" {
			t.Fatalf("receipts for %s = %v, want one preparing and the winner's lifecycle", id, states)
		}
	})
}
