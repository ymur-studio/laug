package jsonx

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// needJQ is the path of jq 1.7.x, which the tests ending in JQ check jsonx against. Without one the test skips and
// says why; LAUG_TEST_SHELL=require turns the skip into a failure. jq --version prints jq-1.7 or jq-1.7.<n>.
func needJQ(t *testing.T) string {
	t.Helper()
	path := maybeJQ(t)
	if path == "" {
		t.Skip("no jq 1.7.x")
	}
	return path
}

// maybeJQ is needJQ for a test that checks jsonx's own table first and jq only when present: "" without one.
func maybeJQ(t *testing.T) string {
	t.Helper()
	why := ""
	path, err := exec.LookPath("jq")
	if err != nil {
		why = "no jq on PATH"
	} else if out, err := exec.Command(path, "--version").Output(); err != nil {
		why = "jq --version failed: " + err.Error()
	} else if v := strings.TrimSpace(string(out)); !regexp.MustCompile(`^jq-1\.7(\.|$)`).MatchString(v) {
		why = "jq is " + v + ", not 1.7.x"
	} else {
		t.Logf("checked against %s", v)
		return path
	}
	if os.Getenv("LAUG_TEST_SHELL") == "require" {
		t.Fatalf("LAUG_TEST_SHELL=require: %s", why)
	}
	t.Logf("not checked against jq: %s", why)
	return ""
}

// runJQ runs jq with args and stdin, and returns its stdout; it fails the test when jq exits other than 0.
func runJQ(t *testing.T, jq string, stdin []byte, args ...string) []byte {
	t.Helper()
	out, ok := tryJQ(jq, stdin, args...)
	if !ok {
		t.Fatalf("jq %s failed: %s", strings.Join(args, " "), out)
	}
	return out
}

// tryJQ runs jq and returns its stdout and whether it exited 0; on a failure, its stderr.
func tryJQ(jq string, stdin []byte, args ...string) ([]byte, bool) {
	cmd := exec.Command(jq, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stderr.Bytes(), false
	}
	return stdout.Bytes(), true
}
