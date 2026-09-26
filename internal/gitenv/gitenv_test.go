package gitenv

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A git command reads the directory it is given, whatever GIT_* variables
// the process carries, including ones no named list would know.
func TestCommandIgnoresEveryInheritedGitVariable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, other := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Gitenv Test", "-c", "user.email=gitenv@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-q", "--allow-empty", "-m", "one"}} {
		if out, err := Command(context.Background(), repo, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_SOMETHING_NEW", "1")
	for _, entry := range Env() {
		if strings.HasPrefix(entry, "GIT_") {
			t.Fatalf("Env kept %s", entry)
		}
	}
	out, err := Command(context.Background(), repo, "rev-parse", "HEAD").Output()
	if err != nil || len(strings.TrimSpace(string(out))) != 40 {
		t.Fatalf("read another repository: %q %v", out, err)
	}
}

// Nothing in the module outside this package builds a git exec.Cmd itself:
// exec.Command or exec.CommandContext whose program is the literal "git".
// Use Command, or Env and Binary where a caller needs the parts.
func TestNoBareGitCommands(t *testing.T) {
	root := moduleRoot(t)
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "dist", "vendor", "bin":
				return filepath.SkipDir
			}
			if path == self {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" {
				return true
			}
			program := -1
			switch sel.Sel.Name {
			case "Command":
				program = 0
			case "CommandContext":
				program = 1
			}
			if program < 0 || len(call.Args) <= program {
				return true
			}
			if lit, ok := call.Args[program].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if name, _ := strconv.Unquote(lit.Value); name == "git" {
					rel, _ := filepath.Rel(root, path)
					found = append(found, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("git run without gitenv (it inherits GIT_DIR under a hook); use gitenv.Command:\n  %s", strings.Join(found, "\n  "))
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
