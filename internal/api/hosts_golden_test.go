package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
)

// L9 answers what the old build answered for the same hosts. testdata/golden-l9 holds a fixture
// hosts.yaml and state file and the JSON main's binary printed for them (`cerberus host list -o
// json`, with and without filters, which serializes the same []HostView as the old socket's
// /hosts), captured under a scratch HOME with the home directory replaced by @HOME@. Every
// fixture timestamp is in the past, so stale is true on both sides.
func TestHostsMatchTheOldBuild(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "golden-l9", name)) //nolint:gosec // fixed test data names
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "@HOME@", home)
	}
	for dst, src := range map[string]string{filepath.Join(root, "hosts.yaml"): "hosts.yaml", filepath.Join(root, "state", "hosts.json"): "state-hosts.json"} {
		if err := os.WriteFile(dst, []byte(read(src)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil },
		Hosts: FileHosts{HostsPath: filepath.Join(root, "hosts.yaml"), StatePath: filepath.Join(root, "state", "hosts.json")}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for query, golden := range map[string]string{"": "list.json", "?role=builder": "list.role-builder.json", "?label=tier%3Dgold": "list.label-tier-gold.json"} {
		rec := httptest.NewRecorder()
		a.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hosts"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		var got, want any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(read(golden)), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			g, _ := json.MarshalIndent(got, "", " ")
			w, _ := json.MarshalIndent(want, "", " ")
			t.Errorf("GET /api/hosts%s differs from the old build:\n got %s\nwant %s", query, g, w)
		}
	}
}
