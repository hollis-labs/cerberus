package cerbapi

import (
	"encoding/json"
	"fmt"
	"github.com/hollis-labs/cerberus/internal/eventexport"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/opsref"
	"github.com/hollis-labs/cerberus/internal/opssink"
)

func TestRecordDeployWritesTheRecordAndPostsOnce(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	zero := 0
	ops := opssink.New(opsref.Settings{Enabled: true, Endpoint: srv.URL, TokenFile: tok, Retries: &zero}, nil)
	mem := audit.NewMemory()
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "tether-hub", Project: "tether"}}}
	svc := NewResourceRuntimeService(opssink.Wrap(mem, ops), WithResourceRuntimeConfigV2(cfg),
		WithResourceRuntimeOpsMapping(func() opsref.Mapping {
			return opsref.Mapping{Projects: map[string]opsref.Entry{"tether": {OpsID: "tether-ops", WorkstreamID: "workstream:T"}}}
		}))

	rec, err := svc.RecordDeploy(t.Context(), DeployRecord{
		Resource: "tether-hub", Revision: "v1.2.3", Outcome: "rolled_back", Health: "http://x/healthz 200",
		Refs: opsref.Refs{Task: "CW-9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ops.Wait(t.Context())
	if rec.Operation != "deploy_record" || rec.DeployOutcome != "rolled_back" || rec.Revision != "v1.2.3" {
		t.Fatalf("record: %+v", rec)
	}
	// Task from the caller; ops_id and workstream_id from the project mapping.
	if rec.Task != "CW-9" || rec.OpsID != "tether-ops" || rec.WorkstreamID != "workstream:T" {
		t.Fatalf("identifiers: %+v", rec)
	}
	if posts.Load() != 1 {
		t.Fatalf("%d Ops posts, want 1", posts.Load())
	}
	records := mem.Records()
	if len(records) != 1 {
		t.Fatalf("%d audit records, want 1", len(records))
	}
}

func TestRecordDeployRefusesBadInput(t *testing.T) {
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	if _, err := svc.RecordDeploy(t.Context(), DeployRecord{Outcome: "deployed"}); err == nil {
		t.Error("no resource accepted")
	}
	if _, err := svc.RecordDeploy(t.Context(), DeployRecord{Resource: "x", Outcome: "started"}); err == nil {
		t.Error("an intermediate state accepted as an outcome")
	}
}

func TestResolveOpsRefsPrecedence(t *testing.T) {
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "web", Project: "p"}}}
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg),
		WithResourceRuntimeOpsMapping(func() opsref.Mapping {
			return opsref.Mapping{
				Resources: map[string]opsref.Entry{"web": {OpsID: "res"}},
				Projects:  map[string]opsref.Entry{"p": {OpsID: "proj", WorkstreamID: "workstream:P"}},
			}
		}))
	snap := svc.snapshotConfig()
	if got := svc.resolveOpsRefs(MutationOpts{OpsID: "flag"}, "web", snap); got != (opsref.Refs{OpsID: "flag", WorkstreamID: "workstream:P"}) {
		t.Errorf("flag over mapping: %+v", got)
	}
	if got := svc.resolveOpsRefs(MutationOpts{}, "web", snap); got != (opsref.Refs{OpsID: "res", WorkstreamID: "workstream:P"}) {
		t.Errorf("resource over project: %+v", got)
	}
	if got := svc.resolveOpsRefs(MutationOpts{}, "ghost", snap); !got.Empty() {
		t.Errorf("none: %+v", got)
	}
	_ = time.Second
}

// A `resource deploy` carries the Ops identifiers on its audit record and posts
// exactly one outcome: the intent, and the intermediate work, post nothing.
func TestResourceDeployRecordsIdentifiersAndPostsOneOutcome(t *testing.T) {
	var kinds []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Params struct {
				Arguments struct {
					Kind    string `json:"kind"`
					OpsID   string `json:"ops_id"`
					Task    string `json:"task"`
					Summary string `json:"summary"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		kinds = append(kinds, body.Params.Arguments.Kind+"|"+body.Params.Arguments.OpsID+"|"+body.Params.Arguments.Task)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(tok, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	zero := 0
	ops := opssink.New(opsref.Settings{Enabled: true, Endpoint: srv.URL, TokenFile: tok, Retries: &zero}, nil)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\t@exit 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("ops-deploy-%s", filepath.Base(dir))
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: id, Project: "proj", Type: "process", Connector: "local", Config: map[string]any{
		"dir": dir, "command": []string{"./app"}, "log_file": filepath.Join(dir, "runtime.log"), "install_root": filepath.Join(dir, "install"),
		"install_after_build": false, "build_strategy": map[string]any{"kind": "make_standard", "rules": map[string]any{"output": "./app"}},
	}}}}
	mem := audit.NewMemory()
	svc := NewResourceRuntimeService(opssink.Wrap(mem, ops), WithResourceRuntimeConfigV2(cfg),
		WithResourceRuntimeOpsMapping(func() opsref.Mapping {
			return opsref.Mapping{Projects: map[string]opsref.Entry{"proj": {OpsID: "mapped-ops"}}}
		}))
	out, err := svc.DeployResource(t.Context(), id, WithAcknowledged(true), WithOpsRefs(opsref.Refs{Task: "CW-7"}))
	if err != nil || out.Success {
		t.Fatalf("want a failed deploy, got %+v %v", out, err)
	}
	ops.Wait(t.Context())

	var outcome audit.Record
	for _, rec := range mem.Records() {
		if rec.Kind == audit.KindOutcome && rec.Operation == "deploy" {
			outcome = rec
		}
	}
	if outcome.OpsID != "mapped-ops" || outcome.Task != "CW-7" || outcome.Health != "failed" {
		t.Fatalf("outcome record: ops=%q task=%q health=%q", outcome.OpsID, outcome.Task, outcome.Health)
	}
	// The intent carries them too, so a deploy that never finishes is still linked.
	for _, rec := range mem.Records() {
		if rec.Kind == audit.KindIntent && rec.Operation == "deploy" && rec.OpsID != "mapped-ops" {
			t.Errorf("intent without ids: %+v", rec)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != 1 || kinds[0] != "deploy_failed|mapped-ops|CW-7" {
		t.Fatalf("Ops posts: %v, want exactly one deploy_failed", kinds)
	}
	// The same record is what the deploy.state_changed event carries.
	ev := eventexport.MapLifecycle(outcome)
	found := false
	for _, m := range ev {
		if m.Envelope.Kind == "deploy.state_changed" && strings.Contains(string(m.Envelope.Payload), `"ops_id":"mapped-ops"`) && strings.Contains(string(m.Envelope.Payload), `"task":"CW-7"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("the deploy event does not carry the identifiers")
	}
}
