package hygiene

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every package with tests runs them under a scratch home (internal/testhome),
// so `go test ./...` never writes the operator's ~/.cerberus or XDG
// directories, whatever HOME the suite starts with (CW-20261114-0143: a
// pre-push run removed a live dev session's pid file). A new test package
// that forgets fails here, naming itself.
func TestEveryTestPackageRunsUnderAScratchHome(t *testing.T) {
	root := repoRoot(t)
	withTests := map[string]bool{}
	isolated := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "testdata", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		dir, _ := filepath.Rel(root, filepath.Dir(p))
		withTests[dir] = true
		src, err := os.ReadFile(p) //nolint:gosec // the repository's own files
		if err != nil {
			return err
		}
		s := string(src)
		if strings.Contains(s, "testhome.Main(") || strings.Contains(s, "testhome.Isolate(") ||
			(dir == filepath.Join("internal", "testhome") && strings.Contains(s, "func TestMain(m *testing.M) { Main(m) }")) {
			isolated[dir] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for dir := range withTests {
		if !isolated[dir] {
			missing = append(missing, dir)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these test packages do not run under a scratch home; add\n\n\tfunc TestMain(m *testing.M) { testhome.Main(m) }\n\n(or call testhome.Isolate first in an existing TestMain):\n  %s", strings.Join(missing, "\n  "))
	}
	if len(withTests) < 40 {
		t.Fatalf("found only %d test packages; the walk is missing some", len(withTests))
	}
}
