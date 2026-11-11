package obs

import (
	"os/exec"
	"strings"
	"testing"
)

// stdlibOnly are this module's packages, which link the standard library, this module's own packages and the main
// module's, nothing else: follow, cellstat, and cmd/obs, bin/obs. The main module's are taken through the replace
// in go.mod: internal/script and internal/gateopen today, and internal/pool through gateopen.
var stdlibOnly = []string{"./follow", "./cellstat", "./cmd/obs"}

// TestStdlibOnly holds the split, as cmd/laug/stdlib_test.go holds it for the main module: stdlibOnly's packages and
// their tests link the standard library, this module and the main module, and never the main module's
// internal/health or internal/orchestrator, which link the Temporal SDK.
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
	parent, ok := strings.CutSuffix(root, "/_obs")
	if !ok {
		t.Fatalf("go list -m: %q is not a module path ending /_obs", root)
	}
	format := "{{.ImportPath}} {{.Standard}} {{with .Module}}{{.Path}}{{end}}"
	out, err := exec.Command(gobin, append([]string{"list", "-deps", "-test", "-f", format}, stdlibOnly...)...).Output()
	if err != nil {
		t.Fatalf("go list -deps -test: %v", err)
	}
	// A test's own copy of a package lists as "<path> [<path>.test]": one field here.
	own, parents := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), " [", "[")), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[1] == "true":
		case len(f) == 3 && f[2] == root:
			own++
		case len(f) == 3 && f[2] == parent:
			parents++
			for _, sdk := range []string{"/internal/health", "/internal/orchestrator"} {
				if p, _, _ := strings.Cut(f[0], "["); p == parent+sdk {
					t.Errorf("%s depends on %s, which links the Temporal SDK", line, p)
				}
			}
		default:
			t.Errorf("not the standard library, %s or %s: %s", root, parent, line)
		}
	}
	if own == 0 || parents == 0 {
		t.Errorf("go list -deps named %d packages of %s and %d of %s, want some of each:\n%s", own, root, parents,
			parent, out)
	}
}
