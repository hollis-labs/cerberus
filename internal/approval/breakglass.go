package approval

import (
	"errors"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
)

// Break glass (P3-5b): a person gets past an approve decision on their own
// call. Each use is an approval with BreakGlass set, so it is rate-limited
// per target, listed, and a follow-up until the operator acknowledges it.

// ErrNotBreakGlass is an acknowledgment of something that is not an
// unacknowledged break-glass use.
var ErrNotBreakGlass = errors.New("not an unacknowledged break-glass use")

// BreakGlassSince is the break-glass uses on target since since, oldest
// first: every one asked for, whether used, pending or still approved. A
// denied or expired one never ran and does not count.
func (s *Store) BreakGlassSince(target audit.Target, since time.Time) []Approval {
	var out []Approval
	for _, a := range s.List() {
		if a.BreakGlass == nil || a.CreatedAt.Before(since) || !sameTarget(a.Target, target) {
			continue
		}
		if a.Status == Denied || a.Status == Expired || a.Status == Revoked {
			continue
		}
		out = append(out, a)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// UnackedBreakGlass are the break-glass uses that ran and whose follow-up
// is still open, newest first.
func (s *Store) UnackedBreakGlass() []Approval {
	var out []Approval
	for _, a := range s.List() {
		if a.BreakGlass != nil && a.BreakGlass.AckedAt.IsZero() && a.Status == Consumed {
			out = append(out, a)
		}
	}
	return out
}

// AckBreakGlass closes a break-glass use's follow-up.
func (s *Store) AckBreakGlass(id string, by Decision) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.state[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	if a.BreakGlass == nil || !a.BreakGlass.AckedAt.IsZero() {
		return *a, ErrNotBreakGlass
	}
	if err := s.append(Event{Type: EventAcked, ApprovalID: id, Decision: &by}); err != nil {
		return Approval{}, err
	}
	return *s.state[id], nil
}
