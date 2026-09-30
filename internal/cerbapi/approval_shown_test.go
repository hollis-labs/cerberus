package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// An approval carries what the call would run: the plan it binds to, whose
// hash is the approval's, and the arguments, marked as the requester's (H3).
func TestAnApprovalCarriesWhatItWouldRun(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Approve})
	sink := audit.NewMemory()
	dir := filepath.Join(t.TempDir(), "approvals")
	broker, err := NewBroker(sink, dir)
	if err != nil {
		t.Fatal(err)
	}
	withEnforcement(t, broker)
	svc, _ := dockerLane(t, sink)
	ctx := BeginRequest(WithPrincipal(context.Background(), Principal{Kind: PrincipalAgent, Via: ViaMCPStdio}), SurfaceSocket)
	_, err = svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true})
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("err = %v", err)
	}
	a, _ := broker.Get(coded.Approval.ID)
	if a.Shown == nil {
		t.Fatal("the approval shows nothing of what it would run")
	}
	var args map[string]any
	if err = json.Unmarshal(a.Shown.Arguments, &args); err != nil || args["container"] != "web" || args["resource"] != "dev-box" {
		t.Fatalf("arguments %s: %v", a.Shown.Arguments, err)
	}
	if !slices.Contains(a.Shown.Untrusted, "/arguments") {
		t.Fatalf("the arguments are not marked as the requester's: %v", a.Shown.Untrusted)
	}
	var shownPlan plan.Plan
	if err = json.Unmarshal(a.Shown.Plan, &shownPlan); err != nil || shownPlan.Connector != "docker" || shownPlan.Operation != "stop" {
		t.Fatalf("plan %s: %v", a.Shown.Plan, err)
	}
	if hash, _ := shownPlan.Hash(); a.PlanHash != "" && hash != a.PlanHash {
		t.Fatalf("the plan shown (%s) is not the plan bound (%s)", hash, a.PlanHash)
	}
	// It persists: a store reopened from disk shows the same.
	reopened, err := NewBroker(sink, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, ok := reopened.Get(a.ID); !ok || again.Shown == nil || string(again.Shown.Arguments) != string(a.Shown.Arguments) {
		t.Fatalf("reopened %+v", again.Shown)
	}
}

// What an approval stores is redacted: a credential the request resolved,
// and a value under a credential-named key, never reach the approvals store.
func TestWhatAnApprovalShowsIsRedacted(t *testing.T) {
	scope := redact.NewScope()
	scope.Add("github/token", "ghp_16C7e42F292c6912E7710c838347Ae178B4a")
	shown := renderShown(scope, &plan.Plan{Connector: "ssh", Operation: "exec", Preview: json.RawMessage(`{"command":"curl -H 'Authorization: token ghp_16C7e42F292c6912E7710c838347Ae178B4a' https://x"}`)},
		map[string]any{"command": "echo ghp_16C7e42F292c6912E7710c838347Ae178B4a", "api_key": "sk-live-abcdef0123456789"}) //nolint:gosec // test sentinels
	data, _ := json.Marshal(shown)
	for _, leaked := range []string{"ghp_16C7e42F", "sk-live-abcdef"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("%s reached what the approval stores: %s", leaked, data)
		}
	}
	if !slices.Contains(shown.Untrusted, "/plan/preview") || !slices.Contains(shown.Untrusted, "/arguments") {
		t.Fatalf("untrusted = %v", shown.Untrusted)
	}
}

// Too much to store is replaced by a note, never cut mid-JSON.
func TestWhatAnApprovalShowsIsBounded(t *testing.T) {
	big := strings.Repeat("x", approval.ShownMaxBytes)
	shown := renderShown(redact.NewScope(), &plan.Plan{Connector: "ssh", Operation: "exec"}, map[string]any{"command": big})
	if !shown.Truncated || len(shown.Plan)+len(shown.Arguments) > approval.ShownMaxBytes {
		t.Fatalf("truncated %v, %d bytes", shown.Truncated, len(shown.Plan)+len(shown.Arguments))
	}
	if !json.Valid(shown.Plan) || !json.Valid(shown.Arguments) {
		t.Fatal("what is stored is not JSON")
	}
	if !strings.Contains(string(shown.Plan), `"connector":"ssh"`) {
		t.Fatal("the plan was dropped when only the arguments were too large")
	}
	if renderShown(redact.NewScope(), nil, nil) != nil {
		t.Fatal("an empty view was stored")
	}
}
