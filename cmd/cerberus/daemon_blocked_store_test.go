package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

const childDaemonEnv = "CERBERUS_TEST_CHILD_DAEMON"

// runChildDaemon is this test binary acting as `cerberus daemon --foreground`
// with an OS credential store that blocks forever (a locked Secret Service
// whose unlock prompt nobody can answer) and a short timeout for it.
func runChildDaemon() {
	secrets.KeyringTimeout = 300 * time.Millisecond
	var calls atomic.Int64
	secrets.BlockCredentialStoreForTests(make(chan struct{}), &calls) // never released
	inProcessStateCheck = func() error { return nil }
	rootCmd.SetArgs([]string{"daemon", "--foreground"})
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "child daemon:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// The outage class (CW-20261030-0199): after a reboot the login keyring is
// locked and every call into it waits for an unlock prompt nobody can answer.
// The daemon must still create its socket, serve, and say it is degraded.
func TestDaemonStartsAndServesWithACredentialStoreThatBlocksForever(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "cerbblk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childDaemonEnv+"=1", "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "xdg-config"), "XDG_DATA_HOME="+filepath.Join(home, "xdg-data"),
		"XDG_CACHE_HOME="+filepath.Join(home, "xdg-cache"), "XDG_STATE_HOME="+filepath.Join(home, "xdg-state"),
		"XPC_SERVICE_NAME=") // not launchd-managed
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	sock := filepath.Join(home, ".cerberus", "cerberus.sock")
	client := cerbapi.NewSocketClient(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ready bool
	var lastErr error
	for !ready {
		select {
		case err := <-exited:
			t.Fatalf("the daemon exited instead of serving (%v):\n%s", err, out.String())
		case <-ctx.Done():
			t.Fatalf("the daemon never answered on %s with a blocked credential store (last ping error: %v):\n%s", sock, lastErr, out.String())
		case <-time.After(100 * time.Millisecond):
		}
		pctx, pcancel := context.WithTimeout(ctx, time.Second)
		lastErr = client.Ping(pctx)
		ready = lastErr == nil
		pcancel()
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket missing: %v", err)
	}

	// An audited read reaches the audit anchor, which asks the (blocked)
	// credential store; the request must still be answered.
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	if _, err := client.ListHosts(rctx, cerbapi.HostFilter{}); err != nil {
		t.Fatalf("a read must be served with the credential store blocked: %v\n%s", err, out.String())
	}

	// The daemon says it is degraded, and `cerberus daemon status` shows it.
	var degraded *cerbapi.CredentialStoreStatus
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		pctx, pcancel := context.WithTimeout(ctx, time.Second)
		st, err := client.PingStatus(pctx)
		pcancel()
		if err == nil && st.CredentialStore != nil {
			degraded = st.CredentialStore
			break
		}
	}
	if degraded == nil || !degraded.Degraded || !strings.Contains(degraded.Reason, "credential store did not answer") {
		t.Fatalf("the daemon did not report a degraded credential store: %+v\n%s", degraded, out.String())
	}
	t.Setenv("HOME", home) // currentDaemonStatus resolves the socket from HOME
	view, err := currentDaemonStatus(ctx)
	if err != nil || !view.SocketReady || !strings.HasPrefix(view.Degraded, "credential store unavailable") {
		t.Fatalf("daemon status: %+v %v", view, err)
	}
}

func TestACredentialStoreThatAnswersIsNotDegraded(t *testing.T) {
	secrets.ResetKeyringGate()
	if h := secrets.Health(); h.Degraded {
		t.Fatalf("a healthy store reads degraded: %+v", h)
	}
}
