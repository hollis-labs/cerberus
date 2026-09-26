package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
)

const reviewTestPluginYAML = `schema_version: "1"
id: widget
version: 1.0.0
protocol: plugin-sdk/subprocess
runtime: subprocess
entrypoint:
  command: bin/plugin
cerberus:
  connector:
    api_version: cerberus.connector/v1
    kind: Connector
    id: widget
    version: 1.0.0
    resource_types: [thing]
    operations:
      - name: list
        effect: read
        output: structured
        input_schema: {type: object}
`

// reviewFixture points the review commands at a scratch state file and a
// fake daemon, and returns the plugin source, the state path and the ids the
// daemon was asked to reload.
func reviewFixture(t *testing.T, terminal bool, reloadErr error) (string, string, *[]string) {
	t.Helper()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "bin", "plugin"), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test entrypoint
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "plugin.yaml"), []byte(reviewTestPluginYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	var reloaded []string
	oldTerm, oldReviewer, oldReload := pluginReviewIsTerminal, newPluginReviewer, reloadManagedPlugin
	pluginReviewIsTerminal = func() bool { return terminal }
	newPluginReviewer = func() (*cerbapi.PluginReviewer, error) {
		return cerbapi.NewPluginReviewer(audit.NewMemory(), statePath), nil
	}
	reloadManagedPlugin = func(_ context.Context, id string) (cerbapi.ManagedPluginConnectorState, error) {
		reloaded = append(reloaded, id)
		return cerbapi.ManagedPluginConnectorState{ID: id}, reloadErr
	}
	t.Cleanup(func() {
		pluginReviewIsTerminal, newPluginReviewer, reloadManagedPlugin = oldTerm, oldReviewer, oldReload
	})
	connectorsPluginDev = false
	return src, statePath, &reloaded
}

func runInstall(t *testing.T, stdin, dir string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := connectorsPluginManagedInstallCmd
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetContext(context.Background())
	t.Cleanup(func() { cmd.SetOut(nil); cmd.SetIn(nil) })
	err := cmd.RunE(cmd, []string{dir})
	return out.String(), err
}

func TestPluginInstallIsAnInteractiveReview(t *testing.T) {
	t.Run("refused without a terminal", func(t *testing.T) {
		src, statePath, reloaded := reviewFixture(t, false, nil)
		_, err := runInstall(t, "widget\n", src)
		if !errors.Is(err, errPluginReviewNotInteractive) || len(*reloaded) != 0 {
			t.Fatalf("err = %v, reloaded %v", err, *reloaded)
		}
		if got := redact.Text(err.Error()); got != err.Error() {
			t.Fatalf("redaction rewrote the refusal: %q", got)
		}
		if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
			t.Fatal("a refused install wrote state")
		}
		connectorsPluginAcceptChanges = true
		defer func() { connectorsPluginAcceptChanges = false }()
		if err := connectorsPluginManagedLoadCmd.RunE(connectorsPluginManagedLoadCmd, []string{"widget"}); !errors.Is(err, errPluginReviewNotInteractive) {
			t.Fatalf("load --accept-changes without a terminal: %v", err)
		}
	})
	t.Run("a wrong confirmation installs nothing", func(t *testing.T) {
		src, statePath, reloaded := reviewFixture(t, true, nil)
		out, err := runInstall(t, "yes\n", src)
		if err == nil || len(*reloaded) != 0 || !strings.Contains(out, "Type the plugin id (widget)") {
			t.Fatalf("err = %v, reloaded %v, out:\n%s", err, *reloaded, out)
		}
		data, _ := os.ReadFile(statePath) //nolint:gosec // test state file
		if strings.Contains(string(data), "widget") {
			t.Fatal("a declined install wrote an entry")
		}
	})
	t.Run("the typed id installs and reloads", func(t *testing.T) {
		src, statePath, reloaded := reviewFixture(t, true, nil)
		out, err := runInstall(t, "widget\n", src)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Plugin: widget 1.0.0", "Operations (1)", "Accepted widget 1.0.0", "registered widget"} {
			if !strings.Contains(out, want) {
				t.Errorf("output is missing %q:\n%s", want, out)
			}
		}
		if strings.Join(*reloaded, ",") != "widget" {
			t.Fatalf("reloaded %v", *reloaded)
		}
		data, _ := os.ReadFile(statePath) //nolint:gosec // test state file
		if !strings.Contains(string(data), `"bundle_digest"`) {
			t.Fatalf("state:\n%s", data)
		}
		out, err = runInstall(t, "widget\n", src)
		if err != nil || !strings.Contains(out, "already installed") {
			t.Fatalf("second install: %q (%v)", out, err)
		}
	})
	t.Run("a stopped daemon picks it up later", func(t *testing.T) {
		src, _, _ := reviewFixture(t, true, &cerbapi.DaemonUnreachableError{Err: errors.New("dial")})
		out, err := runInstall(t, "widget\n", src)
		if err != nil || !strings.Contains(out, "daemon is not running") {
			t.Fatalf("%q (%v)", out, err)
		}
	})
}

// install --yes needs no terminal and no typed id, but only under the
// permissive posture; under secure it is refused with the command that
// changes that, and installs nothing.
func TestPluginInstallYesNeedsThePermissivePosture(t *testing.T) {
	src, statePath, reloaded := reviewFixture(t, false, nil)
	connectorsPluginYes = true
	oldPosture := currentPosture
	t.Cleanup(func() { connectorsPluginYes, currentPosture = false, oldPosture; cerbapi.SetPolicyDecisionPoint(nil) })

	currentPosture = func() policy.PostureSummary { return policy.PostureSummary{Global: policy.PostureSecure} }
	_, err := runInstall(t, "", src)
	if !errors.Is(err, cerbapi.ErrPluginYesNeedsPermissive) || len(*reloaded) != 0 {
		t.Fatalf("--yes under secure: %v, reloaded %v", err, *reloaded)
	}
	if got := redact.Text(err.Error()); got != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", got)
	}
	if _, statErr := os.Stat(statePath); !os.IsNotExist(statErr) {
		t.Fatal("a refused --yes wrote state")
	}

	permissive := policy.File{Version: policy.FileVersion, Posture: policy.PosturePermissive}
	currentPosture = func() policy.PostureSummary { return permissive.PostureSummary("test") }
	cerbapi.SetPolicyDecisionPoint(policy.NewEvaluator(permissive, "test"))
	out, err := runInstall(t, "", src)
	if err != nil || !strings.Contains(out, "Plugin: widget") || !strings.Contains(out, "recorded as unattended") || len(*reloaded) != 1 {
		t.Fatalf("--yes under permissive: %v, reloaded %v\n%s", err, *reloaded, out)
	}
}
