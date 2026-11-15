package hygiene

import (
	"bufio"
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// keyringModule is the OS credential store library: the macOS Keychain, the Windows Credential
// Manager or the Secret Service.
const keyringModule = "github.com/zalando/go-keyring"

const secretsPackage = "github.com/hollis-labs/cerberus/internal/secrets"

// goToolEnv is the toolchain's module and build caches, read before TestMain isolates the home:
// the scratch home holds no module cache, and go list must not download one.
var goToolEnv []string

// readGoToolEnv reads GOMODCACHE, GOCACHE and GOPATH from `go env` as the suite starts.
func readGoToolEnv() []string {
	out, err := exec.CommandContext(context.Background(), "go", "env", "GOMODCACHE", "GOCACHE", "GOPATH").Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return nil
	}
	return []string{"GOMODCACHE=" + lines[0], "GOCACHE=" + lines[1], "GOPATH=" + lines[2], "GOPROXY=off"}
}

// A test binary that links the keyring library, directly or through internal/secrets (or a
// library that wraps it), can reach the operator's real credential store. A scratch HOME does not
// protect a macOS Keychain, and a dead session bus protects only Linux. So every such package
// installs go-keyring's mock for the whole process in its TestMain, with
// secrets.MockStoreForTests(); a new package that links the library and forgets fails here,
// naming itself. The linking packages are found with `go list -test`, which sees the transitive
// imports as the test binary has them.
func TestEveryKeyringTestPackageMocksTheStore(t *testing.T) {
	root := repoRoot(t)
	if goToolEnv == nil {
		t.Fatal("go env could not be read before the home was isolated")
	}
	cmd := exec.CommandContext(context.Background(), "go", "list", "-test", "-f", "{{.ImportPath}}\t{{.Dir}}\t{{join .Deps \" \"}}", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), goToolEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list (%v): %v\n%s", goToolEnv, err, stderr.String())
	}
	dirs := map[string]string{}
	var linking []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), "\t", 3)
		if len(fields) != 3 {
			continue
		}
		path, dir, deps := fields[0], fields[1], " "+fields[2]+" "
		if !strings.Contains(path, " ") && dir != "" {
			dirs[path] = dir
		}
		if strings.HasSuffix(path, ".test") && strings.Contains(deps, " "+keyringModule+" ") {
			linking = append(linking, strings.TrimSuffix(path, ".test"))
		}
	}
	if err = sc.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(linking)
	if len(linking) < 5 {
		t.Fatalf("found only %d test packages linking %s; the listing is missing some: %v", len(linking), keyringModule, linking)
	}
	var missing []string
	for _, pkg := range linking {
		dir, ok := dirs[pkg]
		if !ok {
			missing = append(missing, pkg+" (no directory)")
			continue
		}
		if !testMainMocksTheStore(t, dir, pkg == secretsPackage) {
			rel, _ := filepath.Rel(root, dir)
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("these test packages link %s (directly or through internal/secrets) but their TestMain does not mock the OS credential store; call\n\n\tsecrets.MockStoreForTests()\n\nfirst in TestMain (keeping testhome), e.g.\n\n\tfunc TestMain(m *testing.M) { secrets.MockStoreForTests(); testhome.Main(m) }\n\n(a package internal/secrets imports puts its TestMain in an external _test package):\n  %s",
			keyringModule, strings.Join(missing, "\n  "))
	}
}

// testMainMocksTheStore reports whether a TestMain among dir's _test.go files calls
// secrets.MockStoreForTests (by whatever name the file imports internal/secrets under), or, in
// internal/secrets itself, MockStoreForTests.
func testMainMocksTheStore(t *testing.T, dir string, self bool) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		alias := ""
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == secretsPackage {
				alias = "secrets"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := fun.X.(*ast.Ident); ok && alias != "" && x.Name == alias && fun.Sel.Name == "MockStoreForTests" {
						found = true
					}
				case *ast.Ident:
					if self && fun.Name == "MockStoreForTests" {
						found = true
					}
				}
				return !found
			})
			if found {
				return true
			}
		}
	}
	return false
}
