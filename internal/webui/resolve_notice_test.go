package webui

import (
	"encoding/json"
	"github.com/hollis-labs/cerberus/internal/audit"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/registry"
)

// registryFixture writes a config.yaml plus a sibling registry index
// pointing at one healthy config, one carrying a field this binary does
// not know, and one that fails validation. Returns the config.yaml path.
func registryFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("version: 2\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	bodies := map[string]string{
		"cleanapp": `kind: cerberus-project/v1
owner: cleanapp
project:
  id: cleanapp
  name: Clean App
resources:
  - id: cleanapp-api
    name: Clean API
    type: process
    connector: local
    project: cleanapp
`,
		"futureapp": `kind: cerberus-project/v1
owner: futureapp
future_top_level_field: written-by-a-newer-cerberus
project:
  id: futureapp
  name: Future App
resources:
  - id: futureapp-api
    name: Future API
    type: process
    connector: local
    project: futureapp
`,
		"brokenapp": `kind: cerberus-project/v1
owner: brokenapp
project:
  id: brokenapp
  name: Broken App
resources:
  - id: brokenapp-api
    name: Broken API
    type: process
    connector: local
    project: brokenapp
    config:
      port: 0
`,
	}

	index := "version: 1\nentries:\n"
	for owner, body := range bodies {
		path := filepath.Join(dir, owner+".cerberus.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", owner, err)
		}
		index += "    - owner: " + owner + "\n" +
			"      namespace: local\n" +
			"      path: " + path + "\n" +
			"      kind: cerberus-project/v1\n" +
			"      registered_at: \"2026-09-09T00:00:00Z\"\n"
	}

	indexPath, err := registry.IndexPathFor(cfgPath)
	if err != nil {
		t.Fatalf("IndexPathFor: %v", err)
	}
	if err := os.WriteFile(indexPath, []byte(index), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	return cfgPath
}

// The console's Resources and Projects pages read their skip banner from
// /api/registry. Skips were already reported there; warned configs were
// not, so a config carrying an unknown field looked identical to a clean
// one everywhere in the console.
func TestRegistryEndpointReportsSkippedAndWarned(t *testing.T) {
	srv, err := New(&fakeClient{}, audit.NewMemory(), registryFixture(t), nil, nil)
	if err != nil {
		t.Fatalf("webui.New: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler(testGuard()).ServeHTTP(rec, newTestRequest(http.MethodGet, "/api/registry", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Summary struct {
			ResolveSkips  int `json:"resolve_skips"`
			ResolveWarned int `json:"resolve_warned"`
		} `json:"summary"`
		Skipped []struct {
			Owner string `json:"owner"`
		} `json:"skipped"`
		Warned []struct {
			Owner  string `json:"owner"`
			Detail string `json:"detail"`
		} `json:"warned"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Summary.ResolveSkips != 1 || resp.Summary.ResolveWarned != 1 {
		t.Errorf("summary = %+v, want resolve_skips=1 resolve_warned=1", resp.Summary)
	}
	if len(resp.Skipped) != 1 || resp.Skipped[0].Owner != "brokenapp" {
		t.Errorf("skipped = %+v, want [brokenapp]", resp.Skipped)
	}
	if len(resp.Warned) != 1 || resp.Warned[0].Owner != "futureapp" {
		t.Fatalf("warned = %+v, want [futureapp]", resp.Warned)
	}
	if resp.Warned[0].Detail == "" {
		t.Error("warned entry has no detail; the banner can name the config but not the reason")
	}
}
