// Package brake is the emergency brake (§12): a lockdown that makes the
// whole Cerberus read-only, and freezes that do the same for the targets
// they match. Both apply before policy, in every enforcement mode, shadow
// included: they are the operator's brake, not policy.
//
// Engaging is trivially easy, from any process; lifting is protected (the
// caller's job, cerbapi). The state lives in an append-only, hash-chained
// store any process may append to under an exclusive lock, and every change
// is also a brake_changed audit record. The effective state is the more
// restrictive of the two (Effective), so deleting or editing the store
// cannot lift a brake the audit log recorded.
package brake

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// FileName is the store in its directory.
const FileName = "events.jsonl"

// Event types.
const (
	EventLockdownEngaged = "lockdown_engaged"
	EventLockdownLifted  = "lockdown_lifted"
	EventFreezeEngaged   = "freeze_engaged"
	EventFreezeLifted    = "freeze_lifted"
	EventSuspended       = "session_suspended"
	EventSuspensionReset = "session_reset"
	// EventReanchored marks where an append took up a store whose chain was
	// broken: events after it chain from it and are trusted again. Between
	// the break and it, only what engages a brake is applied (M1).
	EventReanchored = "reanchored"
)

// Lockdown is the whole Cerberus read-only.
type Lockdown struct {
	ID        string          `json:"id"`
	EngagedAt time.Time       `json:"engaged_at"`
	By        audit.Principal `json:"by"`
	Reason    string          `json:"reason,omitempty"`
}

// Freeze is the targets a match selects read-only.
type Freeze struct {
	ID        string             `json:"id"`
	Match     policy.TargetMatch `json:"match"`
	EngagedAt time.Time          `json:"engaged_at"`
	By        audit.Principal    `json:"by"`
	Reason    string             `json:"reason,omitempty"`
}

// MarshalJSON adds the match as a scope string, for a surface to show;
// reading a freeze back ignores it.
func (f Freeze) MarshalJSON() ([]byte, error) {
	type plain Freeze
	return json.Marshal(struct {
		plain
		Scope string `json:"scope"`
	}{plain(f), f.Match.String()})
}

// Suspension is one caller the circuit breaker stopped (P5-c): too many
// real policy denials in its window. It stays until a person resets it.
type Suspension struct {
	ID string `json:"id"`
	// Key is the caller, as the gate counts it: kind, via and session, or
	// client and uid where there is no session.
	Key       string          `json:"key"`
	Principal audit.Principal `json:"principal"`
	TrippedAt time.Time       `json:"tripped_at"`
	Denials   int             `json:"denials"`
	Window    string          `json:"window"`
}

// State is what is braked.
type State struct {
	Lockdown    *Lockdown    `json:"lockdown,omitempty"`
	Freezes     []Freeze     `json:"freezes,omitempty"`
	Suspensions []Suspension `json:"suspensions,omitempty"`
}

// SuspensionFor is the suspension of the caller with key, if any.
func (s State) SuspensionFor(key string) (Suspension, bool) {
	for _, x := range s.Suspensions {
		if x.Key == key {
			return x, true
		}
	}
	return Suspension{}, false
}

// Suspension is the suspension with id.
func (s State) Suspension(id string) (Suspension, bool) {
	for _, x := range s.Suspensions {
		if x.ID == id {
			return x, true
		}
	}
	return Suspension{}, false
}

// Engaged reports whether anything is braked.
func (s State) Engaged() bool { return s.Lockdown != nil || len(s.Freezes) > 0 }

// Freeze is the freeze with id.
func (s State) Freeze(id string) (Freeze, bool) {
	for _, f := range s.Freezes {
		if f.ID == id {
			return f, true
		}
	}
	return Freeze{}, false
}

// Blocks reports whether the brakes refuse an operation, and which brake.
// Only a plain read passes: read_sensitive does not, since a lockdown is
// when secrets should not leave either.
func (s State) Blocks(connector string, effect contract.Effect, t target.Target) (bool, *Lockdown, *Freeze) {
	if effect == contract.EffectRead {
		return false, nil, nil
	}
	if s.Lockdown != nil {
		return true, s.Lockdown, nil
	}
	if f, ok := s.FreezeCovering(connector, t); ok {
		return true, nil, &f
	}
	return false, nil, nil
}

// FreezeCovering is the freeze whose match covers a target, if any.
func (s State) FreezeCovering(connector string, t target.Target) (Freeze, bool) {
	for _, f := range s.Freezes {
		if f.Match.Matches(connector, t) {
			return f, true
		}
	}
	return Freeze{}, false
}

// Effective is the more restrictive of the store's state and the state the
// newest brake_changed record in a verified audit chain carries: a lockdown
// either knows of, and every freeze either knows of.
func Effective(store, recorded State) State {
	out := State{Lockdown: store.Lockdown}
	if out.Lockdown == nil {
		out.Lockdown = recorded.Lockdown
	}
	seen := map[string]bool{}
	for _, list := range [][]Freeze{store.Freezes, recorded.Freezes} {
		for _, f := range list {
			if !seen[f.ID] {
				seen[f.ID] = true
				out.Freezes = append(out.Freezes, f)
			}
		}
	}
	for _, list := range [][]Suspension{store.Suspensions, recorded.Suspensions} {
		for _, x := range list {
			if !seen[x.ID] {
				seen[x.ID] = true
				out.Suspensions = append(out.Suspensions, x)
			}
		}
	}
	return out
}

// Recorded is the brake state the audit log carries: the newest
// brake_changed record the chain vouches for, and every brake engaged in a
// record past a break (audit.Check). A broken chain therefore keeps every
// brake it ever showed engaged after the break, rather than falling back to
// the store alone (M1): editing a line of the log is not a way to lift a
// brake. A person restores trust with cerberus audit reanchor, after which
// the brakes are lifted as usual.
//
// problems says why the log was not read as a whole: its chain does not
// verify, or it could not be read.
func Recorded(auditDir string) (State, []string) {
	if auditDir == "" {
		return State{}, nil
	}
	checked, err := audit.Check(auditDir)
	if err != nil {
		return State{}, []string{"the audit log could not be read, so the brakes are read from their store alone: " + err.Error()}
	}
	var st State
	var problems []string
	for i, r := range checked.Records {
		if r.Kind != audit.KindBrakeChanged || len(r.Brakes) == 0 {
			continue
		}
		var s State
		if err := json.Unmarshal(r.Brakes, &s); err != nil {
			problems = append(problems, fmt.Sprintf("audit seq %d: brake state does not parse", r.Seq))
			continue
		}
		if checked.Trusted[i] {
			st = s
			continue
		}
		st = Effective(st, s)
	}
	switch {
	case !checked.TailTrusted():
		problems = append(problems, "the audit log's chain does not verify, so a brake engaged past the break stays engaged and a lift recorded there is not applied; a person restores it with `cerberus audit reanchor`")
	case len(checked.Problems) > 0:
		problems = append(problems, fmt.Sprintf("the audit log's chain has %d problem(s) before its last reanchor; brakes are read from the reanchor on", len(checked.Problems)))
	}
	return st, problems
}

// Event is one line of the store.
type Event struct {
	V        int       `json:"v"`
	Seq      uint64    `json:"seq"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Lockdown *Lockdown `json:"lockdown,omitempty"`
	Freeze   *Freeze   `json:"freeze,omitempty"`
	// Suspension is a session_suspended event's caller.
	Suspension *Suspension      `json:"suspension,omitempty"`
	ID         string           `json:"id,omitempty"`
	By         *audit.Principal `json:"by,omitempty"`
	// Proof is how a lift was authorized: "passkey:<approval id>", "tty"
	// where no passkey is enrolled, or "typed" for a suspension reset. A
	// lift or reset without one is not applied (M1).
	Proof string `json:"proof,omitempty"`
	// Restored marks an engage written back from the audit log, for a
	// brake the log holds and the store had lost (Store.Restore).
	Restored bool   `json:"restored,omitempty"`
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

// Store is the brake store in a directory.
type Store struct {
	Dir string
	Now func() time.Time
}

func (s Store) path() string { return filepath.Join(s.Dir, FileName) }

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Load folds the store. A damaged line or a broken chain is reported and
// skipped, never fatal: past a break only what engages a brake is applied,
// so editing the store can add a brake but not lift one.
func (s Store) Load() (State, []string) {
	f := s.fold()
	return f.state, f.problems
}

type folded struct {
	state    State
	seq      uint64
	last     string
	problems []string
	// broken is a break not yet followed by a reanchor.
	broken bool
}

func (s Store) fold() folded {
	var f folded
	data, err := os.ReadFile(s.path())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			f.problems = append(f.problems, err.Error())
		}
		return f
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			f.problems = append(f.problems, fmt.Sprintf("line after seq %d does not parse", f.seq))
			f.broken = true
			continue
		}
		chained := ev.Seq == f.seq+1 && ev.PrevHash == f.last && (audit.LineHashMatches(line, ev.Hash) || hashEvent(ev) == ev.Hash)
		if !chained {
			f.problems = append(f.problems, fmt.Sprintf("seq %d does not chain to the one before it", ev.Seq))
			f.broken = true
		}
		switch {
		case ev.Type == EventReanchored && chained:
			f.broken = false
		case lifts(ev) && ev.Proof == "" && !legacyReset(ev):
			f.problems = append(f.problems, fmt.Sprintf("seq %d (%s) carries no proof and is not applied", ev.Seq, ev.Type))
		case f.broken && lifts(ev):
			f.problems = append(f.problems, fmt.Sprintf("seq %d (%s) is past a broken chain and is not applied", ev.Seq, ev.Type))
		default:
			apply(&f.state, ev)
		}
		f.seq, f.last = ev.Seq, ev.Hash
	}
	return f
}

// eventVersion is the store's event format. Version 2 records a proof on a
// suspension reset, which version 1 did not.
const eventVersion = 2

// legacyReset is a suspension reset written before resets carried their
// proof: applied as it always was, so upgrading does not bring back a
// suspension a person reset. A lockdown or freeze lift always carried one.
func legacyReset(ev Event) bool {
	return ev.Type == EventSuspensionReset && ev.V < eventVersion
}

// lifts reports whether an event takes a brake off: what a broken chain or
// a missing proof does not let through.
func lifts(ev Event) bool {
	switch ev.Type {
	case EventLockdownLifted, EventFreezeLifted, EventSuspensionReset:
		return true
	}
	return false
}

func apply(st *State, ev Event) {
	switch ev.Type {
	case EventLockdownEngaged:
		if st.Lockdown == nil && ev.Lockdown != nil {
			l := *ev.Lockdown
			st.Lockdown = &l
		}
	case EventLockdownLifted:
		if st.Lockdown != nil && st.Lockdown.ID == ev.ID {
			st.Lockdown = nil
		}
	case EventFreezeEngaged:
		if ev.Freeze != nil {
			if _, ok := st.Freeze(ev.Freeze.ID); !ok {
				st.Freezes = append(st.Freezes, *ev.Freeze)
			}
		}
	case EventFreezeLifted:
		for i, f := range st.Freezes {
			if f.ID == ev.ID {
				st.Freezes = append(st.Freezes[:i], st.Freezes[i+1:]...)
				break
			}
		}
	case EventSuspended:
		if ev.Suspension != nil {
			if _, ok := st.SuspensionFor(ev.Suspension.Key); !ok {
				st.Suspensions = append(st.Suspensions, *ev.Suspension)
			}
		}
	case EventSuspensionReset:
		for i, x := range st.Suspensions {
			if x.ID == ev.ID {
				st.Suspensions = append(st.Suspensions[:i], st.Suspensions[i+1:]...)
				break
			}
		}
	}
}

func hashEvent(ev Event) string {
	ev.Hash = ""
	data, _ := json.Marshal(ev)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// append folds and appends one event under an exclusive lock, so appends
// from different processes chain, and returns the state after it.
func (s Store) append(ev Event, valid func(State) error) (State, error) {
	if lifts(ev) && ev.Proof == "" {
		return State{}, errors.New("brakes: a lift needs its proof")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return State{}, fmt.Errorf("brakes: %w", err)
	}
	f, err := os.OpenFile(s.path(), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // the store's own file
	if err != nil {
		return State{}, fmt.Errorf("brakes: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // a file descriptor fits an int
		return State{}, fmt.Errorf("brakes: lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }() //nolint:gosec // as above
	cur := s.fold()
	st := cur.state
	if valid != nil {
		if err = valid(st); err != nil {
			return st, err
		}
	}
	var out []byte
	seq, last := cur.seq, cur.last
	events := []Event{ev}
	if cur.broken {
		// Take the broken store up here, so what this appends counts; a
		// lift appended without it would never apply.
		events = []Event{{Type: EventReanchored}, ev}
	}
	for i := range events {
		e := &events[i]
		e.V, e.Seq, e.Time, e.PrevHash = eventVersion, seq+1, s.now(), last
		e.Hash = hashEvent(*e)
		line, err := json.Marshal(*e)
		if err != nil {
			return State{}, err
		}
		out = append(append(out, line...), '\n')
		seq, last = e.Seq, e.Hash
	}
	if _, err := f.Write(out); err != nil {
		return State{}, fmt.Errorf("brakes: append: %w", err)
	}
	if err := f.Sync(); err != nil {
		return State{}, fmt.Errorf("brakes: sync: %w", err)
	}
	apply(&st, ev)
	return st, nil
}

// ErrNotEngaged is a lift of a brake that is not engaged.
var ErrNotEngaged = errors.New("that brake is not engaged")

// EngageLockdown engages the lockdown, or returns the one already engaged.
func (s Store) EngageLockdown(by audit.Principal, reason string) (State, *Lockdown, error) {
	l := &Lockdown{ID: newID("ldn"), EngagedAt: s.now(), By: by, Reason: reason}
	st, err := s.append(Event{Type: EventLockdownEngaged, Lockdown: l, By: &by}, func(cur State) error {
		if cur.Lockdown != nil {
			l = cur.Lockdown
			return errAlready
		}
		return nil
	})
	if errors.Is(err, errAlready) {
		return st, l, nil
	}
	return st, l, err
}

// LiftLockdown lifts the lockdown with id.
func (s Store) LiftLockdown(id string, by audit.Principal, proof string) (State, error) {
	return s.append(Event{Type: EventLockdownLifted, ID: id, By: &by, Proof: proof}, func(cur State) error {
		if cur.Lockdown == nil || cur.Lockdown.ID != id {
			return ErrNotEngaged
		}
		return nil
	})
}

// EngageFreeze freezes the targets match selects, or returns the freeze
// already on that match.
func (s Store) EngageFreeze(match policy.TargetMatch, by audit.Principal, reason string) (State, *Freeze, error) {
	f := &Freeze{ID: newID("frz"), Match: match, EngagedAt: s.now(), By: by, Reason: reason}
	st, err := s.append(Event{Type: EventFreezeEngaged, Freeze: f, By: &by}, func(cur State) error {
		for _, existing := range cur.Freezes {
			if existing.Match.String() == match.String() {
				e := existing
				f = &e
				return errAlready
			}
		}
		return nil
	})
	if errors.Is(err, errAlready) {
		return st, f, nil
	}
	return st, f, err
}

// LiftFreeze lifts the freeze with id.
func (s Store) LiftFreeze(id string, by audit.Principal, proof string) (State, error) {
	return s.append(Event{Type: EventFreezeLifted, ID: id, By: &by, Proof: proof}, func(cur State) error {
		if _, ok := cur.Freeze(id); !ok {
			return ErrNotEngaged
		}
		return nil
	})
}

var errAlready = errors.New("already engaged")

func newID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Suspend suspends the caller with key, or returns its suspension if it is
// already suspended.
func (s Store) Suspend(key string, p audit.Principal, denials int, window string) (State, Suspension, error) {
	x := Suspension{ID: newID("sus"), Key: key, Principal: p, TrippedAt: s.now(), Denials: denials, Window: window}
	st, err := s.append(Event{Type: EventSuspended, Suspension: &x}, func(cur State) error {
		if prev, ok := cur.SuspensionFor(key); ok {
			x = prev
			return errAlready
		}
		return nil
	})
	if errors.Is(err, errAlready) {
		return st, x, nil
	}
	return st, x, err
}

// ResetSuspension ends the suspension with id. proof is how the reset was
// authorized ("typed", the phrase a person typed).
func (s Store) ResetSuspension(id string, by audit.Principal, proof string) (State, error) {
	return s.append(Event{Type: EventSuspensionReset, ID: id, By: &by, Proof: proof}, func(cur State) error {
		if _, ok := cur.Suspension(id); !ok {
			return ErrNotEngaged
		}
		return nil
	})
}

// Restore writes back, as engaged, each brake in recorded that the store
// does not hold: a lockdown when the store has none, and each freeze and
// suspension by id. The audit log is what recorded comes from (Recorded);
// a brake it holds and the store lost is engaged either way (Effective),
// and restoring it is what lets a person lift it again, since a lift is
// checked against the store. It returns the store's state after.
func (s Store) Restore(recorded State) (State, error) {
	st, _ := s.Load()
	var err error
	if l := recorded.Lockdown; l != nil && st.Lockdown == nil {
		lock := *l
		if st, err = s.append(Event{Type: EventLockdownEngaged, Lockdown: &lock, By: &lock.By, Restored: true}, func(cur State) error {
			if cur.Lockdown != nil {
				return errAlready
			}
			return nil
		}); err != nil && !errors.Is(err, errAlready) {
			return st, err
		}
	}
	for _, f := range recorded.Freezes {
		if _, ok := st.Freeze(f.ID); ok {
			continue
		}
		frz := f
		if st, err = s.append(Event{Type: EventFreezeEngaged, Freeze: &frz, By: &frz.By, Restored: true}, func(cur State) error {
			if _, ok := cur.Freeze(frz.ID); ok {
				return errAlready
			}
			return nil
		}); err != nil && !errors.Is(err, errAlready) {
			return st, err
		}
	}
	for _, x := range recorded.Suspensions {
		if _, ok := st.Suspension(x.ID); ok {
			continue
		}
		if _, ok := st.SuspensionFor(x.Key); ok {
			continue
		}
		sus := x
		if st, err = s.append(Event{Type: EventSuspended, Suspension: &sus, Restored: true}, func(cur State) error {
			if _, ok := cur.SuspensionFor(sus.Key); ok {
				return errAlready
			}
			return nil
		}); err != nil && !errors.Is(err, errAlready) {
			return st, err
		}
	}
	st, _ = s.Load()
	return st, nil
}
