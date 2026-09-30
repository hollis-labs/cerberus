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

// State is what is braked.
type State struct {
	Lockdown *Lockdown `json:"lockdown,omitempty"`
	Freezes  []Freeze  `json:"freezes,omitempty"`
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
	return out
}

// Recorded is the state the newest brake_changed record carries, provided
// the audit chain verifies. It reports false when it does not, or when no
// brake was ever recorded.
func Recorded(auditDir string) (State, bool) {
	if auditDir == "" || audit.Verify(auditDir) != nil {
		return State{}, false
	}
	records, err := audit.ReadRecords(auditDir)
	if err != nil {
		return State{}, false
	}
	for i := len(records) - 1; i >= 0; i-- {
		if r := records[i]; r.Kind == audit.KindBrakeChanged && len(r.Brakes) > 0 {
			var s State
			if err := json.Unmarshal(r.Brakes, &s); err != nil {
				return State{}, false
			}
			return s, true
		}
	}
	return State{}, true
}

// Event is one line of the store.
type Event struct {
	V        int              `json:"v"`
	Seq      uint64           `json:"seq"`
	Time     time.Time        `json:"time"`
	Type     string           `json:"type"`
	Lockdown *Lockdown        `json:"lockdown,omitempty"`
	Freeze   *Freeze          `json:"freeze,omitempty"`
	ID       string           `json:"id,omitempty"`
	By       *audit.Principal `json:"by,omitempty"`
	// Proof is how a lift was authorized: "passkey:<approval id>", or
	// "tty" where no passkey is enrolled.
	Proof    string `json:"proof,omitempty"`
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
// skipped, never fatal.
func (s Store) Load() (State, []string) {
	st, _, _, problems := s.fold()
	return st, problems
}

func (s Store) fold() (State, uint64, string, []string) {
	var st State
	var seq uint64
	var last string
	var problems []string
	data, err := os.ReadFile(s.path())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, err.Error())
		}
		return st, 0, "", problems
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
			problems = append(problems, fmt.Sprintf("line after seq %d does not parse", seq))
			continue
		}
		if ev.Seq != seq+1 || ev.PrevHash != last || hashEvent(ev) != ev.Hash {
			problems = append(problems, fmt.Sprintf("seq %d does not chain to the one before it", ev.Seq))
		}
		apply(&st, ev)
		seq, last = ev.Seq, ev.Hash
	}
	return st, seq, last, problems
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
	st, seq, last, _ := s.fold()
	if valid != nil {
		if err = valid(st); err != nil {
			return st, err
		}
	}
	ev.V, ev.Seq, ev.Time, ev.PrevHash = 1, seq+1, s.now(), last
	ev.Hash = hashEvent(ev)
	line, err := json.Marshal(ev)
	if err != nil {
		return State{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
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
