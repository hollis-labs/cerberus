package cerbapi

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// Every record carries the posture its operation was evaluated under: the
// applied posture reaches the audit log with the decision it produced.
func TestRecordsCarryThePostureTheyWereEvaluatedUnder(t *testing.T) {
	for _, posture := range []string{policy.PostureSecure, policy.PosturePermissive} {
		withPDP(t, policy.NewEvaluator(policy.File{Version: policy.FileVersion, Posture: posture}, "test"))
		sink := audit.NewMemory()
		svc := auditedDockerService(sink)
		if _, err := svc.Execute(BeginRequest(context.Background(), SurfaceInProcess), ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"}); err != nil {
			t.Fatalf("%s: %v", posture, err)
		}
		recs := sink.Records()
		if len(recs) == 0 {
			t.Fatalf("%s: no records", posture)
		}
		for _, rec := range recs {
			if rec.Kind != audit.KindIntent && rec.Kind != audit.KindOutcome {
				continue
			}
			if rec.Posture != posture {
				t.Errorf("%s: %s record carries posture %q", posture, rec.Kind, rec.Posture)
			}
		}
	}
}
