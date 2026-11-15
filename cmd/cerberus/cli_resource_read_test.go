package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/registry"
)

// readCmds is `resource` with the read family against c and the registry in root.
func readCmds(c *apiClient, root string) []*cobra.Command {
	r := &cobra.Command{Use: "resource"}
	r.AddCommand(resourceReadCommands(func() (*apiClient, error) { return c, nil },
		func() (*registry.Registry, error) { return registry.ForRoot(root), nil })...)
	return []*cobra.Command{r}
}

// `resource show` is old's config view from the registry, with the process spec's defaults
// spelled out and the config redacted; an unknown id is old's error. It needs no server.
func TestResourceShowIsTheOldConfigView(t *testing.T) {
	root := t.TempDir()
	desc, err := filepath.Abs(filepath.Join("testdata", "golden-cli", "shop.cerberus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.ForRoot(root).Register(desc); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, readCmds(nil, root), "", "resource", "show", "dev")
	want := "ID:        dev\nName:      Dev\nType:      process\nProject:   shop\nConnector: local\nTags:      ui\n" +
		"Mode:      dev_session\nSupervisor:auto\nRun From:  workspace\nConfig:\n  {\n    \"command\": [\n      \"npm\",\n      \"run\",\n      \"dev\"\n    ],\n    \"port\": 5173\n  }\n"
	if err != nil || out != want {
		t.Fatalf("show dev: %v\n got:\n%s\nwant:\n%s", err, out, want)
	}
	out, _, err = runCLI(t, readCmds(nil, root), "", "resource", "show", "box")
	if err != nil || !strings.HasPrefix(out, "ID:        box\nName:      Box\nType:      server\nProject:   shop\nConnector: ssh\nConfig:\n") || strings.Contains(out, "Mode:") {
		t.Fatalf("show box: %v\n%s", err, out)
	}
	if _, _, err = runCLI(t, readCmds(nil, root), "", "resource", "show", "nope"); err == nil ||
		err.Error() != `resource "nope" not found in config; run `+"`cerberus resource list`"+` to see available resources` {
		t.Fatalf("show nope: %v", err)
	}
}

// `resource inspect` and `doctor` read L4 and L5 and print old's records; a resource the local
// lane does not supervise is old's "does not apply" error naming the connector's verbs.
func TestResourceInspectAndDoctorAtTheCLI(t *testing.T) {
	c := goldenServer(t)
	out, _, err := runCLI(t, readCmds(c, t.TempDir()), "", "resource", "inspect", "dev")
	want := "Resource:    dev\nName:        Dev\nStatus:      stopped\nType:        process\nProject:     shop\nConnector:   local\n" +
		"Mode:        dev_session\nSupervisor:  auto\nRun From:    workspace\n"
	if err != nil || out != want {
		t.Fatalf("inspect dev: %v\n got:\n%s\nwant:\n%s", err, out, want)
	}
	out, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", "doctor", "dev")
	want = "Resource:    dev\nStatus:      stopped\nSummary:     observe mode: this build reports state only; the full diagnosis arrives with supervision\nChecks:\n"
	if err != nil || out != want {
		t.Fatalf("doctor dev: %v\n got:\n%s\nwant:\n%s", err, out, want)
	}
	for _, verb := range []string{"inspect", "doctor"} {
		_, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", verb, "box")
		if err == nil || err.Error() != verb+` does not apply to "box": server/ssh resources are administered through the ssh connector, not supervised by the local runtime lane; try: cerberus ssh status box | cerberus ssh exec box -- <command>` {
			t.Fatalf("%s box: %v", verb, err)
		}
		if _, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", verb, "nope"); err == nil || !strings.Contains(err.Error(), "404 not_found") {
			t.Fatalf("%s nope: %v", verb, err)
		}
	}
}

// adviceServer answers L3 with status for every id and records the mutations it is sent.
type adviceServer struct {
	status api.ResourceRuntimeStatus
	posts  []string
	bodies []mutationBody
	refuse string
}

func (a *adviceServer) client(t *testing.T) *apiClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/resources/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		st := a.status
		st.ID = r.PathValue("id")
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("POST /api/resources/{id}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		var b mutationBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.posts, a.bodies = append(a.posts, r.PathValue("verb")), append(a.bodies, b)
		if a.refuse != "" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(a.refuse))
			return
		}
		_ = json.NewEncoder(w).Encode(api.OpResult{Success: true, ServiceID: r.PathValue("id"), Message: r.PathValue("verb") + " done"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &apiClient{base: srv.URL + "/api", key: "test", keyFile: "test.key", http: srv.Client()}
}

// `resource ensure-fresh` is old's composition: --force deploys; otherwise the status's
// recommended deploy, apply or sync runs, with --ack, --approval and the install override passed
// to that one call. With no advice (this build computes none yet) it is pending, exit 1, and
// sends nothing; inspect advice and an unsupervised resource are refusals that send nothing; a
// pending approval on the chosen verb names the console.
func TestResourceEnsureFreshAtTheCLI(t *testing.T) {
	a := &adviceServer{status: api.ResourceRuntimeStatus{Status: "running"}}
	c := a.client(t)

	_, _, err := runCLI(t, readCmds(c, t.TempDir()), "", "resource", "ensure-fresh", "web", "--ack")
	if err == nil || !strings.Contains(err.Error(), "ensure-fresh web: pending (slice 8b)") || !strings.Contains(err.Error(), "nothing was changed") || len(a.posts) != 0 {
		t.Fatalf("no advice: %v %v", err, a.posts)
	}

	out, _, err := runCLI(t, readCmds(c, t.TempDir()), "", "resource", "ensure-fresh", "web", "--force", "--ack", "--approval", "apr_1", "--no-install-after-build")
	if err != nil || out != "web: deploy (forced rebuild)\ndeploy done\n" || len(a.posts) != 1 || a.posts[0] != "deploy" ||
		!a.bodies[0].Acknowledged || a.bodies[0].ApprovalID != "apr_1" || a.bodies[0].InstallAfterBuildOverride == nil || *a.bodies[0].InstallAfterBuildOverride {
		t.Fatalf("--force: %v %q %v %+v", err, out, a.posts, a.bodies)
	}

	for _, verb := range []string{"deploy", "apply", "sync"} {
		a.posts, a.bodies = nil, nil
		a.status = api.ResourceRuntimeStatus{Status: "running", RecommendedAction: verb, RecommendedReason: "installed artifact is stale"}
		out, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", "ensure-fresh", "web", "--ack")
		if err != nil || out != "web: "+verb+" (installed artifact is stale)\n"+verb+" done\n" || len(a.posts) != 1 || a.posts[0] != verb || !a.bodies[0].Acknowledged || a.bodies[0].InstallAfterBuildOverride != nil {
			t.Fatalf("advice %s: %v %q %v %+v", verb, err, out, a.posts, a.bodies)
		}
	}

	a.posts = nil
	a.status = api.ResourceRuntimeStatus{Status: "running", RecommendedAction: "inspect", RecommendedReason: "activation unconfirmed"}
	if _, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", "ensure-fresh", "web", "--ack"); err == nil ||
		!strings.Contains(err.Error(), "Run `cerberus resource inspect <resource-id>`") || len(a.posts) != 0 {
		t.Fatalf("inspect advice: %v %v", err, a.posts)
	}
	a.status = api.ResourceRuntimeStatus{Status: "unsupervised", RecommendedReason: "server/ssh resources are administered through the ssh connector", RecommendedNextStep: "cerberus ssh status web"}
	if _, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", "ensure-fresh", "web", "--ack"); err == nil ||
		!strings.HasPrefix(err.Error(), `ensure-fresh does not apply to "web"`) || len(a.posts) != 0 {
		t.Fatalf("unsupervised: %v %v", err, a.posts)
	}

	a.refuse = `{"code":"approval_pending","message":"local deploy needs approval","details":{"approval":{"id":"apr_9","expires_at":"0001-01-01T00:00:00Z","approve_with":"approve it on the console: /approvals/apr_9","channel":"out_of_band"}}}`
	if _, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", "ensure-fresh", "web", "--force", "--ack"); err == nil ||
		!strings.Contains(err.Error(), "Approve apr_9 on the console, signed in as the owner, then run this again with --approval apr_9") {
		t.Fatalf("pending: %v", err)
	}
	if _, _, err = runCLI(t, readCmds(c, t.TempDir()), "", "resource", "ensure-fresh", "web", "--force", "--install-after-build", "--no-install-after-build"); err == nil ||
		!strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("both install flags: %v", err)
	}
}
