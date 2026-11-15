package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/standby"
)

const childStandbyCLIEnv = "CERBERUS_TEST_STANDBY_CLI"

// runChildStandbyCLI is this test binary acting as one `cerberus` CLI command
// in its own process (no daemon), under the scratch HOME the parent set, with
// an in-memory credential store and a stand-in for the terminal.
func runChildStandbyCLI(argsJSON string) {
	var args []string
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		fmt.Fprintln(os.Stderr, "child cli:", err)
		os.Exit(2)
	}
	secrets.MockStoreForTests()
	inProcessStateCheck = func() error { return nil }
	policyIsTerminal = func() bool { return true }
	secretsReadValue = func(*os.File, io.Writer) (string, error) { return "not-a-real-value", nil }
	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "child cli:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestStandbyCLIInProcessLeavesTheMainChainAlone: a CLI command that builds
// its own audit sink (no daemon, so no fence to divert it) writes nothing to
// the main chain while the standby flag is present, and a gated one is
// refused. Without the flag the same commands do write, so the check can see
// a write.
func TestStandbyCLIInProcessLeavesTheMainChainAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the CLI as a child process")
	}
	home := t.TempDir()
	state := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(state, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("version: 2\nresources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auditDir := filepath.Join(state, "audit")
	if err := os.MkdirAll(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, error) {
		t.Helper()
		b, _ := json.Marshal(args)
		cmd := exec.Command(os.Args[0], "-test.run=^$") //nolint:gosec // re-executes this test binary as the CLI
		cmd.Env = append(os.Environ(), childStandbyCLIEnv+"="+string(b), "HOME="+home, "TMPDIR="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, "xdg-config"), "XDG_DATA_HOME="+filepath.Join(home, "xdg-data"),
			"XDG_CACHE_HOME="+filepath.Join(home, "xdg-cache"), "XDG_STATE_HOME="+filepath.Join(home, "xdg-state"),
			"DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(home, "no-session-bus"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	commands := [][]string{
		{"portfolio", "health", "--config", cfgFile},
		{"secrets", "set", "keeper/ksm_config"},
	}
	chain := func() map[string]string {
		return fingerprint(t, auditDir, func(string) bool { return false })
	}

	// Without the flag both write the main chain.
	for _, args := range commands {
		before := chain()
		if out, err := run(args...); err != nil {
			t.Fatalf("%v without the flag: %v\n%s", args, err, out)
		}
		if d := diffFingerprints(before, chain()); len(d) == 0 {
			t.Fatalf("%v without the flag wrote nothing to the main chain; the check below could not see a write", args)
		}
	}

	if err := os.WriteFile(standby.PathsIn(state).Flag, []byte("flagged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := chain()
	out, err := run(commands[0]...)
	if err != nil {
		t.Fatalf("portfolio health is a read and still answers in standby: %v\n%s", err, out)
	}
	out, err = run(commands[1]...)
	if err == nil {
		t.Fatalf("secrets set is gated and is refused in standby:\n%s", out)
	}
	if !strings.Contains(out, "in standby") || !strings.Contains(out, "new `cerberus` CLI") {
		t.Fatalf("the refusal names standby and the new CLI:\n%s", out)
	}
	if !strings.Contains(out, "nothing was stored") {
		t.Fatalf("the refusal says nothing was stored:\n%s", out)
	}
	if d := diffFingerprints(before, chain()); len(d) != 0 {
		t.Fatalf("the main chain changed in standby: %v", d)
	}
}
