package pluginhost

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// scopedResolver answers by the access a resolution is for.
type scopedResolver struct{}

func (scopedResolver) Get(ctx context.Context, _, _ string) (string, error) {
	s, ok := secrets.CredentialScopeFrom(ctx)
	switch {
	case !ok:
		return "LEGACY", nil
	case s.Access == secrets.AccessWrite:
		return "RW", nil
	}
	return "RO", nil
}

func splitHost(t *testing.T, bindings string) *fakeHost {
	t.Helper()
	h := newFakeHost(t, false, map[string]string{"cf": "creds"}, nil)
	p, _ := h.m.Installed("cf")
	p.Manifest.Config.Secrets = []contract.SecretRequirement{{Name: "token", Required: true}}
	p.Spec.Cerberus.Connector = p.Manifest
	h.m.RegisterInstalled(p)
	h.m.secrets = scopedResolver{}
	file, err := secrets.ParseBindings([]byte(bindings), "f", secretref.IsRef)
	if err != nil {
		t.Fatal(err)
	}
	h.m.bindings = func() (secrets.BindingFile, error) { return file, nil }
	return h
}

func tokenOf(t *testing.T, h *fakeHost, op string) (string, int) {
	t.Helper()
	args := OperationArgs{Connector: "cf", Operation: op, Config: map[string]any{}, Acknowledged: true}
	out, err := h.m.ExecuteOperation(context.Background(), args)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	data, _ := out.Data.(map[string]any)
	tok, _ := data["token"].(string)
	pid, _ := data["pid"].(float64)
	return tok, int(pid)
}

// A plugin whose credentials are bound per access runs two processes: the
// one as loaded holds only the read credential and serves reads; writes go
// to a write instance, started on the first write, which alone holds the
// write credential. Both stop with the plugin.
func TestSplitCredentialsRunTwoInstances(t *testing.T) {
	h := splitHost(t, "cf:\n  read: { token: keychain://cf/ro }\n  write: { token: keychain://cf/rw }\n")
	if err := h.m.Load(context.Background(), "cf"); err != nil {
		t.Fatal(err)
	}
	if split, running := h.m.WriteInstanceRunning("cf"); !split || running {
		t.Fatalf("after load: split %v, writer %v", split, running)
	}
	readTok, readPID := tokenOf(t, h, "read_it")
	if readTok != "RO" {
		t.Fatalf("a read ran with %q", readTok)
	}
	writeTok, writePID := tokenOf(t, h, "write_it")
	if writeTok != "RW" || writePID == readPID {
		t.Fatalf("a write ran with %q in pid %d (the reader is %d)", writeTok, writePID, readPID)
	}
	if again, _ := tokenOf(t, h, "read_it"); again != "RO" {
		t.Fatalf("a read after a write ran with %q", again)
	}
	if _, running := h.m.WriteInstanceRunning("cf"); !running {
		t.Fatal("the write instance is not running")
	}
	if err := h.m.Unload(context.Background(), "cf"); err != nil {
		t.Fatal(err)
	}
	if !gone(writePID) || !gone(readPID) {
		t.Fatal("an instance outlived the unload")
	}
}

// An idle write instance stops; the next write starts another.
func TestIdleWriteInstanceStops(t *testing.T) {
	h := splitHost(t, "cf:\n  read: { token: keychain://cf/ro }\n  write: { token: keychain://cf/rw }\n")
	h.m.writerIdle = 100 * time.Millisecond
	if err := h.m.Load(context.Background(), "cf"); err != nil {
		t.Fatal(err)
	}
	_, first := tokenOf(t, h, "write_it")
	waitFor(t, "the idle write instance to stop", func() bool { _, r := h.m.WriteInstanceRunning("cf"); return !r })
	if !gone(first) {
		t.Fatal("the idle write instance's process survived")
	}
	if tok, second := tokenOf(t, h, "write_it"); tok != "RW" || second == first {
		t.Fatalf("the next write: %q in pid %d", tok, second)
	}
}

// An unsplit plugin keeps one process and the legacy credential; a plugin
// with per-target bindings is refused, never loaded with the connector
// level's.
func TestUnsplitAndPerTargetPlugins(t *testing.T) {
	h := splitHost(t, "other:\n  token: keychain://other/token\n")
	if err := h.m.Load(context.Background(), "cf"); err != nil {
		t.Fatal(err)
	}
	r, rp := tokenOf(t, h, "read_it")
	w, wp := tokenOf(t, h, "write_it")
	if r != "LEGACY" || w != "LEGACY" || rp != wp {
		t.Fatalf("unsplit: %q/%d %q/%d", r, rp, w, wp)
	}
	h2 := splitHost(t, "cf:\n  targets:\n    - match: { env: prod }\n      read: { token: keychain://cf/ro }\n      write: { token: null }\n")
	err := h2.m.Load(context.Background(), "cf")
	if err == nil || !strings.Contains(err.Error(), "per target") || !strings.Contains(err.Error(), "I9-b") || h2.m.Loaded("cf") {
		t.Fatalf("per-target bindings: %v", err)
	}
}
