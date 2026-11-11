package tolerance

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// TestCheck holds Check to the script's usage lines, in its order.
func TestCheck(t *testing.T) {
	dir := t.TempDir()
	for _, g := range []string{"g20261101-000000", "g20261101-000001-2"} {
		if err := os.Mkdir(filepath.Join(dir, g), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dir, "g20261101-000002"), "a file")
	symlink(t, "g20261101-000000", filepath.Join(dir, "g20261101-000003"))
	ok := []string{"g20261101-000000"}
	cases := []struct {
		name string
		r    Request
		why  string // "" for none
	}{
		{"no gate", Request{}, "the gate ids name no gate"},
		{"not a gate id", Request{IDs: []string{"g20261101-000000", "nope"}}, "'nope' is not a gate id, g<date>-<time>"},
		{"a gate twice", Request{IDs: []string{"g20261101-000000", "g20261101-000001-2", "g20261101-000000"}},
			"the gate ids name a gate twice"},
		{"PAIR in capitals", Request{IDs: ok, Pair: "A:b"}, "-pair A:b is not <a>:<b>, two item keys"},
		{"PAIR one key", Request{IDs: ok, Pair: "a"}, "-pair a is not <a>:<b>, two item keys"},
		{"PAIR a key of 33", Request{IDs: ok, Pair: strings.Repeat("a", 33) + ":b"},
			"-pair " + strings.Repeat("a", 33) + ":b is not <a>:<b>, two item keys"},
		{"PAIR a newline at its end", Request{IDs: ok, Pair: "a:b\n"}, "-pair a:b\n is not <a>:<b>, two item keys"},
		{"PAIR one item twice", Request{IDs: ok, Pair: "a:a"}, "-pair a:a names one item twice"},
		{"EVIDENCE elsewhere", Request{IDs: ok, Evidence: "cells/x.txt"},
			"-evidence cells/x.txt is not a file in cells/results/ or docs/tables/gates.jsonl"},
		{"EVIDENCE below", Request{IDs: ok, Evidence: "cells/results/a/b"},
			"-evidence cells/results/a/b is not a file in cells/results/ or docs/tables/gates.jsonl"},
		{"EVIDENCE the gates table", Request{IDs: ok, Evidence: "docs/tables/gates.jsonl#g1"},
			"-evidence docs/tables/gates.jsonl#g1 is not a file in cells/results/ or docs/tables/gates.jsonl"},
		{"no such gate", Request{IDs: []string{"g20261101-000000", "g20261101-999999"}, Results: "runs"},
			"no gate run g20261101-999999 in -results, runs"},
		{"a gate that is a file", Request{IDs: []string{"g20261101-000002"}},
			"no gate run g20261101-000002 in -results, gate/results"},
		{"a gate through a symlink", Request{IDs: []string{"g20261101-000003", "g20261101-000001-2"}, Pair: "a-1:b",
			Evidence: "cells/results/x.txt"}, ""},
	}
	for _, c := range cases {
		err := Check(dir, c.r)
		switch {
		case c.why == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.why != "" && (err == nil || err.Error() != usageLine+": "+c.why):
			t.Errorf("%s: %v, want the usage line and %q", c.name, err, c.why)
		}
	}
}

// transcript is the block a committed transcript printed, as testdata/transcript-<name>.txt holds it (copied from
// file's line at, n lines, unedited but for reworded), its two-blank indent taken off, each line with the words the
// script printed before laug named its verbs as it prints them now (renamed). A block that holds none of them is no
// block the script printed then, and fails. Where file is still in cells/results, the copy must be its lines from at,
// byte for byte, once reworded has run on them.
func transcript(t *testing.T, name, file string, at, n int) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "transcript-"+name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if r, err := os.ReadFile(filepath.Join(root, "cells", "results", file)); err == nil {
		all := strings.Split(reworded.ReplaceAllString(string(r), "(it goes in place of compare:"), "\n")
		if at < 1 || at-1+len(lines) > len(all) ||
			strings.Join(all[at-1:at-1+len(lines)], "\n") != strings.Join(lines, "\n") {
			t.Fatalf("testdata/transcript-%s.txt is not %s's %d lines from %d", name, file, len(lines), at)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(lines) != n {
		t.Fatalf("testdata/transcript-%s.txt: %d lines, and the rule printed %d", name, len(lines), n)
	}
	var out []string
	changed := 0
	for _, x := range lines {
		x = strings.TrimPrefix(x, "  ")
		if y := renamed.Replace(x); y != x {
			x, changed = y, changed+1
		}
		out = append(out, x)
	}
	if changed < 2 {
		t.Fatalf("testdata/transcript-%s.txt renames %d lines, not its first and the drivers' line", name, changed)
	}
	return out
}

// renamed maps what make arm-tolerance printed, which the transcripts hold, to what laug gate tolerance prints: its
// name, -pair for PAIR=, the drivers by laug's verbs, and -evidence for EVIDENCE=. Nothing else in a block changed
// but the clause reworded matches.
var renamed = strings.NewReplacer(
	"laug · arm-tolerance: ", "laug · gate tolerance: ",
	" gate(s), PAIR=", " gate(s), -pair ",
	"mean d by driver (standard error); make gate runs run.sh, make orchestrator-gate step.sh:",
	"mean d by driver (standard error); gate run runs run.sh, gate run -workflow step.sh:",
	"  evidence: null   # EVIDENCE=cells/results/<file> names it;", "  evidence: null   # -evidence cells/results/<file> names it;")

// reworded matches the one clause the rule now says in other words than the calibration's transcript did: where the
// block goes. ReplaceAllString puts the rule's words there before the compare, so the rest of the copy is still held to
// the record byte for byte.
var reworded = regexp.MustCompile(`\(B[0-9]+ sets it in place of compare:`)

// The gates of the calibration and of two PAIR looks, committed gates, as the transcripts name them.
var (
	// calibration is the 20 A/A gates of the calibration, as testdata/transcript-calibration.txt's gates line names
	// them (cells/results/2026-10-17-lab-arm-calibration.txt:261).
	calibration = strings.Fields("g20261017-174403 g20261017-175458 g20261017-180552 g20261017-181635 " +
		"g20261017-182733 g20261017-183826 g20261017-184922 g20261017-190015 g20261017-191109 g20261017-192202 " +
		"g20261017-193258 g20261017-194350 g20261017-195440 g20261017-200534 g20261017-201631 g20261017-202724 " +
		"g20261017-203815 g20261017-204906 g20261017-205956 g20261017-211051")
	// noiseWaves and pilot are the PAIR looks of testdata/transcript-noise-waves.txt and transcript-pilot.txt: the six
	// noise waves with s1:s2 and the pilot's six with incumbent:steady.
	noiseWaves = strings.Fields("g20261015-141922 g20261015-142759 g20261015-143633 g20261015-144517 " +
		"g20261015-145418 g20261015-150315")
	pilot = strings.Fields("g20261017-060633 g20261017-061717 g20261017-062801 g20261017-063843 g20261017-064925 " +
		"g20261017-070006")
)

// testSet is the threshold set load/thresholds.sh "" resolved to when this package was written, as yq -o=json -I=0
// printed it: testdata/set.json. The tests run on it.
func testSet(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "set.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestTranscripts holds the rule to three blocks committed transcripts printed, each the script's whole stdout with
// exit 0, as testdata holds them: the 20 calibration gates (transcript-calibration.txt, 40 lines, copied from
// cells/results/2026-10-17-lab-arm-calibration.txt:258) and the two PAIR looks (transcript-noise-waves.txt and
// transcript-pilot.txt, 26 lines each, copied from cells/results/2026-10-17-arm-offline.txt:523 and :566), with the
// set as testdata/set.json holds it, so that it needs no tool. Each block's lines are renamed first. RunRows, the
// gates read from docs/tables, is held to each block; Run, from the runs' directories, too where they are here.
func TestTranscripts(t *testing.T) {
	set := testSet(t)
	rows, err := ReadRows(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, file string
		at         int
		ids        []string
		pair       string
	}{
		{"calibration", "2026-10-17-lab-arm-calibration.txt", 258, calibration, ""},
		{"noise-waves", "2026-10-17-arm-offline.txt", 523, noiseWaves, "s1:s2"},
		{"pilot", "2026-10-17-arm-offline.txt", 566, pilot, "incumbent:steady"},
	}
	for _, c := range cases {
		r := Request{IDs: c.ids, Pair: c.pair}
		if err := CheckRows(rows, r); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := RunRows(rows, r, set, &stdout, &stderr)
		got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		want := transcript(t, c.name, c.file, c.at, len(got))
		if code != 0 || stderr.Len() > 0 || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s, %d gates, from the rows: exit %d\n%s\nGo:\n%s\nthe transcript:\n%s", c.name, len(c.ids), code,
				&stderr, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		t.Logf("%s: %d lines from the rows, the same", c.name, len(got))
		if err := Check(results(), r); err != nil {
			var u *UsageError
			if errors.As(err, &u) && strings.HasPrefix(u.Why, "no gate run ") {
				t.Logf("%s: its directories are not here; the rows alone", c.name)
				continue
			}
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		code = Run(results(), r, set, &stdout, &stderr)
		got = strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		if code != 0 || stderr.Len() > 0 || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s, %d gates, from the directories: exit %d\n%s\nGo:\n%s\nthe transcript:\n%s", c.name,
				len(c.ids), code, &stderr, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		t.Logf("%s: %d lines from the directories, the same", c.name, len(got))
	}
}

func itoa(n int) string { return strings.TrimSpace(string(jsonx.MarshalCompact(count(n)))) }

// TestJQLine holds jqLine to jq's reads of 4095 bytes: a value whose last byte ends a read has its newline still
// unread. With jq, each case also runs there, as SHOW's error reports it.
func TestJQLine(t *testing.T) {
	jq := maybeJQ(t)
	for _, n := range []int{20, 4094, 4095, 4096, 8189, 8190, 8191} {
		pad := n - len(`{"w":null,"p":""}`)
		line := []byte(`{"w":null,"p":"` + strings.Repeat("x", pad) + `"}`)
		input := append(line, '\n')
		got := jqLine(input, len(line)-1)
		want := 1
		if n%4095 == 0 {
			want = 0
		}
		if got != want {
			t.Errorf("%d bytes: %d, want %d", n, got, want)
		}
		if jq == "" {
			continue
		}
		_, errOut, _ := jqRun(t, jq, input, "-r", `"a", (.w | join(" | "))`)
		if w := "jq: error (at <stdin>:" + itoa(got) + "): Cannot iterate over null (null)\n"; string(errOut) != w {
			t.Errorf("%d bytes: jq %q, Go %q", n, errOut, w)
		}
	}
	// gather's text: its last read is "}]\n", after the newline of the last file's line.
	text := []byte("[{\"a\":\n1\n}]\n")
	if got := jqLine(text, len(text)-2); got != 3 {
		t.Errorf("gathered text: %d, want 3", got)
	}
}

// TestRunPaths holds Run's exits and lines on each path, with made gates and the set as testdata/set.json holds it:
// the table; refused gates; a set that is not JSON; gathered text that does not parse; a RULE error; a SHOW error.
func TestRunPaths(t *testing.T) {
	set := testSet(t)
	dir := t.TempDir()
	ids := makeGates(t, dir)
	byName := map[string]string{}
	for i, m := range madeGates {
		byName[m.name] = ids[i]
	}
	committed := byName["as committed"]
	run := func(r Request, set []byte) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Run(dir, r, set, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	t.Run("the table", func(t *testing.T) {
		code, out, errOut := run(Request{IDs: []string{committed}}, set)
		if code != 0 || errOut != "" || !strings.HasPrefix(out, "laug · gate tolerance: 1 gate(s), A/A gates of 1.31.0 "+
			"on m8i.8xlarge, the incumbent arm\n") || !strings.HasSuffix(out, "    - {name: "+
			"workflow_end_to_end_over_50ms, scale: share, floor: 0.0005, tolerance: 0.0008}\n") {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
	t.Run("refused", func(t *testing.T) {
		code, out, errOut := run(Request{IDs: []string{committed, byName["known-slow of every kind"],
			byName["plan.json absent"]}}, set)
		want := "laug · gate tolerance: 2 gate(s) refused; nothing computed\n" +
			"  " + byName["plan.json absent"] + ": plan.json names no item with arm incumbent; -pair <a>:<b> reads two " +
			"plain items\n"
		if code != 2 || out != "" || !strings.HasPrefix(errOut, want) || strings.Count(errOut, "\n") != 3 {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
	t.Run("a set that is not JSON", func(t *testing.T) {
		code, out, errOut := run(Request{IDs: []string{committed}}, nil)
		if code != 2 || out != "" || !strings.HasSuffix(errOut, "\n"+ruleDidNotRun+"\n") ||
			!strings.HasPrefix(errOut, "laug · gate tolerance: the set is not one JSON value") {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
	t.Run("a file that cannot be read", func(t *testing.T) {
		code, out, errOut := run(Request{IDs: []string{byName["verdict.json a directory"]}}, set)
		if code != 2 || out != "" || !strings.HasSuffix(errOut, "\n"+ruleDidNotRun+"\n") ||
			!strings.HasPrefix(errOut, "laug · gate tolerance: the gates' files, as gather() prints them, are "+
				"not one JSON text") {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
	t.Run("a RULE error", func(t *testing.T) {
		code, out, errOut := run(Request{IDs: []string{committed}}, []byte(`{"thresholds":null}`))
		// One gate gathers to 8 lines: verdict, driver, th, thi, slow, va and vb each end one, then "}]".
		if want := "jq: error (at <stdin>:8): Cannot iterate over null (null)\n" + ruleDidNotRun + "\n"; code != 2 ||
			out != "" || errOut != want {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
	t.Run("a SHOW error", func(t *testing.T) {
		code, out, errOut := run(Request{IDs: []string{byName["a comparison without a wave"]}}, set)
		if want := "jq: error (at <stdin>:1): Cannot iterate over null (null)\n"; code != 5 || errOut != want ||
			!strings.HasPrefix(out, "laug · gate tolerance: 1 gate(s), A/A gates of 1.31.0") || strings.Count(out, "\n") != 1 {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
	t.Run("a SHOW error where a read ends", func(t *testing.T) {
		// RULE's line holds the gate's laug_commit once: pad it to a whole number of jq's 4095-byte reads, so that the
		// line's last byte ends a read and its newline is not yet read. jq then says <stdin>:0 (TestJQLine).
		g := byName["a comparison without a wave"]
		path := filepath.Join(dir, g, "verdict.json")
		commit := func(c string) int {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			v := value(t, string(b))
			o, _ := v.Obj()
			o.Set("laug_commit", str(c))
			write(t, path, string(jsonx.Marshal(v)))
			var out jsonx.Value
			text, _ := gather(dir, dir, []string{g}, "")
			gates, sv := value(t, string(text)), value(t, string(set))
			if e := evaluate(func() { out = rule(gates, sv, "") }); e != nil {
				t.Fatal(e)
			}
			return len(jsonx.MarshalCompact(out)) - 1
		}
		n := commit("")
		if n = commit(strings.Repeat("c", (4095-n%4095)%4095)); n%4095 != 0 {
			t.Fatalf("RULE's line is %d bytes", n)
		}
		code, out, errOut := run(Request{IDs: []string{g}}, set)
		if want := "jq: error (at <stdin>:0): Cannot iterate over null (null)\n"; code != 5 || errOut != want ||
			strings.Count(out, "\n") != 1 {
			t.Errorf("RULE's line of %d bytes: exit %d\n%s%s", n, code, out, errOut)
		}
	})
}
