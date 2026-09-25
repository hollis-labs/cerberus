package cerbapi

import (
	"context"
	"strings"
	"testing"

	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
)

type fakeFreshener struct {
	status         *ResourceRuntimeStatus
	calls          []string
	deployCalls    int
	forbidMutation *testing.T
}

func (f *fakeFreshener) GetResourceRuntime(_ context.Context, _ string) (*ResourceRuntimeStatus, error) {
	return f.status, nil
}
func (f *fakeFreshener) DeployResource(_ context.Context, id string, _ ...MutationOption) (*OpResult, error) {
	if f.forbidMutation != nil {
		f.forbidMutation.Fatal("unexpected deploy while activation needs verification")
	}
	f.calls = append(f.calls, "deploy")
	f.deployCalls++
	return &OpResult{Success: true, ServiceID: id, Message: "deployed"}, nil
}
func (f *fakeFreshener) ApplyResource(_ context.Context, id string, _ ...MutationOption) (*OpResult, error) {
	if f.forbidMutation != nil {
		f.forbidMutation.Fatal("unexpected apply while activation needs verification")
	}
	f.calls = append(f.calls, "apply")
	return &OpResult{Success: true, ServiceID: id, Message: "applied"}, nil
}
func (f *fakeFreshener) SyncResource(_ context.Context, id string, _ ...MutationOption) (*OpResult, error) {
	if f.forbidMutation != nil {
		f.forbidMutation.Fatal("unexpected sync while activation needs verification")
	}
	f.calls = append(f.calls, "sync")
	return &OpResult{Success: true, ServiceID: id, Message: "synced"}, nil
}

func TestEnsureFreshDispatchesRecommendedAction(t *testing.T) {
	cases := []struct {
		name       string
		recommend  string
		wantAction string
		wantCall   string // "" = no lifecycle call
	}{
		{"stale-deploys", "deploy", "deploy", "deploy"},
		{"apply", "apply", "apply", "apply"},
		{"sync", "sync", "sync", "sync"},
		{"current-noops", "", "noop", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeFreshener{status: &ResourceRuntimeStatus{ID: "x", RecommendedAction: tc.recommend}}
			res, err := EnsureFresh(context.Background(), f, "x", false)
			if err != nil {
				t.Fatalf("EnsureFresh: %v", err)
			}
			if res.Action != tc.wantAction {
				t.Fatalf("action = %q, want %q", res.Action, tc.wantAction)
			}
			got := ""
			if len(f.calls) > 0 {
				got = f.calls[0]
			}
			if got != tc.wantCall {
				t.Fatalf("lifecycle call = %q, want %q", got, tc.wantCall)
			}
			if !res.Success {
				t.Fatalf("expected success, got %+v", res)
			}
		})
	}
}

func TestEnsureFreshForceAlwaysDeploys(t *testing.T) {
	// An explicit forced deployment still works when automatic advice requires
	// inspection of an unconfirmed activation.
	f := &fakeFreshener{status: &ResourceRuntimeStatus{ID: "x", RecommendedAction: "inspect"}}
	res, err := EnsureFresh(context.Background(), f, "x", true)
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if res.Action != "deploy" || f.deployCalls != 1 {
		t.Fatalf("force did not deploy: action=%q deploys=%d", res.Action, f.deployCalls)
	}
}

func TestEnsureFreshDevSessionNoopMessage(t *testing.T) {
	f := &fakeFreshener{status: &ResourceRuntimeStatus{ID: "x", Mode: "dev_session", RecommendedAction: ""}}
	res, err := EnsureFresh(context.Background(), f, "x", false)
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if res.Action != "noop" {
		t.Fatalf("action = %q, want noop", res.Action)
	}
	if len(f.calls) != 0 {
		t.Fatalf("dev_session noop should not call a lifecycle verb, got %v", f.calls)
	}
}

func TestEnsureFreshDoesNotRestartUnconfirmedLaunchdActivation(t *testing.T) {
	for _, state := range []domain.State{domain.StateRunning, domain.StateStarting, domain.StateFailed, domain.StateUnknown} {
		t.Run(string(state), func(t *testing.T) {
			action, reason := localconn.RecommendedStatusAction(localconn.ProcessSpec{
				Mode: localconn.ProcessModeOSService, RunFrom: localconn.ProcessRunFromArtifact,
			}, state, localconn.ArtifactStatus{Installed: true, ActivationPending: true})
			f := &fakeFreshener{forbidMutation: t, status: &ResourceRuntimeStatus{
				ID: "recovering", RecommendedAction: action, RecommendedReason: reason,
				RecommendedNextStep: localconn.RecommendedNextStep(action, reason),
			}}
			result, err := EnsureFresh(context.Background(), f, "recovering", false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || result.Action != "noop" || !strings.Contains(result.Message, "verify") {
				t.Fatalf("expected an unsuccessful non-mutating verification result, got %+v", result)
			}
		})
	}
}

func TestEnsureFreshUnknownAdviceDoesNotClaimSuccess(t *testing.T) {
	f := &fakeFreshener{forbidMutation: t, status: &ResourceRuntimeStatus{ID: "x", RecommendedAction: "future-action"}}
	result, err := EnsureFresh(context.Background(), f, "x", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Message, "future-action") {
		t.Fatalf("unknown advice must remain unresolved, got %+v", result)
	}
}
