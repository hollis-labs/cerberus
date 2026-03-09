package service

import (
	"net"
	"strconv"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
)

// listenOnPort opens a TCP listener on a random available port and returns
// the listener and the port number.
func listenOnPort(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return ln, port
}

func TestCheckPortConflict_FreePort(t *testing.T) {
	// Pick a port that is (very likely) free
	ln, port := listenOnPort(t)
	ln.Close() // close immediately so the port is free

	conflict, err := CheckPortConflict(port, "test-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conflict != nil {
		t.Fatalf("expected no conflict on free port %d, got: %+v", port, conflict)
	}
}

func TestCheckPortConflict_InUse(t *testing.T) {
	ln, port := listenOnPort(t)
	defer ln.Close()

	conflict, err := CheckPortConflict(port, "test-svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conflict == nil {
		t.Fatalf("expected conflict on port %d, got nil", port)
	}
	if conflict.Port != port {
		t.Errorf("expected port %d, got %d", port, conflict.Port)
	}
	if conflict.PID <= 0 {
		t.Errorf("expected positive PID, got %d", conflict.PID)
	}
	if conflict.ProcessName == "" {
		t.Error("expected non-empty process name")
	}
}

func TestCheckPortConflict_String(t *testing.T) {
	c := PortConflict{
		Port:             8080,
		PID:              1234,
		ProcessName:      "myapp",
		CerberusManaged:  false,
		ManagedServiceID: "",
	}
	s := c.String()
	if s == "" {
		t.Fatal("expected non-empty string")
	}
	// Should contain the PID and process name
	if !contains(s, "1234") || !contains(s, "myapp") {
		t.Errorf("expected string to contain PID and process name, got: %s", s)
	}

	// Cerberus-managed variant
	c.CerberusManaged = true
	c.ManagedServiceID = "cortex-api"
	s = c.String()
	if !contains(s, "cortex-api") {
		t.Errorf("expected string to contain managed service ID, got: %s", s)
	}
}

func TestScanAllPorts_NoConflict(t *testing.T) {
	// Use ports that are (very likely) free — high ephemeral range
	services := []*Service{
		{Def: config.ServiceDef{ID: "svc-a", Port: 0}},       // port 0 is skipped
		{Def: config.ServiceDef{ID: "svc-b", Port: 59871}},   // likely free
	}
	conflicts := ScanAllPorts(services)
	// svc-a is skipped (port 0), svc-b should be free
	for _, c := range conflicts {
		if c.Port == 59871 {
			t.Errorf("expected no conflict on port 59871, got: %+v", c)
		}
	}
}

func TestScanAllPorts_WithConflict(t *testing.T) {
	ln, port := listenOnPort(t)
	defer ln.Close()

	services := []*Service{
		{Def: config.ServiceDef{ID: "busy-svc", Port: port}},
		{Def: config.ServiceDef{ID: "free-svc", Port: 59872}},
	}
	conflicts := ScanAllPorts(services)

	found := false
	for _, c := range conflicts {
		if c.Port == port {
			found = true
			if c.PID <= 0 {
				t.Error("expected positive PID in conflict")
			}
		}
	}
	if !found {
		t.Errorf("expected conflict for port %d", port)
	}
}

func TestProcessName(t *testing.T) {
	// Our own process should have a name
	name := processName(1) // pid 1 always exists (launchd / init)
	if name == "" {
		t.Error("expected non-empty name for pid 1")
	}
}

func TestDoctor_MissingDir(t *testing.T) {
	services := []*Service{
		{Def: config.ServiceDef{
			ID:      "ghost",
			Dir:     "/tmp/cerberus-test-nonexistent-" + strconv.Itoa(59999),
			Command: []string{"echo", "hi"},
			Port:    59873,
		}},
	}
	results := RunDoctor(services)
	foundDirErr := false
	for _, r := range results {
		if r.Check == "directory" && r.Status == "error" {
			foundDirErr = true
		}
	}
	if !foundDirErr {
		t.Error("expected directory error for nonexistent dir")
	}
}

func TestDoctor_MissingBinary(t *testing.T) {
	services := []*Service{
		{Def: config.ServiceDef{
			ID:      "no-bin",
			Dir:     "/tmp",
			Command: []string{"cerberus-nonexistent-binary-xyz"},
			Port:    59874,
		}},
	}
	results := RunDoctor(services)
	foundBinErr := false
	for _, r := range results {
		if r.Check == "binary" && r.Status == "error" {
			foundBinErr = true
		}
	}
	if !foundBinErr {
		t.Error("expected binary error for nonexistent binary")
	}
}

func TestDoctor_NoCommand(t *testing.T) {
	services := []*Service{
		{Def: config.ServiceDef{
			ID:      "empty",
			Dir:     "/tmp",
			Command: nil,
			Port:    59875,
		}},
	}
	results := RunDoctor(services)
	foundBinErr := false
	for _, r := range results {
		if r.Check == "binary" && r.Status == "error" {
			foundBinErr = true
		}
	}
	if !foundBinErr {
		t.Error("expected binary error for empty command")
	}
}

func TestDoctor_AllOK(t *testing.T) {
	services := []*Service{
		{Def: config.ServiceDef{
			ID:      "ok-svc",
			Dir:     "/tmp",
			Command: []string{"echo", "hi"},
			Port:    59876,
		}},
	}
	results := RunDoctor(services)
	for _, r := range results {
		if r.Status == "error" {
			t.Errorf("expected all ok, got error: %s: %s", r.Check, r.Message)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchIn(s, substr)
}

func searchIn(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
