package webui

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

func TestSummarizeResourceInfosLeavesUnsupervisedKindsOutOfTheTally(t *testing.T) {
	got := summarizeResourceInfos([]cerbapi.ResourceInfo{
		{ID: "api", Status: "running"},
		{ID: "worker", Status: "stopped"},
		{ID: "mtbf-monitor", Status: cerbapi.UnsupervisedStatus},
		{ID: "muctlvaig", Status: cerbapi.UnsupervisedStatus},
	})

	// The switch's default arm is "stopped", so an unsupervised resource left
	// in would be counted as a stopped service — the same misreport in a
	// different place.
	want := overviewRuntimeDTO{Running: 1, Stopped: 1}
	if got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
}

func TestSummarizeRuntimeLeavesUnsupervisedKindsOutOfTheTally(t *testing.T) {
	got := summarizeRuntime([]cerbapi.ResourceHealth{
		{ResourceID: "api", Status: "running", Healthy: true},
		{ResourceID: "mtbf-monitor", Status: cerbapi.UnsupervisedStatus},
	})

	want := overviewRuntimeDTO{Running: 1}
	if got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
}
