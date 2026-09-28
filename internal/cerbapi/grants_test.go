package cerbapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
)

func windowPolicy(t *testing.T) *Broker {
	t.Helper()
	withPDP(t, constantPDP{decision: policy.Approve, approval: &policy.Approval{Scope: approval.ScopeWindow, TTL: 30 * time.Minute}})
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	return broker
}

func stopContainer(name string) ExternalConnectorOperationArgs {
	return ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": name}, Acknowledged: true}
}

func kinds(recs []audit.Record, prefix string) []string {
	var out []string
	for _, r := range recs {
		if strings.HasPrefix(r.Kind, prefix) {
			out = append(out, r.Kind)
		}
	}
	return out
}

// A window grant, once approved, covers every call of the operation on its
// target by its requester with no approval id, each use recorded; another
// target still asks.
func TestWindowGrantCoversLaterCalls(t *testing.T) {
	broker := windowPolicy(t)
	sink := broker.sink.(*audit.Memory)
	svc, backend := dockerLane(t, sink)
	ctx := BeginRequest(WithPrincipal(context.Background(), Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}), SurfaceSocket)
	_, err := svc.Execute(ctx, stopContainer("web"))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil || !strings.Contains(err.Error(), "window grant, good for 30m") {
		t.Fatalf("asking: %v", err)
	}
	if _, err := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		backend.stopped = ""
		if _, err := svc.Execute(ctx, stopContainer("web")); err != nil || backend.stopped != "web" {
			t.Fatalf("use %d: %v", i, err)
		}
		if o := outcome(sink.Records()); o.ApprovalID != coded.Approval.ID {
			t.Fatalf("use %d outcome %+v", i, o)
		}
	}
	if got := strings.Join(kinds(sink.Records(), "grant_"), ","); got != "grant_created,grant_used,grant_used" {
		t.Fatalf("grant records %s", got)
	}
	if a, _ := broker.Get(coded.Approval.ID); a.Status != approval.Approved || a.Uses != 2 {
		t.Fatalf("grant %+v", a)
	}
	if _, err := svc.Execute(ctx, stopContainer("api")); connectorErrorCode(err) != ExternalConnectorApprovalPending {
		t.Fatalf("another target used the grant: %v", err)
	}
	// An agent's grant never covers a human's call.
	human := BeginRequest(WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI}), SurfaceSocket)
	if _, err := svc.Execute(human, stopContainer("web")); connectorErrorCode(err) != ExternalConnectorApprovalPending {
		t.Fatalf("a human's call used an agent's grant: %v", err)
	}
}

// A grant is re-checked against policy on every use: a rule narrowed to
// once is not covered by the grant it gave; a revoked grant covers nothing
// and is recorded as grant_revoked.
func TestGrantIsRecheckedAndRevocable(t *testing.T) {
	broker := windowPolicy(t)
	sink := broker.sink.(*audit.Memory)
	svc, _ := dockerLane(t, sink)
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	_, err := svc.Execute(ctx, stopContainer("web"))
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("asking: %v", err)
	}
	if _, err := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}}); err != nil {
		t.Fatal(err)
	}
	withPDP(t, constantPDP{decision: policy.Approve})
	if _, err := svc.Execute(ctx, stopContainer("web")); connectorErrorCode(err) != ExternalConnectorApprovalPending {
		t.Fatalf("a rule narrowed to once still used the grant: %v", err)
	}
	withPDP(t, constantPDP{decision: policy.Deny})
	if _, err := svc.Execute(ctx, stopContainer("web")); connectorErrorCode(err) != ExternalConnectorPolicyDenied {
		t.Fatalf("a grant widened a deny: %v", err)
	}
	withPDP(t, constantPDP{decision: policy.Approve, approval: &policy.Approval{Scope: approval.ScopeWindow, TTL: 30 * time.Minute}})
	if _, err := broker.Revoke(ctx, coded.Approval.ID, approval.Decision{By: audit.Principal{Kind: "human", Via: "cli"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, stopContainer("web")); connectorErrorCode(err) != ExternalConnectorApprovalPending {
		t.Fatalf("a revoked grant was used: %v", err)
	}
	if got := kinds(sink.Records(), "grant_"); len(got) == 0 || got[len(got)-1] != audit.KindGrantRevoked {
		t.Fatalf("grant records %v", got)
	}
}

// A grant on a protected target is allowed where policy allows it, and
// every use is marked.
func TestGrantOnProtectedTargetIsMarked(t *testing.T) {
	broker := windowPolicy(t)
	broker.SetPresenceVerifier(acceptPresence{})
	sink := broker.sink.(*audit.Memory)
	svc, backend := dockerLane(t, sink)
	ctx := BeginRequest(context.Background(), SurfaceSocket)
	unlabeled := ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true}
	_, err := svc.Execute(ctx, unlabeled)
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil || coded.Approval.Channel != approval.ChannelOutOfBand {
		t.Fatalf("asking: %v", err)
	}
	if _, err := broker.Decide(ctx, coded.Approval.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "web"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, unlabeled); err != nil || backend.stopped != "web" {
		t.Fatalf("use: %v", err)
	}
	var used audit.Record
	for _, r := range sink.Records() {
		if r.Kind == audit.KindGrantUsed {
			used = r
		}
	}
	if used.Approval == nil || !used.Approval.GrantOnProtectedTarget || used.Approval.Uses != 1 {
		t.Fatalf("grant_used %+v", used.Approval)
	}
}
