package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordedSystemctl struct{ calls []string }

func (r *recordedSystemctl) run(_ context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	return nil, nil
}

func newTestSystemdInstaller(t *testing.T, linger bool) (*systemdInstaller, *recordedSystemctl, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	rec := &recordedSystemctl{}
	out := &bytes.Buffer{}
	return &systemdInstaller{
		home:      home,
		binPath:   "/home/op/.local/bin/cerberus",
		envPath:   "/home/op/.local/bin:.:/home/op/.local/go/bin",
		systemctl: rec.run,
		lingerOn:  func(context.Context) (bool, error) { return linger, nil },
		out:       out,
	}, rec, out
}

func TestSystemdInstallWritesEnablesAndRestartsTheUnit(t *testing.T) {
	s, rec, out := newTestSystemdInstaller(t, true)
	if err := s.install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	unitPath := filepath.Join(s.home, ".config", "systemd", "user", "com.hollis-labs.cerberus.service")
	data, err := os.ReadFile(unitPath) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	unit := string(data)
	for _, needle := range []string{
		`ExecStart="/home/op/.local/bin/cerberus" daemon --foreground` + "\n",
		"WorkingDirectory=" + s.home + "\n",
		"Restart=always\n",
		`Environment="PATH=/home/op/.local/bin:/home/op/.local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"` + "\n",
		"StandardOutput=append:" + filepath.Join(s.home, ".cerberus", "logs", "systemd-stdout.log") + "\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(unit, needle) {
			t.Fatalf("unit missing %q:\n%s", needle, unit)
		}
	}
	if strings.Contains(unit, "DBUS_SESSION_BUS_ADDRESS") {
		t.Fatalf("the unit must leave the manager's bus address alone:\n%s", unit)
	}
	if got, want := strings.Join(rec.calls, ","), "daemon-reload,enable com.hollis-labs.cerberus.service,restart com.hollis-labs.cerberus.service"; got != want {
		t.Fatalf("systemctl calls = %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "start automatically at boot") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestSystemdInstallNamesTheLingerCommandWithoutRunningIt(t *testing.T) {
	s, rec, out := newTestSystemdInstaller(t, false)
	if err := s.install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "loginctl enable-linger") {
		t.Fatalf("output does not name the linger command: %s", out.String())
	}
	for _, call := range rec.calls {
		if strings.Contains(call, "linger") {
			t.Fatalf("install ran %q; it must only tell the operator", call)
		}
	}
}

func TestSystemdInstallRetiresTheHandWrittenUnit(t *testing.T) {
	s, rec, out := newTestSystemdInstaller(t, true)
	dir := filepath.Join(s.home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	handWritten := filepath.Join(dir, "cerberus.service")
	if err := os.WriteFile(handWritten, []byte("[Service]\nExecStart=%h/.local/bin/cerberus daemon --foreground\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handWritten); !os.IsNotExist(err) {
		t.Fatalf("hand-written unit still present: %v", err)
	}
	// It is stopped before the new unit starts, so two daemons never run.
	if len(rec.calls) == 0 || rec.calls[0] != "disable --now cerberus.service" {
		t.Fatalf("first call = %v, want the hand-written unit disabled first", rec.calls)
	}
	if !strings.Contains(out.String(), "Retired the hand-written unit") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestSystemdInstallRefusesAForeignCerberusService(t *testing.T) {
	s, rec, _ := newTestSystemdInstaller(t, true)
	dir := filepath.Join(s.home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cerberus.service"), []byte("[Service]\nExecStart=/opt/other/cerberus-proxy --listen :80\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not run `cerberus daemon`") {
		t.Fatalf("install over a foreign cerberus.service: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("install touched systemd before refusing: %v", rec.calls)
	}
}

func TestSystemdUninstallRemovesTheUnit(t *testing.T) {
	s, rec, out := newTestSystemdInstaller(t, true)
	if err := s.install(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.calls = nil
	if err := s.uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.home, ".config", "systemd", "user", "com.hollis-labs.cerberus.service")); !os.IsNotExist(err) {
		t.Fatalf("unit still present: %v", err)
	}
	if got, want := strings.Join(rec.calls, ","), "disable --now com.hollis-labs.cerberus.service,daemon-reload"; got != want {
		t.Fatalf("systemctl calls = %s, want %s", got, want)
	}
	out.Reset()
	if err := s.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Fatalf("second uninstall: %s", out.String())
	}
}
