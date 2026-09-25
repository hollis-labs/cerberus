package pluginhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginsdk "github.com/hollis-labs/cerberus/pkg/plugin"
)

func writeBundle(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o600)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o700
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil { //nolint:gosec // test bundle; entrypoints need their exec bit
			t.Fatal(err)
		}
	}
	return dir
}

// The digest covers every file's path, content and executable bit, and
// nothing about where the bundle sits.
func TestBundleDigestCoversTheWholeBundle(t *testing.T) {
	files := map[string]string{"plugin.yaml": "id: x\n", "bin/plugin": "binary"}
	a, err := BundleDigest(writeBundle(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := BundleDigest(writeBundle(t, files)); b != a {
		t.Fatal("the same files digest differently in another directory")
	}
	for name, mutate := range map[string]func(dir string){
		"content":  func(dir string) { _ = os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte("id: y\n"), 0o600) },
		"exec bit": func(dir string) { _ = os.Chmod(filepath.Join(dir, "bin", "plugin"), 0o600) },
		"new file": func(dir string) { _ = os.WriteFile(filepath.Join(dir, "extra"), nil, 0o600) },
		"rename": func(dir string) {
			_ = os.Rename(filepath.Join(dir, "bin", "plugin"), filepath.Join(dir, "bin", "other"))
		},
	} {
		dir := writeBundle(t, files)
		mutate(dir)
		if got, _ := BundleDigest(dir); got == a {
			t.Errorf("%s: the digest did not change", name)
		}
	}
	dir := writeBundle(t, files)
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := BundleDigest(dir); err == nil {
		t.Fatal("a bundle with a symlink was digested")
	}
}

func TestCheckBundle(t *testing.T) {
	dir := writeBundle(t, map[string]string{"plugin.yaml": "id: x\n"})
	digest, _ := BundleDigest(dir)
	p := InstalledPlugin{ID: "x", Path: dir, BundleDigest: digest}
	if err := CheckBundle(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte("id: y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := CheckBundle(p)
	var changed *ChangedError
	if !errors.Is(err, ErrPluginChanged) || !errors.As(err, &changed) || changed.Found == digest {
		t.Fatalf("err = %v", err)
	}
	p.BundleDigest = "" // review pending: not compared
	if err := CheckBundle(p); err != nil {
		t.Fatalf("a pending plugin was checked: %v", err)
	}
	p.Spec.Cerberus.Host = pluginsdk.HostRange{MinContract: pluginsdk.ContractVersion + 1}
	if err := CheckBundle(p); err == nil || !strings.Contains(err.Error(), "upgrade Cerberus") {
		t.Fatalf("host range: %v", err)
	}
}

func TestStoreCommitAndDiscard(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "plugins")}
	src := writeBundle(t, map[string]string{"plugin.yaml": minimalPluginYAML("x"), "bin/plugin": "binary"})
	staged, err := store.Stage(src)
	if err != nil {
		t.Fatal(err)
	}
	final, err := store.Commit(staged)
	if err != nil {
		t.Fatal(err)
	}
	if final != store.Path("x", staged.Digest) || !store.Owns(final) || store.Owns(src) {
		t.Fatalf("final %s", final)
	}
	info, err := os.Stat(filepath.Join(final, "bin", "plugin"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("entrypoint mode %v (%v)", info.Mode(), err)
	}
	again, _ := store.Stage(src)
	if path, err := store.Commit(again); err != nil || path != final {
		t.Fatalf("recommit %s %v", path, err)
	}
	discard, _ := store.Stage(src)
	store.Discard(discard)
	if left, _ := filepath.Glob(filepath.Join(store.Root, ".staging-*")); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
	if err := store.Remove("../x"); err == nil {
		t.Fatal("removed outside the store")
	}
}

func minimalPluginYAML(id string) string {
	return `schema_version: "1"
id: ` + id + `
version: 1.0.0
protocol: plugin-sdk/subprocess
runtime: subprocess
entrypoint:
  command: bin/plugin
cerberus:
  connector:
    api_version: cerberus.connector/v1
    kind: Connector
    id: ` + id + `
    version: 1.0.0
    resource_types: [thing]
    operations:
      - name: list
        effect: read
        output: structured
        input_schema: {type: object}
      - name: wipe
        input_schema: {type: object}
`
}
