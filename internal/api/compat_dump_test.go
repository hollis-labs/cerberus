package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
)

// TestHostFilesForTheOldBuild changes hosts.yaml through this build's writes (a host update, a
// component created and moved onto the host) and, with CERBERUS_COMPAT_OUT set, copies hosts.yaml
// and hosts.history.jsonl there for main's binary to read (`host list -o json`, `host history`):
// the compatibility check of slice 8b, old reads what new wrote.
func TestHostFilesForTheOldBuild(t *testing.T) {
	w := newHostWorld(t)
	g := gatetest.New(t, gatetest.Options{})
	a := w.api(t, g)
	a.o.HostWriter = w.writer(g)
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/api/hosts/zeta", `"patch":{"labels":{"rack":"4"},"owner":"ops-team"}`},
		{"POST", "/api/components", `"component":{"id":"ram-1","kind":"ram","size":"16GiB","location":{"spare":true}}`},
		{"PATCH", "/api/hosts/alpha", `"patch":{"labels":{"tier":"bronze"}}`},
	} {
		rev := revisionOf(t, a)
		if code, body := send(t, a, apiKey, c.method, c.path, `{"revision":"`+rev+`","acknowledged":true,`+c.body+`}`); code != 200 || !strings.Contains(body, `"changed":true`) {
			t.Fatalf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
	out := os.Getenv("CERBERUS_COMPAT_OUT")
	if out == "" {
		return
	}
	for _, name := range []string{"hosts.yaml", "hosts.history.jsonl"} {
		b, err := os.ReadFile(filepath.Join(w.root, name)) //nolint:gosec // the test's own files
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name), b, 0o600); err != nil { //nolint:gosec // the operator's own scratch directory, for the compatibility check
			t.Fatal(err)
		}
	}
}
