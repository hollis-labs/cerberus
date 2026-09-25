package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runRoot runs the CLI with args against a scratch HOME, so no daemon socket
// is found and the command runs in-process, and returns what cobra printed.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "cerbhome-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	// The pre-run hook sets SilenceUsage on the command it runs, and the
	// command tree is shared across tests; a real process runs once.
	var reset func(*cobra.Command)
	reset = func(c *cobra.Command) {
		c.SilenceUsage = false
		for _, sub := range c.Commands() {
			reset(sub)
		}
	}
	reset(rootCmd)
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	err = rootCmd.Execute()
	return out.String(), err
}

// A refusal is an operational error: the message says what to do, and a
// usage block under it only buries that. Misuse still gets its usage.
func TestRuntimeErrorsDoNotPrintUsage(t *testing.T) {
	out, err := runRoot(t, "server", "stop", "42")
	if err == nil || !strings.Contains(err.Error(), "acknowledgment_required") {
		t.Fatalf("err = %v, want the ack refusal", err)
	}
	if strings.Contains(out, "Usage:") {
		t.Fatalf("usage printed under a runtime error:\n%s", out)
	}

	out, err = runRoot(t, "server", "stop")
	if err == nil || !strings.Contains(out, "Usage:") {
		t.Fatalf("argument misuse lost its usage: err = %v\n%s", err, out)
	}
}

// Every CLI verb over an operation that now needs acknowledgment offers
// --ack (Decision 14).
func TestLifecycleVerbsOfferAck(t *testing.T) {
	for _, path := range [][]string{
		{"server", "start"}, {"server", "stop"}, {"docker", "up"}, {"docker", "down"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if cmd.Flags().Lookup("ack") == nil {
			t.Errorf("%s has no --ack", strings.Join(path, " "))
		}
	}
}
