// Package legacyimporttest materializes the old-tree fixture (internal/legacyimport/testdata,
// written on main by oldtree_gen_on_main.go.txt) into a scratch directory for a test: the
// snapshot, the main.db backup, the registered descriptor at a path of the scratch directory,
// and the old anchor in the (mocked) keychain under the account the old build used.
package legacyimporttest

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// GenRoot is the directory the generator ran in; the fixture's recorded paths start with it.
const GenRoot = "/tmp/oldtree-gen"

// Fixture is one materialized old tree. Every path is under Root.
type Fixture struct {
	Root string
	// Home is the scratch home: its .cerberus and .local/share/cerberus are the "live" old roots.
	Home string
	// OldHome is the snapshot (a copy of the old ~/.cerberus), OldDB the main.db backup.
	OldHome string
	OldDB   string
	// Account is the keychain account the old anchor is under (the live tree's audit dir).
	Account string
	// AnchorRaw is the anchor's keychain value.
	AnchorRaw string
	// DataDir, DBPath and ConfigDir are the targets.
	DataDir   string
	DBPath    string
	ConfigDir string
}

// Testdata is internal/legacyimport/testdata.
func Testdata() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filepath.Dir(file)), "testdata")
}

// Materialize copies the fixture into root (the snapshot at root/snap/.cerberus unless
// oldHome is set) and seeds the old anchor. The keychain must be mocked
// (secrets.MockStoreForTests).
func Materialize(t testing.TB, root string) Fixture {
	t.Helper()
	f := Fixture{Root: root, Home: filepath.Join(root, "user")}
	f.OldHome = filepath.Join(root, "snap", ".cerberus")
	f.OldDB = filepath.Join(root, "backup", "main.db")
	f.Account = filepath.Join(f.Home, ".cerberus", "audit")
	f.DataDir = filepath.Join(f.Home, ".local", "share", "cerberus-substrate")
	f.DBPath = filepath.Join(f.DataDir, "cerberus.db")
	f.ConfigDir = filepath.Join(f.Home, ".config", "cerberus")
	CopyTree(t, f.OldHome)
	td := Testdata()
	must(t, os.MkdirAll(filepath.Dir(f.OldDB), 0o700))
	copyFile(t, filepath.Join(td, "oldmain.db"), f.OldDB, 0o600)
	raw, err := os.ReadFile(filepath.Join(td, "oldanchor.json")) //nolint:gosec // the fixture
	must(t, err)
	f.AnchorRaw = strings.TrimSpace(string(raw))
	must(t, secrets.KeyringSet(auditchain.AnchorService, f.Account, f.AnchorRaw))
	return f
}

// CopyTree writes the fixture's old tree at dst (files 0600, directories 0700, the app binary
// 0700), with its empty directories and the registry pointing at a copy of the descriptor
// beside dst.
func CopyTree(t testing.TB, dst string) {
	t.Helper()
	td := Testdata()
	src := filepath.Join(td, "oldtree")
	must(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		mode := os.FileMode(0o600)
		if strings.Contains(rel, string(filepath.Separator)+"bin"+string(filepath.Separator)) {
			mode = 0o700
		}
		copyFile(t, p, target, mode)
		return nil
	}))
	empty, err := os.ReadFile(filepath.Join(td, "oldtree.emptydirs")) //nolint:gosec // the fixture
	must(t, err)
	for _, rel := range strings.Fields(string(empty)) {
		must(t, os.MkdirAll(filepath.Join(dst, rel), 0o700))
	}
	apps := filepath.Join(filepath.Dir(dst), "apps")
	copyFile(t, filepath.Join(td, "descriptors", "shop.cerberus.yaml"), filepath.Join(apps, "shop", "shop.cerberus.yaml"), 0o600)
	reg := filepath.Join(dst, "registry.yaml")
	data, err := os.ReadFile(reg) //nolint:gosec // the copy
	must(t, err)
	must(t, os.WriteFile(reg, bytes.ReplaceAll(data, []byte(GenRoot+"/apps"), []byte(apps)), 0o600))
}

func copyFile(t testing.TB, src, dst string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(src) //nolint:gosec // the fixture
	must(t, err)
	must(t, os.MkdirAll(filepath.Dir(dst), 0o700))
	must(t, os.WriteFile(dst, data, mode))
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
