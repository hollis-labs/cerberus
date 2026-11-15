package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/api"
)

// `cerberus data query` as main's: the token only from a private regular file, a strict
// request on stdin, the token in its own header beside the CLI's key, an answer only with the
// version mark, printed as JSON; a refusal is one error, with no fallback.
func TestDataQueryAtTheCLI(t *testing.T) {
	var gotToken, gotAuth, gotBody string
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken, gotAuth = r.Header.Get(api.DataTokenHeader), r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set(api.DataVersionHeader, "1")
		if refuse {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"columns":["id"],"rows":[[1]]}`))
	}))
	t.Cleanup(srv.Close)
	cl := &apiClient{base: srv.URL + "/api", key: "cli-key", keyFile: "/keys/cli.key", http: srv.Client()}
	cmds := func() []*cobra.Command { return []*cobra.Command{dataCommand(func() (*apiClient, error) { return cl, nil })} }
	dir := t.TempDir()
	tok := filepath.Join(dir, "session.jwt")
	if err := os.WriteFile(tok, []byte("a.b.c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const req = `{"grant_id":"g","target":"fixture","projection":"documents","version":"v1","parameters":{}}`
	out, _, err := runCLI(t, cmds(), req, "data", "query", "--token-file", tok)
	if err != nil || out != "{\"columns\":[\"id\"],\"rows\":[[1]]}\n" || gotToken != "a.b.c" || gotAuth != "Bearer cli-key" || !strings.Contains(gotBody, `"grant_id":"g"`) {
		t.Fatalf("query: %q %v token %q auth %q", out, err, gotToken, gotAuth)
	}
	if err = os.Chmod(tok, 0o644); err != nil { //nolint:gosec // the test's own file, made readable to be refused
		t.Fatal(err)
	}
	if _, _, err = runCLI(t, cmds(), req, "data", "query", "--token-file", tok); err == nil || err.Error() != "data token requires a private regular file of at most 4096 bytes" {
		t.Fatalf("a readable token file: %v", err)
	}
	_ = os.Chmod(tok, 0o600)
	if _, _, err = runCLI(t, cmds(), `{"sql":"select 1"}`, "data", "query", "--token-file", tok); err == nil || err.Error() != "invalid data request JSON" {
		t.Fatalf("a loose request: %v", err)
	}
	refuse = true
	if _, _, err = runCLI(t, cmds(), req, "data", "query", "--token-file", tok); err == nil || err.Error() != "data request refused or versioned data service unavailable" {
		t.Fatalf("a refusal: %v", err)
	}
	if _, _, err = runCLI(t, cmds(), req, "data", "query"); err == nil || !strings.Contains(err.Error(), `"token-file" not set`) {
		t.Fatalf("no token file: %v", err)
	}
}
