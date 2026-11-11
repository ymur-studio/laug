package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	historypb "go.temporal.io/api/history/v1"

	"github.com/ymur-studio/laug/internal/denylist"
)

// The planted list the readers share: made-up words only, with comments, blank lines and a CRLF line, so each rule's
// number is its line in the file.
const plantedList = "# a planted list, made-up words only\n" + // 1
	"zorblax\n" + // 2
	"\n" + // 3
	"   # an indented comment\n" + // 4
	"blue heron lake\r\n" + // 5
	"re:(^|[^a-z0-9])qx7([^a-z0-9]|$)\n" + // 6: qx7 as a word
	"re:^vrexl[0-9]{2,3}$\n" + // 7: a whole line
	" \t\n" + // 8
	"Quill-Moss\n" + // 9
	"re:fen(d|n)ark\n" + // 10
	"#zorblax, in a comment, is no rule\n" + // 11
	"re:a\\.b\\*c\n" + // 12
	"\xc3\xa9tang\n" + // 13: é as UTF-8, then ASCII
	"re:mor.dax\n" + // 14: a dot, which takes any byte but a newline
	"Brol.Vix[2]*\n" // 15: a plain line of bytes a pattern would read otherwise

// readerCases are the texts, each with the lines of plantedList that match it; at is the line of the text each hit is
// on, and times how many matches the pack counts for each, both 1 when 0.
var readerCases = []struct {
	name, text string
	want       []int
	at, times  int
}{
	{"a plain line as written", "see zorblax here", []int{2}, 0, 0},
	{"a plain line in another case", "SEE ZorBLAX", []int{2}, 0, 0},
	{"a plain line of words, read less its \\r", "Blue Heron LAKE at dawn", []int{5}, 0, 0},
	{"a boundary, at a line's start", "qx7 first", []int{6}, 0, 0},
	{"a boundary, at a line's end, upper case", "last, QX7", []int{6}, 0, 0},
	{"a boundary, between brackets", "(qx7)", []int{6}, 0, 0},
	{"a boundary, inside words: no hit", "aqx7 and qx70 and qx7b", nil, 0, 0},
	{"anchors and a count", "VREXL123", []int{7}, 0, 0},
	{"anchors: too few digits", "vrexl1", nil, 0, 0},
	{"anchors: too many", "vrexl1234", nil, 0, 0},
	{"anchors: not at the start", "a vrexl12", nil, 0, 0},
	{"anchors read a line at a time", "one line\nvrexl12\nanother", []int{7}, 2, 0},
	{"a class takes no newline", "zq\nx7 aqx7", nil, 0, 0},
	{"punctuation, case folded", "quill-moss", []int{9}, 0, 0},
	{"punctuation, not as written: no hit", "QUILL MOSS", nil, 0, 0},
	{"an alternation", "FENDARK and fennark", []int{10}, 0, 2},
	{"an alternation: neither", "fenark", nil, 0, 0},
	{"a comment's word is no rule", "#zorblax", []int{2}, 0, 0},
	{"escapes", "an a.b*c here", []int{12}, 0, 0},
	{"escapes: no wildcard", "axb*c", nil, 0, 0},
	{"two rules in one line", "zorblax beside qx7", []int{2, 6}, 0, 0},
	{"ASCII folds, other bytes as written", "\xc3\xa9TANG", []int{13}, 0, 0},
	{"an upper case \xc3\x89: no hit", "\xc3\x89tang", nil, 0, 0},
	{"none", "a plain line of nothing", nil, 0, 0},
	{"a dot takes a 0 byte", "mor\x00dax", []int{14}, 0, 0},
	{"a dot takes one byte, not none", "MORxdax and mordax", []int{14}, 0, 0},
	{"a plain line's . [ ] * as written", "see brol.VIX[2]*", []int{15}, 0, 0},
	{"a plain line is no pattern", "BROLxVIX2", nil, 0, 0},
	{"a plain line counted as written", "brol.vix[2]* and brolxvix2 and BROL.VIX[2]*", []int{15}, 0, 2},
}

// nest is q inside n groups.
func nest(n int) string { return strings.Repeat("(", n) + "q" + strings.Repeat(")", n) }

// readerRefusals are lists every reader refuses, each with the line and the reason; none may print the line's text.
var readerRefusals = []struct {
	name, list string
	line       int
	why        string
}{
	{"an escape POSIX ERE does not define", "zorblax\nre:\\bqx7\n", 2, denylist.WhySubset},
	{"a (? group", "re:(?i)qx7\n", 1, denylist.WhySubset},
	{"an unclosed group", "# c\n\nzorblax\r\nre:(qx7\r\n", 4, denylist.WhySubset},
	{"a POSIX class", "re:zorb[[:alpha:]]\n", 1, denylist.WhySubset},
	{"a count with no minimum", "re:qx7{,2}\n", 1, denylist.WhySubset},
	{"a lazy quantifier", "re:qx7*?\n", 1, denylist.WhySubset},
	{"a backreference", "re:(qx7)\\1\n", 1, denylist.WhySubset},
	{"an empty alternative", "re:qx7|\n", 1, denylist.WhySubset},
	{"a byte over 127", "re:\xc3\xa9tang\n", 1, denylist.WhySubset},
	{"a backslash in a class", "re:[\\.]qx7\n", 1, denylist.WhySubset},
	{"a group that may be empty", "zorblax\nre:(qx7)?\n", 2, denylist.WhyEmpty},
	{"a star", "re:x*\n", 1, denylist.WhyEmpty},
	{"an anchor alone", "re:^\n", 1, denylist.WhyEmpty},
	{"nothing after re:", "zorblax\nre:\n", 2, denylist.WhyEmpty},
	{"a class that starts with :", "re:qx7[:ab:]\n", 1, denylist.WhySubset},
	{"groups 33 deep", "zorblax\nre:" + nest(33) + "\n", 2, denylist.WhySubset},
	{"800 nested groups", "re:" + nest(800) + "\n", 1, denylist.WhySubset},
	{"a byte-order mark", "\xef\xbb\xbfzorblax\n", 1, denylist.WhyBOM},
}

// TestDenylistReaders runs one table of planted lists and texts through the deny list's three readers:
// check/denylist.sh's pass over a scratch git tree, bin/obs pack's word check (_obs/obs.sh's
// deny_check over a scratch state, as written and decoded) and the recorder's leak check (ScanLeaks over a history).
// Each must name the same lines of the list for each text and refuse each bad list on the same line for the same
// reason, exit 2; no output may hold a planted word, a pattern or a text.
func TestDenylistReaders(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	list := filepath.Join(dir, "list.txt")
	writeFile(t, list, plantedList)

	tree := filepath.Join(dir, "tree")
	state := filepath.Join(dir, "state")
	for i, c := range readerCases {
		writeFile(t, filepath.Join(tree, fmt.Sprintf("case%02d.txt", i)), c.text+"\n")
		writeFile(t, filepath.Join(state, "prometheus", fmt.Sprintf("case%02d", i)), c.text+"\n")
		writeFile(t, filepath.Join(state, "decoded", fmt.Sprintf("case%02d.decoded", i)), c.text+"\n")
	}
	gitInit(t, tree)

	// The private pass: one line a hit, "<path>:<line>: denylist line <N>", the line the text's.
	code, out := runReader(t, root, dir, "check/denylist.sh", "-list", list, tree)
	if code != 1 {
		t.Fatalf("the private pass: exit %d, want 1\n%s", code, out)
	}
	pass := hitsByCase(t, "the private pass", out,
		regexp.MustCompile(`^laug · denylist: case(?P<c>\d+)\.txt:(?P<x>\d+): denylist line (?P<n>\d+)$`),
		"laug · denylist: ", func(c int) int { return or1(readerCases[c].at) })
	noPlant(t, "the private pass", out)

	// The pack's word check, sourced: "<path>[ (decoded)]: denylist line <N>, <n> time(s)", n its count.
	code, out = runPack(t, root, list, state)
	if code != 1 {
		t.Fatalf("the pack's check: exit %d, want 1\n%s", code, out)
	}
	times := func(c int) int { return or1(readerCases[c].times) }
	pack := hitsByCase(t, "the pack's check, as written", out,
		regexp.MustCompile(`^prometheus/case(?P<c>\d+): denylist line (?P<n>\d+), (?P<x>\d+) times?$`), "case", times)
	decoded := hitsByCase(t, "the pack's check, decoded", out,
		regexp.MustCompile(`^case(?P<c>\d+) \(decoded\): denylist line (?P<n>\d+), (?P<x>\d+) times?$`), "prometheus/",
		times)
	noPlant(t, "the pack's check", out)

	for i, c := range readerCases {
		h := &historypb.History{Events: []*historypb.HistoryEvent{completed(1, "laug-worker", note(t, c.text))}}
		var rec []int
		for _, l := range Leaks(h, Scrub{Deny: denyRules(t, plantedList)}) {
			n, ok := strings.CutPrefix(l.Kind, "denylist line ")
			if !ok {
				t.Fatalf("%s: a leak of kind %s", c.name, l.Kind)
			}
			k, _ := strconv.Atoi(n)
			rec = append(rec, k)
		}
		sort.Ints(rec)
		want := fmt.Sprint(c.want)
		for _, r := range []struct {
			name string
			got  []int
		}{{"the private pass", pass[i]}, {"the pack's check, as written", pack[i]},
			{"the pack's check, decoded", decoded[i]}, {"the recorder", rec}} {
			if fmt.Sprint(r.got) != want {
				t.Errorf("%s, %q: %s names lines %v, want %v", c.name, c.text, r.name, r.got, c.want)
			}
		}
	}

	for _, c := range readerRefusals {
		bad := filepath.Join(dir, "bad.txt")
		writeFile(t, bad, c.list)
		want := fmt.Sprintf("denylist line %d: %s", c.line, c.why)
		code, out := runReader(t, root, dir, "check/denylist.sh", "-list", bad, tree)
		if code != 2 || !strings.Contains(out, "laug · denylist: "+want+"\n") {
			t.Errorf("%s: the private pass: exit %d, %q; want 2, %q", c.name, code, out, want)
		}
		pcode, pout := runPack(t, root, bad, state)
		if pcode != 2 || strings.TrimSpace(pout) != want {
			t.Errorf("%s: the pack's check: exit %d, %q; want 2, %q", c.name, pcode, pout, want)
		}
		_, err := readDeny(dir, "bad.txt")
		var le *denylist.LineError
		if !errors.As(err, &le) || err.Error() != "bad.txt: "+want {
			t.Errorf("%s: the recorder: %v; want %q", c.name, err, "bad.txt: "+want)
		}
		for _, o := range []string{out, pout, fmt.Sprint(err)} {
			if p := strings.TrimPrefix(strings.TrimSpace(strings.Split(c.list, "\n")[c.line-1]), "re:"); p != "" &&
				strings.Contains(o, p) {
				t.Errorf("%s: an output holds the refused line's text %q: %q", c.name, p, o)
			}
		}
	}
}

// The readers say so where the list is absent, or holds no rule, and go on; a list with no hit gives the summary,
// which counts its plain and re: lines.
func TestDenylistReadersNoHit(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tree, state := filepath.Join(dir, "tree"), filepath.Join(dir, "state")
	writeFile(t, filepath.Join(tree, "a.txt"), "a plain line of nothing\n")
	writeFile(t, filepath.Join(state, "prometheus", "a"), "a plain line of nothing\n")
	gitInit(t, tree)
	writeFile(t, filepath.Join(dir, "list.txt"), plantedList)
	writeFile(t, filepath.Join(dir, "comments.txt"), "# none\n\n  \r\n")
	for _, c := range []struct{ list, pass string }{
		{"absent.txt", "laug · denylist: no absent.txt on this host, so no private word was checked"},
		{"comments.txt", "laug · denylist: comments.txt holds no patterns, so no private word was checked"},
		{"list.txt", "laug · denylist: 5 plain lines and 5 re: lines of list.txt over 1 file: 0 hits"},
	} {
		code, out := runReader(t, root, dir, "check/denylist.sh", "-list", c.list, tree)
		if code != 0 || strings.TrimSpace(out) != c.pass {
			t.Errorf("%s: the private pass: exit %d, %q; want 0, %q", c.list, code, out, c.pass)
		}
		if code, out := runPack(t, root, filepath.Join(dir, c.list), state); code != 0 || out != "" {
			t.Errorf("%s: the pack's check: exit %d, %q; want 0 and nothing", c.list, code, out)
		}
		l, err := readDeny(dir, c.list)
		n := map[string]string{"absent.txt": "0 plain lines and 0 re: lines", "comments.txt": "0 plain lines and 0 re: lines",
			"list.txt": "5 plain lines and 5 re: lines"}[c.list]
		if err != nil || l.Summary() != n {
			t.Errorf("%s: the recorder: %v, %q; want %q", c.list, err, l.Summary(), n)
		}
	}
	// The pack counts as the pass does, for its summary and PACK.json.
	code, out := runReader(t, root, dir, "check/denylist.sh", "-list", "list.txt", "-count")
	if code != 0 || out != "5 5\n" {
		t.Errorf("-count: exit %d, %q", code, out)
	}
}

// note is a json/plain payload with text as a string.
func note(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"name": "x", "answer": map[string]string{"note": text}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readerEnv is the caller's environment less every GIT_*, with GIT_CEILING_DIRECTORIES at dir, so git takes no work
// tree above a scratch one, and LC_ALL unset, as a shell's would be.
func readerEnv(dir string) []string {
	var out []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			out = append(out, e)
		}
	}
	return append(out, "GIT_CEILING_DIRECTORIES="+dir)
}

func gitInit(t *testing.T, tree string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir, cmd.Env = tree, readerEnv(filepath.Dir(tree))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

// runReader runs a script of the checkout from dir: its exit, and stdout then stderr.
func runReader(t *testing.T, root, dir, script string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(root, script)}, args...)...)
	cmd.Dir, cmd.Env = dir, readerEnv(dir)
	return run(t, cmd)
}

// runPack sources _obs/obs.sh from the checkout and runs deny_check alone on state, with WORDS the list: no host
// words, so only the list and the shapes are grepped.
func runPack(t *testing.T, root, list, state string) (int, string) {
	t.Helper()
	return runPackWith(t, root, "", nil, list, state)
}

// runPackWith is runPack with bash run before deny_check, in the same shell, and env added to the environment.
func runPackWith(t *testing.T, root, before string, env []string, list, state string) (int, string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	cmd := exec.Command("bash", "-c", `source _obs/obs.sh || exit 3; WORDS="$1"; `+before+`
deny_check "$2" "$3" "$4"`, "bash", list, state, filepath.Join(state, "decoded"), work)
	cmd.Dir, cmd.Env = root, append(readerEnv(filepath.Dir(state)), env...)
	code, out := run(t, cmd)
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deny_check left its work directory: %v", err)
	}
	return code, out
}

func run(t *testing.T, cmd *exec.Cmd) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String() + stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stdout.String() + stderr.String()
	}
	t.Fatalf("%v: %v", cmd.Args, err)
	return 0, ""
}

// hitsByCase reads a reader's hit lines, those re matches, into each case's lines of the list, sorted. re's groups are
// c, the case's index, n, the list's line, and x, the pass's line of the text or the pack's count, which must be
// want(c). A line re does not match must start with other: another form of hit, or the reader's own lines.
func hitsByCase(t *testing.T, reader, out string, re *regexp.Regexp, other string, want func(c int) int) map[int][]int {
	t.Helper()
	got := map[int][]int{}
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		m := re.FindStringSubmatch(l)
		if m == nil {
			if !strings.HasPrefix(l, other) {
				t.Errorf("a line that is no hit: %q", l)
			}
			continue
		}
		c, _ := strconv.Atoi(m[re.SubexpIndex("c")])
		n, _ := strconv.Atoi(m[re.SubexpIndex("n")])
		if x, _ := strconv.Atoi(m[re.SubexpIndex("x")]); c < len(readerCases) && x != want(c) {
			t.Errorf("%s, %s: %q gives %d, want %d", reader, readerCases[c].name, l, x, want(c))
		}
		if !slices.Contains(got[c], n) {
			got[c] = append(got[c], n)
		}
	}
	for c := range got {
		sort.Ints(got[c])
	}
	return got
}

// noPlant fails an output that holds a planted word, a part of a pattern, or a case's text.
func noPlant(t *testing.T, reader, out string) {
	t.Helper()
	low := strings.ToLower(out)
	plants := []string{"zorblax", "blue heron lake", "qx7", "vrexl", "quill", "fendark", "fennark", "a.b*c", "tang",
		"[^a-z0-9]", "(d|n)"}
	for _, c := range readerCases {
		for _, l := range strings.Split(c.text, "\n") {
			plants = append(plants, strings.ToLower(l))
		}
	}
	for _, p := range plants {
		if strings.Contains(low, p) {
			t.Errorf("%s's output holds %q:\n%s", reader, p, out)
		}
	}
}

// or1 is n, or 1 when n is 0.
func or1(n int) int {
	if n == 0 {
		return 1
	}
	return n
}

// subsetCase is a pattern of a re: line, less its re:, and why every reader refuses it, "" when each takes it.
type subsetCase struct{ p, why string }

// subsetTable is internal/denylist/testdata/subset.txt's table, as internal/denylist's TestSubset reads it, then the
// subset's limits: 256 bytes and groups 32 deep.
func subsetTable(t *testing.T) []subsetCase {
	t.Helper()
	b, err := os.ReadFile("../denylist/testdata/subset.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []subsetCase
	for i, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		verdict, p, _ := strings.Cut(line, " ")
		why, ok := map[string]string{"take": "", "subset": denylist.WhySubset, "empty": denylist.WhyEmpty}[verdict]
		if strings.HasPrefix(p, `"`) {
			p, err = strconv.Unquote(p)
		}
		if !ok || err != nil {
			t.Fatalf("subset.txt:%d: %q: no verdict and pattern", i+1, line)
		}
		out = append(out, subsetCase{p, why})
	}
	return append(out, subsetCase{nest(32), ""}, subsetCase{nest(33), denylist.WhySubset},
		subsetCase{nest(800), denylist.WhySubset}, subsetCase{strings.Repeat("q", 256), ""},
		subsetCase{strings.Repeat("q", 257), denylist.WhySubset})
}

// bashVerdicts runs check/denylist.sh -count on a one-line list for each pattern, 8 at a time: "" when it takes the
// line, the reason when it refuses it, or what it printed when it did neither.
func bashVerdicts(t *testing.T, root string, patterns []string) []string {
	t.Helper()
	dir := t.TempDir()
	out := make([]string, len(patterns))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, p := range patterns {
		list := filepath.Join(dir, fmt.Sprintf("list%03d.txt", i))
		writeFile(t, list, "re:"+p+"\n")
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			cmd := exec.Command("bash", filepath.Join(root, "check/denylist.sh"), "-list", list, "-count")
			cmd.Dir, cmd.Env = dir, readerEnv(dir)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			switch why, ok := strings.CutPrefix(stderr.String(), "denylist line 1: "); {
			case err == nil && stdout.String() == "0 1\n" && stderr.Len() == 0:
				out[i] = ""
			case errors.As(err, &ee) && ee.ExitCode() == 2 && ok && stdout.Len() == 0:
				out[i] = strings.TrimSuffix(why, "\n")
			default:
				out[i] = fmt.Sprintf("neither: %v, %q, %q", err, stdout.String(), stderr.String())
			}
		}()
	}
	wg.Wait()
	return out
}

// goVerdict is the recorder's verdict on a re: line of pattern p: "" when internal/denylist takes it, else the reason.
func goVerdict(p string) string {
	_, err := denylist.Parse([]byte("re:" + p + "\n"))
	var le *denylist.LineError
	if errors.As(err, &le) {
		return le.Why
	}
	return ""
}

// TestDenylistSubsetBash holds check/denylist.sh's subset reader, which the pass and the pack read the list by, to the
// table internal/denylist's reader is held to, and to Go's verdict on 300 random patterns of 1 to 10 bytes over the
// bytes the subset turns on, from a fixed seed.
func TestDenylistSubsetBash(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	table := subsetTable(t)
	if len(table) < 100 {
		t.Fatalf("%d cases in subset.txt and the limits", len(table))
	}
	var patterns []string
	for _, c := range table {
		patterns = append(patterns, c.p)
	}
	const bytes = `ab(|)[]^-$*+?{}.,0129\&~zA:=<!P#dswb1 `
	rng := rand.New(rand.NewPCG(20261027, 1))
	for range 300 {
		b := make([]byte, 1+rng.IntN(10))
		for i := range b {
			b[i] = bytes[rng.IntN(len(bytes))]
		}
		patterns = append(patterns, string(b))
	}
	got := bashVerdicts(t, root, patterns)
	takes := 0
	for i, p := range patterns {
		want := goVerdict(p)
		if i < len(table) && table[i].why != want {
			t.Errorf("%q: Go gives %q, the table %q", p, want, table[i].why)
		}
		if got[i] != want {
			t.Errorf("%q: check/denylist.sh gives %q, Go %q", p, got[i], want)
		}
		if i >= len(table) && want == "" {
			takes++
		}
	}
	t.Logf("%d of the 300 random patterns taken", takes)
	if takes < 20 {
		t.Errorf("%d of the 300 random patterns taken: too few to hold the readers to each other", takes)
	}
}

// A count grep that fails makes the pack's check fail, and a file the word grep named that no rule counts is still a
// hit, of line ?: a grep on PATH that answers check/denylist.sh's count with exit 2, then with exit 1 and nothing.
func TestDenylistPackCount(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	real, err := exec.LookPath("grep")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	list, state, shim := filepath.Join(dir, "list.txt"), filepath.Join(dir, "state"), filepath.Join(dir, "bin")
	writeFile(t, list, "zorblax\n")
	writeFile(t, filepath.Join(state, "prometheus", "a"), "see zorblax\n")
	writeFile(t, filepath.Join(shim, "grep"), "#!/usr/bin/env bash\n"+
		`case " $* " in *" -o -i "*) exit "$SHIM_EXIT" ;; esac`+"\nexec "+strconv.Quote(real)+` "$@"`+"\n")
	if err := os.Chmod(filepath.Join(shim, "grep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		exit string
		code int
		out  string
	}{
		{"2", 2, "a grep over the state could not read a file (exit 2)\n"},
		{"1", 1, "prometheus/a: denylist line ?, 1 time\n"},
	} {
		code, out := runPackWith(t, root, "", []string{"PATH=" + shim + ":" + os.Getenv("PATH"), "SHIM_EXIT=" + c.exit},
			list, state)
		if code != c.code || out != c.out {
			t.Errorf("a count grep that exits %s: exit %d, %q; want %d, %q", c.exit, code, out, c.code, c.out)
		}
	}
}

// An xtrace the caller turned on prints nothing deny_check matched: neither a word of the list nor one of this host's.
func TestDenylistPackXtrace(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	list, state := filepath.Join(dir, "list.txt"), filepath.Join(dir, "state")
	writeFile(t, list, "zorblax\nre:qx7[0-9]\n")
	writeFile(t, filepath.Join(state, "prometheus", "a"), "see zorblax, qx79 and vexmoor7\n")
	code, out := runPackWith(t, root, `HOST_WORD[vexmoor7]="user name"; HOST_AS_WORD[vexmoor7]=1; set -x`, nil, list, state)
	want := "prometheus/a: denylist line 1, 1 time\nprometheus/a: denylist line 2, 1 time\n" +
		"prometheus/a: this host's user name, 1 time\n"
	if code != 1 || !strings.HasPrefix(out, want) {
		t.Errorf("exit %d, %q; want 1, starting %q", code, out, want)
	}
	if low := strings.ToLower(out); strings.Contains(low, "zorblax") || strings.Contains(low, "qx7") ||
		strings.Contains(low, "vexmoor") {
		t.Errorf("the trace holds a planted word:\n%s", out)
	}
}

// A list that is there but is not a regular file, a directory or a link to nothing, stops the pack's check and the
// recorder, as it stops the pass. The pack's check reads the list through check/denylist.sh -rules, which prints the
// pass's words, "<list> is not a file", without the pass's prefix.
func TestDenylistReadersNotAFile(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	writeFile(t, filepath.Join(state, "prometheus", "a"), "a plain line of nothing\n")
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nothing.txt"), filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"adir", "gone"} {
		list := filepath.Join(dir, name)
		if code, out := runPack(t, root, list, state); code != 2 || out != list+" is not a file\n" {
			t.Errorf("%s: the pack's check: exit %d, %q; want 2, %q", name, code, out, list+" is not a file\n")
		}
		if _, err := readDeny(dir, name); err == nil || err.Error() != name+" does not read" {
			t.Errorf("%s: the recorder: %v; want %q", name, err, name+" does not read")
		}
	}
}
