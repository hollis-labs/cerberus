package eventexport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/ingest"
	"github.com/hollis-labs/libs/mesh/eventbus"
	"github.com/hollis-labs/libs/mesh/eventbus/relay"
)

func hubEvent(t *testing.T, box *FileOutbox, key string) Entry {
	t.Helper()
	env := Envelope{
		Kind: runtimeevents.EventKind("audit.recorded"), Time: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		Subject: "msg://audit/cerberus/x", Source: Source{Channel: ChannelAudit},
		Actor:   actorOf(audit.Principal{Kind: "human", Client: "terminal"}),
		Class:   &Class{Category: "audit", Severity: "info", PayloadSchema: schemaAuditRecorded},
		Payload: mustJSON(map[string]any{"action": "intent", "outcome": "ok", "chain_digest": "h"}),
	}
	entry, _, err := box.Append(key, SourceRef{Source: "audit"}, env)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// What goes to the hub has no actor, record time or cursor (the hub owns them), and
// the outbox entry it came from is not changed.
func TestHubItemClearsWhatTheHubOwnsOnACopy(t *testing.T) {
	box, err := OpenFileOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := hubEvent(t, box, "k1")
	if entry.Envelope.Actor == nil {
		t.Fatal("the test event needs an actor")
	}
	item, err := HubItem(entry)
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != entry.Envelope.ID || item.Priority != eventbus.PriorityHigh {
		t.Fatalf("item = %+v", item)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(item.Body, &sent); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"actor", "recorded_at", "cursor"} {
		if _, ok := sent[f]; ok {
			t.Fatalf("%s must not be sent", f)
		}
	}
	if _, rej := ingest.CheckItem("cerberus", item.Body, ingest.Options{}); rej != nil {
		t.Fatalf("the hub would reject what the relay sends: %v", rej)
	}
	if entry.Envelope.Actor == nil {
		t.Fatal("the outbox entry was changed")
	}
}

func TestRelaySourceReadsInOrderAfterASequenceAndReportsTheHead(t *testing.T) {
	box, err := OpenFileOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		hubEvent(t, box, k)
	}
	src := RelaySource{Outbox: box}
	got, err := src.After(t.Context(), 1, 10)
	if err != nil || len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("After(1) = %+v, %v", got, err)
	}
	if head, err := src.Head(t.Context()); err != nil || head != 3 {
		t.Fatalf("Head = %d, %v", head, err)
	}
	empty, _ := OpenFileOutbox(t.TempDir())
	if head, err := (RelaySource{Outbox: empty}).Head(t.Context()); err != nil || head != 0 {
		t.Fatalf("empty Head = %d, %v", head, err)
	}
}

// fakeHub is a stand-in for the hub's ingest route: it records what it was sent
// and accepts it. Nothing here touches a live system.
type fakeHub struct {
	mu   sync.Mutex
	ids  []string
	body []json.RawMessage
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	items, err := ingest.ParseRequest(raw)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var results []ingest.Result
	h.mu.Lock()
	for i, it := range items {
		var ev struct{ ID string }
		_ = json.Unmarshal(it, &ev)
		h.ids = append(h.ids, ev.ID)
		h.body = append(h.body, it)
		results = append(results, ingest.Result{ID: ev.ID, Status: ingest.StatusAccepted, Cursor: "c" + string(rune('a'+i))})
	}
	h.mu.Unlock()
	_ = json.NewEncoder(w).Encode(ingest.NewResponse(results))
}

func (h *fakeHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ids)
}

// The whole path against a fake hub: the relay ships what the outbox holds, moves
// its cursor, and after a restart sends only what is new.
func TestRelayShipsTheOutboxAndResumesFromItsCursor(t *testing.T) {
	hub := &fakeHub{}
	srv := httptest.NewServer(hub)
	defer srv.Close()
	boxDir, hubDir := t.TempDir(), t.TempDir()
	box, err := OpenFileOutbox(boxDir)
	if err != nil {
		t.Fatal(err)
	}
	first := []Entry{hubEvent(t, box, "a"), hubEvent(t, box, "b")}

	run := func() (stop func(), r *relay.Relay) {
		client, err := eventbus.New(eventbus.Config{URL: srv.URL, SpoolDir: filepath.Join(hubDir, "spool"),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		r, err = relay.New(relay.Config{Source: RelaySource{Outbox: box}, Cursor: relay.FileCursor{Path: filepath.Join(hubDir, "audit.cursor")},
			Deliverer: client, Poll: 5 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); _ = r.Run(ctx) }()
		return func() {
			cancel()
			<-done
			_ = client.Close(context.Background())
		}, r
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s", what)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	stop, r := run()
	waitFor("first two events", func() bool { return hub.count() == 2 && r.Stats().Cursor == 2 })
	stop()
	for i, id := range hub.ids {
		if id != first[i].Envelope.ID {
			t.Fatalf("event %d id = %s, want %s", i, id, first[i].Envelope.ID)
		}
	}

	third := hubEvent(t, box, "c")
	stop, r = run() // a restart: the cursor is on disk
	waitFor("the third event", func() bool { return hub.count() == 3 && r.Stats().Cursor == 3 })
	stop()
	if hub.count() != 3 || hub.ids[2] != third.Envelope.ID {
		t.Fatalf("after restart the hub saw %v, want exactly the three events once", hub.ids)
	}
}

// An outbox line written before app and schema_version existed (#45) is sent with
// both filled in, so the hub does not reject it on shape.
func TestHubItemFillsAppAndSchemaVersionOnALegacyLine(t *testing.T) {
	box, err := OpenFileOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := hubEvent(t, box, "legacy")
	entry.Envelope.App, entry.Envelope.SchemaVersion = "", ""
	item, err := HubItem(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, rej := ingest.CheckItem("cerberus", item.Body, ingest.Options{}); rej != nil {
		t.Fatalf("a legacy line is rejected: %v", rej)
	}
}
