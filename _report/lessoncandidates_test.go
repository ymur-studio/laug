package report

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// tableLine is the line of docs/tables/<name>.jsonl whose key is key, with its LF.
func tableLine(t *testing.T, name, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, tablesPath, name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.SplitAfter(string(data), "\n") {
		if strings.HasPrefix(l, `{"id":"`+key+`",`) {
			return l
		}
	}
	t.Fatalf("docs/tables/%s.jsonl: no row %s", name, key)
	return ""
}

// b3Rows is the rauns table of boot 3's run label alone, its row with edit applied.
func b3Rows(t *testing.T, label string, edit func(string) string) figTable {
	t.Helper()
	line := tableLine(t, "rauns", b3Row(label))
	rauns, err := readFigTable(fstest.MapFS{tablesPath + "/rauns.jsonl": {Data: []byte(edit(line))}}, "rauns")
	if err != nil {
		t.Fatal(err)
	}
	return rauns
}

// TestReadCandidate: a run reads each judged row as its row records it, which decided and which its blocks_on names,
// and is refused when its rows are not the figure's columns, its deciding rows are not the rows that block, it is
// another run, or a row has no value and does not read not produced.
func TestReadCandidate(t *testing.T) {
	same := func(s string) string { return s }
	r, err := readCandidate(b3Rows(t, "benign-lab3-1", same), "benign-lab3-1")
	if err != nil {
		t.Fatal(err)
	}
	if r.runID != "lab3-1" || r.server != "1.31.0" || r.from != "2400" || r.to != "2000" || r.wait != 126 ||
		r.reread != 63 || len(r.cells) != len(candidateCols) || r.cells[0].value.String() != "19.040" ||
		r.cells[6].bound != 5700 || !r.cells[6].floor || r.cells[7].bound != 0 {
		t.Errorf("read %+v", r)
	}
	for _, c := range r.cells {
		if c.decided || c.named {
			t.Errorf("benign-lab3-1: a row decided, %+v", c)
		}
	}
	const v1321 = "persistence-starved-lab3-v1321"
	r, err = readCandidate(b3Rows(t, v1321, same), v1321)
	if err != nil {
		t.Fatal(err)
	}
	var decided, named []string
	for j, c := range r.cells {
		if c.decided {
			decided = append(decided, candidateCols[j].key)
		}
		if c.named {
			named = append(named, candidateCols[j].key)
		}
	}
	if r.cells[1].value.OK || !strings.HasPrefix(r.cells[1].result, "not produced") || !slices.Equal(decided,
		[]string{"workflow_task_schedule_to_start_p99_ms", "workflow_end_to_end_p95_ms", "service_error_rate",
			"workflows_completed", "omes_exit", "workflows_not_completed"}) ||
		!slices.Equal(named, []string{"service_error_rate", "omes_exit"}) {
		t.Errorf("activity s2s %+v; decided %v, named %v", r.cells[1], decided, named)
	}
	for _, c := range []struct {
		label string
		edit  func(string) string
		want  string
	}{
		{v1321, func(s string) string {
			return strings.Replace(s, `"omes_exit","workflows_not_completed"`, `"omes_exit"`, 1)
		}, "against the rows that block"},
		{"benign-lab3-1", func(s string) string {
			return strings.Replace(s, `49.977,"limit":"84"`, `49.977,"limit":"null"`, 1)
		}, "the rows judged against a limit"},
		{"benign-lab3-1", func(s string) string {
			return strings.Replace(s, `"run_id":"lab3-1"`, `"run_id":"lab3-2"`, 1)
		}, "not candidate benign-lab3-1's run"},
		{v1321, func(s string) string {
			return strings.Replace(s, `"value":7.994,`, `"value":null,`, 1)
		}, "has no value"},
	} {
		if _, err := readCandidate(b3Rows(t, c.label, c.edit), c.label); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.label, err, c.want)
		}
	}
}

// TestReadBoot3: each counted run must have run on the lab host by src/hosts.tsv, and boot 3's readings must name a C
// step for each counted run and the warm-up alone besides.
func TestReadBoot3(t *testing.T) {
	read := func(p string) string {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	readings := tablesPath + "/" + boot3Readings + ".jsonl"
	hosts, rows := read(hostsTSV), read(readings)
	checkout := func(hosts, rows string) fstest.MapFS {
		return fstest.MapFS{hostsTSV: {Data: []byte(hosts)}, readings: {Data: []byte(rows)}}
	}
	got, err := readBoot3(checkout(hosts, rows))
	if err != nil || !slices.Equal(got, []string{readings}) {
		t.Fatalf("readBoot3: %v, %v", got, err)
	}
	dropped := tableLine(t, boot3Readings, "C-benign-lab3-2/candidate_benign_lab3-2")
	for _, c := range []struct {
		name, hosts, rows, want string
	}{
		{"a counted run dropped", hosts, strings.Replace(rows, dropped, "", 1), "want a C step for each of the 14 counted runs"},
		{"another host", strings.Replace(hosts, "candidate-*-lab3-*\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)",
			"candidate-*-lab3-*\tthe dev host, a Linux VM with 24 vCPU and 47 GiB running Docker", 1), rows,
			"not the lab host"},
		{"no host", strings.Replace(hosts, "candidate-*-lab3-*", "candidate-*-lab4-*", 1), rows,
			"no line names the host of candidate-benign-lab3-1"},
	} {
		if c.hosts == hosts && c.rows == rows {
			t.Fatalf("%s: the edit changed nothing", c.name)
		}
		if _, err := readBoot3(checkout(c.hosts, c.rows)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
