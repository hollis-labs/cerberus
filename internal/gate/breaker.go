package gate

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The circuit breaker (§12, P5-c). An agent session that policy really
// refuses (policy_denied where enforced, not a shadow would-block) Denials
// times within Window is suspended: its every call but a plain read is
// refused as session_suspended, in every enforcement mode, until a person
// resets it with a typed phrase. There is no automatic reset. The
// suspension lives in the brake store, beside the lockdown, and each trip
// is a brake_changed record and a notification.
//
// Only agents trip it. A person is who resets it, and suspending the
// operator's own CLI would leave the fix behind the fault.

// breakerCounts are the denials counted toward a trip, per caller.
type breakerCounts struct {
	mu        sync.Mutex
	seededFor string
	hits      map[string][]time.Time
}

// callerKey is a caller as the gate counts it: kind, via and session,
// or client and uid where there is no session. Rate limits (P5-b) and the
// breaker count on it.
func callerKey(p audit.Principal) string {
	// A verified caller is its token's subject, which outlives any one
	// session: a suspension survives the agent reconnecting. Nothing the
	// caller claims is in it, so it cannot vary its way to fresh counters
	// (M13).
	if p.AuthMethod == AuthOAuth && !p.SelfReported {
		return p.Kind + "|oauth:" + p.Issuer + "#" + p.Subject
	}
	who := p.Kind + "|" + p.Via + "|"
	if p.Session != "" {
		return who + "session:" + p.Session
	}
	uid := "?"
	if p.UID != nil {
		uid = strconv.Itoa(*p.UID)
	}
	return who + "client:" + p.Client + "|uid:" + uid
}

// suspensionOf is the suspension of the caller with key. A verified
// caller's key once carried its client too ("…|client:<name>"), and a
// suspension recorded under that key is still its suspension: an upgrade
// does not lift it.
func suspensionOf(st brake.State, key string) (brake.Suspension, bool) {
	if x, ok := st.SuspensionFor(key); ok {
		return x, true
	}
	if !strings.Contains(key, "|oauth:") {
		return brake.Suspension{}, false
	}
	for _, x := range st.Suspensions {
		if strings.HasPrefix(x.Key, key+"|client:") {
			return x, true
		}
	}
	return brake.Suspension{}, false
}

// noteDenial counts a real policy denial of p, and trips the breaker when
// it is the Denials-th in the window. Called from the outcome record.
func (g *Gate) noteDenial(p audit.Principal, operationID string, at time.Time) {
	if p.Kind != string(PrincipalAgent) {
		return
	}
	b, cfg := g.brakes, policy.CircuitBreakerOf(g.pdp)
	if b == nil || cfg == nil {
		return
	}
	key := callerKey(p)
	if _, already := suspensionOf(b.Current(), key); already {
		return
	}
	n := g.denials.add(b.AuditDir, *cfg, key, operationID, at)
	if n < cfg.Denials {
		return
	}
	st, x, err := b.Store.Suspend(key, p, n, cfg.Window.String())
	if err != nil {
		return
	}
	g.denials.clear(key)
	ctx := WithPrincipal(BeginRequest(context.Background(), SurfaceMonitor), Principal{Kind: PrincipalAutomation, Via: "circuit_breaker"})
	_ = g.recordBrakes(ctx, g.sink, "suspend", "SESSION SUSPENDED: "+p.Kind+" over "+p.Via+" ("+key+") after "+strconv.Itoa(n)+" policy denials in "+cfg.Window.String()+". Reset: cerberus breaker reset "+x.ID,
		st, audit.Target{Kind: "brake.suspension", Fields: map[string]string{"id": x.ID, "caller": key}})
}

// add counts one denial and returns how many fall in the window. The
// first count in a process, and the first after the breaker changes, is
// seeded from the audit log's real denials.
func (c *breakerCounts) add(auditDir string, cfg policy.CircuitBreaker, key, operationID string, at time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sig := cfg.String(); c.hits == nil || c.seededFor != sig {
		c.hits, c.seededFor = map[string][]time.Time{}, sig
		c.seed(auditDir, at.Add(-cfg.Window), operationID)
	}
	hits := append(c.hits[key], at)
	i := 0
	for i < len(hits) && !hits[i].After(at.Add(-cfg.Window)) {
		i++
	}
	c.hits[key] = hits[i:]
	return len(c.hits[key])
}

// seed counts the real denials of agents the audit log holds since since,
// but not the call being counted now, whose outcome is already written.
func (c *breakerCounts) seed(auditDir string, since time.Time, skip string) {
	if auditDir == "" {
		return
	}
	records, err := audit.ReadRecords(auditDir)
	if err != nil {
		return
	}
	for _, r := range records {
		if r.Kind == audit.KindOutcome && r.OutcomeCode == string(CodePolicyDenied) && r.Principal.Kind == string(PrincipalAgent) &&
			r.Time.After(since) && (skip == "" || r.OperationID != skip) {
			k := callerKey(r.Principal)
			c.hits[k] = append(c.hits[k], r.Time)
		}
	}
}

func (c *breakerCounts) clear(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.hits, key)
}

// suspensionRefusal refuses a suspended caller's call, or is nil. A plain
// read, a dry run and a plan request pass, as under the brakes.
func (g *Gate) suspensionRefusal(ctx context.Context, spec Spec, effect contract.Effect, dryRun bool) error {
	b := g.brakes
	if b == nil || spec.Automation || unbraked[spec.Connector] || dryRun || spec.PlanOnly || effect == contract.EffectRead {
		return nil
	}
	p := principalFor(ctx, spec)
	x, ok := suspensionOf(b.Current(), callerKey(p))
	if !ok {
		return nil
	}
	return codedError(opArgs{Connector: spec.Connector, Operation: spec.Operation}, CodeSessionSuspended,
		redact.Guidance("this session is SUSPENDED by the circuit breaker since %s, after %d policy denials in %s, so %s %s did not run; only plain reads do. Stop and ask your operator: a person resets it with `cerberus breaker reset %s`",
			x.TrippedAt.Local().Format(time.RFC3339), x.Denials, x.Window, spec.Connector, spec.Operation, x.ID))
}

// ResetSuspension ends a suspension. A person does it, from the CLI or the
// console, typing "reset <id>"; the daemon checks the phrase, so no
// surface can skip it.
func (g *Gate) ResetSuspension(ctx context.Context, id, typed string) (brake.State, error) {
	sink, store, err := g.brakeStore()
	if err != nil {
		return brake.State{}, err
	}
	cur := g.brakes.Current()
	x, ok := cur.Suspension(id)
	if !ok {
		return cur, brake.ErrNotEngaged
	}
	args := opArgs{Connector: "brake", Operation: "reset_suspension"}
	p := principalFor(ctx, Spec{})
	if p.Kind != string(PrincipalHuman) || strings.HasPrefix(p.Via, "mcp") {
		return cur, codedError(args, CodeApprovalRequired,
			redact.Guidance("a suspended session is reset by a person, and this caller is %s over %s", p.Kind, p.Via))
	}
	if strings.TrimSpace(typed) != "reset "+id {
		return cur, codedError(args, CodeApprovalRequired,
			redact.Guidance("type %q to reset it; nothing was reset", "reset "+id))
	}
	if err = liftUntrusted(sink, "reset_suspension"); err != nil {
		return cur, err
	}
	st, err := store.ResetSuspension(id, principalFor(ctx, Spec{}), "typed")
	if err != nil {
		return st, err
	}
	g.denials.clear(x.Key)
	return st, g.recordBrakes(ctx, sink, "reset_suspension", "SESSION RESET: "+x.Key+" ("+id+")", st,
		audit.Target{Kind: "brake.suspension", Fields: map[string]string{"id": id, "caller": x.Key}})
}

// BrakeResetArgs resets a suspension, with the phrase typed.
type BrakeResetArgs struct {
	Typed string `json:"typed"`
}
