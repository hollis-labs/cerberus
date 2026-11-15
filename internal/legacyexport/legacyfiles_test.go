package legacyexport

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// carried are the declarations legacyfiles.go carries from main, by name.
var carried = []string{
	"pluginStateVersion", "pluginConnectorPersistedState", "pluginConnectorPersistedEntry", "readPluginConnectorState",
	"PluginInstallOptions", "legacyInstallOptions", "mergeLegacyOptions",
}

// decls returns each named top-level declaration's source text, doc comment included.
func decls(t *testing.T, name string, src []byte) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	out := map[string]string{}
	text := func(doc *ast.CommentGroup, from, to token.Pos) string {
		if doc != nil {
			from = doc.Pos()
		}
		return string(src[fset.Position(from).Offset:fset.Position(to).Offset])
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				out[d.Name.Name] = text(d.Doc, d.Pos(), d.End())
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				var n string
				switch s := s.(type) {
				case *ast.TypeSpec:
					n = s.Name.Name
				case *ast.ValueSpec:
					n = s.Names[0].Name
				}
				out[n] = text(d.Doc, d.Pos(), d.End())
			}
		}
	}
	return out
}

// The decoder is old's, not a port: legacyfiles.go carries main's declarations character for
// character, as recorded in testdata; and, where this checkout has origin/main, the testdata is
// still main's text.
func TestCarriedDecoderIsMains(t *testing.T) {
	ours, err := os.ReadFile("legacyfiles.go")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile(filepath.Join("testdata", "plugin_connector_state.main.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	have, want := decls(t, "legacyfiles.go", ours), decls(t, "main", pinned)
	for _, n := range carried {
		if want[n] == "" {
			t.Fatalf("testdata lacks %s", n)
		}
		if have[n] != want[n] {
			t.Errorf("%s is not main's text:\n--- legacyfiles.go\n%s\n--- main\n%s", n, have[n], want[n])
		}
	}

	// The pinned text against main itself, when the ref is here (a CI checkout may not have it).
	show := func(path string) []byte {
		cmd := exec.CommandContext(context.Background(), "git", "show", "origin/main:"+path) //nolint:gosec // fixed paths of this test
		var out bytes.Buffer
		cmd.Stdout = &out
		if cmd.Run() != nil {
			return nil
		}
		return out.Bytes()
	}
	state, service := show("internal/cerbapi/plugin_connector_state.go"), show("internal/cerbapi/plugin_connector_service.go")
	if state == nil || service == nil {
		t.Log("origin/main is not available here; the pinned testdata was checked against legacyfiles.go only")
		return
	}
	main := decls(t, "plugin_connector_state.go", state)
	for n, v := range decls(t, "plugin_connector_service.go", service) {
		main[n] = v
	}
	for _, n := range carried {
		if main[n] != want[n] {
			t.Errorf("testdata's %s is no longer origin/main's text: re-carry it from main", n)
		}
	}
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Step 0: an unchanged file is not decoded; a changed one old accepts (an empty file, a version 1
// entry without an id, the legacy trust field) passes; one old refuses (a newer version, not
// JSON) stops the rollback.
func TestCheckLegacyFiles(t *testing.T) {
	root := t.TempDir()
	home, d3 := filepath.Join(root, "home"), filepath.Join(root, "d3")
	v2 := `{"version":2,"entries":[{"id":"echo","plugin_dir":"/p","options":{},"loaded":true}]}`
	writeFile(t, filepath.Join(home, pluginStateFile), v2)
	writeFile(t, filepath.Join(d3, D3HomeDir, pluginStateFile), v2)
	o := LegacyFilesOptions{OldHome: home, D3Copy: d3, Now: func() time.Time { return time.Unix(0, 0) }}

	r, err := CheckLegacyFiles(o)
	if err != nil || r.Result != ResultChecked || r.LegacyFiles.Changed || r.LegacyFiles.Decoded {
		t.Fatalf("unchanged: %+v %v", r.LegacyFiles, err)
	}
	for name, body := range map[string]string{
		"empty":         "",
		"v1 without id": `{"entries":[{"plugin_dir":"/p","options":{},"loaded":false,"trust":{"dev_mode":true,"catalog_signed":true}}]}`,
		"v2 with two":   `{"version":2,"entries":[{"id":"a","plugin_dir":"/a","options":{},"loaded":true},{"id":"b","plugin_dir":"/b","options":{"dev_mode":true},"loaded":false}]}`,
	} {
		writeFile(t, filepath.Join(home, pluginStateFile), body)
		r, cerr := CheckLegacyFiles(o)
		if cerr != nil || !r.LegacyFiles.Changed || !r.LegacyFiles.Decoded {
			t.Errorf("%s: %+v %v", name, r.LegacyFiles, cerr)
		}
	}
	writeFile(t, filepath.Join(home, pluginStateFile), `{"version":2,"entries":[{"id":"a","plugin_dir":"/a","options":{},"loaded":true},{"id":"b","plugin_dir":"/b","options":{"dev_mode":true},"loaded":false}]}`)
	state, err := readPluginConnectorState(filepath.Join(home, pluginStateFile))
	if err != nil || len(state.Entries) != 2 || !state.Entries[1].Options.DevMode {
		t.Fatalf("decoded %+v %v", state, err)
	}
	writeFile(t, filepath.Join(home, pluginStateFile), `{"entries":[{"plugin_dir":"/p","trust":{"dev_mode":true}}]}`)
	if state, err = readPluginConnectorState(filepath.Join(home, pluginStateFile)); err != nil || !state.Entries[0].Options.DevMode || state.Entries[0].LegacyTrust != nil {
		t.Fatalf("the legacy trust field is not folded: %+v %v", state, err)
	}
	for name, body := range map[string]string{"newer version": `{"version":3,"entries":[]}`, "not JSON": `{"entries":`} {
		writeFile(t, filepath.Join(home, pluginStateFile), body)
		r, err := CheckLegacyFiles(o)
		if err == nil || r.Result != ResultFailed || r.LegacyFiles.Decoded || r.LegacyFiles.Error == "" {
			t.Errorf("%s: %+v %v, want the rollback stopped", name, r.LegacyFiles, err)
		}
	}
	if _, err := CheckLegacyFiles(LegacyFilesOptions{OldHome: home, D3Copy: filepath.Join(root, "nowhere")}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a D3 copy without %s: %v", D3HomeDir, err)
	}
	if !strings.HasSuffix(filepath.Join(d3, D3HomeDir), "cerberus-home") {
		t.Fatal("the D3 copy's home directory")
	}
}
