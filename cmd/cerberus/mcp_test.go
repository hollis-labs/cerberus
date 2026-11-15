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
	keyFile, override := filepath.Join(side, "cli.key"), filepath.Join(side, "override.yaml")
	addr := "127.0.0.1:" + freePort(t)
	if err := os.WriteFile(keyFile, []byte("key_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("httphost: { addr: \""+addr+"\" }\ncerberus: { cli_key_file: \""+keyFile+"\" }\n"), 0o600); err != nil {
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
	req("initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`)
	_, _ = stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	return req
}

// Against a serving Cerberus (observe mode): `cerberus mcp` forwards reads and gets the API's
// answers in main's shapes, and a mutation gets the API's refusal as a structured tool error.
// HTTP /mcp is mounted beside /api and needs a key holding cerberus.read.
func TestMCPForwardsToServeAndHTTPNeedsAKey(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "cli.key")
	port := freePort(t)
	s := newSandbox(t, "httphost: { addr: \"127.0.0.1:"+port+"\" }\ncerberus: { cli_key_file: \""+keyFile+"\" }\n")
	writeRegistry(t, s)
	if code, out := runWithStdin(t, s, "correct horse battery staple\n", "identity", "setup", "--username", "owner", "--display-name", "Owner"); code != 0 {
		t.Fatalf("identity setup: %d %s", code, out)
	}
	if code, out := runWithStdin(t, s, "", "identity", "key", "create", "--name", "cli", "--scope", "cerberus.read",
		"--scope", "cerberus.operate", "--scope", "cerberus.admin", "--out", keyFile); code != 0 {
		t.Fatalf("key create: %d %s", code, out)
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
	if out, isErr := tool("cerberus_resource_reload", `{"resource_id":"web","acknowledged":true}`); !isErr || !strings.Contains(out, "observe_mode") {
		t.Fatalf("resource_reload in observe mode: %v %s", isErr, out)
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
}
