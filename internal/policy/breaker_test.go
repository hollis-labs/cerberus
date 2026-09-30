package policy

import (
	"strings"
	"testing"
	"time"
)

// circuit_breaker validates, survives the merge of policy files, and is
// off when omitted.
func TestCircuitBreaker(t *testing.T) {
	on := File{Version: FileVersion, CircuitBreaker: &CircuitBreaker{Denials: 5, Window: 10 * time.Minute}}
	if p := on.Validate(); len(p) != 0 {
		t.Fatal(p)
	}
	merged := Merge(File{Version: FileVersion}, on, File{Version: FileVersion})
	if cb := CircuitBreakerOf(NewEvaluator(merged, "t")); cb == nil || cb.Denials != 5 || cb.Window != 10*time.Minute {
		t.Fatalf("after a merge: %+v", cb)
	}
	if CircuitBreakerOf(NewEvaluator(File{Version: FileVersion}, "t")) != nil {
		t.Fatal("a breaker with none set")
	}
	for want, cb := range map[string]CircuitBreaker{
		"denials 0 must be at least 1": {Denials: 0, Window: time.Minute},
		"must be positive":             {Denials: 3},
		"at most 24h":                  {Denials: 3, Window: 48 * time.Hour},
	} {
		cb := cb
		f := File{Version: FileVersion, CircuitBreaker: &cb}
		if p := strings.Join(f.Validate(), "; "); !strings.Contains(p, want) {
			t.Errorf("want %q in %q", want, p)
		}
	}
}
