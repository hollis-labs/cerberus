package cerbapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Every error code is one redaction leaves alone, so a recovery
// instruction after one survives (the list is held to the vocabulary).
func TestEveryErrorCodeIsRedactionExempt(t *testing.T) {
	for _, code := range ExternalConnectorErrorCodes() {
		if !redact.IsErrorCode(string(code)) {
			t.Errorf("%s is not in redact's error codes", code)
		}
	}
}

// The supervisor's errors map to their codes, keep their guidance through
// redaction, and a missed deadline is not recorded as a refusal: a
// non-read may have run.
func TestPluginSupervisionErrorsAreCoded(t *testing.T) {
	args := ExternalConnectorOperationArgs{Connector: "vault", Operation: "rotate"}
	for want, err := range map[ExternalConnectorErrorCode]error{
		ExternalConnectorDeadlineExceeded: &pluginhost.DeadlineError{Connector: "vault", Phase: "call", Operation: "rotate", Timeout: 2 * time.Minute, Effect: contract.EffectWrite},
		ExternalConnectorOutputTooLarge:   &pluginhost.OutputTooLargeError{Connector: "vault", Operation: "list", Bytes: 3 << 20, Cap: 1 << 20},
		ExternalConnectorUnavailable:      &pluginhost.StoppedError{Connector: "vault", Reason: "rotate did not answer within 2m0s"},
	} {
		coded := managedPluginExecuteError(args, err)
		if connectorErrorCode(coded) != want {
			t.Fatalf("%T: %v", err, coded)
		}
		if redact.Text(coded.Error()) != coded.Error() {
			t.Fatalf("%s: redaction rewrote %q", want, coded.Error())
		}
	}
	deadline := managedPluginExecuteError(args, &pluginhost.DeadlineError{Connector: "vault", Phase: "call", Operation: "rotate", Timeout: time.Minute, Effect: contract.EffectWrite})
	if !strings.Contains(deadline.Error(), "may have partly run") || refusalCodes[outcomeCode(deadline)] {
		t.Fatalf("a missed deadline: %v (a refusal: %v)", deadline, refusalCodes[outcomeCode(deadline)])
	}
	if refusalCodes[string(ExternalConnectorOutputTooLarge)] {
		t.Fatal("output_too_large is recorded as not having run, but the read ran")
	}
}

// Daemon start restores plugins in parallel, each bounded by its
// deadlines, so plugins that hang on Init cost one deadline, not the sum,
// and each missed deadline is recorded.
func TestRestoreIsBoundedAndParallel(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	configPath := filepath.Join(t.TempDir(), pluginhost.ConnectorConfigFilename)
	var st pluginConnectorPersistedState
	var cfg strings.Builder
	ids := []string{"hang1", "hang2", "hang3"}
	for _, id := range ids {
		dir := writeTestPluginDir(t, id)
		// Never answers init.
		if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), []byte("#!/bin/sh\nexec /bin/sleep 600\n"), 0o700); err != nil { //nolint:gosec // an entrypoint must be executable
			t.Fatal(err)
		}
		st.Entries = append(st.Entries, pluginConnectorPersistedEntry{PluginDir: dir, Loaded: true})
		fmt.Fprintf(&cfg, "%s:\n  limits:\n    init_timeout: 700ms\n", id)
	}
	if err := writePluginConnectorState(statePath, st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	start := time.Now()
	managed, err := NewManagedPluginConnectorService(sink, "test", nil, statePath, WithManagedPluginConnectorConfig(configPath))
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took > 1800*time.Millisecond {
		t.Fatalf("restore took %s: the three 700ms deadlines ran one after another", took)
	}
	var missed int
	for _, rec := range sink.Records() {
		if rec.Kind == audit.KindOutcome && rec.Connector == "plugin" && rec.Operation == "load" && strings.Contains(rec.OutcomeCode, "error") {
			missed++
		}
	}
	for _, id := range ids {
		if managed.manager.Loaded(id) {
			t.Fatalf("%s loaded", id)
		}
	}
	if missed != 3 {
		t.Fatalf("%d failed loads recorded, not 3", missed)
	}
}
