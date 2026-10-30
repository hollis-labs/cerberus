package cerbapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

const intermittentHost = `version: 1
hosts:
  - {name: box-a, role: worker, reach: {ssh: box-a-ssh}}
  - {name: laptop, role: worker, labels: {intermittent: "true"}, reach: {ssh: box-a-ssh}}
`

// flap takes the rig's hosts online, then offline (three failures), so each
// host appends two events.
func (r *monitorRig) flap(t *testing.T) {
	t.Helper()
	r.tick(t)
	r.clock.Advance(3 * time.Minute)
	r.tick(t) // unknown -> online
	r.down.Store(true)
	for i := 0; i < 3; i++ {
		r.clock.Advance(20 * time.Minute)
		r.tick(t)
	}
}

func TestStatusChangesAppendEventsWithCodesOnly(t *testing.T) {
	r := newMonitorRig(t, intermittentHost, nil)
	r.flap(t)
	res, err := r.svc.HostEvents(context.Background(), EventsQuery{})
	if err != nil || len(res.Events) != 4 || res.LastID != 4 || res.Gap != nil {
		t.Fatalf("%+v %v", res, err)
	}
	var offline, laptopOffline *hosts.Event
	for i, e := range res.Events {
		if e.To == hosts.StatusOffline && e.Host == "box-a" {
			offline = &res.Events[i]
		}
		if e.To == hosts.StatusOffline && e.Host == "laptop" {
			laptopOffline = &res.Events[i]
		}
	}
	if offline == nil || offline.From != hosts.StatusOnline || offline.Reasons[0] != "unreachable" || offline.Expected {
		t.Fatalf("offline event: %+v", offline)
	}
	if laptopOffline == nil || !laptopOffline.Expected {
		t.Fatalf("an intermittent host's events are marked expected: %+v", laptopOffline)
	}
	raw, _ := json.Marshal(res)
	for _, leak := range []string{"192.0.2", "probe@", "i/o timeout", "key_file"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("an event must carry codes only, found %q in %s", leak, raw)
		}
	}
	only, _ := r.svc.HostEvents(context.Background(), EventsQuery{Host: "laptop", AfterID: 1, Limit: 10})
	for _, e := range only.Events {
		if e.Host != "laptop" || e.ID <= 1 {
			t.Fatalf("filter: %+v", e)
		}
	}
}

func TestEventsReportAGapWhenResumePointWasPruned(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, nil)
	r.flap(t)
	ctx := context.Background()
	if _, err := r.store.PruneEvents(ctx, r.clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendEvent(ctx, hosts.Event{At: r.clock.Now(), Host: "box-a", Kind: hosts.EventStatusChanged, From: "offline", To: "online"}); err != nil {
		t.Fatal(err)
	}
	res, err := r.svc.HostEvents(ctx, EventsQuery{AfterID: 1})
	if err != nil || res.Gap == nil || res.Gap.AfterID != 1 || res.Gap.OldestID < 3 || len(res.Events) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if res, _ := r.svc.HostEvents(ctx, EventsQuery{AfterID: res.Gap.OldestID - 1}); res.Gap != nil {
		t.Fatalf("resuming exactly at the start of the log is not a gap: %+v", res.Gap)
	}
}

// sseReader turns a response body into frames. One goroutine owns the scanner.
type sseReader struct{ lines chan string }

func newSSEReader(body *bufio.Scanner) *sseReader {
	r := &sseReader{lines: make(chan string, 64)}
	go func() {
		defer close(r.lines)
		for body.Scan() {
			r.lines <- body.Text()
		}
	}()
	return r
}

func (r *sseReader) frames(t *testing.T, want int) []map[string]string {
	t.Helper()
	var frames []map[string]string
	cur := map[string]string{}
	deadline := time.After(5 * time.Second)
	for len(frames) < want {
		select {
		case l, ok := <-r.lines:
			if !ok {
				return frames
			}
			switch {
			case l == "":
				if len(cur) > 0 {
					frames = append(frames, cur)
					cur = map[string]string{}
				}
			case strings.HasPrefix(l, ":"):
				cur["comment"] = l
			default:
				k, v, _ := strings.Cut(l, ": ")
				cur[k] = v
			}
		case <-deadline:
			t.Fatalf("timed out with %d of %d frames: %v", len(frames), want, frames)
		}
	}
	return frames
}

func TestSSEStreamResumesWakesAndHeartbeats(t *testing.T) {
	old := eventsHeartbeat
	eventsHeartbeat = 100 * time.Millisecond
	defer func() { eventsHeartbeat = old }()
	r := newMonitorRig(t, oneSSHHost, nil)
	r.flap(t) // events 1 (online) and 2 (offline)
	srv := httptest.NewServer(HostHTTP(HostClientOf(NewInProcessClient(WithHostService(r.svc))), "/api/hosts"))
	defer srv.Close()

	open := func(lastID string) (*http.Response, *sseReader) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/hosts/_events", nil)
		req.Header.Set("Accept", "text/event-stream")
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp, newSSEReader(bufio.NewScanner(resp.Body))
	}
	resp, sc := open("")
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	frames := sc.frames(t, 2)
	if frames[0]["id"] != "1" || frames[0]["event"] != hosts.EventStatusChanged || frames[1]["id"] != "2" {
		t.Fatalf("backlog: %v", frames)
	}
	var e hosts.Event
	if err := json.Unmarshal([]byte(frames[1]["data"]), &e); err != nil || e.To != hosts.StatusOffline {
		t.Fatalf("data: %v %v", frames[1], err)
	}
	// An idle stream heartbeats.
	if hb := sc.frames(t, 1); hb[0]["comment"] != ": heartbeat" {
		t.Fatalf("heartbeat: %v", hb)
	}
	// A new event wakes the stream without polling delay.
	r.down.Store(false)
	r.clock.Advance(time.Hour)
	r.tick(t)
	live := sc.frames(t, 1)
	if live[0]["id"] != "3" {
		t.Fatalf("live event: %v", live)
	}
	// Resuming with Last-Event-ID replays only what was missed.
	resp2, sc2 := open("2")
	defer resp2.Body.Close()
	again := sc2.frames(t, 1)
	if again[0]["id"] != "3" {
		t.Fatalf("resume: %v", again)
	}
}

func TestEventHubNeverBlocksAndCapsStreams(t *testing.T) {
	h := newEventHub()
	ch, cancel, ok := h.subscribe()
	if !ok {
		t.Fatal("subscribe")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.notify() // nobody reads: must not block
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify blocked on a slow subscriber")
	}
	select {
	case <-ch:
	default:
		t.Fatal("the subscriber was not woken")
	}
	cancel()
	var cancels []func()
	for i := 0; i < eventsMaxStreams; i++ {
		_, c, ok := h.subscribe()
		if !ok {
			t.Fatalf("stream %d refused early", i)
		}
		cancels = append(cancels, c)
	}
	if _, _, ok := h.subscribe(); ok {
		t.Fatal("more than the cap of streams were accepted")
	}
	cancels[0]()
	if _, c, ok := h.subscribe(); !ok {
		t.Fatal("a freed slot must be reusable")
	} else {
		c()
	}
}

func TestEventsPollOverTheSocket(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, nil)
	r.flap(t)
	client := serveOnSocket(t, NewInProcessClient(WithHostService(r.svc)))
	res, err := client.HostEvents(context.Background(), EventsQuery{AfterID: 1})
	if err != nil || len(res.Events) != 1 || res.Events[0].To != hosts.StatusOffline || res.LastID != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}
