package gate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Every error code must survive redaction: an HTTP layer prints errors
// through redact.Text, and a gate whose explanation is eaten is worse than
// no explanation.
func TestGateRefusalCodesSurviveRedaction(t *testing.T) {
	for _, code := range errorCodes {
		msg := "docker destroy: " + string(code) + ": reload the plugin and retry"
		if got := redact.Text(msg); got != msg {
			t.Errorf("code %s: redact.Text changed\n got %q\nwant %q", code, got, msg)
		}
	}
}

// New refuses to build a gate with no audit sink, and fills in the old
// build's "nothing installed" defaults: the baseline, shadow, no broker.
func TestNewDefaults(t *testing.T) {
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("a gate was built without a sink")
			}
		}()
		New(Options{})
	}()
	g := New(Options{Sink: audit.NewMemory(), Enforcement: SnapshotEnforcement{}})
	if g.Broker() != nil || g.Brakes() != nil || g.Policy() == nil {
		t.Fatalf("defaults: broker %v brakes %v policy %v", g.Broker(), g.Brakes(), g.Policy())
	}
	if se, ok := g.enforcement.(SnapshotEnforcement); !ok || se.PDP != g.Policy() {
		t.Fatalf("a zero SnapshotEnforcement does not read the gate's policy: %#v", g.enforcement)
	}
	if _, _, err := g.EngageLockdown(context.Background(), "x"); err == nil {
		t.Fatal("a gate with no brakes engaged one")
	}
}

// RunSweeper writes down what expired, and returns when its context ends,
// so the daemon can stop it in order.
func TestRunSweeperReturnsWhenTheContextEnds(t *testing.T) {
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	intent := audit.Record{OperationID: "op-1", Connector: "local", Operation: "remove", ArgsDigest: "hmac:x", Principal: audit.Principal{Kind: "agent"}}
	if _, err := broker.Request(context.Background(), intent, constantPDP{decision: policy.Approve}.Authorize(policy.Request{}),
		approval.ChannelTTYConfirm, approval.ScopeOnce, time.Millisecond, PlanSnapshot{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); broker.RunSweeper(ctx, 5*time.Millisecond) }()
	expired := func() bool {
		for _, rec := range sink.Records() {
			if rec.Kind == audit.KindApprovalExpired {
				return true
			}
		}
		return false
	}
	for deadline := time.Now().Add(5 * time.Second); !expired(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the sweeper never recorded the expiry")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSweeper did not return when its context ended")
	}
}
