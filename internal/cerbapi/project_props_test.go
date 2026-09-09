package cerbapi

import (
	"context"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
)

const propsProjectConfig = `kind: cerberus-project/v1
project:
  id: propsapp
  name: Props App
  description: Carries the portable props.
  capabilities: [go, launchd, mcp]
  links:
    - { kind: repo, target: "git@github.com:hollis-labs/propsapp.git" }
    - { kind: owned_by, target: "org:hollis-labs" }
resources:
  - id: propsapp-api
    name: Props API
    type: process
    connector: local
    project: propsapp
  - id: propsapp-worker
    name: Props Worker
    type: process
    connector: local
    project: propsapp
`

const barePropsProjectConfig = `kind: cerberus-project/v1
project:
  id: bareapp
  name: Bare App
resources:
  - id: bareapp-api
    name: Bare API
    type: process
    connector: local
    project: bareapp
`

func TestListProjectsCarriesCapabilitiesAndLinks(t *testing.T) {
	cfgPath := writeRegistryFixture(t, map[string]string{
		"propsapp": propsProjectConfig,
		"bareapp":  barePropsProjectConfig,
	})
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigPath(cfgPath))

	list, err := svc.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("projects = %d, want 2", len(list))
	}
	// Ordered by slug, so bareapp precedes propsapp regardless of the
	// order the registry happened to iterate.
	if list[0].ID != "bareapp" || list[1].ID != "propsapp" {
		t.Fatalf("order = %q, %q; want bareapp, propsapp", list[0].ID, list[1].ID)
	}

	props := list[1]
	if props.Resources != 2 {
		t.Errorf("resource_count = %d, want 2", props.Resources)
	}
	if got := props.Capabilities; len(got) != 3 || got[0] != "go" || got[2] != "mcp" {
		t.Errorf("Capabilities = %v, want [go launchd mcp]", got)
	}
	if len(props.Links) != 2 {
		t.Fatalf("Links = %d, want 2", len(props.Links))
	}
	if props.Links[0] != (config.Link{Kind: "repo", Target: "git@github.com:hollis-labs/propsapp.git"}) {
		t.Errorf("Links[0] = %+v", props.Links[0])
	}
	if props.Links[1].Kind != "owned_by" {
		t.Errorf("Links[1].Kind = %q, want owned_by", props.Links[1].Kind)
	}

	// A project that declares neither must report neither rather than
	// empty slices, so `omitempty` keeps them off the wire entirely.
	bare := list[0]
	if bare.Capabilities != nil || bare.Links != nil {
		t.Errorf("bare project carries %v / %v; want both nil", bare.Capabilities, bare.Links)
	}
}

// Every surface has to report the same project set. InProcessClient used
// to assemble its own ProjectInfo, which is how a field added in one
// place goes missing in another.
func TestInProcessClientListProjectsMatchesRuntimeService(t *testing.T) {
	cfgPath := writeRegistryFixture(t, map[string]string{"propsapp": propsProjectConfig})

	svc := NewResourceRuntimeService(WithResourceRuntimeConfigPath(cfgPath))
	direct, err := svc.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("runtime ListProjects: %v", err)
	}

	viaClient, err := NewInProcessClient(WithConfigPath(cfgPath)).ListProjects(context.Background())
	if err != nil {
		t.Fatalf("client ListProjects: %v", err)
	}

	if len(direct) != len(viaClient) {
		t.Fatalf("lengths differ: runtime %d, client %d", len(direct), len(viaClient))
	}
	for i := range direct {
		if direct[i].ID != viaClient[i].ID ||
			len(direct[i].Capabilities) != len(viaClient[i].Capabilities) ||
			len(direct[i].Links) != len(viaClient[i].Links) {
			t.Errorf("project %d differs:\n runtime %+v\n client  %+v", i, direct[i], viaClient[i])
		}
	}
	if len(viaClient) == 0 || len(viaClient[0].Capabilities) == 0 {
		t.Error("client dropped capabilities; it is not routing through the runtime service")
	}
}

// A project whose config was skipped must not appear. Pairing that with
// the notice is what stops a short project list reading as complete.
func TestListProjectsOmitsSkippedConfig(t *testing.T) {
	cfgPath := writeRegistryFixture(t, map[string]string{
		"propsapp":  propsProjectConfig,
		"brokenapp": brokenProjectConfig,
	})
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigPath(cfgPath))

	list, err := svc.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range list {
		if p.ID == "brokenapp" {
			t.Fatal("skipped config still produced a project")
		}
	}

	diag, err := svc.ResolveDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("ResolveDiagnostics: %v", err)
	}
	if diag.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 alongside the short project list", diag.Skipped)
	}
}
