package approval

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
	"sort"
	"sync"
	"time"
)

// Event types, one per transition.
const (
	EventRequested = "requested"
	EventDecided   = "decided"
	EventExpired   = "expired"
	EventConsumed  = "consumed"
	EventRevoked   = "revoked"
)

// Event is one line of the store: a transition of one approval, hash-chained
// to the line before it like the audit log. The chain makes an edit
// evident; it does not make the store trustworthy, because anything that can
// write the file can rebuild the chain (§0).
type Event struct {
	V          int       `json:"v"`
	Seq        uint64    `json:"seq"`
	Time       time.Time `json:"time"`
	Type       string    `json:"type"`
	ApprovalID string    `json:"approval_id"`

	// Approval is the whole request, on a requested event.
	Approval *Approval `json:"approval,omitempty"`
	// Decision is the decision, on a decided event.
	Decision *Decision `json:"decision,omitempty"`
	// OperationID links a consumed event to the audit intent of the
	// operation it let through.
	OperationID string `json:"operation_id,omitempty"`

	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

const eventVersion = 1

// Errors a transition refuses with. The broker's callers code them.
var (
	ErrNotFound       = errors.New("no such approval")
	ErrNotPending     = errors.New("the approval is not pending")
	ErrNotApproved    = errors.New("the approval is not approved")
	ErrExpired        = errors.New("the approval has expired")
	ErrArgsMismatch   = errors.New("the call's arguments are not the ones approved")
	ErrPlanStale      = errors.New("the plan no longer matches the one approved")
	ErrNoPresence     = errors.New("the out-of-band decision carries no presence proof this Cerberus can verify")
	ErrPolicyNowDenys = errors.New("policy now denies the operation")
)

// Store is the event store and the state folded from it.
type Store struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	state map[string]*Approval
	seq   uint64
	last  string

	// Problems are what the fold found wrong with the file: a broken
	// chain, a line that did not parse, a transition that did not apply.
	Problems []string
}

// FileName is the store's file under its directory.
const FileName = "events.jsonl"

// Open opens (creating if needed) the store in dir and folds it into state.
func Open(dir string) (*Store, error) {
	return open(dir, time.Now)
}

func open(dir string, now func() time.Time) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("approvals: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory: 0700 is owner-only
		return nil, fmt.Errorf("approvals: restrict %s: %w", dir, err)
	}
	s := &Store{path: filepath.Join(dir, FileName), now: now, state: map[string]*Approval{}}
	if err := s.fold(); err != nil {
		return nil, err
	}
	return s, nil
}

// fold replays the file into state. A line that does not parse, a broken
// chain, or a transition that is not allowed is reported and skipped, never
// fatal: a damaged store must not take the daemon down, and nothing in it is
// trusted to authorize anything on its own.
func (s *Store) fold() error {
	data, err := os.ReadFile(s.path) //nolint:gosec // the store's own file
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("approvals: read %s: %w", s.path, err)
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
			s.Problems = append(s.Problems, fmt.Sprintf("line after seq %d does not parse", s.seq))
			continue
		}
		if ev.Seq != s.seq+1 || ev.PrevHash != s.last || hashEvent(ev) != ev.Hash {
			s.Problems = append(s.Problems, fmt.Sprintf("seq %d does not chain to the one before it", ev.Seq))
		}
		if err := s.apply(ev); err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("seq %d: %v", ev.Seq, err))
		}
		s.seq, s.last = ev.Seq, ev.Hash
	}
	return scanner.Err()
}

// apply moves state by one event, refusing a transition the lifecycle does
// not allow.
func (s *Store) apply(ev Event) error {
	if ev.Type == EventRequested {
		if ev.Approval == nil || ev.Approval.ID != ev.ApprovalID {
			return errors.New("a requested event without its approval")
		}
		if _, dup := s.state[ev.ApprovalID]; dup {
			return fmt.Errorf("approval %s requested twice", ev.ApprovalID)
		}
		a := *ev.Approval
		a.Status = Pending
		s.state[a.ID] = &a
		return nil
	}
	a, ok := s.state[ev.ApprovalID]
	if !ok {
		return fmt.Errorf("%s event for unknown approval %s", ev.Type, ev.ApprovalID)
	}
	switch ev.Type {
	case EventDecided:
		if a.Status != Pending || ev.Decision == nil {
			return fmt.Errorf("decided from %s", a.Status)
		}
		d := *ev.Decision
		a.Decision = &d
		if d.Approve {
			a.Status = Approved
		} else {
			a.Status = Denied
		}
	case EventExpired:
		if a.Status != Pending && a.Status != Approved {
			return fmt.Errorf("expired from %s", a.Status)
		}
		a.Status = Expired
	case EventConsumed:
		if a.Status != Approved {
			return fmt.Errorf("consumed from %s", a.Status)
		}
		a.Status, a.ConsumedAt, a.ConsumedOperationID = Consumed, ev.Time, ev.OperationID
	case EventRevoked:
		if a.Status != Approved {
			return fmt.Errorf("revoked from %s", a.Status)
		}
		a.Status, a.RevokedAt = Revoked, ev.Time
		if ev.Decision != nil {
			by := ev.Decision.By
			a.RevokedBy = &by
		}
	default:
		return fmt.Errorf("unknown event type %q", ev.Type)
	}
	return nil
}

// append writes one event, fsynced, then applies it. Called with s.mu held.
func (s *Store) append(ev Event) error {
	ev.V, ev.Seq, ev.Time, ev.PrevHash = eventVersion, s.seq+1, s.now().UTC(), s.last
	ev.Hash = ""
	ev.Hash = hashEvent(ev)
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // the store's own file
	if err != nil {
		return fmt.Errorf("approvals: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("approvals: append: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("approvals: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.apply(ev); err != nil {
		return err
	}
	s.seq, s.last = ev.Seq, ev.Hash
	return nil
}

func hashEvent(ev Event) string {
	ev.Hash = ""
	data, _ := json.Marshal(ev)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NewID is a random approval id, short enough to type.
func NewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("approval: no randomness: " + err.Error())
	}
	return "apr_" + hex.EncodeToString(b[:])
}

// Request records a new pending approval and returns it.
func (s *Store) Request(a Approval, ttl time.Duration) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = NewID()
	}
	now := s.now().UTC()
	a.Status, a.CreatedAt = Pending, now
	if ttl > 0 {
		a.ExpiresAt = now.Add(ttl)
	}
	if a.Scope == "" {
		a.Scope = ScopeOnce
	}
	a.Decision = nil
	if err := s.append(Event{Type: EventRequested, ApprovalID: a.ID, Approval: &a}); err != nil {
		return Approval{}, err
	}
	return *s.state[a.ID], nil
}

// Get is an approval as it reads now.
func (s *Store) Get(id string) (Approval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.state[id]
	if !ok {
		return Approval{}, false
	}
	return a.viewAt(s.now()), true
}

// List is every approval as it reads now, newest first.
func (s *Store) List() []Approval {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]Approval, 0, len(s.state))
	for _, a := range s.state {
		out = append(out, a.viewAt(now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Sweep writes the expired event for every approval past its expiry and
// returns them.
func (s *Store) Sweep() ([]Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var expired []Approval
	ids := make([]string, 0, len(s.state))
	for id := range s.state {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := s.state[id]
		if a.viewAt(now).Status == Expired && a.Status != Expired {
			if err := s.append(Event{Type: EventExpired, ApprovalID: id}); err != nil {
				return expired, err
			}
			expired = append(expired, *s.state[id])
		}
	}
	return expired, nil
}

// PresenceVerifier checks an out-of-band decision's presence proof against
// the enrolled keys (P3-4). Until one is installed, no out-of-band decision
// verifies, so none is consumed: the store's word is never enough.
type PresenceVerifier interface {
	Verify(a Approval, d Decision) error
}

// NoPresence is the verifier before P3-4: it verifies nothing.
type NoPresence struct{}

// Verify implements PresenceVerifier.
func (NoPresence) Verify(Approval, Decision) error { return ErrNoPresence }

// Decide records an approve or deny on a pending approval. An out-of-band
// decision must verify now, and again at consume, because the file it is
// stored in can be edited between the two.
func (s *Store) Decide(id string, d Decision, verifier PresenceVerifier) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.state[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	view := a.viewAt(s.now())
	switch {
	case view.Status == Expired:
		return view, ErrExpired
	case view.Status != Pending:
		return view, ErrNotPending
	}
	if d.Approve && a.Channel == ChannelOutOfBand {
		if verifier == nil {
			verifier = NoPresence{}
		}
		if err := verifier.Verify(*a, d); err != nil {
			return view, err
		}
	}
	d.At = s.now().UTC()
	if err := s.append(Event{Type: EventDecided, ApprovalID: id, Decision: &d}); err != nil {
		return Approval{}, err
	}
	return *s.state[id], nil
}

// ConsumeCheck is what a consume must match: the call's arguments and plan,
// the audit operation it will run as, and policy re-evaluated now (D8).
type ConsumeCheck struct {
	ArgsDigest  string
	PlanHash    string
	OperationID string
	// Reauthorized is the policy decision now: a deny refuses.
	ReauthorizedDeny bool
}

// Consume spends an approved approval on one call, write-ahead: the consumed
// event is durable before the caller runs anything. It refuses an approval
// that is not approved, has expired, was approved for other arguments or
// another plan, whose out-of-band proof does not verify, or that policy now
// denies.
func (s *Store) Consume(id string, check ConsumeCheck, verifier PresenceVerifier) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.state[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	view := a.viewAt(s.now())
	switch {
	case view.Status == Expired:
		return view, ErrExpired
	case view.Status != Approved:
		return view, ErrNotApproved
	case check.ArgsDigest != a.ArgsDigest:
		return view, ErrArgsMismatch
	case a.PlanHash != "" && check.PlanHash != a.PlanHash:
		return view, ErrPlanStale
	case check.ReauthorizedDeny:
		return view, ErrPolicyNowDenys
	}
	if a.Channel == ChannelOutOfBand {
		if verifier == nil {
			verifier = NoPresence{}
		}
		if a.Decision == nil {
			return view, ErrNoPresence
		}
		if err := verifier.Verify(*a, *a.Decision); err != nil {
			return view, err
		}
	}
	if err := s.append(Event{Type: EventConsumed, ApprovalID: id, OperationID: check.OperationID}); err != nil {
		return Approval{}, err
	}
	return *s.state[id], nil
}

// Revoke withdraws an approved approval before it is used.
func (s *Store) Revoke(id string, by Decision) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.state[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	view := a.viewAt(s.now())
	if view.Status != Approved {
		return view, ErrNotApproved
	}
	by.At = s.now().UTC()
	if err := s.append(Event{Type: EventRevoked, ApprovalID: id, Decision: &by}); err != nil {
		return Approval{}, err
	}
	return *s.state[id], nil
}
