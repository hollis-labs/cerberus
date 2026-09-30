package policy

import (
	"fmt"
	"time"
)

// CircuitBreaker (§12, P5-c) suspends an agent's session after Denials
// real policy denials within Window: refusals policy enforced, not shadow
// would-blocks. The suspension holds in every enforcement mode until a
// person resets it. Omitted, there is no breaker.
type CircuitBreaker struct {
	Denials int           `yaml:"denials" json:"denials"`
	Window  time.Duration `yaml:"window" json:"window"`
}

// String is the breaker as a person reads it.
func (b CircuitBreaker) String() string {
	return fmt.Sprintf("%d policy denials in %s", b.Denials, b.Window)
}

// MaxBreakerWindow is the longest breaker window: how far back a process
// seeds the denials it counts from the audit log.
const MaxBreakerWindow = 24 * time.Hour

// CircuitBreakerOf is the breaker pdp's file sets, or nil.
func CircuitBreakerOf(pdp PDP) *CircuitBreaker {
	if withFile, ok := pdp.(interface{ File() File }); ok {
		if b := withFile.File().CircuitBreaker; b != nil && b.Denials > 0 && b.Window > 0 {
			out := *b
			return &out
		}
	}
	return nil
}

func (b *CircuitBreaker) problems() []string {
	if b == nil {
		return nil
	}
	var out []string
	if b.Denials < 1 {
		out = append(out, fmt.Sprintf("circuit_breaker.denials %d must be at least 1", b.Denials))
	}
	if b.Window <= 0 || b.Window > MaxBreakerWindow {
		out = append(out, fmt.Sprintf("circuit_breaker.window %s must be positive and at most %s", b.Window, MaxBreakerWindow))
	}
	return out
}
