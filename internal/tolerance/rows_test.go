package tolerance

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rowCases are the 32 runs TestTranscripts reads, as three runs of the rule, each with its golden: what -tables
// prints on them, testdata/rows-<name>.txt, stdout then a line "exit <code>". The calibration gates cite the gates
// table, as -tables does when no -evidence is given.
var rowCases = []struct {
	name     string
	ids      []string
	pair     string
	evidence string
}{
	{"calibration", calibration, "", TablesEvidence},
	{"noise-waves", noiseWaves, "s1:s2", ""},
	{"pilot", pilot, "incumbent:steady", ""},
}

// TestRowsMatchDirectories: RunRows, the gates read from docs/tables, prints what Run prints from their directories,
// byte for byte with the same exit, on the 32 runs, and what its golden holds. A run whose directories are not here
// (an archived run) is held to the golden alone.
func TestRowsMatchDirectories(t *testing.T) {
	set := testSet(t)
	rows, err := ReadRows(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rowCases {
		r := Request{IDs: c.ids, Pair: c.pair, Evidence: c.evidence}
		if err := CheckRows(rows, r); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		code := RunRows(rows, r, set, &out, &errOut)
		if errOut.Len() > 0 {
			t.Errorf("%s: stderr %q", c.name, &errOut)
		}
		got := out.String() + "exit " + strconv.Itoa(code) + "\n"
		golden := filepath.Join("testdata", "rows-"+c.name+".txt")
		if os.Getenv("LAUG_TOLERANCE_GOLDEN") == "write" {
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s: -tables printed\n%s\nthe golden, %s:\n%s", c.name, got, golden, want)
		}
		if err := Check(results(), r); err != nil {
			var u *UsageError
			if errors.As(err, &u) && strings.HasPrefix(u.Why, "no gate run ") {
				t.Logf("%s: its directories are not here; held to the golden", c.name)
				continue
			}
			t.Fatal(err)
		}
		var dout, derr bytes.Buffer
		dcode := Run(results(), r, set, &dout, &derr)
		if dcode != code || dout.String() != out.String() || derr.String() != errOut.String() {
			t.Errorf("%s: the directories gave exit %d\n%s%s\n-tables exit %d\n%s%s", c.name, dcode, &dout, &derr,
				code, &out, &errOut)
		}
		t.Logf("%s: %d gates, %d lines, the same", c.name, len(c.ids), strings.Count(out.String(), "\n"))
	}
}

// TestCheckRows: -tables refuses what Check refuses, and a gate with no row of the gates table.
func TestCheckRows(t *testing.T) {
	rows, err := ReadRows(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		r   Request
		why string
	}{
		{Request{}, "the gate ids name no gate"},
		{Request{IDs: []string{calibration[0], "g20991231-000000"}},
			"no gate run g20991231-000000 in docs/tables/gates.jsonl"},
		{Request{IDs: calibration[:1], Evidence: "docs/tables/compare.jsonl"},
			"-evidence docs/tables/compare.jsonl is not a file in cells/results/ or docs/tables/gates.jsonl"},
		{Request{IDs: calibration, Evidence: TablesEvidence}, ""},
	} {
		err := CheckRows(rows, c.r)
		if c.why == "" && err != nil || c.why != "" && (err == nil || err.Error() != usageLine+": "+c.why) {
			t.Errorf("%v: %v, want %q", c.r, err, c.why)
		}
	}
	if _, err := ReadRows(os.DirFS(t.TempDir())); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no tables: %v, want a missing file", err)
	}
}
