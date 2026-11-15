package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/boot"

	"github.com/hollis-labs/cerberus/internal/legacyimport"
	"github.com/hollis-labs/cerberus/internal/legacyimport/legacyimporttest"
)

// migrate-from is not a data command: run as a process, boot creates nothing, and the command
// needs no server. Here the child has no session bus, so it cannot read the old anchor and refuses
// before writing anything, with the report on stdout and a non-zero exit.
func TestMigrateFromNeedsNoServerAndRefusesWithoutTheAnchor(t *testing.T) {
	s := newSandbox(t, "")
	f := legacyimporttest.Materialize(t, filepath.Join(filepath.Dir(s.home), "fixture"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := s.command(ctx, "migrate-from", "--old-home", f.OldHome, "--old-db", f.OldDB, "--old-anchor-account", f.Account, "-o", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("migrate-from without the keychain succeeded:\n%s", stdout.String())
	}
	var rep legacyimport.Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not the JSON report: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	if rep.Result != legacyimport.ResultRefused || !strings.Contains(strings.Join(rep.Refusals, "\n"), "cerberus-audit-anchor") {
		t.Fatalf("report: %s %v", rep.Result, rep.Refusals)
	}
	for _, p := range []string{s.data, s.data + ".partial", filepath.Join(s.home, ".local", "share", "cerberus-substrate")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after a refused migrate-from (%v)", p, err)
		}
	}
}

// migrate-from through the CLI, in process (the keychain mocked), then `cerberus serve` on the
// result: the server's boot guard and database module accept the migrated data directory.
func TestMigrateFromThenServe(t *testing.T) {
	f := legacyimporttest.Materialize(t, t.TempDir())
	t.Setenv("CERBERUS_DATA_DIR", f.DataDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.Home, ".config"))
	ctx := context.Background()
	started, err := boot.StartWith(ctx, substrateApp, pickModules(readOnlyModules), substrateConfigJSON, boot.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = started.Stop(ctx) }()
	root := rootCommand(ctx, &cerberus{app: started.App, home: f.Home})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"migrate-from", "--old-home", f.OldHome, "--old-db", f.OldDB, "--old-anchor-account", f.Account})
	if err = root.ExecuteContext(ctx); err != nil {
		t.Fatalf("migrate-from: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "migrate-from: migrated") || !strings.Contains(out.String(), "source_unchanged: true") {
		t.Fatalf("report:\n%s", out.String())
	}
	if _, err = os.Stat(f.DBPath); err != nil {
		t.Fatal(err)
	}

	s := sandbox{home: f.Home, data: f.DataDir, override: filepath.Join(f.Root, "override.yaml")}
	if err = os.WriteFile(s.override, []byte("httphost: { addr: \"127.0.0.1:0\" }\n"), 0o600); err != nil { //nolint:gosec // a path in the test's own dir
		t.Fatal(err)
	}
	_, base, log := start(t, s)
	resp, err := http.Get(base + "/healthz") //nolint:noctx // test against the loopback child
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("GET /healthz on the migrated data dir = %d %s\n%s", resp.StatusCode, body, log)
	}
}
