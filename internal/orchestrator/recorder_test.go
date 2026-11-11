package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/denylist"
)

// readDeny reads -denylist by internal/denylist's rule, a relative path from -repo wherever the recorder runs, as
// laug passes LAUG_DENYLIST's value; a list that is not there, or none named, gives no rules and no error; a refused
// line names the list as given and the line, never its text. The lists are planted.
func TestReadDeny(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(t.TempDir())
	writeFile(t, filepath.Join(repo, "lists/words.txt"), "# planted\nzorblax\r\n\nre:qx7[0-9]\n")
	l, err := readDeny(repo, "lists/words.txt")
	if err != nil || l.Summary() != "1 plain line and 1 re: line" || l.Rules[0].Line != 2 || l.Rules[1].Line != 4 {
		t.Fatalf("%+v, %v", l, err)
	}
	for _, p := range []string{"", "absent.txt", filepath.Join(t.TempDir(), "absent.txt")} {
		if l, err := readDeny(repo, p); l != nil || err != nil {
			t.Errorf("%q: %+v, %v", p, l, err)
		}
	}
	writeFile(t, filepath.Join(repo, "lists/words.txt"), "zorblax\nre:\\bqx7\n")
	_, err = readDeny(repo, "lists/words.txt")
	if want := "lists/words.txt: denylist line 2: " + denylist.WhySubset; err == nil || err.Error() != want ||
		strings.Contains(err.Error(), "qx7") {
		t.Errorf("%v; want %q", err, want)
	}
	if _, err := readDeny(repo, "lists"); err == nil || err.Error() != "lists does not read" {
		t.Errorf("a directory: %v", err)
	}
}

// A relative -out lands under -repo, whatever the recorder's working directory.
func TestPathsRelativeOut(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(t.TempDir())
	root, dst, err := recordPaths(repo, "internal/orchestrator/testdata/histories/advance.json")
	if err != nil || root != repo || dst != filepath.Join(repo, "internal/orchestrator/testdata/histories/advance.json") {
		t.Fatalf("root %q, dst %q, %v", root, dst, err)
	}
	if _, dst, _ := recordPaths(repo, "/elsewhere/x.json"); dst != "/elsewhere/x.json" {
		t.Fatalf("an absolute -out: %q", dst)
	}
}

// denyRules is a planted list's rules, as the recorder reads them.
func denyRules(t *testing.T, list string) []denylist.Rule {
	t.Helper()
	l, err := denylist.Parse([]byte(list))
	if err != nil {
		t.Fatal(err)
	}
	return l.Rules
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
