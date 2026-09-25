package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

var hintFields = map[string]bool{"ReadOnlyHint": true, "DestructiveHint": true, "IdempotentHint": true, "OpenWorldHint": true}

// No tool annotation is written by hand anywhere in internal/mcp: not as a
// field in a Tool literal, not as an assignment. The one place hints are set
// is WithHints in hints.go, from the contract. A hand-written hint is how an
// annotation came to disagree with its operation's gate (Fix first item 8).
func TestNoHandWrittenHints(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok && hintFields[key.Name] {
					t.Errorf("%s: hand-written %s; derive it from the contract (contractTool / WithHints)", fset.Position(node.Pos()), key.Name)
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || !hintFields[sel.Sel.Name] {
						continue
					}
					if file == "hints.go" && enclosingFunc(f, node.Pos()) == "WithHints" {
						continue
					}
					t.Errorf("%s: %s assigned outside WithHints", fset.Position(node.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
}

func enclosingFunc(f *ast.File, pos token.Pos) string {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Pos() <= pos && pos <= fn.End() {
			return fn.Name.Name
		}
	}
	return ""
}

// Every tool the table binds is served, and every served tool is bound.
func TestToolBindingsMatchTheServedTools(t *testing.T) {
	served := map[string]bool{}
	for _, tool := range AllTools(nil) {
		if served[tool.Name] {
			t.Errorf("%s served twice", tool.Name)
		}
		served[tool.Name] = true
	}
	for name := range toolOperations {
		if !served[name] {
			t.Errorf("%s is bound in toolOperations but not served by AllTools", name)
		}
		if _, ok := ToolOperation(name); !ok {
			t.Errorf("%s is bound to an operation its definition does not declare", name)
		}
	}
}
