package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/registry"
)

// Slice 7c's verbs against main's output. The goldens in testdata/golden-cli were printed by
// main's binary (built from `git archive origin/main`) under a scratch HOME, stdout only, for the
// same fixtures: shop.cerberus.yaml registered, approvals/events.jsonl as ~/.cerberus/approvals,
// audit/2026-11.jsonl as ~/.cerberus/audit, and internal/api/testdata/golden-l9 as
// ~/.cerberus/hosts.yaml and state/hosts.json. Where this build says something else on purpose,
// the test names the difference.

// serverFor serves a over httptest and answers a client of it with no guard (output tests).
func serverFor(t *testing.T, a *api.API) func() (*apiClient, error) {
	t.Helper()
	srv := httptest.NewServer(a.Mux())
	t.Cleanup(srv.Close)
	c := &apiClient{base: srv.URL + "/api", key: "test", keyFile: "test.key", http: srv.Client()}
	return func() (*apiClient, error) { return c, nil }
}

// shopRegistry is the golden fixture registered in a temp adopted root.
func shopRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	desc, err := filepath.Abs(filepath.Join("testdata", "golden-cli", "shop.cerberus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.ForRoot(t.TempDir())
	if _, err = reg.Register(desc); err != nil {
		t.Fatal(err)
	}
	return reg
}

func wantGolden(t *testing.T, what, got, name string) {
	t.Helper()
	if want := golden(t, name); got != want {
		t.Errorf("%s:\n got:\n%s\nwant (%s):\n%s", what, got, name, want)
	}
}

func TestProjectVerbsMatchTheOldCLI(t *testing.T) {
	reg := shopRegistry(t)
	a, err := api.New(api.Options{
		Resources: func() ([]config.ResourceDef, error) {
			r, rerr := reg.Resolve()
			if rerr != nil {
				return nil, rerr
			}
			return r.Config.Resources, nil
		},
		Projects: func() ([]config.ProjectDef, error) {
			r, rerr := reg.Resolve()
			if rerr != nil {
				return nil, rerr
			}
			return r.Config.Projects, nil
		},
		Observer: stoppedObserver{}, Home: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := serverFor(t, a)
	cmds := func() []*cobra.Command { return []*cobra.Command{projectCommand(client)} }
	for args, name := range map[string]string{"project list": "project-list.txt", "project list -o json": "project-list.json", "project show shop": "project-show.txt"} {
		out, _, rerr := runCLI(t, cmds(), "", strings.Fields(args)...)
		if rerr != nil {
			t.Fatalf("%s: %v", args, rerr)
		}
		wantGolden(t, args, out, name)
	}
	_, errOut, err := runCLI(t, cmds(), "", "project", "show", "nope")
	if err == nil || err.Error() != `project "nope" not found` || errOut != "project \"nope\" not found\n" {
		t.Fatalf("an unknown project: %v %q", err, errOut)
	}
}

// validate and config validate read the registry in the adopted root, as `registry health`
// does; the file form prints the path as given.
func TestValidateMatchesTheOldCLI(t *testing.T) {
	reg := shopRegistry(t)
	cmds := func() []*cobra.Command {
		top, under := validateCommands(func() (*registry.Registry, error) { return reg, nil })
		config := &cobra.Command{Use: "config"}
		config.AddCommand(under)
		return []*cobra.Command{top, config}
	}
	for _, args := range [][]string{{"validate"}, {"config", "validate"}} {
		out, _, err := runCLI(t, cmds(), "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		wantGolden(t, strings.Join(args, " "), out, "validate.txt")
	}
	path := filepath.Join("testdata", "golden-cli", "shop.cerberus.yaml")
	out, _, err := runCLI(t, cmds(), "", "validate", path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(golden(t, "validate-file.txt"), "shop.cerberus.yaml", path, 1); out != want {
		t.Errorf("validate <path>: got %q want %q", out, want)
	}
	bad := filepath.Join(t.TempDir(), "bad.cerberus.yaml")
	if err = os.WriteFile(bad, []byte("kind: cerberus-project/v1\nowner: x\nproject: {id: x}\nresources:\n  - {id: a, typo_field: 1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runCLI(t, cmds(), "", "validate", bad); err == nil {
		t.Fatal("an invalid descriptor validated")
	}
}

// approvalsServer serves the fixture approvals store through a gate's broker, as the serving
// Cerberus does in supervise mode.
func approvalsServer(t *testing.T) func() (*apiClient, error) {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("testdata", "golden-cli", "approvals", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "events.jsonl"), data, 0o600); err != nil { //nolint:gosec // a temp dir and a fixed name
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	broker, err := gate.NewBroker(sink, dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir(),
		Gate: gate.New(gate.Options{Sink: sink, Broker: broker})})
	if err != nil {
		t.Fatal(err)
	}
	return serverFor(t, a)
}

// The approvals reads print what main printed. Two lines differ on purpose: the source (main read
// its store with the daemon down; here the serving Cerberus answers) and "Decide with" (the
// owner decides on the console, decision 0.1: the server's approve_with).
func TestApprovalsMatchTheOldCLI(t *testing.T) {
	client := approvalsServer(t)
	cmds := func() []*cobra.Command { return []*cobra.Command{approvalsCommand(client)} }
	const pending, denied = "apr_69b3b42eda6c", "apr_fa84bbf553ad"
	oldSource := "(from the store, read directly: the daemon is not running)"
	newSource := "(from " + approvalsSource + ")"
	for args, name := range map[string]string{
		"approvals list": "approvals-list.txt", "approvals list -o json": "approvals-list.json",
		"approvals list --status denied": "approvals-list-denied.txt",
		"approvals show " + pending:      "approvals-show-pending.txt", "approvals show " + pending + " -o json": "approvals-show-pending.json",
		"approvals show " + denied: "approvals-show-denied.txt",
	} {
		out, _, err := runCLI(t, cmds(), "", strings.Fields(args)...)
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		want := strings.ReplaceAll(golden(t, name), oldSource, newSource)
		want = strings.ReplaceAll(want, "cerberus approvals approve "+pending, "approve it on the console: /approvals?id="+pending)
		if out != want {
			t.Errorf("%s:\n got:\n%s\nwant (%s, source and decide line as this build says them):\n%s", args, out, name, want)
		}
	}
	if _, _, err := runCLI(t, cmds(), "", "approvals", "show", "apr_nope"); err == nil || err.Error() != `no approval "apr_nope"; `+"`cerberus approvals list` shows them" {
		t.Fatalf("an unknown approval: %v", err)
	}

	// No CLI decides one (decision 0.1): approve and deny name the console page and fail.
	for _, verb := range []string{"approve", "deny"} {
		out, _, err := runCLI(t, cmds(), "", "approvals", verb, pending, "--reason", "r")
		if err == nil || err.Error() != verb+" it on the console: /approvals?id="+pending || out != "" {
			t.Fatalf("approvals %s: %q %v", verb, out, err)
		}
		if _, _, err = runCLI(t, cmds(), "", "approvals", verb, denied); err == nil || !strings.Contains(err.Error(), "is denied, not pending") {
			t.Fatalf("approvals %s on a decided approval: %v", verb, err)
		}
	}
	out, _, _ := runCLI(t, cmds(), "", "approvals", "show", pending)
	if !strings.Contains(out, "pending") {
		t.Fatalf("approve or deny changed the approval: %s", out)
	}
}

// audit tail and query read <data>/audit as main read ~/.cerberus/audit: the same lines, and
// the records as written for -o json.
func TestAuditReadsMatchTheOldCLI(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("testdata", "golden-cli", "audit", "2026-11.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "2026-11.jsonl"), data, 0o600); err != nil { //nolint:gosec // a temp dir and a fixed name
		t.Fatal(err)
	}
	cmds := func() []*cobra.Command {
		var output string
		group := &cobra.Command{Use: "audit"}
		group.PersistentFlags().StringVarP(&output, "output", "o", outputText, "")
		group.AddCommand(auditReadCommands(func() (string, error) { return dir, nil }, &output)...)
		return []*cobra.Command{group}
	}
	for args, name := range map[string]string{
		"audit tail": "audit-tail.txt", "audit tail -n 3": "audit-tail-3.txt", "audit tail -o json": "audit-tail.json",
		"audit query --connector ssh --outcome refused": "audit-query-ssh-refused.txt",
		"audit query --target dev --limit 2 -o json":    "audit-query-dev.json",
	} {
		out, _, rerr := runCLI(t, cmds(), "", strings.Fields(args)...)
		if rerr != nil {
			t.Fatalf("%s: %v", args, rerr)
		}
		wantGolden(t, args, out, name)
	}
	if _, _, err = runCLI(t, cmds(), "", "audit", "query", "--since", "yesterday"); err == nil || !strings.HasPrefix(err.Error(), "--since: ") {
		t.Fatalf("a bad --since: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "2026-11.jsonl")) //nolint:gosec // a temp dir and a fixed name
	if string(after) != string(data) {
		t.Fatal("a read changed the chain")
	}
}

// hostServer serves the golden-l9 hosts fixture as the hosts lane does.
func hostServer(t *testing.T) (func() (*apiClient, error), string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	for dst, src := range map[string]string{"hosts.yaml": "hosts.yaml", filepath.Join("state", "hosts.json"): "state-hosts.json"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "testdata", "golden-l9", src)) //nolint:gosec // fixed test data
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, dst), []byte(strings.ReplaceAll(string(b), "@HOME@", home)), 0o600); err != nil { //nolint:gosec // a temp dir and fixed names
			t.Fatal(err)
		}
	}
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: home,
		Hosts: api.FileHosts{HostsPath: filepath.Join(root, "hosts.yaml"), StatePath: filepath.Join(root, "state", "hosts.json")}})
	if err != nil {
		t.Fatal(err)
	}
	// GET /api/hosts/{name} needs the host monitor (slice 8's reads, a database); its view is
	// the list's element (the api package checks that), served here from the list.
	mux := a.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, one := strings.CutPrefix(r.URL.Path, "/api/hosts/")
		if !one || r.Method != http.MethodGet || strings.Contains(name, "/") || strings.HasPrefix(name, "_") {
			mux.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hosts", nil))
		var views []api.HostView
		_ = json.Unmarshal(rec.Body.Bytes(), &views)
		for _, v := range views {
			if v.Host.Name == name {
				_ = json.NewEncoder(w).Encode(v)
				return
			}
		}
		http.Error(w, `{"code":"not_found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := &apiClient{base: srv.URL + "/api", key: "test", keyFile: "test.key", http: srv.Client()}
	return func() (*apiClient, error) { return c, nil }, home
}

// capturedAt is when main printed a golden: zeta's probe time plus the age main printed.
func capturedAt(t *testing.T, name string) time.Time {
	t.Helper()
	m := regexp.MustCompile(`(\d+h\d+m\d+s) ago`).FindStringSubmatch(golden(t, name))
	if m == nil {
		t.Fatalf("%s holds no age", name)
	}
	age, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(age)
}

// host list and get print main's table and record for the same hosts.yaml and state. The age
// column is relative to now, so the printers run at the moment main printed the golden.
func TestHostReadsMatchTheOldCLI(t *testing.T) {
	client, home := hostServer(t)
	cmds := func() []*cobra.Command { return []*cobra.Command{hostCommand(client)} }
	views := func(path string) []api.HostView {
		cl, _ := client()
		var v []api.HostView
		if err := cl.get(t.Context(), path, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for path, name := range map[string]string{"/hosts": "host-list.txt", "/hosts?role=builder": "host-list-builder.txt"} {
		var out strings.Builder
		if err := printHostTable(&out, views(path), capturedAt(t, name)); err != nil {
			t.Fatal(err)
		}
		wantGolden(t, "host list "+path, out.String(), name)
	}
	for _, name := range []string{"zeta", "alpha"} {
		cl, _ := client()
		var v api.HostView
		if err := cl.get(t.Context(), "/hosts/"+name, &v); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		now := time.Now()
		if name == "zeta" {
			now = capturedAt(t, "host-get-zeta.txt")
		}
		if err := printHost(&out, v, false, now); err != nil {
			t.Fatal(err)
		}
		wantGolden(t, "host get "+name, out.String(), "host-get-"+name+".txt")
	}
	out, _, err := runCLI(t, cmds(), "", "host", "get", "zeta", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal([]byte(out), &got)
	_ = json.Unmarshal([]byte(strings.ReplaceAll(golden(t, "host-get-zeta.json"), "@HOME@", home)), &want)
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("host get zeta --json:\n got %s\nwant %s", g, w)
	}
	if out, _, err = runCLI(t, cmds(), "", "host", "list", "--label", "tier=gold"); err != nil || !strings.Contains(out, "zeta") || strings.Contains(out, "alpha") {
		t.Fatalf("host list --label: %v\n%s", err, out)
	}
	if _, _, err = runCLI(t, cmds(), "", "host", "list", "--label", "nokey"); err == nil {
		t.Fatal("a malformed label was sent")
	}
	// Observe mode: the server measures nothing and says so.
	if _, _, err = runCLI(t, cmds(), "", "host", "probe", "zeta"); err == nil || !strings.Contains(err.Error(), "observe_mode") {
		t.Fatalf("host probe in observe mode: %v", err)
	}
}

// opServer is a fake admin lane: it records each operation call and answers with reply.
type opServer struct {
	calls []admin.OperationArgs
	paths []string
	reply func(op string) (int, string)
}

func newOpServer(t *testing.T, reply func(op string) (int, string)) (*opServer, func() (*apiClient, error)) {
	t.Helper()
	s := &opServer{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var args admin.OperationArgs
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &args)
		s.calls, s.paths = append(s.calls, args), append(s.paths, r.Method+" "+r.URL.Path)
		code, out := s.reply(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	c := &apiClient{base: srv.URL + "/api", key: "test", keyFile: "test.key", http: srv.Client()}
	return s, func() (*apiClient, error) { return c, nil }
}

// docker and ssh run over the admin lane's operation route with old's configs and print old's
// output; a refusal is the server's, and an approval to ask for is named with the --approval
// command. (Goldens from main would need a docker daemon and an ssh host; the printers are
// main's, line for line.)
func TestDockerAndSSHOverTheAdminLane(t *testing.T) {
	data := map[string]string{
		"list_containers": `[{"id":"0123456789abcdef","name":"web","image":"nginx:1","status":"Up 2 hours","ports":["80/tcp","443/tcp"]}]`,
		"logs":            `"line one\nline two\n"`,
		"start":           `null`,
		"exec":            `{"stdout":"hello","stderr":"warn","exit_code":3}`,
		"status":          `"{\"reachable\":true}"`,
		"put":             `{"local_path":"/l/a.env","remote_path":"/r/a.env","bytes":42}`,
		"get_dir":         `{"direction":"download","local_path":"/l/d","remote_path":"/r/d","files":2,"dirs":1,"skipped":1,"bytes":9,"entries":[{"path":"x","action":"skip","reason":"symlink out of the tree"}]}`,
	}
	s, client := newOpServer(t, func(op string) (int, string) {
		if op == "stop" {
			return http.StatusConflict, `{"code":"approval_pending","message":"ssh stop needs approval","details":{"approval":{"id":"apr_1"}}}`
		}
		return http.StatusOK, `{"connector":"x","operation":"` + op + `","data":` + data[op] + `}`
	})
	reg := shopRegistry(t)
	cmds := func() []*cobra.Command {
		return []*cobra.Command{dockerCommand(client, func() (*registry.Registry, error) { return reg, nil }), sshCommand(client)}
	}

	out, _, err := runCLI(t, cmds(), "", "docker", "ps")
	if err != nil || out != "ID            NAME  IMAGE    STATUS      PORTS\n--            ----  -----    ------      -----\n0123456789ab  web   nginx:1  Up 2 hours  80/tcp (+1)\n" {
		t.Fatalf("docker ps: %v\n%q", err, out)
	}
	if out, _, err = runCLI(t, cmds(), "", "docker", "logs", "web", "--lines", "5"); err != nil || out != "line one\nline two\n" {
		t.Fatalf("docker logs: %v %q", err, out)
	}
	last := s.calls[len(s.calls)-1]
	if s.paths[len(s.paths)-1] != "POST /api/connectors/docker/operations/logs" || last.Config["container"] != "web" || last.Config["lines"] != float64(5) {
		t.Fatalf("docker logs call: %s %+v", s.paths[len(s.paths)-1], last)
	}
	if out, _, err = runCLI(t, cmds(), "", "docker", "up", "web", "--ack"); err != nil || out != "Container started: web\n" || !s.calls[len(s.calls)-1].Acknowledged {
		t.Fatalf("docker up: %v %q", err, out)
	}
	// A declared resource of another connector is old's error, and nothing is sent.
	n := len(s.calls)
	if _, _, err = runCLI(t, cmds(), "", "docker", "up", "box", "--ack"); err == nil || !strings.Contains(err.Error(), `resource "box" is server/ssh, not a docker resource; try: cerberus ssh status box`) || len(s.calls) != n {
		t.Fatalf("docker up on an ssh resource: %v", err)
	}

	out, errOut, err := runCLI(t, cmds(), "", "ssh", "exec", "box", "--ack", "--", "uname", "-a")
	if err == nil || err.Error() != "remote command exited with code 3" || out != "hello\n" || errOut != "warn\n" {
		t.Fatalf("ssh exec: %v %q %q", err, out, errOut)
	}
	if c := s.calls[len(s.calls)-1]; c.Config["id"] != "box" || c.Config["command"] != "uname -a" || !c.Acknowledged {
		t.Fatalf("ssh exec call: %+v", c)
	}
	if out, _, err = runCLI(t, cmds(), "", "ssh", "status", "box"); err != nil || out != "{\"reachable\":true}\n" {
		t.Fatalf("ssh status: %v %q", err, out)
	}
	if out, _, err = runCLI(t, cmds(), "", "ssh", "put", "box", "a.env", "/r/a.env", "--ack"); err != nil || out != "/l/a.env → /r/a.env (42 bytes)\n" {
		t.Fatalf("ssh put: %v %q", err, out)
	}
	if lp, _ := s.calls[len(s.calls)-1].Config["local_path"].(string); !filepath.IsAbs(lp) {
		t.Fatalf("ssh put sent a relative local path: %q", lp)
	}
	if out, _, err = runCLI(t, cmds(), "", "ssh", "get-dir", "box", "/r/d", "d", "--ack"); err != nil ||
		out != "/r/d → /l/d: 2 files, 1 dir, 1 skipped, 9 bytes\n  skip x (symlink out of the tree)\n" {
		t.Fatalf("ssh get-dir: %v %q", err, out)
	}
	_, _, err = runCLI(t, cmds(), "", "ssh", "stop", "box", "--ack")
	var refused *apiError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "run this again with --approval apr_1") {
		t.Fatalf("ssh stop pending an approval: %v", err)
	}
	if _, _, err = runCLI(t, cmds(), "", "ssh", "exec", "box"); err == nil || !strings.Contains(err.Error(), "no command specified") {
		t.Fatalf("ssh exec with no command: %v", err)
	}
}

// Every pending verb exists with old's flags (the parity test), parses them, and answers
// pending with the slice that owns its backend (7d or 8b), exit 1, having called nothing.
func TestPendingVerbsAnswerPending(t *testing.T) {
	tree := newTree(t)
	for _, s := range pendingSpecs {
		if s.group {
			continue
		}
		c := tree["cerberus "+s.path]
		if c == nil {
			t.Fatalf("%s is not in the tree", s.path)
		}
		err := c.RunE(c, nil)
		want := "pending (slice 8b)"
		if top := strings.Fields(s.path)[0]; top == "data" {
			want = "pending (slice 7d)"
		}
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing was done") {
			t.Fatalf("%s: %v", s.path, err)
		}
	}
	root := tree["cerberus"]
	root.SilenceErrors, root.SilenceUsage = true, true
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	for _, c := range []struct {
		slice string
		args  []string
	}{
		{"slice 8b", []string{"host", "set", "zeta", "--ack", "--label", "a=b", "--revision", "r1", "-o", "json"}},
		{"slice 8b", []string{"schedule", "--json", "create", "--file", "-", "--ack"}},
		{"slice 8b", []string{"pipeline", "show", "release"}},
		{"slice 8b", []string{"host", "capacity", "--max-age", "5m", "--recent-max-load", "0.5", "--free-slot", "nvme_m2=1"}},
		{"slice 7d", []string{"data", "query", "--token-file", "x"}},
	} {
		root.SetArgs(c.args)
		if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "pending ("+c.slice+")") {
			t.Fatalf("%v: %v", c.args, err)
		}
	}
}
