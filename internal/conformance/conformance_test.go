// Package conformance_test holds every Cerberus operation — built-in
// connectors, the runtime's own operations, the disabled namecheap writes
// and our plugins' manifests — to the contract conformance suite.
package conformance_test

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	forgeconn "github.com/hollis-labs/cerberus/internal/connector/forge"
	ghconn "github.com/hollis-labs/cerberus/internal/connector/github"
	ncconn "github.com/hollis-labs/cerberus/internal/connector/namecheap"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/connector/conformance"
)

func builtins() []contract.Definition {
	defs := []contract.Definition{
		dockerconn.Definition(), forgeconn.Definition(),
		ghconn.Definition(), ncconn.Definition(), sshconn.Definition(),
	}
	return append(defs, cerbapi.RuntimeDefinitions()...)
}

func TestBuiltinOperationsConform(t *testing.T) {
	for _, def := range builtins() {
		if problems := conformance.Definition(def); len(problems) > 0 {
			t.Errorf("%s:\n  %s", def.ID, conformance.Report(problems))
		}
	}
	for _, op := range ncconn.DisabledOperations() {
		if problems := conformance.Operation(op); len(problems) > 0 {
			t.Errorf("namecheap disabled:\n  %s", conformance.Report(problems))
		}
		if err := op.Validate(); err != nil {
			t.Errorf("namecheap disabled: %v", err)
		}
	}
}

// The installed plugins' manifests, copied from cerberus-plugins' dist as
// fixtures (testdata/plugins). They declare effects since P1-6a; a manifest
// that loses one fails here as a contract gap.
func TestPluginManifestsConform(t *testing.T) {
	paths, err := filepath.Glob("testdata/plugins/*.plugin.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no plugin fixtures: %v", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // a testdata fixture from the glob above
		if err != nil {
			t.Fatal(err)
		}
		var spec pluginhost.PluginYAML
		if err := yaml.Unmarshal(data, &spec); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(spec.Cerberus.Connector.Operations) == 0 {
			t.Fatalf("%s parsed with no operations; the fixture or its shape changed", path)
		}
		if problems := conformance.Manifest(spec.Cerberus.Connector); len(problems) > 0 {
			t.Errorf("%s:\n  %s", filepath.Base(path), conformance.Report(problems))
		}
	}
}

// The suite catches what it claims to: a missing effect, an ack that
// disagrees with the effect, a preview flag without a preview, a schema that
// advertises a key the table refuses.
func TestConformanceCatchesBrokenContracts(t *testing.T) {
	good := contract.Operation{
		Name: "op", Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: "x"},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
		Inputs: []contract.Input{contract.RequiredField("id", contract.StringSchema("ID."))},
	}
	if problems := conformance.Operation(good); len(problems) > 0 {
		t.Fatalf("a good contract failed:\n  %s", conformance.Report(problems))
	}
	gap := contract.Manifest{APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "p", Version: "1", ResourceTypes: []string{"x"},
		Operations: []contract.ManifestOperation{{Name: "list", InputSchema: contract.ObjectSchema(map[string]any{})}}}
	if problems := conformance.Manifest(gap); len(problems) == 0 {
		t.Fatal("a manifest with no effect conformed")
	}
	// A schema that advertises more than the key table accepts. Finalize
	// rebuilds a built-in's schema from its inputs, so drift can only come
	// from a plugin whose schema is open while its table is not.
	open := contract.Operation{Name: "open", Effect: contract.EffectRead, InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}
	open = open.Finalize()
	open.InputsOpen = false
	if problems := conformance.Operation(open); len(problems) == 0 {
		t.Fatal("an open schema over a closed key table conformed")
	}
}
