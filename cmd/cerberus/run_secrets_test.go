package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// fakeBackends is a vault router that records what it was asked and when it was closed.
type fakeBackends struct {
	events []string
	err    error
}

func (f *fakeBackends) Claims(string) bool { return true }
func (f *fakeBackends) ResolveSecret(_ context.Context, ref string) (string, error) {
	f.events = append(f.events, "resolve "+ref)
	if f.err != nil {
		return "", f.err
	}
	return "vault-value", nil
}
func (f *fakeBackends) Close(context.Context) { f.events = append(f.events, "close") }

// capture is an exec that records its arguments instead of replacing the test process.
type capture struct {
	argv0      string
	argv, envv []string
	calls      int
	backends   *fakeBackends
}

func (c *capture) exec(argv0 string, argv, envv []string) error {
	c.calls++
	c.argv0, c.argv, c.envv = argv0, argv, envv
	if c.backends != nil {
		c.backends.events = append(c.backends.events, "exec")
	}
	return nil
}

func envValue(envv []string, name string) (string, bool) {
	for _, e := range envv {
		if k, v, ok := strings.Cut(e, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// The in-process shim: a keychain:// reference resolves from the OS credential store (mocked),
// a vault reference through the backends, a literal passes through, the program is found on
// PATH, the backends are closed before exec, and the environment is handed over sorted.
func TestRunSecretsResolvesTheEnvironmentAndExecs(t *testing.T) {
	secrets.MockStoreForTests()
	if err := secrets.KeyringSet("cerberus", "cerberus-test/token", "keyring-value"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RS_REF", "keychain://cerberus-test/token")
	t.Setenv("RS_VAULT", "op://Deploy/Database/password")
	t.Setenv("RS_LITERAL", "a=b")
	backends := &fakeBackends{}
	c := &capture{backends: backends}
	if err := runSecrets(t.Context(), []string{"sh", "-c", "true"}, secrets.NewKeychainProvider(), backends, c.exec); err != nil {
		t.Fatal(err)
	}
	want, _ := exec.LookPath("sh")
	if c.calls != 1 || c.argv0 != want || !slices.Equal(c.argv, []string{"sh", "-c", "true"}) {
		t.Fatalf("exec(%q, %q), %d calls", c.argv0, c.argv, c.calls)
	}
	for name, value := range map[string]string{"RS_REF": "keyring-value", "RS_VAULT": "vault-value", "RS_LITERAL": "a=b"} {
		if got, ok := envValue(c.envv, name); !ok || got != value {
			t.Errorf("%s = %q (%v), want %q", name, got, ok, value)
		}
	}
	if !slices.IsSorted(c.envv) {
		t.Error("the environment is not sorted")
	}
	if i, j := slices.Index(backends.events, "close"), slices.Index(backends.events, "exec"); i < 0 || j < 0 || i > j {
		t.Fatalf("the backends were not closed before exec: %v", backends.events)
	}
}

// A reference that cannot be resolved is fatal: nothing is exec'd and no value is printed.
func TestRunSecretsRefusesAnUnresolvedReference(t *testing.T) {
	secrets.MockStoreForTests()
	t.Setenv("RS_REF", "keychain://cerberus-test/absent")
	c := &capture{}
	err := runSecrets(t.Context(), []string{"sh"}, secrets.NewKeychainProvider(), &fakeBackends{}, c.exec)
	if err == nil || !strings.Contains(err.Error(), "resolve service environment") || !strings.Contains(err.Error(), "RS_REF") {
		t.Fatalf("err = %v", err)
	}
	if c.calls != 0 {
		t.Fatal("the program was exec'd with an unresolved credential")
	}

	t.Setenv("RS_REF", "op://a/b/c")
	backends := &fakeBackends{err: errors.New("credential_missing: no")}
	if err = runSecrets(t.Context(), []string{"sh"}, secrets.NewKeychainProvider(), backends, c.exec); err == nil || c.calls != 0 {
		t.Fatalf("a failed vault reference: err %v, %d execs", err, c.calls)
	}
	if !slices.Contains(backends.events, "close") {
		t.Fatal("the backends were not closed on failure")
	}
}

// No program, or one that is not there, is an error before exec.
func TestRunSecretsNeedsAProgram(t *testing.T) {
	c := &capture{}
	if err := runSecrets(t.Context(), nil, nil, &fakeBackends{}, c.exec); err == nil {
		t.Fatal("no program accepted")
	}
	err := runSecrets(t.Context(), []string{filepath.Join(t.TempDir(), "absent")}, nil, &fakeBackends{}, c.exec)
	if err == nil || !strings.Contains(err.Error(), "locate ") || c.calls != 0 {
		t.Fatalf("err = %v, %d execs", err, c.calls)
	}
}

// Without the audit chain a vault reference is refused (fail closed) and nothing is created in
// the data directory.
func TestServiceSecretBackendsRefuseWithoutTheAuditChain(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	getenv := func(k string) string {
		if k == "CERBERUS_DATA_DIR" {
			return data
		}
		return ""
	}
	c := &cerberus{home: t.TempDir()}
	b := c.serviceSecretBackends(getenv, nil)
	_, err := b.ResolveSecret(t.Context(), "op://a/b/c")
	if err == nil || !strings.Contains(err.Error(), "credential_missing") || !strings.Contains(err.Error(), "does not exist yet") {
		t.Fatalf("err = %v", err)
	}
	if b.Claims("op") {
		t.Fatal("a backend claims op:// with no audit chain")
	}
	b.Close(t.Context())
	if _, statErr := os.Stat(data); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the data directory was created: %v", statErr)
	}
}

// runSecretsSandbox is a sandbox with a fake program that writes the value of $RS_NAME's
// variable and its pid where the test reads them.
type runSecretsSandbox struct {
	sandbox
	prog, out, pid string
}

func newRunSecretsSandbox(t *testing.T) runSecretsSandbox {
	t.Helper()
	s := runSecretsSandbox{sandbox: newSandbox(t, "")}
	dir := filepath.Join(filepath.Dir(s.home), "prog")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.prog, s.out, s.pid = filepath.Join(dir, "fake-service"), filepath.Join(dir, "value"), filepath.Join(dir, "pid")
	script := "#!/bin/sh\neval \"v=\\${$RS_NAME}\"\nprintf '%s' \"$v\" > " + s.out + "\necho $$ > " + s.pid + "\n"
	if err := os.WriteFile(s.prog, []byte(script), 0o700); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}
	return s
}

func (s runSecretsSandbox) ran() (string, bool) {
	b, err := os.ReadFile(s.out)
	return string(b), err == nil
}

// tree is every path under root with its size and mode, for a before-and-after comparison.
func tree(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			out[p] = fmt.Sprintf("%v %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return out
}

// The process: `cerberus run-secrets -- <prog>` resolves a keychain:// reference (here from its
// CERBERUS_<SERVICE>_<KEY> environment fallback, so no credential store is needed), execs the
// program as the same process, opens no database and writes nothing in the data directory.
func TestRunSecretsProcessExecsTheProgram(t *testing.T) {
	s := newRunSecretsSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	before := tree(s.data)
	// A shell that records its pid and then becomes cerberus: the program must have that pid.
	cmd := s.command(ctx, "-c", `echo $$ > "$RS_SHELL_PID"; exec "$RS_BIN" run-secrets -- "$RS_PROG" ignored-arg`)
	cmd.Path, cmd.Args[0] = "/bin/sh", "/bin/sh"
	shellPid := filepath.Join(filepath.Dir(s.pid), "shell-pid")
	cmd.Env = append(cmd.Env, "RS_SHELL_PID="+shellPid, "RS_BIN="+binary, "RS_PROG="+s.prog, "RS_NAME=RS_REF",
		"RS_REF=keychain://cerberus-test/token", "CERBERUS_CERBERUS_TEST_TOKEN=resolved-from-the-fallback")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-secrets: %v\n%s", err, out)
	}
	if got, ok := s.ran(); !ok || got != "resolved-from-the-fallback" {
		t.Fatalf("the program saw %q (ran %v)", got, ok)
	}
	if a, b := readPid(t, shellPid), readPid(t, s.pid); a != b {
		t.Fatalf("the program ran as pid %d, run-secrets was %d: a child, not exec", b, a)
	}
	if after := tree(s.data); !maps.Equal(before, after) {
		t.Fatalf("the data directory changed: %v -> %v", before, after)
	}
}

// An unresolvable reference exits 1 with the variable named, and the program never runs.
func TestRunSecretsProcessRefusesAnUnresolvedReference(t *testing.T) {
	s := newRunSecretsSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := s.command(ctx, "run-secrets", "--", s.prog)
	// The session bus is dead, so the credential store is unavailable: credential_missing.
	cmd.Env = append(cmd.Env, "RS_NAME=RS_REF", "RS_REF=keychain://cerberus-test/absent")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want 1\n%s", err, out)
	}
	if !strings.Contains(string(out), "resolve service environment") || !strings.Contains(string(out), "RS_REF") {
		t.Fatalf("stderr = %s", out)
	}
	if _, ran := s.ran(); ran {
		t.Fatal("the program ran without its credential")
	}
	if _, statErr := os.Stat(s.data); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the data directory was created: %v", statErr)
	}
}

// The golden secret-refs unit's ExecStart (the renderer's output, which the old build wrote too)
// parses with the new root: run-secrets and "--" are accepted and the program reaches the shim.
func TestRunSecretsAcceptsTheRenderedExecStart(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "..", "internal", "connector", "local", "testdata", "parity", "secret-refs", "unit.golden"))
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	sc := bufio.NewScanner(strings.NewReader(string(golden)))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "ExecStart="); ok {
			for _, f := range strings.Fields(rest) {
				unq, uerr := strconv.Unquote(f)
				if uerr != nil {
					t.Fatalf("ExecStart word %s: %v", f, uerr)
				}
				argv = append(argv, unq)
			}
		}
	}
	if len(argv) != 4 || argv[1] != "run-secrets" || argv[2] != "--" {
		t.Fatalf("the golden ExecStart is %q; this test expects <cerberus> run-secrets -- <program>", argv)
	}
	s := newSandbox(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The program is the fixture's path, absent here: the shim got as far as locating it.
	out, err := s.command(ctx, argv[1:]...).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "locate "+argv[3]) || strings.Contains(string(out), "unknown command") {
		t.Fatalf("run-secrets with the rendered arguments: %v\n%s", err, out)
	}
}

// The process with the real OS credential store: a throwaway gnome-keyring on a private session
// bus holds the secret under the keyring contract (service "cerberus", account
// "<service>/<key>"); a missing entry exits 1 without running the program.
func TestRunSecretsProcessReadsTheCredentialStore(t *testing.T) {
	for _, tool := range []string{"dbus-run-session", "gnome-keyring-daemon", "secret-tool", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s for a throwaway keyring on a private session bus (as scripts/verify-continued-chain.sh); the in-process variant covers resolution", tool)
		}
	}
	s := newRunSecretsSandbox(t)
	run := filepath.Join(filepath.Dir(s.home), "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	before := tree(s.data)
	script := `set -euo pipefail
eval "$(printf scratch-keyring | gnome-keyring-daemon --unlock --components=secrets)" >/dev/null
printf %s "$RS_VALUE" | secret-tool store --label="cerberus test" service cerberus username cerberus-test/token
if "$RS_BIN" run-secrets -- "$RS_PROG" 2>"$RS_ERR_MISSING"; then exit 3; fi
export RS_REF=keychain://cerberus-test/token
echo $$ > "$RS_SHELL_PID"
exec "$RS_BIN" run-secrets -- "$RS_PROG"`
	shellPid := filepath.Join(filepath.Dir(s.pid), "shell-pid")
	missingErr := filepath.Join(filepath.Dir(s.pid), "missing.err")
	cmd := exec.CommandContext(ctx, "dbus-run-session", "--", "bash", "-c", script)
	for _, e := range s.command(ctx).Env {
		if !strings.HasPrefix(e, "DBUS_SESSION_BUS_ADDRESS=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "XDG_RUNTIME_DIR="+run, "RS_BIN="+binary, "RS_PROG="+s.prog, "RS_NAME=RS_REF",
		"RS_VALUE=from-the-credential-store", "RS_SHELL_PID="+shellPid, "RS_ERR_MISSING="+missingErr,
		"RS_REF=keychain://cerberus-test/absent")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dbus-run-session: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(missingErr); !strings.Contains(string(b), "RS_REF") || !strings.Contains(string(b), "empty") { //nolint:gosec // the sandbox's own file
		t.Fatalf("the missing entry's error: %s", b)
	}
	if got, ok := s.ran(); !ok || got != "from-the-credential-store" {
		t.Fatalf("the program saw %q (ran %v)", got, ok)
	}
	if a, b := readPid(t, shellPid), readPid(t, s.pid); a != b {
		t.Fatalf("the program ran as pid %d, run-secrets was %d: a child, not exec", b, a)
	}
	if after := tree(s.data); !maps.Equal(before, after) {
		t.Fatalf("the data directory changed: %v -> %v", before, after)
	}
}

// installBackend puts the synthetic secret backend in the sandbox's adopted root and, unless
// noChain, the audit chain's digest key in its data directory, as a migrated install has.
func installBackend(t *testing.T, s runSecretsSandbox, p admintest.Plugin, noChain bool) string {
	t.Helper()
	root := filepath.Join(s.home, ".cerberus")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := admintest.Install(root, echoPlugin, p); err != nil {
		t.Fatal(err)
	}
	if !noChain {
		dir := filepath.Join(s.data, "audit")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		if err := os.WriteFile(filepath.Join(dir, ".digest_key"), key, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A vault reference: the backend that claims op:// is loaded in run-secrets' own process,
// resolves the reference, is recorded on the audit chain as automation via run_secrets, and is
// stopped before the program runs. Nothing but the audit chain is written in the data directory.
func TestRunSecretsProcessResolvesThroughASecretBackend(t *testing.T) {
	s := newRunSecretsSandbox(t)
	root := installBackend(t, s, admintest.Plugin{ID: "vault", SecretBackend: "op"}, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := s.command(ctx, "run-secrets", "--", s.prog)
	cmd.Env = append(cmd.Env, "RS_NAME=RS_DB", "RS_DB=op://Deploy/Database/password")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-secrets: %v\n%s", err, out)
	}
	if got, ok := s.ran(); !ok || got != "resolved-op:--Deploy-Database-password" {
		t.Fatalf("the program saw %q (ran %v)", got, ok)
	}
	if len(admintest.Marks(root, "loaded")) != 1 || len(admintest.Marks(root, "unloaded")) != 1 {
		t.Fatalf("the backend was not loaded and stopped once: loaded %v, unloaded %v", admintest.Marks(root, "loaded"), admintest.Marks(root, "unloaded"))
	}
	records, err := audit.ReadRecords(filepath.Join(s.data, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	var outcome audit.Record
	for _, r := range records {
		if r.Kind == audit.KindOutcome && r.Operation == "load" {
			outcome = r
		}
		if strings.Contains(fmt.Sprintf("%+v", r), "resolved-op") {
			t.Fatal("a resolved value reached the audit chain")
		}
	}
	if outcome.Principal.Via != "run_secrets" || outcome.Principal.Surface != "run_secrets" || outcome.Principal.Kind != audit.PrincipalAutomation ||
		outcome.Decision != audit.DecisionAllowed || outcome.Target.Fields["id"] != "vault" {
		t.Fatalf("the load was not recorded as run-secrets: %+v", outcome)
	}
	entries, err := os.ReadDir(s.data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "audit" {
		t.Fatalf("the data directory holds more than the audit chain: %v", entries)
	}
}

// A backend whose review is pending is refused: exit 1, the plugin and the program never run.
// Without the audit chain a vault reference is refused the same way, and none is created.
func TestRunSecretsProcessRefusesAPendingBackendAndAMissingChain(t *testing.T) {
	for name, tc := range map[string]struct {
		plugin  admintest.Plugin
		noChain bool
		want    string
	}{
		"pending review": {admintest.Plugin{ID: "vault", Pending: true, SecretBackend: "op"}, false, "install review is pending"},
		"no audit chain": {admintest.Plugin{ID: "vault", SecretBackend: "op"}, true, "does not exist yet"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newRunSecretsSandbox(t)
			root := installBackend(t, s, tc.plugin, tc.noChain)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := s.command(ctx, "run-secrets", "--", s.prog)
			cmd.Env = append(cmd.Env, "RS_NAME=RS_DB", "RS_DB=op://a/b/c")
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), tc.want) {
				t.Fatalf("exit %v, want 1 naming %q:\n%s", err, tc.want, out)
			}
			if _, ran := s.ran(); ran || len(admintest.Marks(root, "loaded")) != 0 {
				t.Fatal("the program or the backend ran")
			}
			if tc.noChain {
				if _, statErr := os.Stat(s.data); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("the data directory was created: %v", statErr)
				}
			}
		})
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // the sandbox's own file
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
