// Package gatetest builds a real operation gate over scratch directories for the tests of the
// lanes and surfaces that call it: the built-in baseline policy with the applied snapshot's
// enforcement (what a daemon with no policy applied runs), an approvals broker with the
// owner-session verifier, and brakes a test can engage.
package gatetest

import (
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// World is a gate and what it was built over.
type World struct {
	Gate   *gate.Gate
	Sink   *audit.Memory
	Broker *gate.Broker
	Brakes brake.Store
}

// Options change what New builds. The zero value is the daemon's default.
type Options struct {
	// Policy replaces the built-in baseline.
	Policy policy.PDP
	// Enforcement replaces the applied snapshot's enforcement.
	Enforcement gate.Enforcement
}

// New builds a gate over directories under t.TempDir().
func New(t *testing.T, o Options) *World {
	t.Helper()
	dir := t.TempDir()
	w := &World{Sink: audit.NewMemory(), Brakes: brake.Store{Dir: filepath.Join(dir, "brakes")}}
	var err error
	if w.Broker, err = gate.NewBroker(w.Sink, filepath.Join(dir, "approvals")); err != nil {
		t.Fatal(err)
	}
	enforcement := o.Enforcement
	if enforcement == nil {
		enforcement = gate.SnapshotEnforcement{}
	}
	w.Gate = gate.New(gate.Options{Sink: w.Sink, Policy: o.Policy, Enforcement: enforcement, Broker: w.Broker,
		Brakes: &gate.Brakes{Store: w.Brakes, AuditDir: filepath.Join(dir, "audit"), Sink: w.Sink}})
	return w
}

// Engage writes st into the brake store, as an engaged lockdown or freeze would be.
func (w *World) Engage(t *testing.T, st brake.State) {
	t.Helper()
	if _, err := w.Brakes.Restore(st); err != nil {
		t.Fatal(err)
	}
}

// Records are the audit records of kind written so far.
func (w *World) Records(kind string) []audit.Record {
	var out []audit.Record
	for _, r := range w.Sink.Records() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}
