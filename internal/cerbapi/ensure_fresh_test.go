package cerbapi

import (
	"context"
	"testing"
)

type fakeFreshener struct {
	status      *ResourceRuntimeStatus
	calls       []string
	deployCalls int
}

func (f *fakeFreshener) GetResourceRuntime(_ context.Context, _ string) (*ResourceRuntimeStatus, error) {
	return f.status, nil
}
func (f *fakeFreshener) DeployResource(_ context.Context, id string, _ ...DeployResourceOption) (*OpResult, error) {
	f.calls = append(f.calls, "deploy")
	f.deployCalls++
	return &OpResult{Success: true, ServiceID: id, Message: "deployed"}, nil
}
func (f *fakeFreshener) ApplyResource(_ context.Context, id string) (*OpResult, error) {
	f.calls = append(f.calls, "apply")
	return &OpResult{Success: true, ServiceID: id, Message: "applied"}, nil
}
func (f *fakeFreshener) SyncResource(_ context.Context, id string) (*OpResult, error) {
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
	// Even when status says "current" (no recommended action), force must deploy.
	f := &fakeFreshener{status: &ResourceRuntimeStatus{ID: "x", RecommendedAction: ""}}
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
