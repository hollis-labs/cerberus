package local

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/service"
)

func TestDevSessionProcessHelper(t *testing.T) {
	if os.Getenv("CERBERUS_TEST_LISTENER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(listener.Addr().String())
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			os.Exit(1)
		}
		_ = conn.Close()
	}
}

func foreignListener(t *testing.T) (int, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDevSessionProcessHelper$") //nolint:gosec // re-execute only this test helper
	cmd.Env = append(os.Environ(), "CERBERUS_TEST_LISTENER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	_, rawPort, err := net.SplitHostPort(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return port, cmd.Process.Pid
}

func testDevResource(t *testing.T) *domain.Resource {
	t.Helper()
	dir := t.TempDir()
	id := fmt.Sprintf("test-dev-%d-%s", os.Getpid(), filepath.Base(dir))
	t.Cleanup(func() { _ = service.RemovePIDFile(id) })
	return &domain.Resource{ID: id, Connector: "local", Type: domain.ResourceProcess, Config: map[string]any{
		"command": []string{"/bin/sleep", "60"}, "log_file": filepath.Join(dir, "process.log"),
	}}
}

func TestDevSessionNeverAdoptsOrStopsForeignPort(t *testing.T) {
	port, pid := foreignListener(t)
	res := testDevResource(t)
	res.Config["port"] = port
	c := New()
	state, err := c.Status(context.Background(), res)
	if state != domain.StateUnknown || err == nil || !strings.Contains(err.Error(), "unowned") {
		t.Fatalf("expected foreign-port diagnostic, got %s / %v", state, err)
	}
	if _, err := service.ReadPIDFile(res.ID); err == nil {
		t.Fatal("poll adopted the foreign listener")
	}
	for _, operation := range []func(context.Context, *domain.Resource) error{c.Stop, c.Reload, c.Start} {
		if err := operation(context.Background(), res); err == nil {
			t.Fatal("foreign process operation unexpectedly succeeded")
		}
		if !processAlive(pid) {
			t.Fatal("foreign listener was stopped")
		}
	}
}

func TestDevSessionRejectsPIDFileWithWrongLaunchIdentity(t *testing.T) {
	_, pid := foreignListener(t)
	res := testDevResource(t)
	if err := service.WritePIDFile(res.ID, pid); err != nil {
		t.Fatal(err)
	}
	if err := service.WriteMetaFile(res.ID, service.PIDMeta{PID: pid, StartedAt: time.Now(), ProcessStart: "a different process"}); err != nil {
		t.Fatal(err)
	}
	if err := New().Stop(context.Background(), res); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("expected identity refusal, got %v", err)
	}
	if !processAlive(pid) {
		t.Fatal("foreign process was stopped")
	}
}

func TestDevSessionRefreshesSpecAndWaitsForOwnedStop(t *testing.T) {
	port, foreignPID := foreignListener(t)
	res := testDevResource(t)
	c := New()
	if err := c.Start(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	pid, err := service.ReadPIDFile(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { delete(res.Config, "port"); _ = c.Stop(context.Background(), res) })
	// A config edit must not redirect ownership to the new port's listener.
	res.Config["port"] = port
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Stop(ctx, res); err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Fatal("stop returned while the owned process was still alive")
	}
	if !processAlive(foreignPID) {
		t.Fatal("stop targeted the new port's foreign process")
	}
	if _, err := c.Status(ctx, res); err == nil || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("status did not use the edited port: %v", err)
	}
}

func TestDevSessionReloadWaitsForPreviousProcess(t *testing.T) {
	res := testDevResource(t)
	c := New()
	if err := c.Start(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background(), res) })
	oldPID, err := service.ReadPIDFile(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadErr := c.Reload(context.Background(), res); reloadErr != nil {
		t.Fatal(reloadErr)
	}
	newPID, err := service.ReadPIDFile(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldPID == newPID || processAlive(oldPID) || !processAlive(newPID) {
		t.Fatal("reload did not replace the owned process")
	}
}

func TestActivateBuiltReplacesOwnedRunningDevSession(t *testing.T) {
	res := testDevResource(t)
	c := New()
	ctx := context.Background()
	if err := c.Start(ctx, res); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop(ctx, res) })
	before, err := service.ReadPIDFile(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.ActivateBuilt(ctx, res)
	if err != nil || result.Action != ApplyActionRestarted {
		t.Fatalf("deploy activation failed: %+v %v", result, err)
	}
	after, err := service.ReadPIDFile(res.ID)
	if err != nil || after == before || processAlive(before) || !processAlive(after) {
		t.Fatalf("old build still running: old=%d new=%d %v", before, after, err)
	}
}
