package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/eventexport"
	"github.com/hollis-labs/cerberus/internal/opsref"
	"github.com/spf13/cobra"
)

func TestOpsRefsFromCommandFlagThenEnv(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	addOpsFlags(cmd)
	t.Setenv("OPS_ID", "env-ops")
	t.Setenv("TETHER_WORKSTREAM_ID", "workstream:ENV")
	t.Setenv("TETHER_TASK_ID", "CW-ENV")
	if got := opsRefsFromCommand(cmd); got != (opsref.Refs{OpsID: "env-ops", WorkstreamID: "workstream:ENV", Task: "CW-ENV"}) {
		t.Fatalf("env only: %+v", got)
	}
	if err := cmd.Flags().Set("task", "CW-FLAG"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("ops-id", "flag-ops"); err != nil {
		t.Fatal(err)
	}
	if got := opsRefsFromCommand(cmd); got != (opsref.Refs{OpsID: "flag-ops", WorkstreamID: "workstream:ENV", Task: "CW-FLAG"}) {
		t.Fatalf("flag over env, each id separately: %+v", got)
	}
}

func TestResourceVerbsTakeOpsFlags(t *testing.T) {
	for _, verb := range []string{"deploy", "apply", "reload"} {
		sub, _, err := rootCmd.Find([]string{"resource", verb})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"ops-id", "workstream", "task"} {
			if sub.Flags().Lookup(f) == nil {
				t.Errorf("resource %s has no --%s", verb, f)
			}
		}
	}
}

// deploy record writes the audit record, and the record maps to the
// deploy.state_changed event; the Ops post is covered where the sink is wired.
func TestDeployRecordCommandWritesAuditRecord(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("version: 2\nresources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCfg := cfgPath
	cfgPath = cfg
	t.Cleanup(func() {
		cfgPath = oldCfg
		rootCmd.SetArgs(nil)
		for _, f := range []string{"resource", "revision", "outcome", "health", "ops-id", "workstream", "task"} {
			_ = deployRecordCmd.Flags().Set(f, "")
		}
	})
	rootCmd.SetArgs([]string{"deploy", "record", "--resource", "tether-hub", "--revision", "abc1234", "--outcome", "rolled_back",
		"--health", "http://h/healthz 503", "--ops-id", "ops-x", "--workstream", "workstream:W", "--task", "CW-5"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	auditDir, err := app.AuditDir()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := audit.ReadRecords(auditDir)
	if err != nil {
		t.Fatal(err)
	}
	var rec audit.Record
	for _, r := range recs {
		if r.Operation == "deploy_record" {
			rec = r
		}
	}
	if rec.DeployOutcome != "rolled_back" || rec.OpsID != "ops-x" || rec.WorkstreamID != "workstream:W" || rec.Task != "CW-5" || rec.Revision != "abc1234" {
		t.Fatalf("audit record: %+v", rec)
	}
	ev := eventexport.MapLifecycle(rec)
	if len(ev) != 1 || !strings.Contains(string(ev[0].Envelope.Payload), `"to":"rolled_back"`) {
		t.Fatalf("event: %+v", ev)
	}

	// A bad outcome fails before anything is written.
	rootCmd.SetArgs([]string{"deploy", "record", "--resource", "x", "--outcome", "started"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("an intermediate state was accepted as an outcome")
	}
}
