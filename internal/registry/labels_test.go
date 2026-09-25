package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/target"
)

// A label outside the vocabulary warns rather than dropping the config,
// whose resources would otherwise vanish, and reads as unknown.
func TestInvalidLabelsWarnAndReadAsUnknown(t *testing.T) {
	pc := &ProjectConfig{Kind: ProjectConfigKind, Owner: "app", Project: config.ProjectDef{ID: "app"},
		Resources: []config.ResourceDef{{ID: "api", Type: "process", Connector: "local", Env: "production", Admin: target.Admin{Default: "us"}}}}
	vr := ValidateProjectConfig(pc)
	if vr.HasErrors() {
		t.Fatalf("a bad label is an error: %v", vr.Errors())
	}
	var labelWarnings int
	for _, w := range vr.Warnings() {
		if strings.HasSuffix(w.Field, ".labels") && strings.Contains(w.Message, "read as unknown") {
			labelWarnings++
		}
	}
	if labelWarnings != 2 {
		t.Fatalf("warnings %v", vr.Warnings())
	}

	dir := t.TempDir()
	global := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(global, []byte(`version: 2
resources:
  - id: box
    type: server
    connector: ssh
    env: production
    owner: infra
    admin: { default: owner, docker: self, software: sometimes }
    config: {host: box}
  - id: good
    type: server
    connector: ssh
    env: work
    admin: owner
    config: {host: good}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	indexPath, err := IndexPathFor(global)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(ResolveOptions{IndexPath: indexPath, GlobalPath: global})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(resolved.Warnings, "\n")
	if !strings.Contains(joined, `resource "box": env "production"`) || !strings.Contains(joined, `admin.software "sometimes"`) {
		t.Fatalf("warnings:\n%s", joined)
	}
	for _, r := range resolved.Config.Resources {
		switch r.ID {
		case "box":
			if r.Env != "" || r.Owner != "infra" || r.Admin.ByKind["docker"] != target.AdminSelf || r.Admin.ByKind["software"] != "" {
				t.Errorf("box not sanitized: %+v", r)
			}
		case "good":
			if r.Env != target.EnvWork || r.Admin.Default != target.AdminOwner {
				t.Errorf("good %+v", r)
			}
		}
	}
}
