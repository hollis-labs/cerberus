package registry

import (
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
)

// The slug is the portfolio-wide join key: Cerberus's registry key, the
// Tesseract namespace segment, and the agent-setup template basename.
// A slug that round-trips differently through any of those is a join
// that silently misses, so the pattern is stricter than the one used
// for owner and namespace.
func TestValidateProjectConfigSlugPattern(t *testing.T) {
	valid := []string{"cerberus", "stack-explorer", "chrispian-dev", "nil", "h2o", "a"}
	for _, slug := range valid {
		t.Run("valid/"+slug, func(t *testing.T) {
			pc := newValidProjectConfig()
			pc.Owner = slug
			pc.Project.ID = slug
			for i := range pc.Resources {
				pc.Resources[i].Project = slug
			}
			if result := ValidateProjectConfig(pc); result.HasErrors() {
				t.Errorf("slug %q rejected: %v", slug, result.Errors())
			}
		})
	}

	// The four hyphen cases are the ones identPattern (used for owner
	// and namespace) would let through; they are why the slug gets its
	// own stricter pattern rather than reusing that one.
	invalid := map[string]string{
		"uppercase":       "Cerberus",
		"underscore":      "stack_explorer",
		"trailing hyphen": "cerberus-",
		"leading hyphen":  "-cerberus",
		"doubled hyphen":  "stack--explorer",
		"dot":             "chrispian.dev",
		"space":           "stack explorer",
		"slash":           "hollis/cerberus",
	}

	for name, slug := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			pc := newValidProjectConfig()
			pc.Owner = slug
			pc.Project.ID = slug
			result := ValidateProjectConfig(pc)
			if !result.HasErrors() {
				t.Fatalf("slug %q accepted; want rejected", slug)
			}
			if !hasIssueField(result.Errors(), "project.id") && !hasIssueField(result.Errors(), "owner") {
				t.Errorf("slug %q rejected on the wrong field: %v", slug, result.Errors())
			}
		})
	}
}

// owner and project.id name the same project. Two names for one thing is
// a join waiting to pick the wrong one; they have never diverged across
// the 18 registered configs and this keeps it that way.
func TestValidateProjectConfigOwnerMustMatchSlug(t *testing.T) {
	pc := newValidProjectConfig()
	pc.Owner = "clockwork"
	pc.Project.ID = "clockwork-api"

	result := ValidateProjectConfig(pc)
	if !result.HasErrors() {
		t.Fatal("divergent owner and project.id accepted")
	}
	if !hasIssueField(result.Errors(), "project.id") {
		t.Errorf("expected error on project.id, got %v", result.Errors())
	}
	var named bool
	for _, issue := range result.Errors() {
		if strings.Contains(issue.Message, "clockwork") && strings.Contains(issue.Message, "clockwork-api") {
			named = true
		}
	}
	if !named {
		t.Errorf("error should name both values so the fix is obvious: %v", result.Errors())
	}
}

// A config need only write the slug once. An omitted owner used to be a
// hard validation error, so defaulting it changes nothing that already
// works.
func TestLoadProjectConfigDefaultsOwnerFromSlug(t *testing.T) {
	const body = `kind: cerberus-project/v1
project:
  id: clockwork
  name: Clockwork
resources:
  - id: clockwork-api
    name: Clockwork API
    type: process
    connector: local
    project: clockwork
`
	pc, err := LoadProjectConfig(writeFile(t, "clockwork.cerberus.yaml", body))
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if pc.Owner != "clockwork" {
		t.Errorf("Owner = %q, want it defaulted from project.id", pc.Owner)
	}
	if result := ValidateProjectConfig(pc); result.HasErrors() {
		t.Errorf("config with only project.id rejected: %v", result.Errors())
	}
}

// ---- capabilities and links (CW-20260909-0021) ----

func TestLoadProjectConfigReadsCapabilitiesAndLinks(t *testing.T) {
	const body = `kind: cerberus-project/v1
owner: clockwork
project:
  id: clockwork
  name: Clockwork
  capabilities: [go, launchd, mcp]
  links:
    - { kind: repo, target: "git@github.com:hollis-labs/clockwork.git" }
    - { kind: docs, target: ./docs }
    - { kind: owned_by, target: "org:hollis-labs" }
resources:
  - id: clockwork-api
    name: Clockwork API
    type: process
    connector: local
    project: clockwork
`
	pc, err := LoadProjectConfig(writeFile(t, "clockwork.cerberus.yaml", body))
	if err != nil {
		t.Fatalf("LoadProjectConfig: %v", err)
	}
	if got := pc.Project.Capabilities; len(got) != 3 || got[0] != "go" || got[2] != "mcp" {
		t.Errorf("Capabilities = %v, want [go launchd mcp]", got)
	}
	if len(pc.Project.Links) != 3 {
		t.Fatalf("Links = %d, want 3", len(pc.Project.Links))
	}
	if pc.Project.Links[0] != (config.Link{Kind: "repo", Target: "git@github.com:hollis-labs/clockwork.git"}) {
		t.Errorf("Links[0] = %+v", pc.Project.Links[0])
	}
	if pc.Project.Links[2].Kind != "owned_by" {
		t.Errorf("Links[2].Kind = %q, want owned_by", pc.Project.Links[2].Kind)
	}
	if result := ValidateProjectConfig(pc); result.HasErrors() {
		t.Errorf("valid capabilities/links rejected: %v", result.Errors())
	}
}

// link.kind is free-form on purpose (Tether ADR 0041 D16): a closed
// vocabulary would need a coordinated schema change in every reader for
// each new relation. A kind nobody has blessed still has to validate.
func TestValidateProjectConfigAcceptsUnblessedLinkKind(t *testing.T) {
	pc := newValidProjectConfig()
	pc.Project.Links = []config.Link{
		{Kind: "some_future_relation", Target: "whatever://target"},
	}
	if result := ValidateProjectConfig(pc); result.HasErrors() {
		t.Errorf("unblessed link kind rejected: %v", result.Errors())
	}
}

func TestValidateProjectConfigIncompleteLink(t *testing.T) {
	cases := []struct {
		name  string
		link  config.Link
		field string
	}{
		{"missing kind", config.Link{Target: "./docs"}, "project.links[0].kind"},
		{"missing target", config.Link{Kind: "docs"}, "project.links[0].target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := newValidProjectConfig()
			pc.Project.Links = []config.Link{tc.link}
			result := ValidateProjectConfig(pc)
			if !result.HasErrors() {
				t.Fatalf("incomplete link accepted: %+v", tc.link)
			}
			if !hasIssueField(result.Errors(), tc.field) {
				t.Errorf("expected error on %q, got %v", tc.field, result.Errors())
			}
		})
	}
}
