package eventexport

import (
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
)

// gapKey names one approval-store event that should have a matching audit
// record: the approval and the transition.
type gapKey struct {
	approvalID string
	transition string // requested, decided, expired, revoked
}

// auditKindOf is the audit record kind that accompanies each approval
// transition (internal/cerbapi/approvals.go writes it after the store).
var auditKindOf = map[string]string{
	approval.EventRequested: audit.KindApprovalRequested,
	approval.EventDecided:   audit.KindApprovalDecided,
	approval.EventExpired:   audit.KindApprovalExpired,
	approval.EventRevoked:   audit.KindApprovalRevoked,
}

var transitionOfAuditKind = func() map[string]string {
	out := map[string]string{}
	for transition, kind := range auditKindOf {
		out[kind] = transition
	}
	return out
}()

// GapDetector notices an approval-store event that has no audit record after a
// grace window. The audit write follows the store write and can fail
// (cerbapi/approvals.go logs it and goes on), so a store event may have no audit
// counterpart. It never invents the missing record: it only reports the gap,
// once per event.
//
// It remembers what it has seen only in memory, seeded at start from the recent
// audit chain, so a restart inside the grace window can miss one alert; the
// authoritative stores are unaffected.
type GapDetector struct {
	grace time.Duration

	mu      sync.Mutex
	waiting map[gapKey]time.Time // store event seen, audit record not yet
	seen    map[gapKey]time.Time // audit record seen, store event not yet
	alerted map[gapKey]bool
}

// NewGapDetector returns a detector with the given grace window.
func NewGapDetector(grace time.Duration) *GapDetector {
	return &GapDetector{grace: grace, waiting: map[gapKey]time.Time{}, seen: map[gapKey]time.Time{}, alerted: map[gapKey]bool{}}
}

// ObserveApproval records a store event that expects an audit record.
func (g *GapDetector) ObserveApproval(ev approval.Event) {
	if _, ok := auditKindOf[ev.Type]; !ok {
		return
	}
	key := gapKey{ev.ApprovalID, ev.Type}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, matched := g.seen[key]; matched {
		delete(g.seen, key)
		return
	}
	g.waiting[key] = ev.Time
}

// ObserveAudit records an audit record, matching it to a waiting store event.
func (g *GapDetector) ObserveAudit(rec audit.Record) {
	transition, ok := transitionOfAuditKind[rec.Kind]
	if !ok || rec.ApprovalID == "" {
		return
	}
	key := gapKey{rec.ApprovalID, transition}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, waiting := g.waiting[key]; waiting {
		delete(g.waiting, key)
		return
	}
	g.seen[key] = rec.Time
}

// Gap is an approval-store event with no audit record after the grace window.
type Gap struct {
	ApprovalID string
	Transition string
	StoreTime  time.Time
}

// Due returns the gaps whose grace window has passed at now and marks them
// alerted, so each is returned once. It also forgets audit records that no
// store event claimed within twice the window.
func (g *GapDetector) Due(now time.Time) []Gap {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Gap
	for key, at := range g.waiting {
		if now.Sub(at) >= g.grace && !g.alerted[key] {
			g.alerted[key] = true
			delete(g.waiting, key)
			out = append(out, Gap{key.approvalID, key.transition, at})
		}
	}
	for key, at := range g.seen {
		if now.Sub(at) >= 2*g.grace {
			delete(g.seen, key)
		}
	}
	return out
}

// Key is the outbox key of the alert for g, which makes the alert idempotent
// across restarts.
func (g Gap) Key() string { return "gap/" + g.ApprovalID + "/" + g.Transition }
