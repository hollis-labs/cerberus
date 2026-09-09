package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
)

const validProjectConfigYAML = `kind: cerberus-project/v1
owner: clockwork
project:
  id: clockwork
  name: Clockwork
resources:
  - id: clockwork-api
    name: Clockwork API
    type: process
    connector: local
    project: clockwork
    config:
      port: 8080
  - id: clockwork-worker
    name: Clockwork Worker
    type: process
    connector: local
    project: clockwork
    depends_on: [clockwork-api]
`

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// ---- ProjectConfig ----

func TestLoadProjectConfigValid(t *testing.T) {
	pc, err := LoadProjectConfig(writeFile(t, "clockwork.cerberus.yaml", validProjectConfigYAML))
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if pc.Owner != "clockwork" {
		t.Errorf("owner = %q, want clockwork", pc.Owner)
	}
	if pc.Namespace != DefaultNamespace {
		t.Errorf("namespace = %q, want default %q", pc.Namespace, DefaultNamespace)
	}
	if pc.Project.ID != "clockwork" {
		t.Errorf("project.id = %q, want clockwork", pc.Project.ID)
	}
	if len(pc.Resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(pc.Resources))
	}
}

// TestLoadProjectConfigRecordsUnknownField: the load used to reject an
// unknown top-level field. It now records it instead, because the
// resolver drops any config that fails to load and a strict parse there
// meant one field from a newer writer could blank the whole registry.
// The field is still not ignored — it becomes a validation warning, and
// register/validate reject on it.
func TestLoadProjectConfigRecordsUnknownField(t *testing.T) {
	pc, err := LoadProjectConfig(writeFile(t, "x.cerberus.yaml", validProjectConfigYAML+"surprise: true\n"))
	if err != nil {
		t.Fatalf("load must tolerate an unknown top-level field, got: %v", err)
	}
	if len(pc.UnknownFields) != 1 {
		t.Fatalf("UnknownFields = %v, want one entry naming `surprise`", pc.UnknownFields)
	}
	if issues := ValidateProjectConfig(pc).UnknownFieldIssues(); len(issues) != 1 {
		t.Fatalf("UnknownFieldIssues = %d, want 1", len(issues))
	}
}

func TestLoadProjectConfigAcceptsRegistryURN(t *testing.T) {
	pc, err := LoadProjectConfig(writeFile(t, "clockwork.cerberus.yaml",
		validProjectConfigYAML+"registry_urn: msg://project/agent-mux/prj_clockwork\n"))
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if pc.RegistryURN != "msg://project/agent-mux/prj_clockwork" {
		t.Fatalf("registry_urn = %q, want shared URN", pc.RegistryURN)
	}
}

func TestLoadProjectConfigMissingFile(t *testing.T) {
	if _, err := LoadProjectConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidateProjectConfigValid(t *testing.T) {
	pc, err := LoadProjectConfig(writeFile(t, "clockwork.cerberus.yaml", validProjectConfigYAML))
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	result := ValidateProjectConfig(pc)
	if result.HasErrors() {
		t.Fatalf("valid config reported errors: %v", result.Errors())
	}
	if len(result.Warnings()) != 0 {
		t.Errorf("valid config reported warnings: %v", result.Warnings())
	}
}

func TestValidateProjectConfigEnvelopeErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProjectConfig)
		field  string
	}{
		{"missing kind", func(p *ProjectConfig) { p.Kind = "" }, "kind"},
		{"wrong kind", func(p *ProjectConfig) { p.Kind = "cerberus-project/v2" }, "kind"},
		{"missing owner", func(p *ProjectConfig) { p.Owner = "" }, "owner"},
		{"bad owner", func(p *ProjectConfig) { p.Owner = "Clock_Work" }, "owner"},
		{"bad namespace", func(p *ProjectConfig) { p.Namespace = "Bad NS" }, "namespace"},
		{"missing project id", func(p *ProjectConfig) { p.Project.ID = "" }, "project.id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := newValidProjectConfig()
			tc.mutate(pc)
			result := ValidateProjectConfig(pc)
			if !result.HasErrors() {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !hasIssueField(result.Errors(), tc.field) {
				t.Errorf("expected error on field %q, got %v", tc.field, result.Errors())
			}
		})
	}
}

func TestValidateProjectConfigPortZeroIsError(t *testing.T) {
	pc := newValidProjectConfig()
	pc.Resources[0].Config = map[string]any{"port": 0}
	result := ValidateProjectConfig(pc)
	if !hasIssueField(result.Errors(), "resources[clockwork-api].config.port") {
		t.Errorf("port: 0 must be a validation error, got %v", result.Errors())
	}
}

func TestValidateProjectConfigDuplicateResourceID(t *testing.T) {
	pc := newValidProjectConfig()
	pc.Resources = append(pc.Resources, pc.Resources[0])
	if !ValidateProjectConfig(pc).HasErrors() {
		t.Fatal("duplicate resource id must be an error")
	}
}

func TestValidateProjectConfigMissingResourceFields(t *testing.T) {
	pc := newValidProjectConfig()
	pc.Resources[0].Type = ""
	pc.Resources[0].Connector = ""
	result := ValidateProjectConfig(pc)
	if !hasIssueField(result.Errors(), "resources[clockwork-api].type") {
		t.Errorf("expected missing-type error, got %v", result.Errors())
	}
	if !hasIssueField(result.Errors(), "resources[clockwork-api].connector") {
		t.Errorf("expected missing-connector error, got %v", result.Errors())
	}
}

func TestValidateProjectConfigCrossConfigRefsAreWarnings(t *testing.T) {
	pc := newValidProjectConfig()
	pc.Resources[0].Project = "some-other-app"
	pc.Resources[1].DependsOn = []string{"external-thing"}
	result := ValidateProjectConfig(pc)
	if result.HasErrors() {
		t.Fatalf("cross-config refs must not be errors: %v", result.Errors())
	}
	if len(result.Warnings()) != 2 {
		t.Errorf("warnings = %d, want 2: %v", len(result.Warnings()), result.Warnings())
	}
}

func TestValidateProjectConfigLegacyBuildIsWarning(t *testing.T) {
	pc := newValidProjectConfig()
	pc.Resources[0].Config = map[string]any{"build": []any{"make", "build"}}
	result := ValidateProjectConfig(pc)
	if result.HasErrors() {
		t.Fatalf("legacy build must not be an error: %v", result.Errors())
	}
	if !hasIssueField(result.Warnings(), "resources[clockwork-api].config.build") {
		t.Fatalf("expected deprecation warning for legacy build, got %v", result.Warnings())
	}
}

// ---- Bundle manifest ----

func TestLoadBundleResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, DefaultBundleFilename)
	content := "kind: cerberus-bundle/v1\nprojects:\n  - ./torque.cerberus.yaml\n  - /abs/clockwork.cerberus.yaml\n"
	if err := os.WriteFile(manifest, []byte(content), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	bundle, err := LoadBundle(manifest)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	want := filepath.Join(dir, "torque.cerberus.yaml")
	if bundle.Projects[0] != want {
		t.Errorf("relative path = %q, want %q", bundle.Projects[0], want)
	}
	if bundle.Projects[1] != "/abs/clockwork.cerberus.yaml" {
		t.Errorf("absolute path = %q, want unchanged", bundle.Projects[1])
	}
}

// TestLoadBundleRecordsUnknownField mirrors the project-config case:
// recorded, not rejected, at load time.
func TestLoadBundleRecordsUnknownField(t *testing.T) {
	bundle, err := LoadBundle(writeFile(t, DefaultBundleFilename,
		"kind: cerberus-bundle/v1\nprojects: []\nsurprise: true\n"))
	if err != nil {
		t.Fatalf("load must tolerate an unknown top-level field, got: %v", err)
	}
	if len(bundle.UnknownFields) != 1 {
		t.Fatalf("UnknownFields = %v, want one entry naming `surprise`", bundle.UnknownFields)
	}
	if issues := ValidateBundle(bundle).UnknownFieldIssues(); len(issues) != 1 {
		t.Fatalf("UnknownFieldIssues = %d, want 1", len(issues))
	}
}

func TestValidateBundle(t *testing.T) {
	if ValidateBundle(&Bundle{Kind: BundleKind, Projects: []string{"a.cerberus.yaml"}}).HasErrors() {
		t.Error("valid manifest reported errors")
	}
	if !ValidateBundle(&Bundle{Kind: "wrong", Projects: []string{"a"}}).HasErrors() {
		t.Error("wrong kind must be an error")
	}
	if !ValidateBundle(&Bundle{Kind: BundleKind}).HasErrors() {
		t.Error("empty projects list must be an error")
	}
}

func TestPeekKind(t *testing.T) {
	pc := writeFile(t, "p.cerberus.yaml", validProjectConfigYAML)
	if kind, err := PeekKind(pc); err != nil || kind != ProjectConfigKind {
		t.Errorf("PeekKind = %q, %v; want %q", kind, err, ProjectConfigKind)
	}
}

// ---- helpers ----

func newValidProjectConfig() *ProjectConfig {
	return &ProjectConfig{
		Kind:      ProjectConfigKind,
		Owner:     "clockwork",
		Namespace: DefaultNamespace,
		Project:   config.ProjectDef{ID: "clockwork", Name: "Clockwork"},
		Resources: []config.ResourceDef{
			{ID: "clockwork-api", Name: "Clockwork API", Type: "process", Connector: "local", Project: "clockwork"},
			{ID: "clockwork-worker", Name: "Clockwork Worker", Type: "process", Connector: "local", Project: "clockwork"},
		},
	}
}

func hasIssueField(issues []ValidationIssue, field string) bool {
	for _, issue := range issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}
