package cerbapi

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
)

func TestSupervisedLocallyOwnsOnlyLocalProcesses(t *testing.T) {
	cases := []struct {
		resourceType string
		connector    string
		want         bool
	}{
		{"process", "local", true},
		{"container", "docker", false},
		{"server", "ssh", false},
		{"process", "docker", false},
		{"container", "local", false},
	}
	for _, tc := range cases {
		if got := SupervisedLocally(tc.resourceType, tc.connector); got != tc.want {
			t.Errorf("SupervisedLocally(%q, %q) = %v, want %v", tc.resourceType, tc.connector, got, tc.want)
		}
	}
}

func TestUnsupervisedNextStepNamesTheConnectorsOwnCommands(t *testing.T) {
	cases := map[string]string{
		"docker": "cerberus docker up mtbf-monitor",
		"ssh":    "cerberus ssh status mtbf-monitor",
		"github": "cerberus connectors describe github",
	}
	for connector, want := range cases {
		got := UnsupervisedNextStep("mtbf-monitor", connector)
		if !strings.Contains(got, want) {
			t.Errorf("UnsupervisedNextStep(_, %q) = %q, want it to contain %q", connector, got, want)
		}
	}
}

func TestUnsupervisedRuntimeStatusAnswersInsteadOfRefusing(t *testing.T) {
	st := NewUnsupervisedRuntimeStatus(&config.ResourceDef{
		ID:        "mtbf-monitor",
		Name:      "MTBF Monitor",
		Type:      "container",
		Connector: "docker",
		Config:    map[string]any{"compose_file": "/tmp/docker-compose.yml", "port": 8090},
	})

	if st.Status != UnsupervisedStatus {
		t.Errorf("Status = %q, want %q", st.Status, UnsupervisedStatus)
	}
	if st.ID != "mtbf-monitor" || st.Name != "MTBF Monitor" || st.Type != "container" {
		t.Errorf("identity not carried over: %#v", st)
	}
	if st.Port != 8090 {
		t.Errorf("Port = %d, want 8090", st.Port)
	}
	// The old message said what did not work and left the operator to guess
	// what did. Both halves have to be present now.
	if !strings.Contains(st.RecommendedReason, "not supervised") {
		t.Errorf("RecommendedReason does not explain: %q", st.RecommendedReason)
	}
	if !strings.Contains(st.RecommendedNextStep, "cerberus docker") {
		t.Errorf("RecommendedNextStep does not name the command: %q", st.RecommendedNextStep)
	}
}

func TestUnsupervisedOperationErrorNamesWhatToRunInstead(t *testing.T) {
	err := UnsupervisedOperationError("deploy", &config.ResourceDef{
		ID: "muctlvaig", Type: "server", Connector: "ssh",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"deploy", "muctlvaig", "server/ssh", "cerberus ssh exec"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestListResourcesReportsUnsupervisedKindsInsteadOfABlankCell(t *testing.T) {
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{
		Resources: []config.ResourceDef{
			{ID: "mtbf-monitor", Type: "container", Connector: "docker",
				Config: map[string]any{"compose_file": "/tmp/docker-compose.yml"}},
			{ID: "muctlvaig", Type: "server", Connector: "ssh",
				Config: map[string]any{"host": "muctlvaig.corp.adtran.com"}},
		},
	}))

	list, err := svc.ListResources(t.Context(), ResourceListArgs{})
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d resources, want 2", len(list))
	}
	for _, info := range list {
		// A blank STATUS cell is indistinguishable from a failed probe, which
		// is what made a working resource read as broken.
		if info.Status != UnsupervisedStatus {
			t.Errorf("%s: Status = %q, want %q", info.ID, info.Status, UnsupervisedStatus)
		}
		if info.RecommendedNextStep == "" {
			t.Errorf("%s: no next step offered", info.ID)
		}
		// NEXT in the table is RecommendedAction; leaving it empty keeps an
		// administered resource out of the "needs action" reading.
		if info.RecommendedAction != "" {
			t.Errorf("%s: RecommendedAction = %q, want empty", info.ID, info.RecommendedAction)
		}
	}
}
