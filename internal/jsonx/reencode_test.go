package jsonx

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// results is gate/results, from this package's directory.
var results = filepath.Join("..", "..", "gate", "results")

// judgeWritten is the files written in jq's default form in the committed runs here: each verdict.json, which
// the shell judge wrote with jq, or bin/laug judge in jq's form in the 21 runs that record v3, and each item
// verdict, which load/verdict.sh wrote with jq. The summary tables count them (tableCounts): 158 and 297, 455 in all;
// the files here must be the ones the rows of the runs here count. all is whether every run of the gates table is
// here.
func judgeWritten(t *testing.T) (files []string, all bool) {
	t.Helper()
	gates, err := filepath.Glob(filepath.Join(results, "*", "verdict.json"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := filepath.Glob(filepath.Join(results, "*", "*", "*.verdict.json"))
	if err != nil {
		t.Fatal(err)
	}
	var here []string
	for _, g := range gates {
		here = append(here, filepath.Base(filepath.Dir(g)))
	}
	rows, rowItems, hereItems := tableCounts(t, here)
	if rows != 158 || rowItems != 297 {
		t.Fatalf("docs/tables gives %d gates rows with a verdict.json and %d load items with a verdict, want 158 and "+
			"297", rows, rowItems)
	}
	if len(items) != hereItems {
		t.Fatalf("gate/results holds %d verdict.json and %d item verdicts; the rows of those runs count %d item "+
			"verdicts", len(gates), len(items), hereItems)
	}
	return slices.Concat(gates, items), len(gates) == rows
}

// tableCounts reads docs/tables: the gates rows that record a verdict.json, which must name each run of here, the
// load items with a verdict file in gate-items, and those of the runs of here.
func tableCounts(t *testing.T, here []string) (gates, items, hereItems int) {
	t.Helper()
	read := func(name string) []*Object {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", "tables", name+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		vs, err := ParseStream(data)
		if err != nil {
			t.Fatalf("docs/tables/%s.jsonl: %v", name, err)
		}
		var out []*Object
		for _, v := range vs {
			o, ok := v.Obj()
			if !ok {
				t.Fatalf("docs/tables/%s.jsonl: a row that is not an object", name)
			}
			out = append(out, o)
		}
		return out
	}
	get := func(o *Object, k string) Value { v, _ := o.Get(k); return v }
	str := func(v Value) string { s, _ := v.Str(); return s }
	named := map[string]bool{}
	for _, o := range read("gates") {
		if !get(o, "verdict_sha256").IsNull() {
			gates++
			named[str(get(o, "id"))] = true
		}
	}
	for _, id := range here {
		if !named[id] {
			t.Fatalf("gate/results/%s/verdict.json: no gates row that records one", id)
		}
	}
	for _, o := range read("gate-items") {
		if b, _ := get(o, "verdict_file").Bool(); !b || str(get(o, "kind")) != "load" {
			continue
		}
		items++
		if slices.Contains(here, str(get(o, "gate"))) {
			hereItems++
		}
	}
	return gates, items, hereItems
}

// expD is a d jq computed below 1e-4, which it printed in exponent form.
var expD = regexp.MustCompile(`^(\s*"d": )(-?[0-9]+(?:\.[0-9]+)?e[-+][0-9]+)(,?)$`)

// TestReencode reads each of the 455 files, or with runs archived each of those here, and writes it back as jq's
// default form, as `jq .` would print it. The bytes come back whole, but for the 87 comparison d values computed below
// 1e-4 and printed in exponent form (4.5e-05), 82 by jq and 5 by bin/laug judge, which writes jq's form: read back,
// each is a literal, so it prints in decNumber's canonical form (0.000045), which for these is the plain decimal, as
// strconv's shortest 'f' form writes it.
func TestReencode(t *testing.T) {
	files, changed, tokens := 0, 0, 0
	written, all := judgeWritten(t)
	for _, path := range written {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Parse(data)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		files++
		got := Marshal(v)
		if bytes.Equal(got, data) {
			continue
		}
		lines := strings.SplitAfter(string(data), "\n")
		n := 0
		for i, line := range lines {
			m := expD.FindStringSubmatch(strings.TrimSuffix(line, "\n"))
			if m == nil {
				continue
			}
			f, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			lines[i] = m[1] + strconv.FormatFloat(f, 'f', -1, 64) + m[3] + "\n"
			n++
		}
		if want := strings.Join(lines, ""); string(got) != want || n == 0 {
			t.Errorf("%s: written back with a difference beyond the exponent-form d values:\n%s", path,
				firstDiff(want, string(got)))
			continue
		}
		changed++
		tokens += n
	}
	if all && (files != 455 || changed != 64 || tokens != 87) {
		t.Errorf("%d files read, %d written back changed, with %d exponent-form d values; want 455, 64 and 87",
			files, changed, tokens)
	}
	if !all && files != len(written) {
		t.Errorf("%d files read of the %d here", files, len(written))
	}
	t.Logf("%d files: %d the same bytes, %d changed only in %d exponent-form d values", files, files-changed, changed,
		tokens)
}

// firstDiff is the first line where two texts differ, from each.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range min(len(w), len(g)) {
		if w[i] != g[i] {
			return "line " + strconv.Itoa(i+1) + ":\n  want " + w[i] + "\n  got  " + g[i]
		}
	}
	return "lengths " + strconv.Itoa(len(w)) + " and " + strconv.Itoa(len(g)) + " lines"
}

// TestReencodeMatchesJQ reads every JSON file under gate/results, each .json as one value and each .jsonl as a
// stream, and writes it in both of jq's forms; each must equal what jq 1.7 prints for the file, `jq .` and
// `jq -c .`.
func TestReencodeMatchesJQ(t *testing.T) {
	jq := needJQ(t)
	var paths []string
	err := filepath.WalkDir(results, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".jsonl")) {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string][]Value{}
	count := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, ".json") {
			v, err := Parse(data)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			values[path] = []Value{v}
		} else if values[path], err = ParseStream(data); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		count += len(values[path])
	}
	for _, form := range []struct {
		flag string
		enc  func(Value) []byte
	}{{"", Marshal}, {"-c", MarshalCompact}} {
		for chunk := range slices.Chunk(paths, 500) {
			args := slices.DeleteFunc([]string{form.flag, "."}, func(s string) bool { return s == "" })
			want := runJQ(t, jq, nil, append(args, chunk...)...)
			var got []byte
			for _, path := range chunk {
				for _, v := range values[path] {
					got = append(got, form.enc(v)...)
				}
			}
			if bytes.Equal(got, want) {
				continue
			}
			for _, path := range chunk { // find the file
				var one []byte
				for _, v := range values[path] {
					one = append(one, form.enc(v)...)
				}
				if w := runJQ(t, jq, nil, append(args, path)...); !bytes.Equal(one, w) {
					t.Errorf("jq %s %s differs:\n%s", strings.Join(args, " "), path, firstDiff(string(w), string(one)))
				}
			}
		}
	}
	t.Logf("%d files, %d values, each the same as jq's in both forms", len(paths), count)
}
