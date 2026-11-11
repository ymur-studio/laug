// Package check holds tests of the tree and no other Go file: docs_test.go holds the pages check/docs.txt lists to
// their rules, and the other files hold the tree's results, records, Makefile, module and workflows to theirs. It
// links into nothing.
package check

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// modulePath is the module's path, as go.mod names it and every import of the tree's own packages starts.
const modulePath = "github.com/ymur-studio/laug"

// go.mod's module line is modulePath, and every package go list ./... names at the root is modulePath or under it, so
// no other module prefix, the placeholder's among them, is left in the tree.
func TestModulePath(t *testing.T) {
	var module []string
	gomod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(gomod), "\n") {
		if strings.HasPrefix(l, "module ") {
			module = append(module, l)
		}
	}
	if want := "module " + modulePath; len(module) != 1 || module[0] != want {
		t.Errorf("go.mod's module lines %q; want one, %q", module, want)
	}
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v %s", err, stderr.String())
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) == 0 {
		t.Fatal("go list ./... named no package")
	}
	for _, p := range pkgs {
		if p != modulePath && !strings.HasPrefix(p, modulePath+"/") {
			t.Errorf("go list ./... names %s, outside %s", p, modulePath)
		}
	}
}
