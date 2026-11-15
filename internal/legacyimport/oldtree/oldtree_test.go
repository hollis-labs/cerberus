package oldtree_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/legacyimport/oldtree"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) {
	secrets.MockStoreForTests()
	testhome.Main(m)
}

// The prefix proof hashes exactly the first n bytes, and a file shorter than n is an error,
// never the digest of what is there.
func TestPrefixSHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := oldtree.PrefixSHA256(p, 3)
	if err != nil || got != oldtree.BytesSHA256([]byte("abc")) {
		t.Fatalf("prefix 3 = %s %v", got, err)
	}
	whole, _ := oldtree.FileSHA256(p)
	if got, err := oldtree.PrefixSHA256(p, 6); err != nil || got != whole {
		t.Fatalf("prefix 6 = %s %v, want the file's %s", got, err, whole)
	}
	if got, err := oldtree.PrefixSHA256(p, 0); err != nil || got != oldtree.BytesSHA256(nil) {
		t.Fatalf("prefix 0 = %s %v", got, err)
	}
	if _, err := oldtree.PrefixSHA256(p, 7); err == nil {
		t.Fatal("a prefix longer than the file hashed")
	}
}

func TestPauseFile(t *testing.T) {
	if oldtree.PauseFile(storage.AllTargets) != "paused" || oldtree.PauseFile("web") != "paused.web" {
		t.Fatal("pause file names")
	}
}
