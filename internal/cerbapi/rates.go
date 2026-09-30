package cerbapi

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// Rate limits (§12, P5-b). A rule's `rate: 5/h` caps the calls it matched
// that ran, per principal and effect, over a sliding window. The counters
// live in the process that gates the call and are seeded from the audit
// log the first time a rate matters, so a restarted daemon, or the CLI
// running in-process, counts what already ran. Exceeding one is a deny:
// enforced, it refuses the call as policy_denied naming when the next one
// is allowed; in shadow, it is recorded as a would-block and the call runs.

// RateLimiter holds the sliding-window counters.
type RateLimiter struct {
	// AuditDir is where the counters are seeded from; empty seeds nothing.
	AuditDir string
	Now      func() time.Time

	mu        sync.Mutex
	seededFor string
	hits      map[string][]time.Time
}

var ratesPoint atomic.Pointer[RateLimiter]

// SetRateLimiter installs the process's rate limiter; nil removes it, and
// with none no rate is counted or enforced.
func SetRateLimiter(l *RateLimiter) { ratesPoint.Store(l) }

// ProcessRateLimiter is the installed rate limiter, or nil.
func ProcessRateLimiter() *RateLimiter { return ratesPoint.Load() }

func (l *RateLimiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// rateHold is one matched rate: the rule, its limit and the counter key.
type rateHold struct {
	rule string
	rate policy.Rate
	key  string
	at   time.Time
}

// rateKey is the counter a call counts against: the rule, the principal
// (kind, via and session, or client and uid where there is no session)
// and the effect.
func rateKey(rule string, p audit.Principal, effect string) string {
	who := p.Kind + "|" + p.Via + "|"
	if p.Session != "" {
		who += "session:" + p.Session
	} else {
		uid := "?"
		if p.UID != nil {
			uid = strconv.Itoa(*p.UID)
		}
		who += "client:" + p.Client + "|uid:" + uid
	}
	return rule + "|" + who + "|" + effect
}

// rateHolds are the rates res matched for this call. Automation, dry runs
// and plan requests run nothing on anyone's behalf and are not counted.
func rateHolds(ctx context.Context, spec auditSpec, res policy.Result) []rateHold {
	if spec.automation || spec.dryRun || spec.planOnly {
		return nil
	}
	p := principalFor(ctx, spec)
	var out []rateHold
	for _, m := range res.Matched {
		if m.Rate != nil {
			out = append(out, rateHold{rule: m.Rule, rate: *m.Rate, key: rateKey(m.Rule, p, string(spec.op.Effect))})
		}
	}
	return out
}

// authorizeRated is the policy decision with the rate limits applied: a
// matched rate already spent for this caller adds a deny naming when the
// next call is allowed.
func authorizeRated(ctx context.Context, spec auditSpec, req policy.Request) policy.Result {
	res := PolicyDecisionPoint().Authorize(req)
	l := ProcessRateLimiter()
	if l == nil || req.DryRun {
		return res
	}
	for _, h := range rateHolds(ctx, spec, res) {
		n, next := l.count(h)
		if n < h.rate.Limit {
			continue
		}
		res.Matched = append(res.Matched, policy.Match{Rule: "rate." + h.rule, Decision: policy.Deny,
			Reason: fmt.Sprintf("rate limit: rule %s allows %s for this caller's %s operations, and %d ran in the last %s; the next is allowed at %s",
				h.rule, h.rate, spec.op.Effect, n, window(h.rate.Window), next.Local().Format(time.RFC3339))})
		res.Decision, res.WouldBlock = policy.Deny, true
	}
	return res
}

func window(d time.Duration) string {
	names := map[time.Duration]string{time.Minute: "minute", time.Hour: "hour", 24 * time.Hour: "day"}
	if n, ok := names[d]; ok {
		return n
	}
	return d.String()
}

// count is how many calls ran against h's counter within its window, and
// when the oldest of them leaves it.
func (l *RateLimiter) count(h rateHold) (int, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seed()
	now := l.now()
	hits := l.prune(h.key, now, h.rate.Window)
	if len(hits) == 0 {
		return 0, now
	}
	return len(hits), hits[0].Add(h.rate.Window)
}

func (l *RateLimiter) prune(key string, now time.Time, w time.Duration) []time.Time {
	hits := l.hits[key]
	i := 0
	for i < len(hits) && !hits[i].After(now.Add(-w)) {
		i++
	}
	hits = hits[i:]
	if len(hits) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = hits
	}
	return hits
}

// hold counts a call that is about to run against each of its rates; a
// call the gate or the connector then refuses gives its holds back.
func (l *RateLimiter) hold(holds []rateHold) []rateHold {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seed()
	now := l.now()
	for i := range holds {
		holds[i].at = now
		l.hits[holds[i].key] = append(l.hits[holds[i].key], now)
	}
	return holds
}

func (l *RateLimiter) release(holds []rateHold) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, h := range holds {
		hits := l.hits[h.key]
		for i := len(hits) - 1; i >= 0; i-- {
			if hits[i].Equal(h.at) {
				l.hits[h.key] = append(hits[:i:i], hits[i+1:]...)
				break
			}
		}
	}
}

// seed counts the calls the audit log says ran in the last day under a
// rule that now has a rate. It runs again when the set of rated rules
// changes (a policy apply that adds one), so a new rate starts from what
// already ran, not from zero. Called with l.mu held.
func (l *RateLimiter) seed() {
	var ids []string
	if ev, ok := PolicyDecisionPoint().(interface{ File() policy.File }); ok {
		ids = ratedRules(ev.File())
	}
	sort.Strings(ids)
	sig := strings.Join(ids, ",")
	if l.hits != nil && l.seededFor == sig {
		return
	}
	l.hits, l.seededFor = map[string][]time.Time{}, sig
	if l.AuditDir == "" || len(ids) == 0 {
		return
	}
	records, err := audit.ReadRecords(l.AuditDir)
	if err != nil {
		return
	}
	rates := map[string]bool{}
	for _, id := range ids {
		rates[id] = true
	}
	since := l.now().Add(-policy.MaxRateWindow)
	for _, r := range records {
		if r.Kind != audit.KindOutcome || r.Decision != audit.DecisionAllowed || r.DryRun || r.Policy == nil || r.Time.Before(since) ||
			r.Principal.Kind == audit.PrincipalAutomation {
			continue
		}
		for _, m := range r.Policy.MatchedRules {
			if rates[m.Rule] {
				k := rateKey(m.Rule, r.Principal, r.Effect)
				l.hits[k] = append(l.hits[k], r.Time)
			}
		}
	}
}

// ratedRules are the ids of the file's rules that carry a rate.
func ratedRules(f policy.File) []string {
	var ids []string
	add := func(rules []policy.Rule) {
		for _, r := range rules {
			if r.Rate != "" && r.ID != "" {
				ids = append(ids, r.ID)
			}
		}
	}
	for _, p := range f.Providers {
		add(p.Rules)
	}
	for _, t := range f.Targets {
		add(t.Rules)
	}
	for _, p := range f.Principals {
		add(p.Rules)
	}
	return ids
}
