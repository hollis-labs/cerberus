package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// OpHostEvents reads the status event log.
const OpHostEvents = "host_events"

const (
	eventsBatch      = 200
	eventsMaxStreams = 16
)

// eventsHeartbeat is how often an idle stream sends a comment, so a proxy or a
// dead peer is noticed. A variable so a test need not wait twenty seconds.
var eventsHeartbeat = 20 * time.Second

// eventHub wakes subscribers when an event is appended. It never blocks the
// monitor: each subscriber has a one-slot channel and a wake that finds it full
// is dropped (the subscriber is already going to read).
type eventHub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newEventHub() *eventHub { return &eventHub{subs: map[chan struct{}]struct{}{}} }

func (h *eventHub) subscribe() (<-chan struct{}, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= eventsMaxStreams {
		return nil, nil, false
	}
	ch := make(chan struct{}, 1)
	h.subs[ch] = struct{}{}
	return ch, func() { h.mu.Lock(); delete(h.subs, ch); h.mu.Unlock() }, true
}

func (h *eventHub) notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// EventsQuery selects status events after an id.
type EventsQuery struct {
	AfterID int64  `json:"after_id,omitempty"`
	Host    string `json:"host,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// EventGap says events the subscriber asked to resume after are gone: the log
// now starts at OldestID.
type EventGap struct {
	AfterID  int64 `json:"after_id"`
	OldestID int64 `json:"oldest_id"`
}

// EventsResult is a batch of events, oldest first. LastID is where to resume.
type EventsResult struct {
	Events []hosts.Event `json:"events"`
	LastID int64         `json:"last_id"`
	Gap    *EventGap     `json:"gap,omitempty"`
}

func (s *HostService) hostEvents(ctx context.Context, q EventsQuery) (EventsResult, error) {
	if s == nil || s.mon == nil {
		return EventsResult{}, errMonitoringUnavailable
	}
	st := s.mon.store
	evs, err := st.Events(ctx, hosts.EventQuery{AfterID: q.AfterID, Host: q.Host, Kind: q.Kind, Limit: q.Limit})
	if err != nil {
		return EventsResult{}, err
	}
	res := EventsResult{Events: evs, LastID: q.AfterID}
	if n := len(evs); n > 0 {
		res.LastID = evs[n-1].ID
	}
	oldest, _, err := st.EventBounds(ctx)
	if err != nil {
		return res, err
	}
	if q.AfterID > 0 && oldest > 0 && q.AfterID+1 < oldest {
		res.Gap = &EventGap{AfterID: q.AfterID, OldestID: oldest}
	}
	return res, nil
}

// HostEvents returns status events after an id, oldest first. It is an audited
// read; the SSE stream audits once when it opens, not per batch.
func (s *HostService) HostEvents(ctx context.Context, q EventsQuery) (_ EventsResult, retErr error) {
	if s == nil || s.mon == nil {
		return EventsResult{}, errMonitoringUnavailable
	}
	call, err := s.beginRead(ctx, OpHostEvents, map[string]any{"query": q})
	if err != nil {
		return EventsResult{}, err
	}
	defer func() { call.finish(retErr) }()
	res, err := s.hostEvents(ctx, q)
	call.note(fmt.Sprintf("events=%d", len(res.Events)))
	return res, err
}

// HostEventsClient reads events: by id, and by waiting for the next one.
type HostEventsClient interface {
	HostEvents(ctx context.Context, q EventsQuery) (EventsResult, error)
	// SubscribeHostEvents returns a channel that is signalled when events may
	// be waiting, and a cancel. A client that cannot be told (it reaches the
	// daemon over the socket) polls instead.
	SubscribeHostEvents(ctx context.Context) (<-chan struct{}, func(), error)
}

// HostEventsClientOf returns client as a HostEventsClient, or one that refuses.
func HostEventsClientOf(client any) HostEventsClient {
	if e, ok := client.(HostEventsClient); ok {
		return e
	}
	return unavailableHostClient{}
}

func (unavailableHostClient) HostEvents(context.Context, EventsQuery) (EventsResult, error) {
	return EventsResult{}, errHostsUnavailable
}
func (unavailableHostClient) SubscribeHostEvents(context.Context) (<-chan struct{}, func(), error) {
	return nil, nil, errHostsUnavailable
}

func (c *InProcessClient) HostEvents(ctx context.Context, q EventsQuery) (EventsResult, error) {
	s, err := c.hostService()
	if err != nil {
		return EventsResult{}, err
	}
	return s.HostEvents(ctx, q)
}

func (c *InProcessClient) SubscribeHostEvents(ctx context.Context) (<-chan struct{}, func(), error) {
	s, err := c.hostService()
	if err != nil {
		return nil, nil, err
	}
	if s.mon == nil {
		return nil, nil, errMonitoringUnavailable
	}
	ch, cancel, ok := s.mon.hub.subscribe()
	if !ok {
		return nil, nil, &HostInvalidError{Err: redact.Guidance("too many event streams are open (%d); close one and retry", eventsMaxStreams)}
	}
	return ch, cancel, nil
}

func (c *SocketClient) HostEvents(ctx context.Context, q EventsQuery) (EventsResult, error) {
	v := url.Values{}
	if q.AfterID > 0 {
		v.Set("after_id", strconv.FormatInt(q.AfterID, 10))
	}
	if q.Host != "" {
		v.Set("host", q.Host)
	}
	if q.Kind != "" {
		v.Set("kind", q.Kind)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/hosts/_events"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var out EventsResult
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return EventsResult{}, hostRouteError(err)
	}
	return out, nil
}

// SubscribeHostEvents over the socket polls every two seconds.
func (c *SocketClient) SubscribeHostEvents(ctx context.Context) (<-chan struct{}, func(), error) {
	ch := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
	return ch, func() { close(done) }, nil
}

// hostEventsHTTP serves GET {prefix}/_events: a JSON batch, or a Server-Sent
// Events stream when the client asks for text/event-stream. It reports whether
// it handled the request.
func hostEventsHTTP(w http.ResponseWriter, r *http.Request, client HostClient, parts []string) bool {
	if r.Method != http.MethodGet || len(parts) != 1 || parts[0] != "_events" {
		return false
	}
	ec := HostEventsClientOf(client)
	fail := func(err error) {
		err = scopeError(redact.ScopeFrom(r.Context()), err)
		writeServiceError(w, hostStatus(err), err)
	}
	q := EventsQuery{Host: r.URL.Query().Get("host"), Kind: r.URL.Query().Get("kind")}
	q.AfterID, _ = strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			q.AfterID = n
		}
	}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))

	if !wantsEventStream(r) {
		res, err := ec.HostEvents(r.Context(), q)
		if err != nil {
			fail(err)
			return true
		}
		writeJSON(w, http.StatusOK, res)
		return true
	}

	// Audit once, when the stream opens; the batches are not audited.
	first, err := ec.HostEvents(r.Context(), EventsQuery{AfterID: q.AfterID, Host: q.Host, Kind: q.Kind, Limit: eventsBatch})
	if err != nil {
		fail(err)
		return true
	}
	wake, cancel, err := ec.SubscribeHostEvents(r.Context())
	if err != nil {
		fail(err)
		return true
	}
	defer cancel()
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	last := q.AfterID
	send := func(res EventsResult) bool {
		if res.Gap != nil {
			data, _ := json.Marshal(res.Gap)
			if _, err := fmt.Fprintf(w, "event: gap\ndata: %s\n\n", data); err != nil {
				return false
			}
		}
		for _, e := range res.Events {
			data, _ := json.Marshal(e)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, data); err != nil {
				return false
			}
			last = e.ID
		}
		return rc.Flush() == nil
	}
	if !send(first) {
		return true
	}
	beat := time.NewTicker(eventsHeartbeat)
	defer beat.Stop()
	for {
		// Drain whatever is waiting before sleeping, so a burst is not
		// delivered one wake at a time.
		for {
			res, err := eventsQuiet(r.Context(), ec, EventsQuery{AfterID: last, Host: q.Host, Kind: q.Kind, Limit: eventsBatch})
			if err != nil {
				return true
			}
			res.Gap = nil
			if len(res.Events) == 0 {
				break
			}
			if !send(res) {
				return true
			}
			if len(res.Events) < eventsBatch {
				break
			}
		}
		select {
		case <-r.Context().Done():
			return true
		case <-wake:
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || rc.Flush() != nil {
				return true
			}
		}
	}
}

func wantsEventStream(r *http.Request) bool {
	for _, a := range r.Header.Values("Accept") {
		if a == "text/event-stream" || (len(a) >= 17 && a[:17] == "text/event-stream") {
			return true
		}
	}
	return r.URL.Query().Get("stream") == "1"
}

// eventsQuiet reads a batch without an audit record when the client is in
// process; a socket client reads through the daemon's audited route.
func eventsQuiet(ctx context.Context, c HostEventsClient, q EventsQuery) (EventsResult, error) {
	if ip, ok := c.(*InProcessClient); ok {
		s, err := ip.hostService()
		if err != nil {
			return EventsResult{}, err
		}
		return s.hostEvents(ctx, q)
	}
	return c.HostEvents(ctx, q)
}
