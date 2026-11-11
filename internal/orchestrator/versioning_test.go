package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoGetVersion holds what two pages say. docs/DESIGN.md, section 8: "Gate has no workflow.GetVersion branch, by
// choice." This directory's README.md, under Extend: "Gate has no workflow.GetVersion branch." Instead, a build that
// changes Gate's commands waits for the open gates to end, or ends them. Before it polls, the worker replays each open
// gate on this build and refuses on a mismatch (replay.go). The test parses every non-test .go file here. It fails on
// each use of GetVersion from go.temporal.io/sdk/workflow, called or not: as workflow.GetVersion, on an alias of the
// package, or bare after a dot import. A commit that adds a GetVersion branch changes both pages and this test.
func TestNoGetVersion(t *testing.T) {
	const sdk = `"go.temporal.io/sdk/workflow"`
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files, imports := 0, 0
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files++
		names := map[string]bool{} // what this file calls the package: workflow, an alias, or . for a dot import
		for _, im := range f.Imports {
			if im.Path.Value != sdk {
				continue
			}
			imports++
			switch {
			case im.Name == nil:
				names["workflow"] = true
			case im.Name.Name != "_":
				names[im.Name.Name] = true
			}
		}
		sels := map[*ast.Ident]bool{} // the right side of each selector, which is not a bare name
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				sels[n.Sel] = true
				if x, ok := n.X.(*ast.Ident); ok && names[x.Name] && n.Sel.Name == "GetVersion" {
					t.Errorf("%s: %s.GetVersion, a GetVersion branch: DESIGN and README.md say Gate has none",
						fset.Position(n.Pos()), x.Name)
				}
			case *ast.Ident:
				if names["."] && !sels[n] && n.Name == "GetVersion" {
					t.Errorf("%s: GetVersion after a dot import, a GetVersion branch: DESIGN and README.md say Gate "+
						"has none", fset.Position(n.Pos()))
				}
			}
			return true
		})
	}
	// gate.go imports the package, so a scan that sees no import of it did not read Gate's code.
	if imports == 0 {
		t.Fatalf("%d files read, none imports %s: the scan did not read gate.go", files, sdk)
	}
	t.Logf("%d files read, %d imports of %s", files, imports, sdk)
}
