package check

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestMakeTestSaysPassed: make test's last line says it passed, and in how many seconds, since make prints nothing
// when a target passes and harness-test's tally sits lines above lint's. make -n prints the recipes it would run, the
// test target's own last.
func TestMakeTestSaysPassed(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("no make on PATH")
	}
	cmd := exec.Command("make", "-n", "test")
	cmd.Dir = ".."
	cmd.Env = append(cmd.Environ(), "MAKEFLAGS=", "MAKELEVEL=")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("make -n test: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := lines[len(lines)-1]
	want := regexp.MustCompile(`^echo "laug · make test: passed, test-go, harness-test and lint, in \$\(\( \$\(date ` +
		`\+%s\) - [0-9]+ \)\) s"$`)
	if !want.MatchString(last) {
		t.Errorf("make -n test's last line:\n%s\nwant one that matches %s", last, want)
	}
}
