package daemon

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestProductionRecordEventNamesBelongToCanonicalCatalog(t *testing.T) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve lifecycle contract test path")
	}
	daemonDir := filepath.Dir(currentFile)
	repoRoot := filepath.Clean(filepath.Join(daemonDir, "..", ".."))
	data, err := os.ReadFile(filepath.Join(repoRoot, "protocol", "events", "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Types []struct {
			Name string `json:"name"`
		} `json:"types"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool, len(catalog.Types))
	for _, event := range catalog.Types {
		known[event.Name] = true
	}

	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, daemonDir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	recorders := map[string]bool{
		"record": true, "recordChecked": true, "recordStrict": true, "RecordEventWithCursor": true,
	}
	for _, pkg := range packages {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !recorders[selector.Sel.Name] {
					return true
				}
				literal, ok := call.Args[1].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				name, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Errorf("%s:%d invalid event literal: %v", path, fset.Position(literal.Pos()).Line, err)
					return true
				}
				if name == "TaskCreated" || name == "ExecutionStatusChanged" {
					t.Errorf("%s:%d emits forbidden legacy lifecycle event %q", path, fset.Position(literal.Pos()).Line, name)
				} else if !known[name] {
					t.Errorf("%s:%d emits event %q outside protocol/events/events.json", path, fset.Position(literal.Pos()).Line, name)
				}
				return true
			})
		}
	}
}
