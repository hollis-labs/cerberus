package policy

import (
	"fmt"
	"time"
)

// Break glass (P3-5b) gets a person past an approve decision, never past a
// deny, and is rate-limited per target by the applied snapshot, so the
// operator changes the limit through `policy apply`, not a code change.

// Defaults for the break-glass rate limit.
const (
	DefaultBreakGlassPerTarget = 3
	DefaultBreakGlassWindow    = 24 * time.Hour
)

// BreakGlass is the rate limit on breaking glass: at most PerTarget uses on
// one target in a rolling Window.
type BreakGlass struct {
	PerTarget int           `yaml:"per_target,omitempty" json:"per_target"`
	Window    time.Duration `yaml:"window,omitempty" json:"window"`
}

// String is the limit as a person reads it.
func (b BreakGlass) String() string {
	return fmt.Sprintf("%d per target per %s", b.PerTarget, b.Window)
}

// BreakGlassLimits is the file's limit, with the defaults for what it
// leaves out.
func (f File) BreakGlassLimits() BreakGlass {
	out := BreakGlass{PerTarget: DefaultBreakGlassPerTarget, Window: DefaultBreakGlassWindow}
	if f.BreakGlass != nil {
		if f.BreakGlass.PerTarget > 0 {
			out.PerTarget = f.BreakGlass.PerTarget
		}
		if f.BreakGlass.Window > 0 {
			out.Window = f.BreakGlass.Window
		}
	}
	return out
}

// BreakGlassLimitsOf is the limit pdp decides with: its file's, or the
// defaults for a decision point with no file.
func BreakGlassLimitsOf(pdp PDP) BreakGlass {
	if ev, ok := pdp.(*Evaluator); ok {
		return ev.file.BreakGlassLimits()
	}
	return File{}.BreakGlassLimits()
}

func (b *BreakGlass) problems() []string {
	if b == nil {
		return nil
	}
	var out []string
	if b.PerTarget < 0 {
		out = append(out, fmt.Sprintf("break_glass.per_target %d must be at least 1", b.PerTarget))
	}
	if b.Window < 0 {
		out = append(out, fmt.Sprintf("break_glass.window %s must be positive", b.Window))
	}
	return out
}
