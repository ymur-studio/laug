package main

import (
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// stdlibOnly are this module's packages that link the standard library and the module's own packages, nothing else:
// the pool's, the gate-open check, the runner, the judge's, the summary tables' reader and the deny list's rule.
// TestStdlibOnly holds them and TestSDKFiles reads them as this package's own.
var stdlibOnly = []string{"internal/pool", "internal/poolapi", "internal/script", "internal/gateopen",
	"internal/jsonx", "internal/judge", "internal/rejudge", "internal/tolerance", "internal/tables", "internal/denylist"}

// renderer is the renderer's module, _report, its own go.mod under the tree's root: the module path it builds as and
// its directory from this package. go list reads one module at a time, so its packages are listed from there.
const (
	rendererModule = "/_report"
	rendererDir    = "../../_report"
)

// TestStdlibOnly holds the split: stdlibOnly's packages link the standard library and this module's own packages,
// nothing else, and so do the renderer's two packages in _report, its own module, which link this module's judge,
// rejudge, jsonx and tables through a replace directive. The Temporal SDK stays under internal/health and
// internal/orchestrator, which laug links to run the SDK's roles in its own process: TestSDKFiles holds which of this
// package's files import them. One go.mod serves the main module, so this test is what keeps an SDK import out of the
// pool, the runner, the judge, the tables and the figures. check, the shape pass's and the pages' tests, has only test
// files, so its tests' imports are listed (-test) and held the same way.
func TestStdlibOnly(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go list needs the go command: %v", err)
	}
	mod, err := exec.Command(gobin, "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	root := strings.TrimSpace(string(mod))
	format := "{{.ImportPath}} {{.Standard}} {{with .Module}}{{.Path}}{{end}}"
	var args []string
	for _, p := range stdlibOnly {
		args = append(args, "../../"+p)
	}
	out, err := exec.Command(gobin, append([]string{"list", "-deps", "-f", format}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	tests, err := exec.Command(gobin, "list", "-deps", "-test", "-f", format, "../../check").Output()
	if err != nil {
		t.Fatalf("go list -deps -test ../../check: %v", err)
	}
	// A test's own copy of a package lists as "<path> [<path>.test]": one field here.
	out = append(out, strings.ReplaceAll(string(tests), " [", "[")...)
	renderer := exec.Command(gobin, "list", "-deps", "-test", "-f", format, ".", "./cmd/report")
	renderer.Dir = rendererDir
	rendered, err := renderer.Output()
	if err != nil {
		t.Fatalf("go list -deps -test in %s: %v", rendererDir, err)
	}
	out = append(out, strings.ReplaceAll(string(rendered), " [", "[")...)
	own, rendererOwn := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[1] == "true":
		case len(f) == 3 && f[2] == root:
			own++
			for _, sdk := range []string{"/internal/health", "/internal/orchestrator"} {
				if f[0] == root+sdk {
					t.Errorf("%s depends on %s, which links the Temporal SDK", line, f[0])
				}
			}
		case len(f) == 3 && f[2] == root+rendererModule:
			rendererOwn++
		default:
			t.Errorf("not the standard library, %s or %s: %s", root, root+rendererModule, line)
		}
	}
	if own == 0 {
		t.Errorf("go list -deps named no package of %s, so it did not list this one:\n%s", root, out)
	}
	if rendererOwn == 0 {
		t.Errorf("go list -deps in %s named no package of %s, so it did not list the renderer", rendererDir,
			root+rendererModule)
	}
}

// sdkFiles are the files of this package that import a package outside the standard library and this module's
// standard-library packages: the two that mount the SDK's roles as verbs, health.go (internal/health) and
// orchestrator.go (internal/orchestrator).
var sdkFiles = []string{"health.go", "orchestrator.go"}

// TestSDKFiles holds that only sdkFiles import internal/health, internal/orchestrator or anything else that is
// neither the standard library nor one of this module's packages TestStdlibOnly holds, so the rest of the command
// line reads, and builds, as the standard library and laug's own code.
func TestSDKFiles(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go list needs the go command: %v", err)
	}
	mod, err := exec.Command(gobin, "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	root := strings.TrimSpace(string(mod))
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			rel, own := strings.CutPrefix(path, root+"/")
			if own && slices.Contains(stdlibOnly, rel) || !own && !strings.Contains(strings.Split(path, "/")[0], ".") {
				continue
			}
			got = append(got, p)
			if !own || rel != "internal/health" && rel != "internal/orchestrator" {
				t.Errorf("%s imports %s: the SDK reaches this package through internal/health and "+
					"internal/orchestrator alone", p, path)
			}
			break
		}
	}
	if !slices.Equal(got, sdkFiles) {
		t.Errorf("files importing the SDK's packages: %q, want %q", got, sdkFiles)
	}
}
