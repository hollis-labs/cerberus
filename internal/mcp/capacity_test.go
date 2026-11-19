package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
)

// cerberus_host_capacity sends its arguments as the query and answers with main's notes: the
// counts, then each unknown and excluded host with its reasons.
func TestHostCapacityTool(t *testing.T) {
	var query map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/hosts/capacity" || json.NewDecoder(r.Body).Decode(&query) != nil {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"matched":[],"unknown":[{"name":"mid","state":"unknown","reasons":["never probed"],"next_step":"Probe it."}],` +
			`"excluded":[{"name":"box","state":"online","reasons":["4 cpu, 16 needed"]}],"max_stale_age":"10m0s","total_hosts":2}`))
	})
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: h}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, stdioScopes)
	out, isErr := call(t, cs, "cerberus_host_capacity", map[string]any{"min_cpu": 16, "explain": true})
	if isErr || query["min_cpu"] != float64(16) || query["explain"] != true {
		t.Fatalf("capacity: %v %s (query %v)", isErr, out, query)
	}
	for _, want := range []string{"0 matched, 1 unknown, of 2 declared hosts (probe age limit 10m0s)",
		"UNKNOWN mid (not guessed to fit): never probed. Probe it.", "EXCLUDED box: 4 cpu, 16 needed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}
