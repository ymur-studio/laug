package judge

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fusionPackages are the packages whose arithmetic reaches a verdict, a signature or a tolerance, where one rounding
// fewer could change a digit, from the repository root; fusionModule is _report, the renderer, its own module, whose
// figures are the same bytes on every host (svg.go), with its two packages. A tree without internal/tolerance or
// without _report skips it, and the test says so.
var (
	fusionPackages = []string{"internal/jsonx", "internal/judge", "internal/rejudge", "internal/tolerance"}
	fusionModule   = struct {
		dir      string
		patterns []string
	}{"_report", []string{".", "./cmd/report"}}
)

// TestNoFusableExpressions holds every float product in fusionPackages to an explicit conversion, float64(x*y). The
// Go spec lets an implementation fuse x*y+z into one operation with one rounding, across statements too, unless an
// explicit conversion rounds the product first (the Go spec, Floating-point operators). jq rounds every product. Go
// 1.26's arm64 compiler fuses and its amd64 compiler does not (seen: ARM64.rules makes FMADDD of FADDD and FMULD
// under useFMA; AMD64.rules has no such rule), so an unrounded product could change a verdict's digits on arm64
// alone, where no amd64 test sees it. Only a run on arm64 checks the result.
//
// The test type-checks each package's non-test files and fails on a float product that is not the operand of a
// conversion to a float type; on a float *=, which no conversion can round: write x = float64(x * y); and on any
// complex product, whose parts a compiler may fuse inside it. A product of constants is exact and passes.
func TestNoFusableExpressions(t *testing.T) {
	root := filepath.Join("..", "..")
	var patterns []string
	for _, p := range fusionPackages {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); errors.Is(err, fs.ErrNotExist) &&
			p == "internal/tolerance" {
			t.Logf("%s is not there: not checked", p)
			continue
		}
		patterns = append(patterns, "./"+p)
	}
	// Each module lists its own packages, with its own importer: go list reads one module at a time.
	type listing struct {
		pkgs []listed
		imp  func(*token.FileSet) types.Importer
	}
	pkgs, imp := listExport(t, root, patterns)
	lists := []listing{{pkgs, imp}}
	if _, err := os.Stat(filepath.Join(root, fusionModule.dir)); errors.Is(err, fs.ErrNotExist) {
		t.Logf("%s is not there: not checked", fusionModule.dir)
	} else {
		pkgs, imp := listExport(t, filepath.Join(root, fusionModule.dir), fusionModule.patterns)
		lists = append(lists, listing{pkgs, imp})
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	rounded, checked := 0, 0
	for _, l := range lists {
		pkgs, imp := l.pkgs, l.imp
		for _, p := range pkgs {
			fset := token.NewFileSet()
			var files []*ast.File
			for _, name := range p.GoFiles {
				path := filepath.Join(p.Dir, name)
				src, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				shown, err := filepath.Rel(abs, path) // positions from the repository root
				if err != nil {
					t.Fatal(err)
				}
				f, err := parser.ParseFile(fset, filepath.ToSlash(shown), src, parser.SkipObjectResolution)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, f)
			}
			bad, n, err := fusable(fset, p.ImportPath, files, imp(fset))
			if err != nil {
				t.Fatalf("%s: %v", p.ImportPath, err)
			}
			for _, b := range bad {
				t.Errorf("%s", b)
			}
			t.Logf("%s: files %d, float products %d, each rounded by a conversion", p.ImportPath, len(files), n)
			rounded += n
			checked++
		}
	}
	// times() in defs.go holds one, so a check that sees none did not type the code.
	if rounded == 0 {
		t.Errorf("no float product found in %d packages: the check did not see times() in defs.go", checked)
	}

	// The check itself, on code that fuses and code that does not.
	src := `package p

func f(x, y, z float64, i, j int, p *float64) {
	_ = x*y + z           // 1: fusable
	t := x * y            // 2: fusable across statements
	_ = t + z
	*p = x * y            // 3: fusable
	x *= y                // 4: a *= cannot be rounded
	_ = float64(x*y) + z  // rounded
	_ = float64((x * y))  // rounded
	_ = float32(x * y)    // rounded
	_ = i*j + 1           // integers
	_ = 2.5 * 4           // constants
	_ = x / y             // a quotient
	_ = float64(x*y*z)    // 5: the inner product x*y is not rounded
	_ = complex128(complex(x, y) * 2) // 6: a complex product
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	bad, n, err := fusable(fset, "p", []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, b := range bad {
		_, rest, _ := strings.Cut(b, "p.go:")
		line, _, _ := strings.Cut(rest, ":")
		lines = append(lines, line)
	}
	if got := strings.Join(lines, " "); got != "4 5 7 8 15 16" || n != 4 {
		t.Errorf("on the check's own code: fusable at lines %q, want \"4 5 7 8 15 16\"; %d rounded, want 4:\n%s",
			got, n, strings.Join(bad, "\n"))
	}
}

// fusable type-checks files, the package path, and names each float product that no float conversion rounds, each
// float *= and each complex product; n counts the rounded float products.
func fusable(fset *token.FileSet, path string, files []*ast.File, imp types.Importer) (bad []string, n int,
	err error) {
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(path, fset, files, info); err != nil {
		return nil, 0, err
	}
	// kind is e's float or complex kind, or 0 for any other type and for a constant, which is exact.
	kind := func(e ast.Expr) types.BasicInfo {
		tv, ok := info.Types[e]
		if !ok || tv.Value != nil {
			return 0
		}
		if b, ok := tv.Type.Underlying().(*types.Basic); ok {
			return b.Info() & (types.IsFloat | types.IsComplex)
		}
		return 0
	}
	// roundedBy says the product at the top of stack is the one operand of a conversion to a float type, parentheses
	// aside.
	roundedBy := func(stack []ast.Node) bool {
		i := len(stack) - 2
		for i >= 0 {
			if _, ok := stack[i].(*ast.ParenExpr); !ok {
				break
			}
			i--
		}
		if i < 0 {
			return false
		}
		call, ok := stack[i].(*ast.CallExpr)
		if !ok || len(call.Args) != 1 || !info.Types[call.Fun].IsType() {
			return false
		}
		b, ok := info.Types[call.Fun].Type.Underlying().(*types.Basic)
		return ok && b.Info()&types.IsFloat != 0
	}
	for _, f := range files {
		var stack []ast.Node
		ast.Inspect(f, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, node)
			switch e := node.(type) {
			case *ast.BinaryExpr:
				switch {
				case e.Op != token.MUL || kind(e) == 0:
				case kind(e) == types.IsComplex:
					bad = append(bad, fmt.Sprintf("%s: a complex product, whose parts a compiler may fuse whatever "+
						"the conversion", fset.Position(e.OpPos)))
				case roundedBy(stack):
					n++
				default:
					bad = append(bad, fmt.Sprintf("%s: a float product no conversion rounds, which a compiler may "+
						"fuse with an add: write float64(x * y)", fset.Position(e.OpPos)))
				}
			case *ast.AssignStmt:
				if e.Tok == token.MUL_ASSIGN && kind(e.Lhs[0]) != 0 {
					bad = append(bad, fmt.Sprintf("%s: a float *=, which no conversion can round: write "+
						"x = float64(x * y)", fset.Position(e.TokPos)))
				}
			}
			return true
		})
	}
	return bad, n, nil
}

// A listed package is what `go list -export -json` says of one.
type listed struct {
	ImportPath, Dir, Export string
	GoFiles                 []string
	DepOnly                 bool
}

// listExport lists the packages patterns name, from root, and gives an importer maker that reads each import from
// the export data `go list -export` builds or finds in the build cache.
func listExport(t *testing.T, root string, patterns []string) ([]listed, func(*token.FileSet) types.Importer) {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go list needs the go command: %v", err)
	}
	args := append([]string{"list", "-export", "-deps", "-json=ImportPath,Dir,Export,GoFiles,DepOnly"}, patterns...)
	cmd := exec.Command(gobin, args...)
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export: %v\n%s", err, stderr.String())
	}
	var pkgs []listed
	exports := map[string]string{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p listed
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("go list -export: %v", err)
		}
		exports[p.ImportPath] = p.Export
		if !p.DepOnly {
			pkgs = append(pkgs, p)
		}
	}
	if len(pkgs) != len(patterns) {
		t.Fatalf("go list -export named %d packages for %d patterns", len(pkgs), len(patterns))
	}
	return pkgs, func(fset *token.FileSet) types.Importer {
		return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
			file := exports[path]
			if file == "" {
				return nil, fmt.Errorf("go list -export gave no export data for %s", path)
			}
			return os.Open(file)
		})
	}
}
