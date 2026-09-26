package cerbapi

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// oldDaemon serves /connectors/ the way a daemon predating plans did: the
// exact three-part route, and a body whose plan field it does not know. Any
// call that reaches it runs.
func oldDaemon(t *testing.T) (*SocketClient, *atomic.Int32) {
	t.Helper()
	sock := filepath.Join(tempSocketDir(t), "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/connectors/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/connectors/"), "/")
		if len(parts) != 3 || parts[0] == "" || parts[1] != "operations" || parts[2] == "" {
			writeJSONError(w, http.StatusNotFound, "expected /connectors/{id}/operations/{operation}")
			return
		}
		ran.Add(1)
		writeJSON(w, http.StatusOK, ExternalConnectorOperationResult{Connector: parts[0], Operation: parts[2], Data: map[string]any{"ran": true}})
	})
	srv := &http.Server{Handler: mux} //nolint:gosec // a test server on a temp unix socket
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return NewSocketClient(sock), &ran
}

// A plan request to a daemon that predates plans is refused and runs
// nothing, and the refusal says why. Sent as a body field on the old route,
// the same request would have run.
func TestPlanRequestNeverRunsOnAnOlderDaemon(t *testing.T) {
	client, ran := oldDaemon(t)
	args := ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Config: map[string]any{"container": "web"}, Acknowledged: true, Plan: true}
	_, err := client.ExecuteConnectorOperation(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "predates plans") || !strings.Contains(err.Error(), "nothing ran") {
		t.Fatalf("err = %v", err)
	}
	if got := redact.Text(err.Error()); got != err.Error() {
		t.Fatalf("redaction ate the recovery:\n  %s\n  %s", err.Error(), got)
	}
	if ran.Load() != 0 {
		t.Fatal("a plan request ran the operation on an older daemon")
	}
	// The same call on the ordinary route reaches the old daemon and runs:
	// that is what the plan route keeps a plan request from doing. (The fake
	// does not speak the stream envelope, so only the run is checked.)
	args.Plan = false
	_, _ = client.ExecuteConnectorOperation(context.Background(), args)
	if ran.Load() != 1 {
		t.Fatalf("the ordinary route ran %d times, want 1", ran.Load())
	}
}

// A current daemon answers the plan route with the plan, and runs nothing.
func TestPlanRouteAnswersWithThePlan(t *testing.T) {
	svc, backend := dockerLane(t, audit.NewMemory())
	client := startConnectorSocket(t, NewInProcessClient(WithExternalConnectorService(svc)))
	res, err := client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "stop", Config: map[string]any{"resource": "dev-box", "container": "web"}, Acknowledged: true, Plan: true})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := res.Data.(map[string]any)
	if hash, _ := data["plan_hash"].(string); !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("result %#v", res.Data)
	}
	if backend.stopped != "" {
		t.Fatal("the plan route ran the operation")
	}
}
