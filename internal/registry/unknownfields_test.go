package registry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/registry"
)

// futureConfig is a valid cerberus-project/v1 file that additionally
// carries a field this binary does not know. It stands in for the
// 2026-05-25 incident shape, where a newer writer added `registry_urn`
// and every strict reader dropped the whole project.
const futureConfig = `kind: cerberus-project/v1
owner: futureapp
namespace: local
future_top_level_field: something-this-binary-has-never-heard-of
project:
  id: futureapp
  name: Future App
resources:
  - id: futureapp-api
    name: Future App API
    type: process
    project: futureapp
    connector: local
    config:
      command: ["./futureapp"]
      dir: /tmp/futureapp
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "futureapp.cerberus.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoadProjectConfigToleratesUnknownFields is the regression guard.
// A strict load here is what took 17 of 18 projects offline: the
// resolver skips any config that fails to load, so the parse must
// survive a field it does not recognise.
func TestLoadProjectConfigToleratesUnknownFields(t *testing.T) {
	pc, err := registry.LoadProjectConfig(writeConfig(t, futureConfig))
	if err != nil {
		t.Fatalf("load must tolerate an unknown field, got: %v", err)
	}
	if pc.Owner != "futureapp" {
		t.Errorf("Owner = %q, want %q", pc.Owner, "futureapp")
	}
	if pc.Project.ID != "futureapp" {
		t.Errorf("Project.ID = %q, want %q", pc.Project.ID, "futureapp")
	}
	if len(pc.Resources) != 1 {
		t.Fatalf("Resources = %d, want 1 — the known config must survive intact", len(pc.Resources))
	}
	if len(pc.UnknownFields) != 1 {
		t.Fatalf("UnknownFields = %v, want exactly one entry", pc.UnknownFields)
	}
	if !strings.Contains(pc.UnknownFields[0], "future_top_level_field") {
		t.Errorf("UnknownFields[0] = %q, want it to name the offending field", pc.UnknownFields[0])
	}
}

// TestUnknownFieldIsWarningNotError pins the asymmetry the fix depends
// on. Promoting these to errors would put the silent drop straight back:
// the runtime resolver skips a config whose validation has errors.
func TestUnknownFieldIsWarningNotError(t *testing.T) {
	pc, err := registry.LoadProjectConfig(writeConfig(t, futureConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	result := registry.ValidateProjectConfig(pc)
	if result.HasErrors() {
		t.Fatalf("an unknown field must not be an error — the resolver drops configs with errors; got %v", result.Errors())
	}

	unknown := result.UnknownFieldIssues()
	if len(unknown) != 1 {
		t.Fatalf("UnknownFieldIssues = %d, want 1", len(unknown))
	}
	if unknown[0].Severity != registry.SeverityWarning {
		t.Errorf("severity = %q, want %q", unknown[0].Severity, registry.SeverityWarning)
	}
}

// TestResolveKeepsProjectWithUnknownField is the end-to-end version of
// the regression: a registered config carrying a future field must still
// contribute its project and resources to the assembled runtime config.
func TestResolveKeepsProjectWithUnknownField(t *testing.T) {
	dir := t.TempDir()

	cfgPath := filepath.Join(dir, "futureapp.cerberus.yaml")
	if err := os.WriteFile(cfgPath, []byte(futureConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	indexPath := filepath.Join(dir, "registry.yaml")
	index := "version: 1\nentries:\n" +
		"    - owner: futureapp\n" +
		"      namespace: local\n" +
		"      path: " + cfgPath + "\n" +
		"      kind: cerberus-project/v1\n" +
		"      registered_at: \"2026-09-09T00:00:00Z\"\n"
	if err := os.WriteFile(indexPath, []byte(index), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}

	resolved, err := registry.Resolve(registry.ResolveOptions{IndexPath: indexPath})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if len(resolved.Skipped) != 0 {
		t.Fatalf("Skipped = %+v, want none — an unknown field must never drop a project", resolved.Skipped)
	}
	if len(resolved.Config.Projects) != 1 {
		t.Fatalf("Projects = %d, want 1", len(resolved.Config.Projects))
	}
	if len(resolved.Config.Resources) != 1 {
		t.Fatalf("Resources = %d, want 1", len(resolved.Config.Resources))
	}
}

// TestRegisterRejectsUnknownField holds the other half of the contract:
// leniency is a runtime property only. Registration is author time, so a
// typo still has to stop there.
func TestRegisterRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "futureapp.cerberus.yaml")
	if err := os.WriteFile(cfgPath, []byte(futureConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	reg := registry.New(filepath.Join(dir, "registry.yaml"))
	_, err := reg.Register(cfgPath)
	if err == nil {
		t.Fatal("register must reject an unrecognised field so a typo is caught at author time")
	}
	if !strings.Contains(err.Error(), "future_top_level_field") {
		t.Errorf("error must name the offending field, got: %v", err)
	}
	if !strings.Contains(err.Error(), "upgrade cerberus") {
		t.Errorf("error must offer the newer-binary explanation, got: %v", err)
	}
}

// TestLoadProjectConfigStillFailsOnBrokenYAML confirms leniency was
// scoped to unknown fields and did not swallow real parse failures.
func TestLoadProjectConfigStillFailsOnBrokenYAML(t *testing.T) {
	path := writeConfig(t, "kind: cerberus-project/v1\nowner: [unclosed\n")
	if _, err := registry.LoadProjectConfig(path); err == nil {
		t.Fatal("malformed YAML must still be a load error")
	}
}

// TestLoadProjectConfigCleanFileHasNoUnknownFields guards against the
// probe reporting false positives on well-formed input.
func TestLoadProjectConfigCleanFileHasNoUnknownFields(t *testing.T) {
	clean := strings.ReplaceAll(futureConfig,
		"future_top_level_field: something-this-binary-has-never-heard-of\n", "")

	pc, err := registry.LoadProjectConfig(writeConfig(t, clean))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(pc.UnknownFields) != 0 {
		t.Errorf("UnknownFields = %v, want none for a clean config", pc.UnknownFields)
	}
	if issues := registry.ValidateProjectConfig(pc).UnknownFieldIssues(); len(issues) != 0 {
		t.Errorf("UnknownFieldIssues = %v, want none", issues)
	}
}
