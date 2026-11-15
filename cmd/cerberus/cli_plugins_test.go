package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libauth "github.com/hollis-labs/libs/security/auth"
	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
)

// reviewServer is a brakeServer whose API runs an admin lane over a scratch adopted root.
func newReviewServer(t *testing.T, root string) *brakeServer {
	t.Helper()
	s := &brakeServer{as: &libauth.Principal{}, w: gatetest.New(t, gatetest.Options{})}
	*s.as = cliKey
	if root == "" {
		root = filepath.Join(t.TempDir(), "adopted")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	svc, err := admin.New(admin.Options{Gate: s.w.Gate, Paths: admin.Paths{AdoptRoot: root, ConfigDir: t.TempDir()}, HostVersion: "test", Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Plugins().Stop(context.Background()) })
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(), Gate: s.w.Gate, Admin: svc})
	if err != nil {
		t.Fatal(err)
	}
	mux := a.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(libauth.WithPrincipal(r.Context(), *s.as)))
	}))
	t.Cleanup(srv.Close)
	s.c = &apiClient{base: srv.URL + "/api", key: "test", keyFile: "/keys/cli.key", http: srv.Client()}
	return s
}

// runPlugins runs the plugin verbs with args and stdin, on a terminal or not.
func (s *brakeServer) runPlugins(t *testing.T, terminal bool, stdin string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cerberus", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(pluginCommands(func() (*apiClient, error) { return s.c, nil }, func() bool { return terminal })...)
	var out bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// The old `connectors plugin managed install <dir>` over the API with the CLI's key. Asking is
// the old interactive act: on a terminal, the review printed and the plugin id typed; the key
// is answered with the owner's out-of-band approval, and the CLI says to approve it on the
// console and run it again with --approval. A pending approval installs nothing (an error);
// once the owner approves, --approval installs with no terminal, and prints the old "Accepted"
// line. Installing the same bundle again is the old "already installed" message.
func TestPluginInstallAtTheCLINeedsTheOwnersApproval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "adopted")
	s := newReviewServer(t, root)
	src := filepath.Join(t.TempDir(), "fresh")
	if err := admintest.WriteSource(src, echoPlugin, "fresh"); err != nil {
		t.Fatal(err)
	}
	install := []string{"connectors", "plugin", "managed", "install", src}
	if _, err := s.runPlugins(t, false, "fresh\n", install...); err == nil || !strings.Contains(err.Error(), "interactive terminal") || !strings.Contains(err.Error(), "--approval <id>") {
		t.Fatalf("asking off a terminal: %v", err)
	}
	out, err := s.runPlugins(t, true, "echo\n", install...)
	if err == nil || !strings.Contains(err.Error(), "did not match the plugin id") || !strings.Contains(out, "Accept? Type the plugin id (fresh) to confirm: ") {
		t.Fatalf("a wrong id typed: %v\n%s", err, out)
	}
	if len(s.w.Broker.List()) != 0 {
		t.Fatal("a refused ask asked the owner")
	}
	out, err = s.runPlugins(t, true, "fresh\n", install...)
	if err != nil {
		t.Fatal(err)
	}
	id := s.onlyApproval(t)
	if !strings.Contains(out, "fresh") || !strings.Contains(out, "Approve "+id+" on the console") || !strings.Contains(out, "--approval "+id) {
		t.Fatalf("pending install output:\n%s", out)
	}
	withApproval := append(append([]string(nil), install...), "--approval", id)
	if _, err = s.runPlugins(t, false, "", withApproval...); err == nil || !strings.Contains(err.Error(), "409 approval_pending") || !strings.Contains(err.Error(), "not approved yet") {
		t.Fatalf("an install on a pending approval: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "plugin-connectors.json")); !os.IsNotExist(err) {
		t.Fatalf("a pending approval wrote the state file: %v", err)
	}
	s.ownerApproves(t, id)
	out, err = s.runPlugins(t, false, "", withApproval...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	stored, _ := filepath.Glob(filepath.Join(root, "plugins", "fresh", "*"))
	if len(stored) != 1 || !strings.Contains(out, "Accepted fresh 1.0.0 (sha256:"+filepath.Base(stored[0])+"); it runs from "+stored[0]+"\n") ||
		!strings.Contains(out, "Cerberus has registered fresh") {
		t.Fatalf("accepted output (store %v):\n%s", stored, out)
	}
	out, err = s.runPlugins(t, true, "", install...)
	if err != nil || !strings.Contains(out, "already installed") {
		t.Fatalf("the same bundle again: %v\n%s", err, out)
	}
}

// A refusal of the key's accept (an approval that does not exist) names both owner paths.
func TestPluginReviewAtTheCLINamesTheOwnerPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "adopted")
	if _, err := admintest.Install(root, echoPlugin, admintest.Plugin{Pending: true}); err != nil {
		t.Fatal(err)
	}
	s := newReviewServer(t, root)
	_, err := s.runPlugins(t, false, "", "connectors", "plugin", "managed", "review", "echo", "--approval", "apr_nope")
	if err == nil || !strings.Contains(err.Error(), "428 approval_required") || !strings.Contains(err.Error(), "on the console, signed in") || !strings.Contains(err.Error(), "/keys/cli.key") {
		t.Fatalf("a review under an approval that does not exist: %v", err)
	}
	out, err := s.runPlugins(t, true, "echo\n", "connectors", "plugin", "managed", "review", "echo")
	if err != nil || !strings.Contains(out, "Approve ") {
		t.Fatalf("asking for the review: %v\n%s", err, out)
	}
}
