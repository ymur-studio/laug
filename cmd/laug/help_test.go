package main

import (
	"slices"
	"strings"
	"testing"
)

// TestHelp checks the list: the header first, then where to start, each line a command and verb of the table, and
// the four words before any command; the exit-code line last, every row once under its group's heading, and no
// heading without a row. help <command> <verb> prints the verb's flags, as its -h does; a third argument, a verb of a
// command with none and an unknown verb are refused.
func TestHelp(t *testing.T) {
	code, out, errOut := runArgs(t, "help")
	if code != 0 || errOut != "" {
		t.Fatalf("help: exit %d, stderr %q", code, errOut)
	}
	before, _, _ := strings.Cut(out, "\n"+groupHeadings[groupDaily]+"\n")
	if !strings.Contains(before, "\nStart here, offline, with no Docker\n") {
		t.Errorf("no start before the commands:\n%s", before)
	}
	for _, l := range startHere {
		f := strings.Fields(l.line)
		c, ok := lookup(f[0])
		named := ok && c.verbs[0].name != ""
		if !ok || named && (len(f) < 2 || !slices.ContainsFunc(c.verbs, func(v verb) bool { return v.name == f[1] })) {
			t.Errorf("start line %q names no command and verb of the table", l.line)
		}
		if !strings.Contains(before, "\n  laug "+l.line+"  ") {
			t.Errorf("start line %q is not in the help before the commands", l.line)
		}
	}
	for _, w := range []string{"cell", "raun", "gate", "Omes"} {
		if !strings.Contains(before, "\n  "+w+"  ") {
			t.Errorf("the word %s is not said before the commands", w)
		}
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != "laug · ephemeral Temporal cells from a warm pool, for release validation" {
		t.Errorf("first line %q", lines[0])
	}
	if last := lines[len(lines)-1]; last != "Exit 0 done or ADVANCE, 1 BLOCK or failed, 2 usage or refused, 3 short run." {
		t.Errorf("last line %q", last)
	}
	under := map[string]string{} // command: the heading above it
	heading := ""
	headings := map[string]bool{}
	for _, l := range lines[1:] {
		for g, h := range groupHeadings {
			if g > 0 && l == h {
				if headings[h] {
					t.Errorf("heading %q twice", h)
				}
				heading, headings[h] = h, true
			}
		}
		if name, ok := strings.CutPrefix(l, "  "); ok && heading != "" {
			name, _, _ = strings.Cut(name, " ")
			if _, dup := under[name]; dup {
				t.Errorf("%s listed twice", name)
			}
			under[name] = heading
		}
	}
	for _, c := range commands {
		if under[c.name] != groupHeadings[c.group] {
			t.Errorf("%s: under %q, want %q", c.name, under[c.name], groupHeadings[c.group])
		}
	}
	if len(under) != len(commands) {
		t.Errorf("help lists %d commands, the table has %d", len(under), len(commands))
	}
	for h := range headings {
		empty := true
		for _, c := range commands {
			if groupHeadings[c.group] == h {
				empty = false
			}
		}
		if empty {
			t.Errorf("heading %q has no command", h)
		}
	}
	if _, bare, _ := runArgs(t); bare != "" {
		t.Errorf("bare laug printed on stdout:\n%s", bare)
	}
	if _, _, bareErr := runArgs(t); bareErr != out {
		t.Errorf("bare laug's stderr differs from help's stdout:\n%s", bareErr)
	}

	_, flags, _ := runArgs(t, "gate", "run", "-h")
	if code, out, errOut := runArgs(t, "help", "gate", "run"); code != 0 || errOut != "" || out != flags ||
		!strings.HasPrefix(out, "laug · gate run: judge a server release beside the one it replaces\n") {
		t.Errorf("help gate run: exit %d, stderr %q\nstdout:\n%s\nwant gate run -h's:\n%s", code, errOut, out, flags)
	}
	for _, c := range []struct {
		args  []string
		first string
	}{
		{[]string{"help", "gate", "run", "x"}, `laug · help: takes a command and a verb at most, got ["gate" "run" "x"]`},
		{[]string{"help", "lease", "x"}, "laug · help: lease has no verbs; laug help lease prints its flags"},
		{[]string{"help", "gate", "nope"}, `laug · gate: unknown verb "nope"`},
		{[]string{"help", "nope"}, `laug · help: unknown command "nope"`},
	} {
		if code, out, errOut := runArgs(t, c.args...); code != 2 || out != "" || firstLine(errOut) != c.first {
			t.Errorf("%s: exit %d, want 2; stdout %q; stderr's first line %q, want %q", strings.Join(c.args, " "),
				code, out, firstLine(errOut), c.first)
		}
	}
}
