package cerbapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// agentOverMCP is an MCP-only agent: the caller B3 is about.
var agentOverMCP = Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code"}

// transferFixture is a scratch home and transfer root, and an ssh service
// whose backend records what it was asked to transfer.
func transferFixture(t *testing.T) (home, root string, svc *ExternalConnectorService, backend *fakeSSHBackend, sink *audit.Memory) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	root = filepath.Join(home, ".cerberus", "transfers")
	prev := TransferRoot()
	SetTransferRoot(root)
	t.Cleanup(func() { SetTransferRoot(prev) })
	backend = &fakeSSHBackend{}
	svc = newSSHTestService(backend)
	sink = audit.NewMemory()
	svc.audit = sink
	return home, root, svc, backend, sink
}

func transfer(svc *ExternalConnectorService, p Principal, operation, localPath string, dryRun bool) error {
	cfg := map[string]any{"local_path": localPath, "remote_path": "/srv/payload"}
	_, err := svc.Execute(as(p), ExternalConnectorOperationArgs{
		Connector: "ssh", Operation: operation, Config: sshTransferConfig(cfg), Acknowledged: true, DryRun: dryRun,
	})
	return err
}

// The re-review's repros (B3): an MCP-only agent that can put content on a
// reachable host cannot then `ssh get` it over a shell startup file, a
// LaunchAgent or Cerberus's own policy. Each is refused before the backend
// runs, and the refusal is recorded.
func TestAnMCPAgentCannotWriteOutsideTheTransferRoot(t *testing.T) {
	home, root, svc, backend, sink := transferFixture(t)
	for _, target := range []string{
		filepath.Join(home, ".zshrc"),
		"~/.zshrc",
		filepath.Join(home, "Library", "LaunchAgents", "com.example.payload.plist"),
		filepath.Join(home, ".cerberus", "policy", "policy.yaml"),
		"../policy/policy.yaml",
	} {
		for _, operation := range []string{"get", "get_dir"} {
			err := transfer(svc, agentOverMCP, operation, target, false)
			if connectorErrorCode(err) != ExternalConnectorInvalidArgs || !strings.Contains(err.Error(), "transfer root "+root) {
				t.Fatalf("%s %s: err = %v, want invalid_args naming the transfer root", operation, target, err)
			}
		}
	}
	if backend.getCalls != 0 || backend.getDirCalls != 0 {
		t.Fatalf("the backend ran: get=%d get_dir=%d", backend.getCalls, backend.getDirCalls)
	}
	refused := 0
	for _, r := range sink.Records() {
		if r.Kind == audit.KindOutcome && r.Operation == "get" && r.OutcomeCode != audit.OutcomeOK {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("the refusals are not on the record")
	}
}

// ssh put's read side follows the same rule: the agent cannot upload a key
// or any file outside the root, and a dry run does not stat one either.
func TestAnMCPAgentCannotReadOutsideTheTransferRoot(t *testing.T) {
	home, _, svc, backend, _ := transferFixture(t)
	secret := filepath.Join(home, ".ssh", "id_ed25519")
	for _, operation := range []string{"put", "put_dir"} {
		for _, dryRun := range []bool{false, true} {
			if err := transfer(svc, agentOverMCP, operation, secret, dryRun); connectorErrorCode(err) != ExternalConnectorInvalidArgs {
				t.Fatalf("%s (dry run %v): err = %v", operation, dryRun, err)
			}
		}
	}
	if backend.putCalls != 0 || backend.putDirCalls != 0 {
		t.Fatalf("the backend ran: put=%d put_dir=%d", backend.putCalls, backend.putDirCalls)
	}
}

// Inside the root is fine, relative or absolute, and the operation gets the
// resolved path.
func TestTransfersInsideTheRootRun(t *testing.T) {
	_, root, svc, backend, _ := transferFixture(t)
	if err := transfer(svc, agentOverMCP, "get", "reports/today.csv", false); err != nil {
		t.Fatalf("relative: %v", err)
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	if want := filepath.Join(realRoot, "reports", "today.csv"); backend.localPath != want {
		t.Fatalf("backend got %q, want %q", backend.localPath, want)
	}
	if err := transfer(svc, agentOverMCP, "get", filepath.Join(root, "b.txt"), false); err != nil {
		t.Fatalf("absolute inside the root: %v", err)
	}
}

// A symlink in the root does not lead out of it, whether it names the file,
// a directory on the way, or nothing at all.
func TestASymlinkCannotEscapeTheTransferRoot(t *testing.T) {
	home, root, svc, backend, _ := transferFixture(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"rc":       filepath.Join(home, ".zshrc"),
		"agents":   filepath.Join(home, "Library", "LaunchAgents"),
		"dangling": filepath.Join(home, "nowhere", "file"),
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("# rc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"rc", "agents/com.example.payload.plist", "dangling"} {
		if err := transfer(svc, agentOverMCP, "get", path, false); connectorErrorCode(err) != ExternalConnectorInvalidArgs {
			t.Fatalf("%s: err = %v, want refused", path, err)
		}
	}
	if err := transfer(svc, agentOverMCP, "put", "rc", false); connectorErrorCode(err) != ExternalConnectorInvalidArgs {
		t.Fatalf("put through a symlink out: %v", err)
	}
	if backend.getCalls != 0 || backend.putCalls != 0 {
		t.Fatalf("the backend ran: get=%d put=%d", backend.getCalls, backend.putCalls)
	}
	// A symlink that stays inside the root is followed.
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := transfer(svc, agentOverMCP, "get", "alias/x", false); err != nil {
		t.Fatalf("a symlink inside the root: %v", err)
	}
}

// A person at the CLI names any path, as before.
func TestAHumanAtTheCLINamesAnyPath(t *testing.T) {
	home, _, svc, backend, _ := transferFixture(t)
	target := filepath.Join(home, "Downloads", "nginx.conf")
	if err := transfer(svc, humanCLI, "get", target, false); err != nil {
		t.Fatalf("human CLI: %v", err)
	}
	if backend.localPath != target {
		t.Fatalf("backend got %q, want it unchanged", backend.localPath)
	}
	// An agent at the CLI, or a console session, is held to the root.
	for _, p := range []Principal{{Kind: PrincipalAgent, Via: ViaCLI}, {Kind: PrincipalHuman, Via: ViaWeb, Session: "s1"}, {}} {
		if err := transfer(svc, p, "get", target, false); connectorErrorCode(err) != ExternalConnectorInvalidArgs {
			t.Fatalf("%+v: err = %v", p, err)
		}
	}
}

// The refusal's recovery survives redaction.
func TestTheTransferRootRefusalSurvivesRedaction(t *testing.T) {
	home, _, svc, _, _ := transferFixture(t)
	err := transfer(svc, agentOverMCP, "get", filepath.Join(home, ".zshrc"), false)
	if err == nil {
		t.Fatal("not refused")
	}
	msg := err.Error()
	if redact.Text(msg) != msg || !strings.Contains(msg, "run the transfer from your own terminal") {
		t.Fatalf("message = %q, redacted %q", msg, redact.Text(msg))
	}
}

// The transfer root comes from the operator's config, "~" expanded.
func TestConfineToRootNeedsAnAbsoluteRoot(t *testing.T) {
	if _, err := ConfineToRoot("relative/root", "x"); err == nil {
		t.Fatal("a relative root was accepted")
	}
}
