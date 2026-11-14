package testhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { Main(m) }

// Under Main every home variable points inside one scratch directory, which is
// not the home the process started with.
func TestIsolateRedirectsEveryHomeVariable(t *testing.T) {
	home := os.Getenv("HOME")
	if !strings.Contains(home, "cerb-test-home-") {
		t.Fatalf("HOME = %q, not a scratch home", home)
	}
	scratch := filepath.Dir(home)
	for _, k := range Vars {
		v := os.Getenv(k)
		if !strings.Contains(v, scratch) {
			t.Errorf("%s = %q, outside the scratch home %s", k, v, scratch)
		}
	}
	if fi, err := os.Stat(home); err != nil || fi.Mode().Perm() != 0o700 { //nolint:gosec // the scratch home this package made
		t.Fatalf("scratch home: %v %v", fi, err)
	}
	if got, _ := os.UserHomeDir(); got != home {
		t.Fatalf("os.UserHomeDir() = %q, want the scratch home", got)
	}
}

// Isolate's cleanup removes the scratch home.
func TestIsolateCleansUp(t *testing.T) {
	saved := map[string]string{}
	for _, k := range Vars {
		saved[k] = os.Getenv(k)
	}
	t.Cleanup(func() {
		for k, v := range saved {
			_ = os.Setenv(k, v)
		}
	})
	cleanup, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(os.Getenv("HOME"))
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) { //nolint:gosec // the scratch dir this test made
		t.Fatalf("scratch dir still there: %v", err)
	}
}
