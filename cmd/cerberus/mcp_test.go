package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// `cerberus mcp` under a read-only, empty HOME: it answers initialize and lists main's 71 tools
// without creating anything (no data directory, database, signing key or loop); with no serving
// Cerberus a tool call fails naming the address it tried, and nothing runs in-process.
func TestMCPStdioCreatesNothingAndForwardsToServe(t *testing.T) {
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil { //nolint:gosec // a read-only directory is the point
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) }) //nolint:gosec // a directory needs x to be removed
	side := t.TempDir()
	keyFile, override := filepath.Join(side, "mcp.key"), filepath.Join(side, "override.yaml")
	addr := "127.0.0.1:" + freePort(t)
	if err := os.WriteFile(keyFile, []byte("key_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("httphost: { addr: \""+addr+"\" }\ncerberus: { mcp_key_file: \""+keyFile+"\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp") //nolint:gosec // the test binary
	cmd.Env = []string{"HOME=" + ro, "PATH=" + os.Getenv("PATH"), "SUBSTRATE_CONFIG=" + override}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 1<<20), 8<<20)
	send := func(msg string) { _, _ = stdin.Write([]byte(msg + "\n")) }
	answer := func(id int) map[string]any {
		t.Helper()
		for lines.Scan() {
			var m map[string]any
			if json.Unmarshal(lines.Bytes(), &m) == nil && m["id"] == float64(id) {
				return m
			}
		}
		t.Fatalf("no answer to %d; stderr:\n%s", id, stderr.String())
		return nil
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	if info, _ := answer(1)["result"].(map[string]any)["serverInfo"].(map[string]any); info["name"] != "cerberus" || info["version"] != "0.1.0" {
		t.Fatalf("serverInfo %v", info)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools, _ := answer(2)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 71 {
		t.Fatalf("tools/list: %d tools, want 71", len(tools))
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"cerberus_resource_list","arguments":{}}}`)
	res, _ := answer(3)["result"].(map[string]any)
	text, _ := json.Marshal(res["content"])
	if res["isError"] != true || !strings.Contains(string(text), "not answering") || !strings.Contains(string(text), addr) {
		t.Fatalf("a call with no serving Cerberus: %v", res)
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	if !strings.Contains(stderr.String(), "local filesystem trust") {
		t.Fatalf("no local-trust notice on stderr:\n%s", stderr.String())
	}
	if entries, _ := os.ReadDir(ro); len(entries) != 0 {
		t.Fatalf("cerberus mcp wrote under HOME: %v", entries)
	}
}

// mcpSession runs `cerberus mcp` in s and returns a function that sends one JSON-RPC request
// and returns its answer, after initialize.
func mcpSession(t *testing.T, s sandbox) func(method, params string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	cmd := s.command(ctx, "mcp")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 1<<20), 8<<20)
	id := 0
	req := func(method, params string) map[string]any {
		t.Helper()
		id++
		_, _ = stdin.Write([]byte(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":` + params + "}\n"))
		for lines.Scan() {
			var m map[string]any
			if json.Unmarshal(lines.Bytes(), &m) == nil && m["id"] == float64(id) {
				return m
			}
		}
		t.Fatalf("no answer to %s; stderr:\n%s", method, stderr.String())
		return nil
	}
	req("initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e-client","version":"0"}}`)
	_, _ = stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	return req
}

// Against a serving Cerberus in supervise mode (the fake user manager, never the host's):
// `cerberus mcp` forwards with its own key and gets the API's answers in main's shapes; HTTP /mcp
// needs a key holding cerberus.read. The audit chain records the stdio calls as an agent via
// mcp_stdio, labeled with the MCP client's name, and the /mcp calls via mcp_http.
func TestMCPForwardsToServeAndTheAuditNamesTheChannel(t *testing.T) {
	side := t.TempDir()
	cliKeyFile, mcpKeyFile := filepath.Join(side, "cli.key"), filepath.Join(side, "mcp.key")
	port := freePort(t)
	s := newSandbox(t, "httphost: { addr: \"127.0.0.1:"+port+"\" }\ncerberus: { mode: supervise, cli_key_file: \""+cliKeyFile+
		"\", mcp_key_file: \""+mcpKeyFile+"\" }\n")
	s.bin = filepath.Join(filepath.Dir(s.home), "bin")
	if err := os.MkdirAll(s.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.bin, "systemctl"), []byte(fakeSystemctl), 0o700); err != nil { //nolint:gosec // the fake must run
		t.Fatal(err)
	}
	writeRegistry(t, s)
	if code, out := runWithStdin(t, s, "correct horse battery staple\n", "identity", "setup", "--username", "owner", "--display-name", "Owner"); code != 0 {
		t.Fatalf("identity setup: %d %s", code, out)
	}
	for name, file := range map[string]string{"cerberus-cli": cliKeyFile, "cerberus-mcp": mcpKeyFile} {
		if code, out := runWithStdin(t, s, "", "identity", "key", "create", "--name", name, "--scope", "cerberus.read",
			"--scope", "cerberus.operate", "--scope", "cerberus.admin", "--out", file); code != 0 {
			t.Fatalf("key create %s: %d %s", name, code, out)
		}
	}
	read := mintKey(t, s, "reader", "cerberus.read")
	cmd, base, log := start(t, s)
	defer stopServe(t, cmd, log)

	req := mcpSession(t, s)
	tool := func(name, args string) (string, bool) {
		res, _ := req("tools/call", `{"name":"`+name+`","arguments":`+args+`}`)["result"].(map[string]any)
		text, _ := json.Marshal(res["content"])
		return string(text), res["isError"] == true
	}
	if out, isErr := tool("cerberus_resource_list", `{}`); isErr || !strings.Contains(out, `\"id\":\"web\"`) || !strings.Contains(out, `\"total\":1`) {
		t.Fatalf("resource_list: %v %s", isErr, out)
	}
	if out, isErr := tool("cerberus_resource_status", `{"resource_id":"web"}`); isErr || !strings.Contains(out, `\"id\": \"web\"`) {
		t.Fatalf("resource_status: %v %s", isErr, out)
	}
	tool("cerberus_resource_logs", `{"resource_id":"web","lines":5}`)
	// The host reads and the project list (slice 9 part 2) answer through /api.
	if out, isErr := tool("cerberus_project_list", `{}`); isErr || !strings.Contains(out, `\"id\":\"shop\"`) || !strings.Contains(out, `\"resource_count\":1`) {
		t.Fatalf("project_list: %v %s", isErr, out)
	}
	if out, isErr := tool("cerberus_host_monitor", `{}`); isErr || !strings.Contains(out, `\"enabled\"`) {
		t.Fatalf("host_monitor: %v %s", isErr, out)
	}
	if out, isErr := tool("cerberus_host_get", `{"name":"nope"}`); !isErr || !strings.Contains(out, "not_found") {
		t.Fatalf("host_get of an undeclared host: %v %s", isErr, out)
	}

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	post := func(key string) (int, string) {
		r, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(initialize)) //nolint:noctx // loopback child
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := post(""); code != http.StatusUnauthorized {
		t.Fatalf("/mcp without a key: %d %s", code, body)
	}
	if code, body := post(read); code != http.StatusOK || !strings.Contains(body, `"name":"cerberus"`) {
		t.Fatalf("/mcp with a read key: %d %s", code, body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hc := &http.Client{Transport: bearer{key: read}}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-client", Version: "1"}, nil).
		Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = cs.Close() }()
	if _, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "cerberus_resource_logs", Arguments: map[string]any{"resource_id": "web", "lines": 5}}); err != nil {
		t.Fatalf("logs over /mcp: %v", err)
	}

	chain := auditText(t, s.data)
	for _, want := range []string{`"via":"mcp_stdio"`, `"client":"e2e-client"`, `"via":"mcp_http"`, `"client":"http-client"`} {
		if !strings.Contains(chain, want) {
			t.Errorf("the audit chain has no %s", want)
		}
	}
	if strings.Contains(chain, `"via":"api"`) {
		t.Errorf("an MCP call was recorded via api")
	}
}

// bearer adds an API key to every request.
type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// auditText is every audit JSONL file under data, joined.
func auditText(t *testing.T, data string) string {
	t.Helper()
	var sb strings.Builder
	_ = filepath.WalkDir(data, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			b, _ := os.ReadFile(p) //nolint:gosec // the sandbox's own tree
			sb.Write(b)
		}
		return nil
	})
	return sb.String()
}
