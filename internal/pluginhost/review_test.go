package pluginhost

import (
	"strings"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

func TestReviewReadsGapsStrictly(t *testing.T) {
	dir := writeBundle(t, map[string]string{"plugin.yaml": minimalPluginYAML("x"), "bin/plugin": "binary"})
	staged, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := BuildReview(staged, dir, OriginInstalled)
	text := r.Render()
	for _, want := range []string{
		"exec              1   wipe*",
		"declares no effect, read as exec",
		`operation "wipe" declares no effect: treated as exec`,
		"no telemetry declared for wipe",
		`operation "wipe" declares no output kind: read as free text`,
		"no host range declared",
		"Previews: none declared",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("review is missing %q:\n%s", want, text)
		}
	}
	if r.SummaryDigest() != BuildReview(staged, dir, OriginInstalled).SummaryDigest() {
		t.Fatal("the summary digest is not stable")
	}
}

func TestDiffNamesEveryDeclaredChange(t *testing.T) {
	base := Review{Version: "1", BundleDigest: "sha256:a", Operations: []ReviewOperation{{Name: "list", Effect: contract.EffectRead, Preview: contract.PreviewNone}}}
	if got := Diff(base, base); len(got) != 0 {
		t.Fatalf("no change: %v", got)
	}
	binary := base
	binary.BundleDigest = "sha256:b"
	if got := Diff(base, binary); len(got) != 1 || !strings.Contains(got[0], "files changed that the declarations do not describe") {
		t.Fatalf("binary only: %v", got)
	}
	next := base
	next.Operations = []ReviewOperation{{Name: "list", Effect: contract.EffectWrite, Preview: contract.PreviewPlugin}}
	next.Capabilities = []string{"ssh_agent"}
	next.Host = pluginsdk.HostRange{MinContract: 1}
	got := strings.Join(Diff(base, next), "\n")
	for _, want := range []string{"~ operation list effect read -> write", "~ operation list preview none -> plugin", "+ capability ssh_agent", "~ host contract undeclared -> >= 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff is missing %q:\n%s", want, got)
		}
	}
}

// A plugin narrowing itself is honored: a CLI-only operation is not exposed
// to MCP even when the operator's file lists it.
func TestCLIOnlyNarrowsMCPExposure(t *testing.T) {
	p := InstalledPlugin{ID: "x", Manifest: contract.Manifest{Operations: []contract.ManifestOperation{{Name: "list"}, {Name: "exec_pod"}}}}
	p.Spec.Cerberus.Surfaces.CLIOnly = []string{"exec_pod"}
	cfg := ConnectorConfig{Entries: map[string]PluginSettings{"x": {MCP: MCPSettings{Expose: []string{"list", "exec_pod"}}}}}
	got := cfg.ForPlugin(p)
	if strings.Join(got.Expose, ",") != "list" || len(got.Problems) != 0 || !strings.Contains(strings.Join(got.Warnings, "\n"), "CLI-only") {
		t.Fatalf("settings %+v", got)
	}
}
