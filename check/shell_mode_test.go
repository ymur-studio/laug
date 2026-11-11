package check

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The mode of a shell script: every tracked .sh whose line 1 is a shebang is mode 100755 in git ls-files -s, so a
// fresh clone runs it by its path. A .sh with no shebang is a library a script sources (gate/lib.sh, faults/lib.sh,
// load/lost-work.sh, cells/rootfs/fc-lib.sh) and keeps the mode it has.

// shellModeHit says what is wrong with one entry of git ls-files -s, given its file's first bytes, or "" when
// nothing is: a shebang on line 1 and a mode other than 100755.
func shellModeHit(entry string, head []byte) string {
	meta, path, ok := strings.Cut(entry, "\t")
	f := strings.Fields(meta)
	if !ok || len(f) != 3 {
		return fmt.Sprintf("%q is not an entry of git ls-files -s", entry)
	}
	if !bytes.HasPrefix(head, []byte("#!")) || f[0] == "100755" {
		return ""
	}
	return fmt.Sprintf("%s: line 1 is a shebang and the mode is %s, not 100755 (git update-index --chmod=+x %s)",
		path, f[0], path)
}

// TestShellMode holds every tracked .sh with a shebang to mode 100755, and names each file that is not.
func TestShellMode(t *testing.T) {
	out, err := git("ls-files", "-s", "-z", "--", "*.sh").Output()
	if err != nil {
		t.Fatal(err)
	}
	scripts := 0
	for _, e := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		_, p, _ := strings.Cut(e, "\t")
		b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if bytes.HasPrefix(b, []byte("#!")) {
			scripts++
		}
		if hit := shellModeHit(e, b); hit != "" {
			t.Error(hit)
		}
	}
	if scripts == 0 {
		t.Fatal("git ls-files -s names no .sh with a shebang: the test held nothing")
	}
	t.Logf("%d tracked .sh files with a shebang", scripts)
}

// TestShellModeHit holds the rule to planted entries: a shebang needs 100755, and a sourced library without one passes
// at 100644.
func TestShellModeHit(t *testing.T) {
	const blob = "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		entry, head string
		hit         bool
	}{
		{"100755 " + blob + " 0\tgate/run.sh", "#!/usr/bin/env bash\n", false},
		{"100644 " + blob + " 0\tgate/x.sh", "#!/usr/bin/env bash\n", true},
		{"100644 " + blob + " 0\tgate/lib.sh", "# shellcheck shell=bash\n", false},
		{"100644 " + blob + " 0\tload/lost-work.sh", "\n#!/bin/sh\n", false},
		{"100644 " + blob + " 0\tfaults/empty.sh", "", false},
		{"not an entry", "#!", true},
	} {
		hit := shellModeHit(c.entry, []byte(c.head))
		if (hit != "") != c.hit {
			t.Errorf("%q: hit %q, want a hit %v", c.entry, hit, c.hit)
		}
		if c.hit && strings.Contains(c.entry, "\t") && !strings.Contains(hit, strings.SplitN(c.entry, "\t", 2)[1]) {
			t.Errorf("%q: the hit %q does not name the file", c.entry, hit)
		}
	}
}
