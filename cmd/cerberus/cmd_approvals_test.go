package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
)

// With no daemon, list and show read the store directly and say so.
func TestApprovalsListAndShowFromTheStore(t *testing.T) {
	dir, err := app.ApprovalsDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := approval.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Request(approval.Approval{Principal: audit.Principal{Kind: "agent", Via: "mcp_stdio", Client: "claude-code/2"},
		Connector: "local", Operation: "remove", Effect: "destructive", Target: audit.Target{Resource: "notes-api", Env: "dev", Owner: "self", Admin: "self"},
		Rule: "baseline.destructive.agent", Channel: approval.ChannelTTYConfirm}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		var out bytes.Buffer
		rootCmd.SetArgs(append([]string{"approvals"}, args...))
		rootCmd.SetOut(&out)
		t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil) })
		approvalsFlags.status, approvalsFlags.output = "", outputFormatText
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}
	list := run("list")
	for _, want := range []string{a.ID, "pending", "local.remove", "notes-api [dev/self/self]", "agent via mcp_stdio (claude-code/2)", "read directly: the daemon is not running"} {
		if !strings.Contains(list, want) {
			t.Errorf("list is missing %q:\n%s", want, list)
		}
	}
	show := run("show", a.ID)
	if !strings.Contains(show, "Decide with:  cerberus approvals approve "+a.ID) || !strings.Contains(show, "rule baseline.destructive.agent") {
		t.Fatalf("show:\n%s", show)
	}
	if out := run("list", "--status", "approved"); !strings.Contains(out, "No approval requests") {
		t.Fatalf("status filter:\n%s", out)
	}
}
