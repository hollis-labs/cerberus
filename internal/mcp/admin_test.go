package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/target"
)

type memKeyring map[string]string

func (m memKeyring) Get(_ context.Context, service, key string) (string, error) {
	return m[service+"/"+key], nil
}
func (m memKeyring) Set(_ context.Context, service, key, value string) error {
	m[service+"/"+key] = value
	return nil
}
func (m memKeyring) Delete(_ context.Context, service, key string) error {
	delete(m, service+"/"+key)
	return nil
}

// adminLane is the API with the admin lane (slice 7) over a scratch adopted root and one
// production ssh host, behind the real gate.
func adminLane(t *testing.T) *api.API {
	t.Helper()
	w := gatetest.New(t, gatetest.Options{})
	box := config.ResourceDef{ID: "box", Name: "box", Type: "server", Connector: "ssh", Env: target.EnvProd, Owner: target.OwnerSelf,
		Admin: target.Admin{Default: target.AdminSelf}, Config: map[string]any{"host": "box.invalid", "key_file": "/keys/box"}}
	root := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	svc, err := admin.New(admin.Options{Gate: w.Gate, Paths: admin.Paths{AdoptRoot: root, ConfigDir: t.TempDir()}, Keyring: memKeyring{},
		Resources: func(id string) (*config.ResourceDef, bool) {
			if id != box.ID {
				return nil, false
			}
			return &box, true
		}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return []config.ResourceDef{box}, nil },
		Home: t.TempDir(), Gate: w.Gate, Admin: svc})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The admin lane's tools (slice 7) answer as main's did over the admin lane's routes: the
// connector list and descriptions, an ssh exec that stops at the gate with main's refusal, the
// docker tools' argument checks; and the three read tools served by the admin route need
// cerberus.admin.
func TestAdminLaneTools(t *testing.T) {
	a := adminLane(t)
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: a.Mux()}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, stdioScopes)

	if out, isErr := call(t, cs, "cerberus_connector_list", map[string]any{}); isErr || !strings.Contains(out, `"id":"ssh"`) || !strings.Contains(out, `"id":"docker"`) {
		t.Fatalf("connector_list: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "cerberus_connector_describe", map[string]any{"id": "ssh"}); isErr || !strings.Contains(out, `"id": "ssh"`) || !strings.Contains(out, `"exec"`) {
		t.Fatalf("connector_describe: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "cerberus_connector_describe", map[string]any{"id": "nope"}); !isErr || !strings.Contains(out, "connector not found: nope") {
		t.Fatalf("connector_describe of an unknown id: %v %s", isErr, out)
	}
	out, isErr := call(t, cs, "cerberus_ssh_exec", map[string]any{"resource_id": "box", "command": "uptime", "acknowledged": true})
	var refusal struct {
		Code     string `json:"code"`
		NextStep string `json:"next_step"`
	}
	if !isErr || json.Unmarshal([]byte(out), &refusal) != nil || refusal.Code != "approval_pending" || refusal.NextStep == "" {
		t.Fatalf("ssh_exec on a production host: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "cerberus_docker_up", map[string]any{"resource_id": "a", "container_name": "b", "acknowledged": true}); !isErr || !strings.Contains(out, "not both") {
		t.Fatalf("docker_up with both: %v %s", isErr, out)
	}

	reader := connect(t, app, []string{api.ScopeRead})
	for _, tool := range []string{"cerberus_ssh_status", "cerberus_docker_ps", "cerberus_docker_logs"} {
		if out, isErr := call(t, reader, tool, map[string]any{"resource_id": "box", "container": "c"}); !isErr || strings.Contains(out, "approval") {
			t.Fatalf("%s with read only: %v %s", tool, isErr, out)
		}
	}
	if out, isErr := call(t, reader, "cerberus_connector_list", map[string]any{}); isErr {
		t.Fatalf("connector_list with read: %s", out)
	}
}
