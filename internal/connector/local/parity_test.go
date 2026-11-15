package local

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/domain"
)

func loadParityResource(t *testing.T, path string) *domain.Resource {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test fixtures
	if err != nil {
		t.Fatal(err)
	}
	var r config.ResourceDef
	if err := yaml.Unmarshal(raw, &r); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &domain.Resource{ID: r.ID, Name: r.Name, Type: domain.ResourceType(r.Type), ProjectID: r.Project, Connector: r.Connector, Config: r.Config}
}

// unitDiff names the first line where two units differ.
func unitDiff(want, got []byte) string {
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  old: %s\n  new: %s", i+1, wl, gl)
		}
	}
	return "identical"
}

// The new renderer writes, byte for byte, the unit the old one (main) writes for every
// descriptor shape: a workspace service, a custom unit name, run_from: artifact, an
// env_file, secret references fronted by run-secrets, a native unit and values that need
// quoting. A difference would restart a workload on the first apply after the handover.
func TestRendererMatchesTheOldBuild(t *testing.T) {
	env := RenderEnv{Home: "/u/op", Path: "/u/op/.local/bin:/usr/local/bin:/usr/bin:/bin", Cerberus: "/u/op/.local/bin/cerberus"}
	cases, err := filepath.Glob(filepath.Join("testdata", "parity", "*", "resource.yaml"))
	if err != nil || len(cases) < 7 {
		t.Fatalf("found %d parity cases: %v", len(cases), err)
	}
	for _, p := range cases {
		dir := filepath.Dir(p)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join(dir, "unit.golden")) //nolint:gosec // test fixtures
			if err != nil {
				t.Fatal(err)
			}
			nameLine, want, _ := bytes.Cut(golden, []byte("\n"))
			name, unit, err := RenderSystemdUnit(loadParityResource(t, p), env)
			if err != nil {
				t.Fatal(err)
			}
			if wantName := strings.TrimPrefix(string(nameLine), "# unit: "); name != wantName {
				t.Fatalf("unit name %q, old renderer %q", name, wantName)
			}
			if !bytes.Equal(unit, want) {
				t.Fatalf("unit differs from the old renderer's, %s", unitDiff(want, unit))
			}
		})
	}
}

// The renderer against the live host's own unit files, taken at rehearsal into
// $CERBERUS_PARITY_FIXTURES (testdata/parity/README.md). Never committed: they are the
// operator's.
func TestRendererMatchesRealUnits(t *testing.T) {
	dir := os.Getenv("CERBERUS_PARITY_FIXTURES")
	if dir == "" {
		t.Skip("CERBERUS_PARITY_FIXTURES is unset; the real-unit parity check runs at rehearsal")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "env.yaml")) //nolint:gosec // rehearsal fixtures
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		Home     string `yaml:"home"`
		Path     string `yaml:"path"`
		Cerberus string `yaml:"cerberus"`
	}
	if err := yaml.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	cases, _ := filepath.Glob(filepath.Join(dir, "*", "resource.yaml"))
	if len(cases) == 0 {
		t.Fatalf("no cases under %s", dir)
	}
	for _, p := range cases {
		c := filepath.Dir(p)
		t.Run(filepath.Base(c), func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(c, "unit")) //nolint:gosec // rehearsal fixtures
			if err != nil {
				t.Fatal(err)
			}
			wantName, _ := os.ReadFile(filepath.Join(c, "name")) //nolint:gosec // rehearsal fixtures
			name, unit, err := RenderSystemdUnit(loadParityResource(t, p), RenderEnv{Home: e.Home, Path: e.Path, Cerberus: e.Cerberus})
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.TrimSpace(string(wantName)); n != "" && n != name {
				t.Fatalf("unit name %q, on disk %q", name, n)
			}
			if !bytes.Equal(unit, want) {
				t.Fatalf("rendered unit differs from the one on disk, %s", unitDiff(want, unit))
			}
		})
	}
}
