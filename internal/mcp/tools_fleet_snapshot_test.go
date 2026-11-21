package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

type snapshotFake struct{ q cerbapi.FleetSnapshotQuery }

func (f *snapshotFake) FleetSnapshot(_ context.Context, q cerbapi.FleetSnapshotQuery) (cerbapi.FleetSnapshot, error) {
	f.q = q
	return cerbapi.FleetSnapshot{Schema: "cerberus.fleet.snapshot.v1", Hosts: []cerbapi.FleetSnapshotHost{}, StepSeconds: 300}, nil
}
func TestFleetSnapshotMCPReadContractAndText(t *testing.T) {
	f := &snapshotFake{}
	tool := NewCerberusFleetSnapshotTool(f)
	res := callHostTool(t, []Tool{tool}, "cerberus_fleet_snapshot", map[string]any{"offset": 2, "limit": 3})
	if res.IsError {
		t.Fatal(textBlocks(res))
	}
	var out cerbapi.FleetSnapshot
	if err := json.Unmarshal([]byte(textBlocks(res)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Schema != "cerberus.fleet.snapshot.v1" || f.q.Offset != 2 || f.q.Limit != 3 {
		t.Fatalf("%+v %+v", out, f.q)
	}
	for _, args := range []map[string]any{{"offset": -1.0}, {"offset": 0.1}, {"limit": "3"}} {
		if _, err := tool.Handler(context.Background(), args); err == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
	op, ok := cerbapi.HostDefinition().Operation(cerbapi.OpFleetSnapshot)
	if !ok || string(op.Effect) != "read" || op.RequiresAck {
		t.Fatalf("operation: %+v", op)
	}
	found := false
	for _, tool := range AllTools(nil) {
		if tool.Name == "cerberus_fleet_snapshot" {
			found = true
		}
	}
	if !found {
		t.Fatal("tool missing from registry")
	}
}
