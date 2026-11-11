package check

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The shell splitter: the commands of a page's shell blocks and of a workflow's run: scripts, read the same way.

// shellCommand is one command of a shell block: the line it starts on, its words with their quotes kept, so that a
// jq program is one word, the two things the runs rule refuses in it and the two the call rule refuses.
type shellCommand struct {
	line   int
	words  []string
	write  bool // an unquoted >
	hidden bool // a backquote, or a $( inside double quotes
	subst  bool // a $( outside quotes, which ends this command's words and starts a command of its own
	vars   bool // a $ before a name, a { or a special parameter, outside single quotes
}

// varStart says whether b, after a $, makes a variable: a letter, _, {, a digit or a special parameter.
func varStart(b byte) bool {
	return b == '_' || (b|0x20 >= 'a' && b|0x20 <= 'z') || strings.IndexByte("{0123456789@*#?$!-", b) >= 0
}

// shellCommands splits each shell block of a page into its commands. Outside quotes a newline, ;, &, |, ( and ) end a
// command, $( starts one, a backslash takes the next byte as it is or joins a line to the next, and a # that starts a
// word comments out the rest of its line. Inside single quotes every byte is the word's; inside double quotes a
// backslash takes the next byte with it.
func shellCommands(lines []line) []shellCommand {
	var block []line
	var cmds []shellCommand
	flush := func() {
		if len(block) > 0 {
			cmds = append(cmds, splitShell(block)...)
			block = nil
		}
	}
	for _, l := range lines {
		if l.fence && l.code != nil {
			block = append(block, l)
			continue
		}
		flush()
	}
	flush()
	return cmds
}

// splitShell splits one shell block, its lines in order, into commands. A $( outside quotes marks the command it
// stands in, and a $ that makes a variable marks its command, for the call rule.
func splitShell(block []line) []shellCommand {
	var texts []string
	for _, l := range block {
		texts = append(texts, l.text)
	}
	src := strings.Join(texts, "\n") + "\n"
	var out []shellCommand
	var cur shellCommand
	var w strings.Builder
	n, open, single, double := block[0].n, false, false, false
	mark := func() {
		if cur.line == 0 {
			cur.line = n
		}
	}
	put := func(b byte) {
		mark()
		open = true
		w.WriteByte(b)
	}
	endWord := func() {
		if open {
			cur.words = append(cur.words, w.String())
			w.Reset()
			open = false
		}
	}
	end := func() {
		endWord()
		if len(cur.words) > 0 || cur.write || cur.hidden || cur.subst || cur.vars {
			out = append(out, cur)
		}
		cur = shellCommand{}
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}
		switch {
		case single:
			put(c)
			single = c != '\''
		case double:
			switch {
			case c == '\\' && next != 0:
				put(c)
				put(next)
				i++
				if next == '\n' {
					n++
				}
			case c == '`' || (c == '$' && next == '('):
				cur.hidden = true
				put(c)
			case c == '$' && varStart(next):
				cur.vars = true
				put(c)
			default:
				put(c)
				double = c != '"'
			}
		case c == '\\' && next != 0:
			i++
			if next == '\n' {
				n++
			} else {
				put(c)
				put(next)
			}
		case c == '\'':
			put(c)
			single = true
		case c == '"':
			put(c)
			double = true
		case c == '#' && !open:
			for i+1 < len(src) && src[i+1] != '\n' {
				i++
			}
		case c == '`':
			mark()
			cur.hidden = true
		case c == '>':
			mark()
			cur.write = true
			endWord()
		case c == '$' && next == '(':
			mark()
			cur.subst = true
			end()
			i++
		case c == '$' && varStart(next):
			cur.vars = true
			put(c)
		case c == ' ' || c == '\t':
			endWord()
		case strings.IndexByte("\n;&|()", c) >= 0:
			end()
		default:
			put(c)
		}
		if c == '\n' {
			n++
		}
	}
	end()
	return out
}

// A shell block splits into its commands as a skill's block or a workflow's run: script holds them, with
// assignments, a $( after a name and a variable among them, each command marked for what the runs and call rules
// read in it.
func TestShellCommands(t *testing.T) {
	block := []string{
		"```sh",
		"g=g20261018-092540   # a comment: > and $( are words here",
		"git rev-parse --short HEAD && bin/laug version",
		`bin/laug gate rejudge "$g" -brief; bin/obs find "$g"`,
		`out="${TMPDIR:-/tmp}/laug-plant" k=steady`,
		`bin/laug report -out "$out"`,
		`h=$(grep -o 'host/[^"]*\.html' "$out/run/$g.html" | head -1)`,
		`grep -F "\"../run/$g.html\"" "$out/$h" | sort -u`,
		`jq -r 'select(.value > 0) | "\(.d)"' "gate/results/$g/verdict.json" \`,
		`  gate/results/g20261018-092540/verdict.json`,
		`jq -Rr 'select(test("^g[0-9]{8}-[0-9]{6}$"))' _obs/aa/arm-3item-m8i.8xlarge.txt | xargs -n1 jq -r .verdict`,
		"jq -r '.rules",
		"  | keys[]' \"gate/results/$g/verdict.json\" >\"$out/rules.txt\"",
		"x=\"$(head -1 \"$out/y\")\" z=`head -1 y`",
		"```",
	}
	mark := func(c shellCommand) string {
		var m []string
		for _, f := range []struct {
			on   bool
			name string
		}{{c.write, "write"}, {c.hidden, "hidden"}, {c.subst, "subst"}, {c.vars, "vars"}} {
			if f.on {
				m = append(m, f.name)
			}
		}
		return strings.Join(m, ",")
	}
	var got []string
	for _, c := range shellCommands(scan(strings.Join(block, "\n") + "\n")) {
		got = append(got, strings.TrimSpace(fmt.Sprintf("%d %s %s", c.line, c.words[0], mark(c))))
	}
	want := []string{"2 g=g20261018-092540", "3 git", "3 bin/laug", "4 bin/laug vars", "4 bin/obs vars",
		`5 out="${TMPDIR:-/tmp}/laug-plant" vars`, "6 bin/laug vars", "7 h= subst", "7 grep vars", "7 head",
		"8 grep vars", "8 sort", "9 jq vars", "11 jq", "11 xargs", "12 jq write,vars",
		`14 x="$(head -1 "$out/y")" hidden,vars`}
	if !slices.Equal(got, want) {
		t.Errorf("the block's commands, by line, first word and marks:\n got %q\nwant %q", got, want)
	}
}
