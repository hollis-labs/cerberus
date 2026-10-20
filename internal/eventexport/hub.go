package eventexport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/libs/mesh/eventbus"
	"github.com/hollis-labs/libs/mesh/eventbus/relay"
)

// HubItem turns an outbox entry into what the hub ingest takes. The hub owns the
// actor, the record time and the cursor, so a producer never sends them: they are
// cleared here, on a copy. The outbox entry itself is not changed. Everything the
// exporter writes is a state, approval, audit or alert event, so it is sent as
// protected (the relay sends from a durable outbox and never drops, so the
// priority only matters to the client's queue, which Deliver bypasses).
func HubItem(e Entry) (eventbus.Item, error) {
	env := e.Envelope
	env.Actor = nil
	env.RecordedAt = nil
	env.Cursor = ""
	body, err := json.Marshal(env)
	if err != nil {
		return eventbus.Item{}, fmt.Errorf("eventexport: outbox entry %d: %w", e.Seq, err)
	}
	return eventbus.Item{ID: env.ID, Priority: eventbus.PriorityHigh, Body: body}, nil
}

// RelaySource reads an Outbox for a relay.Relay. It also reports the head so the
// relay can say how far behind it is.
type RelaySource struct{ Outbox Outbox }

var (
	_ relay.Source     = RelaySource{}
	_ relay.HeadSource = RelaySource{}
)

// After implements relay.Source.
func (s RelaySource) After(ctx context.Context, after int64, limit int) ([]relay.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if after < 0 {
		after = 0
	}
	entries, err := s.Outbox.After(uint64(after), limit) //nolint:gosec // after is checked non-negative above
	if err != nil {
		return nil, err
	}
	out := make([]relay.Entry, 0, len(entries))
	for _, e := range entries {
		item, err := HubItem(e)
		if err != nil {
			return nil, err
		}
		out = append(out, relay.Entry{Seq: int64(e.Seq), Item: item}) //nolint:gosec // outbox sequence numbers stay far below 2^63
	}
	return out, nil
}

// Head implements relay.HeadSource.
func (s RelaySource) Head(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	e, ok, err := s.Outbox.Head()
	if err != nil || !ok {
		return 0, err
	}
	return int64(e.Seq), nil //nolint:gosec // outbox sequence numbers stay far below 2^63
}
