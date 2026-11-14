package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// sandboxHome points HOME and the XDG roots at a temp dir for the test, so nothing here can
// read or write the real home. It returns the fake home.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, filepath.Join(home, k))
	}
	return home
}

// copyFixture copies testdata/<name> into a repo-like dir and returns its path.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // a fixture in this package
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "repo", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	return path
}

// A v1 descriptor registers through ForRoot and loads unchanged: the index lands at
// <adopt_root>/registry.yaml in the old format, and the resolved resources are the file's.
func TestForRootLoadsAV1DescriptorUnchanged(t *testing.T) {
	home := sandboxHome(t)
	adopt := filepath.Join(t.TempDir(), "adopt")
	desc := copyFixture(t, "sample.cerberus.yaml")
	before, err := os.ReadFile(desc) //nolint:gosec // the copied fixture
	if err != nil {
		t.Fatal(err)
	}

	reg := ForRoot(adopt)
	if got, want := reg.IndexPath(), filepath.Join(adopt, "registry.yaml"); got != want {
		t.Fatalf("IndexPath = %q, want %q", got, want)
	}
	entries, err := reg.Register(desc)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(entries) != 1 || entries[0].Owner != "sample-app" || entries[0].Kind != "cerberus-project/v1" || entries[0].Path != desc {
		t.Fatalf("entries = %+v", entries)
	}

	// The index is the old format: version 1 and pointer entries, nothing else.
	raw, err := os.ReadFile(filepath.Join(adopt, "registry.yaml")) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if yerr := yaml.Unmarshal(raw, &doc); yerr != nil {
		t.Fatal(yerr)
	}
	if doc["version"] != 1 || len(doc) != 2 {
		t.Fatalf("index keys = %v", doc)
	}
	idx, err := LoadIndex(filepath.Join(adopt, "registry.yaml"))
	if err != nil || len(idx.Entries) != 1 || idx.Entries[0].Path != desc {
		t.Fatalf("LoadIndex = %+v, %v", idx, err)
	}

	// The descriptor is read, never written.
	after, _ := os.ReadFile(desc) //nolint:gosec // the copied fixture
	if string(after) != string(before) {
		t.Fatal("registering changed the descriptor")
	}

	resolved, err := Resolve(reg.options())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var ids []string
	for _, r := range resolved.Config.Resources {
		ids = append(ids, r.ID)
	}
	if !reflect.DeepEqual(ids, []string{"sample-api", "sample-worker"}) {
		t.Fatalf("resolved resources = %v", ids)
	}
	if len(resolved.Skipped) != 0 {
		t.Fatalf("skipped = %+v", resolved.Skipped)
	}

	// Nothing was created under the (fake) home: ForRoot reads no home directory.
	if _, err := os.Stat(filepath.Join(home, ".cerberus")); err == nil {
		t.Fatal("ForRoot created ~/.cerberus")
	}
}

// An existing old-format index is read in place, and a register adds to it without losing
// the entries it already had (tether sysop reads the same file).
func TestForRootKeepsAnExistingIndex(t *testing.T) {
	sandboxHome(t)
	adopt := t.TempDir()
	other := filepath.Join(t.TempDir(), "other.cerberus.yaml")
	existing := "version: 1\nentries:\n  - owner: other-app\n    namespace: default\n    path: " + other +
		"\n    kind: cerberus-project/v1\n    registered_at: \"2026-01-02T03:04:05Z\"\n"
	if err := os.WriteFile(filepath.Join(adopt, "registry.yaml"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ForRoot(adopt).Register(copyFixture(t, "sample.cerberus.yaml")); err != nil {
		t.Fatal(err)
	}
	idx, err := LoadIndex(filepath.Join(adopt, "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var owners []string
	for _, e := range idx.Entries {
		owners = append(owners, e.Owner)
		if e.Owner == "other-app" && e.RegisteredAt != "2026-01-02T03:04:05Z" {
			t.Fatalf("existing entry rewritten: %+v", e)
		}
	}
	if !reflect.DeepEqual(owners, []string{"other-app", "sample-app"}) {
		t.Fatalf("owners = %v", owners)
	}
}

// Invalid descriptors are refused with the old messages, and nothing is written.
func TestForRootRefusesInvalidDescriptorsWithTheOldMessages(t *testing.T) {
	sandboxHome(t)
	data, err := os.ReadFile(filepath.Join("testdata", "sample.cerberus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, from, to, want string
	}{
		{"port zero", "port: 8080", "port: 0", "port is 0; omit the port field entirely for resources that do not listen on a TCP port"},
		{"bad owner", "owner: sample-app", "owner: Sample_App", `invalid value "Sample_App"; must be lowercase kebab-case`},
		{"wrong kind", "kind: cerberus-project/v1", "kind: cerberus-project/v9", "cerberus-project/v9"},
	} {
		t.Run(c.name, func(t *testing.T) {
			adopt := t.TempDir()
			path := filepath.Join(t.TempDir(), "bad.cerberus.yaml")
			if err := os.WriteFile(path, []byte(strings.Replace(string(data), c.from, c.to, 1)), 0o600); err != nil { //nolint:gosec // a test temp dir
				t.Fatal(err)
			}
			_, err := ForRoot(adopt).Register(path)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Register err = %v, want it to contain %q", err, c.want)
			}
			if _, err := os.Stat(filepath.Join(adopt, "registry.yaml")); err == nil {
				t.Fatal("a refused register wrote the index")
			}
		})
	}
}

// The retired central directory is <adopt_root>/projects.
func TestForRootRefusesTheRetiredDirectory(t *testing.T) {
	sandboxHome(t)
	adopt := t.TempDir()
	data, _ := os.ReadFile(filepath.Join("testdata", "sample.cerberus.yaml"))
	path := filepath.Join(adopt, "projects", "sample.cerberus.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	if _, err := ForRoot(adopt).Register(path); err == nil || !strings.Contains(err.Error(), "retired centralized directory") {
		t.Fatalf("Register err = %v, want the retired-directory refusal", err)
	}
}
