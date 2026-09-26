package cerbapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

func withPosture(t *testing.T, file policy.File) {
	t.Helper()
	file.Version = policy.FileVersion
	withPDP(t, policy.NewEvaluator(file, "test"))
}

// install --yes accepts without the typed confirmation only under a global
// permissive posture, and says so in the review record. Under secure, or
// for a kind of review that is not an install, it refuses and changes
// nothing.
func TestUnattendedInstallNeedsThePermissivePosture(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	sink := audit.NewMemory()
	r := NewPluginReviewer(sink, statePath)
	source := reviewablePluginDir(t, "docker", nil)

	for name, file := range map[string]policy.File{
		"secure": {},
		"a scoped rule is not a host-wide switch": {PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "dev"}, Posture: policy.PosturePermissive}}},
	} {
		withPosture(t, file)
		pending, err := r.PrepareInstall(inProcess(), source, false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.AcceptUnattended(inProcess(), pending)
		if connectorErrorCode(err) != ExternalConnectorAckRequired || !strings.Contains(err.Error(), "only under the permissive posture") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if got := redact.Text(err.Error()); got != err.Error() {
			t.Fatalf("%s: redaction rewrote the refusal: %q", name, got)
		}
		if st, _ := readPluginConnectorState(statePath); len(st.Entries) != 0 {
			t.Fatalf("%s: a refused --yes installed %+v", name, st.Entries)
		}
	}

	withPosture(t, policy.File{Posture: policy.PosturePermissive})
	pending, err := r.PrepareInstall(inProcess(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.AcceptUnattended(inProcess(), pending)
	if err != nil || state.ID != "docker" {
		t.Fatalf("unattended install: %+v, %v", state, err)
	}
	var review *audit.Record
	for _, rec := range sink.Records() {
		if rec.Kind == audit.KindIntent && rec.Operation == ReviewInstall && rec.PluginReview != nil {
			rec := rec
			review = &rec
		}
	}
	if review == nil || !review.PluginReview.Unattended || !review.Acknowledged || review.Posture != policy.PosturePermissive {
		t.Fatalf("the review record does not say unattended under permissive: %+v", review)
	}
}

// A changed bundle is refused under secure, and under a global permissive
// posture loads with a warning and an audit record that names both digests.
func TestChangedBundleLoadsWithAWarningUnderPermissive(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	sink := audit.NewMemory()
	state := reviewInstall(t, sink, statePath, reviewablePluginDir(t, "docker", nil))
	var stderr bytes.Buffer
	managed, err := NewManagedPluginConnectorService(sink, "test", &stderr, statePath)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := pluginhost.ReadPluginYAML(state.Path)
	if err != nil {
		t.Fatal(err)
	}
	spec.Cerberus.Connector.Operations[0].Effect = contract.EffectExec
	writePluginYAML(t, state.Path, spec)

	withPosture(t, policy.File{PostureRules: []policy.PostureRule{{Match: policy.TargetMatch{Env: "dev"}, Posture: policy.PosturePermissive}}})
	if _, err = managed.Load(context.Background(), "docker"); connectorErrorCode(err) != ExternalConnectorPluginChanged {
		t.Fatalf("a scoped permissive rule must not relax the changed-bundle refusal: %v", err)
	}

	withPosture(t, policy.File{Posture: policy.PosturePermissive})
	if _, err = managed.Load(context.Background(), "docker"); err != nil {
		t.Fatalf("a changed bundle under permissive: %v", err)
	}
	defer func() { _, _ = managed.Unload(context.Background(), "docker") }()
	if !managed.Loaded("docker") {
		t.Fatal("not loaded")
	}
	if !strings.Contains(stderr.String(), "WARNING: plugin \"docker\" is not the bundle you reviewed") {
		t.Fatalf("no warning: %s", stderr.String())
	}
	found := false
	for _, rec := range sink.Records() {
		if rec.Kind == audit.KindOutcome && rec.OutcomeCode == PluginChangedAcceptedByPosture {
			found = rec.Target.Fields["id"] == "docker" && rec.Target.Fields["accepted"] == state.BundleDigest &&
				rec.Target.Fields["found"] != "" && rec.Posture == policy.PosturePermissive
		}
	}
	if !found {
		t.Fatalf("no %s record naming both digests: %+v", PluginChangedAcceptedByPosture, sink.Records())
	}
}

// Under permissive the changed-bundle load still needs its record: an
// unwritable log refuses it.
func TestChangedBundleUnderPermissiveNeedsItsRecord(t *testing.T) {
	t.Setenv("GO_WANT_PLUGIN_CONNECTOR_API_HELPER", "1")
	statePath := filepath.Join(t.TempDir(), "plugin-connectors.json")
	state := reviewInstall(t, audit.NewMemory(), statePath, reviewablePluginDir(t, "docker", nil))
	managed, err := NewManagedPluginConnectorService(audit.NewMemory(), "test", nil, statePath)
	if err != nil {
		t.Fatal(err)
	}
	managed.audit = audit.Failing{}
	if err = os.WriteFile(filepath.Join(state.Path, "extra.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	withPosture(t, policy.File{Posture: policy.PosturePermissive})
	if managed.acceptChangedBundle(&pluginhost.ChangedError{ID: "docker", Accepted: state.BundleDigest, Found: "sha256:x"}) {
		t.Fatal("a changed bundle was accepted with no audit record")
	}
}
