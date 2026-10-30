package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetWriteFlags() {
	resetHostFlags()
	hostSetOpts.writeFlags, hostSetOpts.role, hostSetOpts.owner, hostSetOpts.patch, hostSetOpts.patchFile = writeFlags{}, "", "", "", ""
	hostSetOpts.labels, hostSetOpts.unlabels, hostSetOpts.create = nil, nil, false
	hostRemoveOpts = writeFlags{}
	hostInventoryOpts.writeFlags, hostInventoryOpts.dryRun = writeFlags{}, false
	compOpts = componentOpts{}
	historyLimit = 0
}

// writeHome is hostHome plus the audit directory a write needs.
func writeHome(t *testing.T) {
	t.Helper()
	hostHome(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".cerberus", "audit"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func runWrite(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetWriteFlags()
	var out bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&out)
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); resetWriteFlags() })
	err := rootCmd.Execute()
	return out.String(), err
}

func TestHostSetNeedsAckAndRecordsHistory(t *testing.T) {
	writeHome(t)
	if _, err := runWrite(t, "host", "set", "box-a", "--label", "zone=a"); err == nil || !strings.Contains(err.Error(), "acknowledg") {
		t.Fatalf("without --ack: %v", err)
	}
	out, err := runWrite(t, "host", "set", "box-a", "--ack", "--label", "zone=a", "--patch", `{"declared":{"memory":"16GiB"}}`)
	if err != nil || !strings.Contains(out, "labels.zone: (unset) -> \"a\"") || !strings.Contains(out, "declared.memory") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	out, err = runWrite(t, "host", "history", "box-a")
	if err != nil || !strings.Contains(out, "host_update") || !strings.Contains(out, "labels.zone") {
		t.Fatalf("history: %v\n%s", err, out)
	}
	hostsPath := filepath.Join(os.Getenv("HOME"), ".cerberus", "hosts.yaml")
	before, _ := os.ReadFile(hostsPath)
	if _, err = runWrite(t, "host", "set", "box-a", "--ack", "--role", "wizard"); err == nil {
		t.Fatal("bad role accepted")
	}
	if after, _ := os.ReadFile(hostsPath); string(after) != string(before) {
		t.Fatal("a refused change altered hosts.yaml")
	}
}

func TestComponentCommandsEndToEnd(t *testing.T) {
	writeHome(t)
	if _, err := runWrite(t, "component", "create", "--ack", "--id", "ram-1", "--kind", "ram", "--size", "8GiB", "--spare"); err != nil {
		t.Fatal(err)
	}
	out, err := runWrite(t, "component", "list", "--spare")
	if err != nil || !strings.Contains(out, "ram-1") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, err = runWrite(t, "component", "update", "ram-1", "--ack", "--size", "16GiB"); err != nil || !strings.Contains(out, "size:") {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if _, err = runWrite(t, "component", "move", "ram-1", "--ack", "--host", "box-a", "--slot", "dimm0"); err == nil {
		t.Fatal("box-a records no memory slots; the move must be refused")
	}
	out, err = runWrite(t, "component", "history", "ram-1")
	if err != nil || strings.Count(out, "component_") != 2 {
		t.Fatalf("history: %v\n%s", err, out)
	}
	if _, err = runWrite(t, "component", "remove", "ram-1", "--ack"); err != nil {
		t.Fatal(err)
	}
}

func TestHostInventoryNeedsAnSSHReach(t *testing.T) {
	writeHome(t)
	_, err := runWrite(t, "host", "inventory", "box-a", "--ack", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "reach.ssh") {
		t.Fatalf("err = %v", err)
	}
	if _, err = runWrite(t, "host", "inventory", "box-a", "--dry-run"); err == nil || !strings.Contains(err.Error(), "acknowledg") {
		t.Fatalf("without --ack: %v", err)
	}
}

func TestHostStatusWithoutADaemonSaysSo(t *testing.T) {
	writeHome(t)
	for _, args := range [][]string{{"host", "status"}, {"host", "monitor"}, {"host", "samples", "box-a"}, {"host", "events"}} {
		if _, err := runWrite(t, args...); err == nil || !strings.Contains(err.Error(), "monitoring is not available") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestHostCapacityHardwareFlags(t *testing.T) {
	writeHome(t)
	hostsPath := filepath.Join(os.Getenv("HOME"), ".cerberus", "hosts.yaml")
	if err := os.WriteFile(hostsPath, []byte(`version: 1
hosts:
  - name: big
    role: worker
    hardware:
      memory: {type: DDR4, total: 32GiB, slots: 4, slots_used: 2}
      disks: [{name: nvme0n1, size: 1TB, type: nvme}]
  - name: small
    role: worker
    hardware:
      memory: {type: DDR4, total: 8GiB, slots: 2, slots_used: 2}
      disks: [{name: sda, size: 500GB, type: hdd}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runWrite(t, "host", "capacity", "--min-memory-total", "16GiB", "--free-memory-slots", "2", "--disk-type", "nvme", "--explain")
	if err != nil || !strings.Contains(out, "1 matched, 0 unknown") || !strings.Contains(out, "excluded small") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err = runWrite(t, "host", "capacity", "--free-slot", "nvme_m2"); err == nil || !strings.Contains(err.Error(), "kind=N") {
		t.Fatalf("a malformed --free-slot: %v", err)
	}
	if _, err = runWrite(t, "host", "capacity", "--free-slot", "floppy=1"); err == nil || !strings.Contains(err.Error(), "unknown storage slot kind") {
		t.Fatalf("an unknown kind: %v", err)
	}
}
