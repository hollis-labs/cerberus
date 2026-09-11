package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigKeyTypoRejectsRegistrationButKeepsExistingResourceVisible(t *testing.T) {
	path := writeFile(t, "app.cerberus.yaml", validProjectConfigYAML)
	reg := New(filepath.Join(t.TempDir(), DefaultIndexFilename))
	if _, err := reg.Register(path); err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(validProjectConfigYAML, "port: 8080", "port: 8080\n      ENV: {API_KEY: secret-sentinel}", 1)
	if err := os.WriteFile(path, []byte(mutated), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Register(path); err == nil || !strings.Contains(err.Error(), "config.ENV") {
		t.Fatalf("authoring accepted typo: %v", err)
	}
	resolved, err := Resolve(ResolveOptions{IndexPath: reg.IndexPath()})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(resolved.Warnings, " ")
	if !strings.Contains(warnings, "config.ENV") || strings.Contains(warnings, "secret-sentinel") {
		t.Fatalf("missing warning or leaked value: %s", warnings)
	}
	for _, resource := range resolved.Config.Resources {
		if resource.ID == "clockwork-api" {
			return
		}
	}
	t.Fatal("runtime silently dropped resource with a config typo")
}
