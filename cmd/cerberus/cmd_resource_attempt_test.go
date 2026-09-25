package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/audit"
)

// Every mutation attempt is recorded, whichever surface it came from. A CLI
// mutation on an id the config does not have, or on a resource the
// supervision lane does not operate, reaches the runtime, which refuses it
// and records the refusal; the CLI's own lookup is added as a hint.
func TestResourceMutationAttemptsAreRecorded(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(`version: 2
resources:
  - id: box
    type: server
    connector: ssh
    config:
      host: example.invalid
`), 0o600); err != nil {
		t.Fatal(err)
	}
	auditDir, err := app.AuditDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ verb, id, hint string }{
		{"stop", "ghost", `hint: resource "ghost" not found in config`},
		{"deploy", "ghost", `hint: resource "ghost" not found in config`},
		{"remove", "box", `hint: remove does not apply to "box"`},
	} {
		t.Run(c.verb+" "+c.id, func(t *testing.T) {
			// Flags outlive an Execute, and a changed --config would make
			// later tests read the config as explicit; so the path is set
			// directly and put back, as is the verb's --ack.
			oldCfg := cfgPath
			cfgPath = cfg
			rootCmd.SetArgs([]string{"resource", c.verb, c.id, "--ack"})
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				cfgPath = oldCfg
				if sub, _, findErr := rootCmd.Find([]string{"resource", c.verb}); findErr == nil {
					_ = sub.Flags().Set("ack", "false")
				}
			})
			err := rootCmd.Execute()
			if err == nil || !strings.Contains(err.Error(), c.hint) {
				t.Fatalf("err = %v, want the runtime's refusal with %q", err, c.hint)
			}
			recs, readErr := audit.ReadRecords(auditDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, rec := range recs {
				if rec.Kind == audit.KindOutcome && rec.Operation == c.verb && rec.Target.Fields["id"] == c.id && rec.OutcomeCode != audit.OutcomeOK {
					return
				}
			}
			t.Fatalf("no refused %s outcome for %q among %d records", c.verb, c.id, len(recs))
		})
	}
}
