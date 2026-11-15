package mcp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/config"
	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

type recordingLane struct{ verbs []string }

func (l *recordingLane) Mutate(_ context.Context, req supervise.Request) (*supervise.OpResult, error) {
	l.verbs = append(l.verbs, req.Verb)
	return &supervise.OpResult{Success: true, ServiceID: req.ID, Message: "resource " + req.ID + " " + req.Verb + "ed"}, nil
}

// ensure_fresh composes over /api as main's client composed it over the socket: with no
// recommended action it runs nothing and says why; with force it deploys through L7.
func TestEnsureFresh(t *testing.T) {
	lane := &recordingLane{}
	web := config.ResourceDef{ID: "web", Type: "process", Project: "shop", Connector: "local",
		Config: map[string]any{"mode": "os_service", "supervisor": "systemd_user", "command": []any{"/bin/true"}}}
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return []config.ResourceDef{web}, nil },
		Home: t.TempDir(), Mutator: lane})
	if err != nil {
		t.Fatal(err)
	}
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: a.Mux()}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, stdioScopes)
	if out, isErr := call(t, cs, "cerberus_resource_ensure_fresh", map[string]any{"resource_id": "web", "acknowledged": true}); isErr ||
		!strings.Contains(out, `"action": "noop"`) || !strings.Contains(out, "no built-binary drift detected") || len(lane.verbs) != 0 {
		t.Fatalf("ensure_fresh: %v %s (ran %v)", isErr, out, lane.verbs)
	}
	if out, isErr := call(t, cs, "cerberus_resource_ensure_fresh", map[string]any{"resource_id": "web", "force": true, "acknowledged": true}); isErr ||
		!strings.Contains(out, `"action": "deploy"`) || !strings.Contains(out, `"reason": "forced rebuild"`) || strings.Join(lane.verbs, ",") != "deploy" {
		t.Fatalf("ensure_fresh --force: %v %s (ran %v)", isErr, out, lane.verbs)
	}
}
