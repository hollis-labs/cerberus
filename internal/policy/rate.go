package policy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Rate is a rule's rate limit (§12, P5-b): at most Limit calls that the
// rule allowed, and that ran, per Window, counted per principal and effect.
// It is written `rate: 5/h`, with m, h or d.
type Rate struct {
	Limit  int
	Window time.Duration
	text   string
}

// ParseRate reads "N/m", "N/h" or "N/d".
func ParseRate(s string) (Rate, error) {
	n, unit, ok := strings.Cut(strings.TrimSpace(s), "/")
	limit, err := strconv.Atoi(strings.TrimSpace(n))
	if !ok || err != nil || limit < 1 {
		return Rate{}, fmt.Errorf("rate %q is not N/m, N/h or N/d with N at least 1", s)
	}
	windows := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}
	w, ok := windows[strings.TrimSpace(unit)]
	if !ok {
		return Rate{}, fmt.Errorf("rate %q is not N/m, N/h or N/d with N at least 1", s)
	}
	return Rate{Limit: limit, Window: w, text: fmt.Sprintf("%d/%s", limit, strings.TrimSpace(unit))}, nil
}

// MaxRateWindow is the longest window a rate can have: how far back the
// counters are seeded from the audit log.
const MaxRateWindow = 24 * time.Hour

func (r Rate) String() string { return r.text }

// MarshalJSON writes a rate as it is written in policy.
func (r Rate) MarshalJSON() ([]byte, error) { return json.Marshal(r.text) }

// rate is the rule's parsed rate, or nil; Validate refuses one that does
// not parse, so a rule that reaches evaluation parses.
func (r Rule) rate() *Rate {
	if r.Rate == "" {
		return nil
	}
	rt, err := ParseRate(r.Rate)
	if err != nil {
		return nil
	}
	return &rt
}

// rateProblems checks a rule's rate: it parses, it rides on an allow or an
// approve (a deny has nothing to count, and dry_run_only runs nothing), and
// the rule has an id, which names its counter so the count survives edits
// that move the rule. seen holds the ids rates already use.
func rateProblems(at string, r Rule, seen map[string]string) []string {
	if r.Rate == "" {
		return nil
	}
	var problems []string
	if _, err := ParseRate(r.Rate); err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", at, err))
	}
	if r.Decision != Allow && r.Decision != Approve {
		problems = append(problems, fmt.Sprintf("%s: rate applies to an allow or approve rule, not %s", at, r.Decision))
	}
	switch prev, dup := seen[r.ID]; {
	case r.ID == "":
		problems = append(problems, fmt.Sprintf("%s: a rule with a rate needs an id, which names its counter", at))
	case dup:
		problems = append(problems, fmt.Sprintf("%s: id %q already names the rate at %s; a rate's id is its counter, so each is its own", at, r.ID, prev))
	default:
		seen[r.ID] = at
	}
	return problems
}
