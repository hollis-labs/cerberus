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
