package cerbapi

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/target"
)

func labeledConfig() *config.ConfigV2 {
	return &config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "toolbox-stage", Type: "container", Connector: "docker", Tags: []string{"poc"},
			Env: target.EnvStaging, Owner: "aws-team",
			Admin:  target.Admin{Default: target.AdminOwner, ByKind: map[string]string{"docker": target.AdminSelf}},
			Config: map[string]any{"host": "ssh://deploy@toolbox"}},
		{ID: "api", Type: "process", Connector: "local", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
			Config: map[string]any{"command": []string{"/bin/true"}}},
		{ID: "bare", Type: "process", Connector: "local", Config: map[string]any{"command": []string{"/bin/true"}}},
	}}
}

// An operation on a registered resource records the resource's labels, and
// who administers the part it touches (Decision 18). An unlabeled one is
// unknown (Decision 17); one named by connection settings is ad hoc.
func TestAuditRecordsCarryTargetLabels(t *testing.T) {
	cfg := labeledConfig()
	sink := audit.NewMemory()
	svc := auditedDockerService(sink)
	svc.SetResourceLookup(ConfigResourceLookup(cfg))
	ctx := BeginRequest(context.Background(), SurfaceInProcess)

	_, _ = svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers", Config: map[string]any{"resource": "toolbox-stage"}})
	_, _ = svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers", Config: map[string]any{"host": "ssh://someone@elsewhere"}})

	runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
	_, _ = runtime.StopResource(ctx, "api")
	_, _ = runtime.StopResource(ctx, "bare")

	var got []audit.Target
	for _, rec := range sink.Records() {
		if rec.Kind == audit.KindIntent {
			got = append(got, rec.Target)
		}
	}
	if len(got) != 4 {
		t.Fatalf("%d intents", len(got))
	}
	docker, adhoc, api, bare := got[0], got[1], got[2], got[3]
	if docker.Resource != "toolbox-stage" || docker.Env != "staging" || docker.Owner != "aws-team" || docker.Admin != target.AdminSelf || len(docker.Tags) != 1 || docker.Adhoc {
		t.Errorf("docker on a labeled host: %+v", docker)
	}
	if !adhoc.Adhoc || adhoc.Resource != "" || adhoc.Env != "unknown" || adhoc.Owner != "unknown" || adhoc.Admin != target.AdminUnknown {
		t.Errorf("ad hoc: %+v", adhoc)
	}
	if api.Resource != "api" || api.Env != "dev" || api.Owner != "self" || api.Admin != target.AdminSelf {
		t.Errorf("runtime on a labeled resource: %+v", api)
	}
	if bare.Resource != "bare" || bare.Env != "unknown" || bare.Owner != "unknown" || bare.Admin != target.AdminUnknown {
		t.Errorf("unlabeled: %+v", bare)
	}
}

func TestResourceListShowsLabels(t *testing.T) {
	runtime := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(labeledConfig()))
	list, err := runtime.ListResources(context.Background(), ResourceListArgs{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ResourceInfo{}
	for _, r := range list {
		by[r.ID] = r
	}
	if r := by["toolbox-stage"]; r.Env != "staging" || r.Owner != "aws-team" || r.Admin != "default=owner,docker=self" {
		t.Errorf("toolbox-stage %+v", r)
	}
	if r := by["bare"]; r.Env != "unknown" || r.Owner != "unknown" || r.Admin != "unknown" {
		t.Errorf("bare %+v", r)
	}
}
