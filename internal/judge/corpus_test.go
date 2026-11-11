package judge

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// The mutation corpus: small edits of 3 committed runs, each judged under its base run's
// version and under v3 (corpusPasses). Each pass's file in testdata keeps the archived shell judges' answers on every
// case; TestCorpusAnswers holds Go's to the same files.
//
// The edits, per base: each jq path of the rules inventory's section 7 table that the base's version reads is set to
// null, false, 0, "x", [] and {}, and deleted; one element of each array the judge walks is set to {} and to [];
// each input file is removed, emptied, doubled, cut in half, made a directory and made unreadable; numbers take other
// spellings of the same or a nearby value (RFC 8259 spellings only: jq 1.7 also reads 01, 1., .5 and nan, which the
// reader refuses on purpose, TestReaderDifferences) and a NUL byte, which
// ends a number token in both; strings take bad UTF-8, escapes and deep nesting; item directories are removed,
// renamed, linked and added; the run directory is removed or made a file; the gate id takes names the gid rule
// refuses or that name no file; R7's start times take edge dates of fromdateiso8601; and a few edits reach join,
// render and the class table.

// corpusBases are the runs the corpus edits: a v2 run whose comparison is judging, a v2 run that records its
// comparison, both ADVANCE with an incumbent arm, a load item and a fault item; and a v1 run that BLOCKs on a load
// item (class regression) and a fault item that was never steady, a dev host run read from its copy (runDir).
var corpusBases = []struct{ name, gid, version string }{
	{"judging", "g20261018-075357", "v2"},
	{"recording", "g20261017-172556", "v2"},
	{"v1", "g20261014-215905", "v1"},
}

// A mutant is one case of the corpus: a copy of a base run with one edit.
type mutant struct {
	base int    // the index of its base in corpusBases
	name string // "<base> <what the edit does>", unique: the line testdata/shell-corpus.txt keeps
	edit func(t *testing.T, dir string)
	// perm says the edit takes a permission away, which a root user keeps: the file is not unreadable then.
	perm bool
}

// variants are the values each corpus path is set to: one of each jq type, and 0 and "x" for the number and the
// string. A path is also deleted.
var variants = []string{"null", "false", "0", `"x"`, "[]", "{}"}

// corpusPaths are the jq paths each base's edits set to variants and delete, by file: "<file>: <path> ...". A file
// "<key>/run" is the item's <gid>-<key>.json and "<key>/verdict" its <gid>-<key>.verdict.json. In the v2 bases
// plan.json's items are incumbent, steady and pl; in v1 steady and pl. thresholds[4] is service_error_rate. A path
// ending in + is not in the base's file: it is set, and not deleted. A variant equal to the base's own value is no
// edit, and is left out. Each path is one the base's judge reads: a path it reads only after another edit is in
// judgingMutants, after that edit.
var corpusPaths = map[string][]string{
	"judging": {
		"gate.json: gate_id server_tag refused items_limit fake laug_commit tree_dirty base inputs host host.type " +
			"host.vcpu host.mem_gib",
		"clock.json: t0 t0.wall t0.mono t_post t_post.wall t_post.mono",
		"plan.json: deadline_s items_in_plan incumbent items items[0].key items[0].kind items[0].expect items[0].arm " +
			"items[0].pair items[0].scenario items[0].rate items[0].max_concurrent items[0].duration items[1].key " +
			"items[2].kind items[2].fault items[2].scenario",
		"thresholds.json: thresholds thresholds[4].name thresholds[4].max resolved " +
			"resolved.release_file resolved.host resolved.host.type resolved.host.mem_gib compare compare.release " +
			"compare.wave compare.gates compare.evidence compare.rows compare.rows[0].name compare.rows[0].scale " +
			"compare.rows[0].floor compare.rows[0].tolerance",
		"thresholds-incumbent.json: resolved resolved.release_file",
		"harness.json: exit",
		"poll.jsonl: [0].busy [0].projects",
		"steady/lease.json: id project block",
		"steady/exit.json: exit stopped_by end end.wall",
		"steady/run: server_version start partial+",
		"incumbent/run: start",
		"pl/run: result partial+ server_tag during.service_error_rate pool_busy thresholds_release",
		"steady/verdict: verdict exit short_window thresholds_release readings " +
			"readings.workflow_task_schedule_to_start_p99_ms window_s",
		"incumbent/verdict: verdict readings window_s",
	},
	"recording": {
		"thresholds.json: compare compare.release compare.rows[0].tolerance",
		"plan.json: incumbent items[0].pair",
		"incumbent/verdict: verdict window_s readings",
	},
	"v1": {
		"gate.json: gate_id server_tag refused items_limit fake tree_dirty host",
		"clock.json: t0 t_post.mono",
		"plan.json: deadline_s items_in_plan items items[0].key items[0].kind items[1].expect",
		"thresholds.json: thresholds thresholds[4].max resolved resolved.release_file",
		"harness.json: exit",
		"poll.jsonl: [0].busy [0].projects",
		"steady/lease.json: id",
		"steady/exit.json: exit stopped_by end.wall",
		"steady/run: server_version partial+",
		"pl/run: result reason partial+ server_tag during.service_error_rate+ pool_busy thresholds_release",
		"steady/verdict: verdict exit short_window rows rows[0].blocks rows[0].metric rows[0].value rows[0].result " +
			"thresholds_release",
	},
}

// corpusElements are the arrays one element of which is set to {} and to [], by base.
var corpusElements = map[string][]string{
	"judging": {"gate.json: inputs[0]", "plan.json: items[1]", "thresholds.json: thresholds[4] compare.rows[0] " +
		"compare.wave[0]", "poll.jsonl: [0] [0].projects[0]", "steady/verdict: rows[0]"},
	"v1": {"plan.json: items[0]", "thresholds.json: thresholds[4]", "poll.jsonl: [0]", "steady/verdict: rows[0]"},
}

// corpusFiles are the input files each base's file edits take, by base.
var corpusFiles = map[string][]string{
	"judging": {"gate.json", "clock.json", "plan.json", "thresholds.json", "thresholds-incumbent.json",
		"harness.json", "poll.jsonl", "incumbent/run", "incumbent/verdict", "steady/lease.json", "steady/exit.json",
		"steady/run", "steady/verdict", "pl/exit.json", "pl/run"},
	"recording": {"thresholds-incumbent.json", "incumbent/verdict"},
	"v1": {"gate.json", "clock.json", "plan.json", "thresholds.json", "harness.json", "poll.jsonl",
		"steady/exit.json", "steady/verdict", "pl/run"},
}

// A fileEdit is one edit of a whole input file.
type fileEdit struct {
	name string
	perm bool
	edit func(t *testing.T, p string)
}

var fileEdits = []fileEdit{
	{"removed", false, func(t *testing.T, p string) { must(t, os.Remove(p)) }},
	{"emptied", false, func(t *testing.T, p string) { must(t, os.WriteFile(p, nil, 0o644)) }},
	{"doubled", false, func(t *testing.T, p string) {
		data := readAll(t, p)
		must(t, os.WriteFile(p, append(data, data...), 0o644))
	}},
	{"cut in half", false, func(t *testing.T, p string) {
		data := readAll(t, p)
		must(t, os.WriteFile(p, data[:len(data)/2], 0o644))
	}},
	{"a directory", false, func(t *testing.T, p string) { must(t, os.Remove(p)); must(t, os.Mkdir(p, 0o755)) }},
	{"unreadable", true, func(t *testing.T, p string) { chmod(t, p, 0) }},
}

// mutants is the corpus, in a fixed order.
func mutants(t *testing.T) []mutant {
	t.Helper()
	var ms []mutant
	for i, b := range corpusBases {
		g := &corpusGen{base: i, gid: b.gid, name: b.name}
		for _, line := range corpusPaths[b.name] {
			file, paths := splitLine(line)
			for _, path := range paths {
				path, absent := strings.CutSuffix(path, "+")
				own, held := g.baseValue(t, file, path)
				if held == absent {
					t.Fatalf("%s %s %s: in the base %v, marked absent %v", b.name, file, path, held, absent)
				}
				for _, v := range variants {
					if !held || string(jsonx.AppendCompact(nil, own)) != v {
						g.set(file, path, v)
					}
				}
				if held {
					g.del(file, path)
				}
			}
		}
		for _, line := range corpusElements[b.name] {
			file, paths := splitLine(line)
			for _, path := range paths {
				g.set(file, path, "{}")
				g.set(file, path, "[]")
			}
		}
		for _, file := range corpusFiles[b.name] {
			for _, e := range fileEdits {
				g.add(file+" "+e.name, e.perm, func(t *testing.T, dir string) { e.edit(t, g.path(dir, file)) })
			}
		}
		switch b.name {
		case "judging":
			judgingMutants(g)
		case "recording":
			recordingMutants(g)
		case "v1":
			v1Mutants(g)
		}
		ms = append(ms, g.ms...)
	}
	return ms
}

// judgingMutants are the judging base's edits beyond the tables: number spellings, strings, item directories, the
// gate id, R7's dates and the edits that reach join, render and the class table.
func judgingMutants(g *corpusGen) {
	// Numbers. Two literals compare as decimals in jq 1.7, not as doubles: a max, a floor, an exit of 0
	// or 2 and a busy count each meet a literal that is equal as a double and not as a decimal.
	for _, c := range []struct{ file, path, texts string }{
		{"clock.json", "t0.wall", "1792310037.4480 1.792310037448e9 1792310037448e-3 1792310037.448E0 " +
			"1e400 -0"},
		{"clock.json", "t_post.mono", "6178.160 6.17816E+3 6178.16000000000000000001"},
		{"plan.json", "deadline_s", "780.0 7.8e2 7.80E+2 1e400 -0 1E-400 527.1 527.0999999999999"},
		{"plan.json", "items_in_plan", "3.0 3e0 2.9999999999999999999 3.0000000000000000001 4 2"},
		{"harness.json", "exit", "0.0 -0 0e5 1e-400 1"},
		{"thresholds.json", "thresholds[4].max", "0.0010 1e-3 1E-3 0.20962544362398816 0.20962544362398815 " +
			"0.2096254436239881600001"},
		{"pl/run", "during.service_error_rate", "2.0962544362398816e-1 0.0010 1e-3 0.0010000000000000000001 -0"},
		{"thresholds.json", "compare.rows[0].tolerance", "0.1 0.09999999999999999999 1.020 1.02E0 -0 1e-400 " +
			"0.2 0.19999999999999999999"},
		{"thresholds.json", "compare.rows[0].floor", "-0 1e-400 1.02 1.0200000000000000001"},
		{"steady/exit.json", "exit", "0.0 -0 0e0 2.0 3 1e-400"},
		{"steady/verdict", "exit", "0.0 -0 1"},
		{"steady/verdict", "readings.workflow_task_schedule_to_start_p99_ms", "1.6886543382355622E+1 " +
			"17.086628703175624 17.0866287031756239 17.0866287031756241 18.106628703175624 18.10662870317562 -0 1e400"},
		{"steady/verdict", "readings.activity_schedule_to_start_over_10ms", "0.016465956266924724 0.0164659 " +
			"0.009220784988040798 9.220784988040798e-3"},
		{"steady/verdict", "readings.workflows_completed", "5954 5954.0 1e16 5953.5"},
		{"steady/verdict", "window_s", "301.0 3.01E+2 303 304 303.0000000000000000001 299"},
		{"gate.json", "host.mem_gib", "123.0 124 124.0000000000001 122"},
		{"gate.json", "host.vcpu", "32.0 3.2e1 31"},
		{"gate.json", "items_limit", "2 2.0 3e0"},
		{"poll.jsonl", "[0].busy", "0.0 -0 1e-400 1 2.50"},
		{"steady/lease.json", "block", "1.0 1e400 -0"},
		{"pl/run", "pool_busy.during", "1 0.5 -1 1e-400"},
	} {
		for _, text := range strings.Fields(c.texts) {
			g.set(c.file, c.path, text)
		}
	}
	// A NUL byte ends a number token in jq 1.7, which hands the token to decNumber as a C string, and in the reader
	// (TestReaderDifferences): 780 and a NUL reads 780, 7, a NUL and 80 reads 7, and a NUL first is no number. In
	// true it is an error in both.
	for _, c := range [][3]string{
		{"plan.json", "deadline_s", `780\x00`}, {"plan.json", "deadline_s", `7\x0080`},
		{"plan.json", "deadline_s", `\x00780`}, {"poll.jsonl", "[0].busy", `0\x001`}, {"gate.json", "fake", `true\x00`},
	} {
		g.setLabel(c[0], c[1], strings.ReplaceAll(c[2], `\x00`, "\x00"), c[2])
	}
	// Paths the base never reads, each set to variants and deleted after the edits that make the judge read it: a
	// fault's reason only when it BLOCKs (ownOf, reasonOf), its cell only when its lease has no id (item), no_data
	// only for a reading one arm lacks (nj), and a load verdict's rows only when it BLOCKs (ownOf).
	plBlocks := [][3]string{{"pl/run", "result", `"BLOCK"`}}
	steadyBlocks := [][3]string{{"steady/verdict", "verdict", `"BLOCK"`}}
	releaseLacks := [][3]string{{"steady/verdict", "readings.persistence_latency_p99_ms", "null"}}
	incumbentLacks := [][3]string{{"incumbent/verdict", "readings.persistence_latency_p99_ms", "null"}}
	for _, c := range []struct {
		pre        [][3]string
		file, path string
	}{
		{plBlocks, "pl/run", "reason"},
		{[][3]string{{"pl/lease.json", "id", "null"}}, "pl/run", "cell"},
		{releaseLacks, "thresholds.json", "thresholds[3].no_data"},
		{incumbentLacks, "thresholds-incumbent.json", "thresholds"},
		{incumbentLacks, "thresholds-incumbent.json", "thresholds[3].no_data"},
		{steadyBlocks, "steady/verdict", "rows"},
		{steadyBlocks, "steady/verdict", "rows[0].blocks"},
		{append(steadyBlocks, [3]string{"steady/verdict", "rows[0].blocks", "true"}), "steady/verdict",
			"rows[0].value"},
	} {
		for _, v := range variants {
			g.setAfter(c.pre, c.file, c.path, v, v)
		}
		g.delAfter(c.pre, c.file, c.path)
	}
	// Booleans the judge compares with true (fake in output, tree_dirty in Render, partial), which no variant is.
	for _, c := range [][2]string{{"gate.json", "fake"}, {"gate.json", "tree_dirty"}, {"steady/run", "partial"},
		{"pl/run", "partial"}} {
		g.set(c[0], c[1], "true")
	}
	// Whole files: other top-level values, a BOM, streams and links.
	for _, c := range []struct{ file, text string }{
		{"gate.json", "[1]\n"}, {"gate.json", `"unreadable"` + "\n"}, {"gate.json", "null\n"}, {"plan.json", "0\n"},
		{"clock.json", "[]\n"}, {"harness.json", " \n\t\n"}, {"poll.jsonl", "{}{}\n[]\n"},
		{"poll.jsonl", "{\"busy\":\n0,\n\"projects\":[]}\n\n\n1 2\n"},
	} {
		g.add(c.file+" holds "+jsonText(c.text), false, func(t *testing.T, dir string) {
			must(t, os.WriteFile(g.path(dir, c.file), []byte(c.text), 0o644))
		})
	}
	g.add("plan.json starts with a BOM", false, func(t *testing.T, dir string) {
		p := g.path(dir, "plan.json")
		must(t, os.WriteFile(p, append([]byte("\xef\xbb\xbf"), readAll(t, p)...), 0o644))
	})
	g.add("harness.json a dangling link", false, func(t *testing.T, dir string) {
		p := g.path(dir, "harness.json")
		must(t, os.Remove(p))
		must(t, os.Symlink("nowhere.json", p))
	})
	g.add("thresholds.json a link to a copy", false, func(t *testing.T, dir string) {
		p := g.path(dir, "thresholds.json")
		must(t, os.Rename(p, g.path(dir, ".thresholds.json")))
		must(t, os.Symlink(".thresholds.json", p))
	})
	// Strings: what the reader makes of bad UTF-8 and escapes, and what the writer escapes, through a fault's
	// reason, which reaches verdict.json and a verdict.md cell.
	for _, c := range []struct{ label, text string }{
		{"bytes ff fe", "\"a\xff\xfeb\""},
		{"a cut sequence", "\"a\xe2\x82b\""},
		{"an overlong form", "\"a\xc0\xafb\""},
		{"an encoded surrogate", "\"a\xed\xa0\x80b\""},
		{"a lone high surrogate escape", `"a\ud800b"`},
		{"a lone low surrogate escape", `"a\udc00b"`},
		{"a surrogate pair escape", `"a\ud83d\ude00b"`},
		{"U+2028, <, > and &", "\"a\u2028<b>&c\""},
		{"control bytes", `"a\u0001\u001f\u007f\tb\r\n"`},
		{"a | and a newline", `"never steady: a | b\nc"`},
		{"a BOM in the middle", "\"a\xef\xbb\xbfb\""},
		{"247 nested arrays", strings.Repeat("[", 247) + strings.Repeat("]", 247)},
		{"248 nested arrays", strings.Repeat("[", 248) + strings.Repeat("]", 248)},
		{"249 nested arrays", strings.Repeat("[", 249) + strings.Repeat("]", 249)},
	} {
		g.setAfter(plBlocks, "pl/run", "reason", c.text, c.label)
	}
	// Reasons of the class table's patterns that are not literal prefixes (classes.go), and a reason's first clause.
	for _, reason := range []string{`"recovered in 30 s, not within 20 s"`, `"recovered in x"`,
		`"12 failed iteration(s) after the fault"`, `"failed iteration(s) "`, `"sweep found no workflows after x"`,
		`"control: x; harness: y"`, `"refused: x"`, `"never steady: x\n"`, `"never steady:x"`} {
		g.setAfter(plBlocks, "pl/run", "reason", reason, reason)
	}
	// The run directory itself: Judge refuses one that is not a directory (ErrNoRunDir).
	g.add("the run directory removed", false, func(t *testing.T, dir string) { must(t, os.RemoveAll(dir)) })
	g.add("the run directory a file", false, func(t *testing.T, dir string) {
		must(t, os.RemoveAll(dir))
		must(t, os.WriteFile(dir, []byte("{}\n"), 0o644))
	})
	// Item directories.
	g.add("steady/ removed", false, func(t *testing.T, dir string) { must(t, os.RemoveAll(g.path(dir, "steady"))) })
	g.add("incumbent/ removed", false, func(t *testing.T, dir string) {
		must(t, os.RemoveAll(g.path(dir, "incumbent")))
	})
	g.add("pl/ removed", false, func(t *testing.T, dir string) { must(t, os.RemoveAll(g.path(dir, "pl"))) })
	g.add("steady/ a file", false, func(t *testing.T, dir string) {
		must(t, os.RemoveAll(g.path(dir, "steady")))
		must(t, os.WriteFile(g.path(dir, "steady"), []byte("{}\n"), 0o644))
	})
	g.add("steady/ a link to a dot directory", false, func(t *testing.T, dir string) {
		must(t, os.Rename(g.path(dir, "steady"), g.path(dir, ".steady")))
		must(t, os.Symlink(".steady", g.path(dir, "steady")))
	})
	g.add("steady/ unreadable", true, func(t *testing.T, dir string) { chmod(t, g.path(dir, "steady"), 0) })
	g.add("steady/ not searchable", true, func(t *testing.T, dir string) {
		chmod(t, g.path(dir, "steady"), 0o644)
	})
	for _, name := range []string{"zz", "a", "a-b", "incumbent-2", "Z"} {
		g.add("an empty directory "+name, false, func(t *testing.T, dir string) {
			must(t, os.Mkdir(g.path(dir, name), 0o755))
		})
	}
	g.add("a dot directory with an item's files", false, func(t *testing.T, dir string) {
		copyDir(t, g.path(dir, "pl"), g.path(dir, ".pl"))
	})
	g.add("a dangling link zz", false, func(t *testing.T, dir string) {
		must(t, os.Symlink("nowhere", g.path(dir, "zz")))
	})
	g.add("empty directories a and a-b", false, func(t *testing.T, dir string) {
		must(t, os.Mkdir(g.path(dir, "a"), 0o755))
		must(t, os.Mkdir(g.path(dir, "a-b"), 0o755))
	})
	// "steady\n/" sorts before "steady/" in the glob, and both are the key steady: the object keeps the first place
	// and the last value, the real steady's.
	g.add(`a copy of steady as "steady\n" without exit.json`, false, func(t *testing.T, dir string) {
		copyDir(t, g.path(dir, "steady"), g.path(dir, "steady\n"))
		must(t, os.Remove(g.path(dir, "steady\n/exit.json")))
	})
	g.add("a copy of pl as pl-2", false, func(t *testing.T, dir string) {
		copyDir(t, g.path(dir, "pl"), g.path(dir, "pl-2"))
	})
	g.add(`steady/ named "steady\n"`, false, func(t *testing.T, dir string) {
		must(t, os.Rename(g.path(dir, "steady"), g.path(dir, "steady\n")))
	})
	// Renamed items: the directory, the plan's key, the files, the pair, alone and together.
	g.add("steady/ renamed release/", false, func(t *testing.T, dir string) { g.renameDir(t, dir, "steady", "release") })
	g.add("plan key steady renamed release", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "plan.json", "items[1].key", `"release"`)
	})
	g.add("steady renamed release, its files too", false, func(t *testing.T, dir string) {
		g.renameItem(t, dir, "steady", "release", 1)
	})
	g.add("steady renamed release, its files and the pair too", false, func(t *testing.T, dir string) {
		g.renameItem(t, dir, "steady", "release", 1)
		g.setIn(t, dir, "plan.json", "items[0].pair", `"release"`)
	})
	g.add("incumbent renamed a, its files too", false, func(t *testing.T, dir string) {
		g.renameItem(t, dir, "incumbent", "a", 0)
	})
	g.add("pl renamed zz, its files too", false, func(t *testing.T, dir string) { g.renameItem(t, dir, "pl", "zz", 2) })
	for _, key := range []string{`"Steady"`, `"steady\n"`, `"st.eady"`, `"-steady"`, `"steady "`, `"incumbent"`,
		`"pl"`} {
		g.set("plan.json", "items[1].key", key)
	}
	g.set("plan.json", "items[0].pair", `"pl"`)
	g.set("plan.json", "items[0].pair", `"incumbent"`)
	g.set("plan.json", "items[1].arm", `"incumbent"`)
	g.set("plan.json", "items[2].kind", `"load"`)
	g.set("plan.json", "items[1].kind", `"fault"`)
	g.set("plan.json", "items[1].expect", `"BLOCK"`)
	g.set("plan.json", "items[2].expect", `"BLOCK"`)
	// The gate id: names the gid rule (isName) refuses, and names of no file.
	for _, id := range []string{`"g2"`, `"G20261018-075357"`, `"g20261018-075357\n"`, `" g20261018-075357"`,
		`"g20261018-075357 "`, `"../g"`, `"g20261018-075357\u0000"`, "1.50", "true", `""`} {
		g.set("gate.json", "gate_id", id)
	}
	// R7's start times (epoch): fromdateiso8601 is glibc strptime and timegm, and jq checks one byte after Z.
	for _, date := range []string{
		"2026-10-18T7:57:42Z", " 2026-10-18T07:57:42Z", "\t2026-10-18T07:57:42Z", "2026-10-18T07: 57:42Z",
		"2026-10-18T07:57:42Z\n", "2026-10-18T07:57:42Z xyz", "2026-10-18T07:57:42Z\njunk", "2026-10-18T07:57:42Zxyz",
		"2026-10-18T07:57:42.5Z", "2026-10-18T07:57:42+00:00", "2026-10-18T07:57:42", "2026-10-18t07:57:42Z",
		"2026-10-18T07:57:42z", "2026-10-18 07:57:42Z", "2026-02-31T07:57:42Z", "2026-10-18T07:57:60Z",
		"2026-10-18T07:57:61Z", "2026-10-18T07:57:62Z", "2026-10-18T24:00:00Z", "2026-13-18T07:57:42Z",
		"2026-10-00T07:57:42Z", "2026-10-18T07:57:43Z", "2026-10-18T07:57:44Z", "2026-10-18T07:57:45Z",
		"2026-10-18T07:57:40Z", "1969-12-31T23:59:59Z", "1969-12-31T23:59:58Z", "1969-12-31T23:59:57Z",
		"1970-01-01T00:00:00Z", "0000-01-01T00:00:00Z", "10000-01-01T00:00:00Z", "2026-1-8T7:7:4Z",
		"+2026-10-18T07:57:42Z", "2038-01-19T03:14:08Z", "",
	} {
		g.set("steady/run", "start", jsonText(date))
	}
	// Edits that reach join (whyOf, notesOf), render (comparison) and the rows' forms.
	both := func(extra ...[3]string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			g.setIn(t, dir, "steady/verdict", "verdict", `"BLOCK"`)
			g.setIn(t, dir, "incumbent/verdict", "verdict", `"BLOCK"`)
			for _, e := range extra {
				g.setIn(t, dir, e[0], e[1], e[2])
			}
		}
	}
	g.add("both arms BLOCK", false, both())
	g.add("both arms BLOCK, deciding [{}]", false, both([3]string{"incumbent/verdict", "deciding", `[{}]`}))
	g.add(`both arms BLOCK, deciding [1,"a",null,true]`, false,
		both([3]string{"incumbent/verdict", "deciding", `[1,"a",null,true]`}))
	g.add(`both arms BLOCK, deciding "x"`, false, both([3]string{"incumbent/verdict", "deciding", `"x"`}))
	g.add("a wave of [1,null,true] in thresholds.json", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "thresholds.json", "compare.wave", `[1,null,true]`)
	})
	g.add(`host "x" and a set with no host`, false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "gate.json", "host", `"x"`)
		g.setIn(t, dir, "thresholds.json", "resolved.host", "null")
	})
	g.add("host type null and a set with no host", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "gate.json", "host.type", "null")
		g.setIn(t, dir, "thresholds.json", "resolved.host", "null")
	})
	g.add("compare release 1.30.0", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "thresholds.json", "compare.release", `"1.30.0"`)
	})
	g.add("a reading the release lacks, not judged", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "steady/verdict", "readings.persistence_latency_p99_ms", "null")
		g.setIn(t, dir, "thresholds.json", "thresholds[3].no_data", `"not_judged"`)
	})
	g.add("a reading the release lacks", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "steady/verdict", "readings.persistence_latency_p99_ms", "null")
	})
	g.add("a reading the incumbent lacks", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "incumbent/verdict", "readings.persistence_latency_p99_ms", "null")
	})
	g.add("two worse readings", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "steady/verdict", "readings.persistence_latency_p99_ms", "100")
		g.setIn(t, dir, "steady/verdict", "readings.workflow_end_to_end_over_50ms", "0.5")
	})
	g.add("a compare row of no threshold", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "thresholds.json", "compare.rows[0].name", `"made_up_ms"`)
		g.setIn(t, dir, "steady/verdict", "readings.made_up_ms", "3")
		g.setIn(t, dir, "incumbent/verdict", "readings.made_up_ms", "1")
	})
	g.add("a poll of busy 2 and an unleased project", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "poll.jsonl", "[0].busy", "2")
		g.setIn(t, dir, "poll.jsonl", "[1].projects", `["laug-zz","laug-1f4b7921",1,{}]`)
	})
	g.add("refused, and a fault item that BLOCKs", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "gate.json", "refused", `"at preflight: a | b"`)
		g.setIn(t, dir, "pl/run", "result", `"BLOCK"`)
	})
	g.add("watchdog stopped steady", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "steady/exit.json", "stopped_by", `"watchdog"`)
		g.setIn(t, dir, "steady/exit.json", "exit", "137")
	})
	g.add("ITEMS cut the plan", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "plan.json", "items_in_plan", "5")
		g.setIn(t, dir, "gate.json", "items_limit", "3")
	})
	// Readings a hair below the incumbent's, so each d rounds to -0 (rd): a compare row in ms and in a share,
	// another threshold in ms, and a count. v2 prints the first three 0.0-0 or 0.00-0 points and v3 as zero
	// (ZeroFix); a count's d prints -0 under both.
	for _, c := range [][2]string{{"persistence_latency_p99_ms", "7.9751"},
		{"activity_schedule_to_start_over_10ms", "0.0092207"}, {"workflow_end_to_end_p95_ms", "49.3159"},
		{"workflows_completed", "5953.6"}} {
		g.set("steady/verdict", "readings."+c[0], c[1])
	}
}

// recordingMutants are the recording base's edits beyond the tables: R7's dates and pairing in recording mode.
func recordingMutants(g *corpusGen) {
	for _, date := range []string{"2026-10-17T17:29:45Z", "2026-10-17T17:29:42.5Z", "1969-12-31T23:59:59Z",
		"1969-12-31T23:59:57Z"} {
		g.set("incumbent/run", "start", jsonText(date))
	}
	g.set("incumbent/verdict", "window_s", "304")
	g.set("plan.json", "items[0].pair", `"pl"`)
	// A recorded d that rounds to -0: 0.0-0 under v2, 0.000 under v3.
	g.set("steady/verdict", "readings.workflow_end_to_end_p95_ms", "49.2638")
	g.add("both arms BLOCK", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "steady/verdict", "verdict", `"BLOCK"`)
		g.setIn(t, dir, "incumbent/verdict", "verdict", `"BLOCK"`)
	})
}

// v1Mutants are the v1 base's edits beyond the tables.
func v1Mutants(g *corpusGen) {
	for _, c := range []struct{ file, path, texts string }{
		{"harness.json", "exit", "0.0 1e-400"},
		{"plan.json", "deadline_s", "7.8e2 539.5 539.6"},
		{"pl/exit.json", "exit", "1.0 1e0 2"},
		{"steady/verdict", "rows[1].value", "28850.0000 2.8850000E+4 null"},
		{"thresholds.json", "thresholds[4].max", "1e-3 0.048283842817079384"},
		{"pl/run", "during.service_error_rate", "0.0010 1e-3"},
	} {
		for _, text := range strings.Fields(c.texts) {
			g.set(c.file, c.path, text)
		}
	}
	for _, c := range [][2]string{{"gate.json", "fake"}, {"gate.json", "tree_dirty"}, {"steady/run", "partial"},
		{"pl/run", "partial"}} {
		g.set(c[0], c[1], "true")
	}
	g.set("gate.json", "gate_id", `"g2"`)
	g.set("gate.json", "gate_id", `"g20261014-215905\n"`)
	g.add("steady/ renamed release/", false, func(t *testing.T, dir string) { g.renameDir(t, dir, "steady", "release") })
	g.add("steady/ removed", false, func(t *testing.T, dir string) { must(t, os.RemoveAll(g.path(dir, "steady"))) })
	g.add("an empty directory zz", false, func(t *testing.T, dir string) { must(t, os.Mkdir(g.path(dir, "zz"), 0o755)) })
	g.add("a sweep that failed", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "steady/verdict", "rows[0].metric", `"workflows_not_completed"`)
		g.setIn(t, dir, "steady/verdict", "rows[0].result", `"the sweep at 2026-10-14T22:04:00Z failed\n"`)
	})
	g.add("an arm in a v1 plan", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "plan.json", "items[0].arm", `"incumbent"`)
		g.setIn(t, dir, "plan.json", "incumbent", `"1.30.0"`)
	})
	g.add("a gate.json host of another type", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "gate.json", "host", `{"type":"x","vcpu":1,"mem_gib":1}`)
	})
	g.add("a reason of class incumbent", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "pl/run", "reason", `"incumbent: x"`)
	})
	g.add("a reason of class not compared", false, func(t *testing.T, dir string) {
		g.setIn(t, dir, "pl/run", "reason", `"not compared: x"`)
	})
}

// A corpusGen builds one base's mutants.
type corpusGen struct {
	base      int
	gid, name string
	ms        []mutant
}

// add adds the mutant "<base> <what>".
func (g *corpusGen) add(what string, perm bool, edit func(t *testing.T, dir string)) {
	g.ms = append(g.ms, mutant{base: g.base, name: g.name + " " + what, edit: edit, perm: perm})
}

// set adds the mutant that sets path in file to text, a JSON text written into the file as it is.
func (g *corpusGen) set(file, path, text string) {
	g.setLabel(file, path, text, text)
}

// setLabel is set with label naming the value.
func (g *corpusGen) setLabel(file, path, text, label string) {
	g.setAfter(nil, file, path, text, label)
}

// setAfter is setLabel after the edits pre, each {file, path, text}; the case's name lists them first.
func (g *corpusGen) setAfter(pre [][3]string, file, path, text, label string) {
	g.add(preName(pre)+file+" "+path+"="+label, false, func(t *testing.T, dir string) {
		g.apply(t, dir, pre)
		g.setIn(t, dir, file, path, text)
	})
}

// delAfter is del after the edits pre.
func (g *corpusGen) delAfter(pre [][3]string, file, path string) {
	g.add(preName(pre)+file+" "+path+" deleted", false, func(t *testing.T, dir string) {
		g.apply(t, dir, pre)
		g.delIn(t, dir, file, path)
	})
}

// apply makes the edits pre, each {file, path, text}, in the run copy dir.
func (g *corpusGen) apply(t *testing.T, dir string, pre [][3]string) {
	t.Helper()
	for _, e := range pre {
		g.setIn(t, dir, e[0], e[1], e[2])
	}
}

// preName is the edits pre as a case's name starts with them: "<file> <path>=<text>, ...; ", or "" for none.
func preName(pre [][3]string) string {
	if len(pre) == 0 {
		return ""
	}
	var names []string
	for _, e := range pre {
		names = append(names, e[0]+" "+e[1]+"="+e[2])
	}
	return strings.Join(names, ", ") + "; "
}

// del adds the mutant that deletes path in file.
func (g *corpusGen) del(file, path string) {
	g.add(file+" "+path+" deleted", false, func(t *testing.T, dir string) { g.delIn(t, dir, file, path) })
}

// delIn deletes path in file in the run copy dir.
func (g *corpusGen) delIn(t *testing.T, dir, file, path string) {
	t.Helper()
	editJSON(t, g.path(dir, file), "", func(v jsonx.Value) (jsonx.Value, error) {
		return setPath(v, steps(t, streamPath(file, path)), jsonx.Value{}, true)
	})
}

// baseValue is the value at path in the base run's file, and whether the file holds the path.
func (g *corpusGen) baseValue(t *testing.T, file, path string) (jsonx.Value, bool) {
	t.Helper()
	p := g.path(runDir(g.gid), file)
	data := readAll(t, p)
	var v jsonx.Value
	if file == "poll.jsonl" {
		vs, err := jsonx.ParseStream(data)
		must(t, err)
		v = jsonx.Arr(vs)
	} else {
		var err error
		v, err = jsonx.Parse(data)
		must(t, err)
	}
	for _, s := range steps(t, streamPath(file, path))[boolInt(file == "poll.jsonl"):] {
		if s.place >= 0 {
			a, _ := v.Arr()
			if s.place >= len(a) {
				return jsonx.Value{}, false
			}
			v = a[s.place]
			continue
		}
		o, ok := v.Obj()
		if !ok {
			return jsonx.Value{}, false
		}
		if v, ok = o.Get(s.key); !ok {
			return jsonx.Value{}, false
		}
	}
	return v, true
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// setIn sets path in file to text in the run copy dir.
func (g *corpusGen) setIn(t *testing.T, dir, file, path, text string) {
	t.Helper()
	editJSON(t, g.path(dir, file), text, func(v jsonx.Value) (jsonx.Value, error) {
		return setPath(v, steps(t, streamPath(file, path)), jsonx.Str(rawMarker), false)
	})
}

// path is file's path in dir, with "<key>/run" and "<key>/verdict" named as the base's gid names them.
func (g *corpusGen) path(dir, file string) string {
	if key, ok := strings.CutSuffix(file, "/run"); ok {
		file = key + "/" + g.gid + "-" + key + ".json"
	} else if key, ok := strings.CutSuffix(file, "/verdict"); ok {
		file = key + "/" + g.gid + "-" + key + ".verdict.json"
	}
	return filepath.Join(dir, filepath.FromSlash(file))
}

// renameDir renames the item directory key to name.
func (g *corpusGen) renameDir(t *testing.T, dir, key, name string) {
	t.Helper()
	must(t, os.Rename(g.path(dir, key), g.path(dir, name)))
}

// renameItem renames item key, plan.json's items[i], to name: its directory, its run and verdict files and its key.
func (g *corpusGen) renameItem(t *testing.T, dir, key, name string, i int) {
	t.Helper()
	g.renameDir(t, dir, key, name)
	for _, suffix := range []string{".json", ".verdict.json"} {
		from := filepath.Join(dir, name, g.gid+"-"+key+suffix)
		if _, err := os.Stat(from); err == nil {
			must(t, os.Rename(from, filepath.Join(dir, name, g.gid+"-"+name+suffix)))
		}
	}
	g.setIn(t, dir, "plan.json", "items["+strconv.Itoa(i)+"].key", jsonText(name))
}

// rawMarker stands in a value for the text an edit writes there as it is, which need not be JSON jsonx reads.
const rawMarker = "@@raw@@"

// streamPath is path in file as editJSON hands the file's value over: poll.jsonl's values are the array "lines".
func streamPath(file, path string) string {
	if file == "poll.jsonl" {
		return "lines" + path
	}
	return path
}

// editJSON rewrites the JSON file p: f gets its value and gives the new one, which is written in jq's default form,
// with the string rawMarker, when f put it there, replaced by raw. poll.jsonl is a stream of values: f gets them as
// {"lines": [...]}, and each goes back on its own line, compact.
func editJSON(t *testing.T, p, raw string, f func(jsonx.Value) (jsonx.Value, error)) {
	t.Helper()
	data := readAll(t, p)
	stream := filepath.Base(p) == "poll.jsonl"
	var v jsonx.Value
	if stream {
		vs, err := jsonx.ParseStream(data)
		must(t, err)
		o := jsonx.NewObject()
		o.Set("lines", jsonx.Arr(vs))
		v = jsonx.Obj(o)
	} else {
		var err error
		v, err = jsonx.Parse(data)
		must(t, err)
	}
	v, err := f(v)
	must(t, err)
	var out []byte
	if stream {
		lines, _ := at(t, v, "lines").Arr()
		for _, e := range lines {
			out = append(out, jsonx.MarshalCompact(e)...)
		}
	} else {
		out = jsonx.Marshal(v)
	}
	if marker := []byte(`"` + rawMarker + `"`); bytes.Contains(out, marker) {
		if bytes.Count(out, marker) != 1 || bytes.Count(data, []byte(rawMarker)) != 0 {
			t.Fatalf("%s: the marker %s is not there once", p, rawMarker)
		}
		out = bytes.Replace(out, marker, []byte(raw), 1)
	}
	must(t, os.WriteFile(p, out, 0o644))
}

// jsonText is s as a JSON string, in jq's escapes.
func jsonText(s string) string { return string(jsonx.AppendString(nil, s)) }

func splitLine(line string) (string, []string) {
	file, paths, _ := strings.Cut(line, ": ")
	return file, strings.Fields(paths)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	must(t, err)
	return data
}

// chmod sets p's mode, and gives it back its own when the test ends, before t.TempDir removes it.
func chmod(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	fi, err := os.Stat(p)
	must(t, err)
	must(t, os.Chmod(p, mode))
	t.Cleanup(func() { os.Chmod(p, fi.Mode().Perm()) })
}

// copyDir copies the directory from to the new directory to.
func copyDir(t *testing.T, from, to string) {
	t.Helper()
	must(t, os.CopyFS(to, os.DirFS(from)))
}

// runCopy is a copy of the run directory src in a new temporary directory, named as src is (copyRun).
func runCopy(t *testing.T, src string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), filepath.Base(src))
	copyRun(t, src, dir)
	return dir
}

// copyRun copies the run directory src to the new directory dir, without the run's verdict.json and verdict.md:
// the judges' output, not their input.
func copyRun(t *testing.T, src, dir string) {
	t.Helper()
	copyDir(t, src, dir)
	for _, name := range []string{"verdict.json", "verdict.md"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

// mutantDir is the case m made: a copy of its base run with its edit.
func mutantDir(t *testing.T, m mutant) string {
	t.Helper()
	dir := runCopy(t, runDir(corpusBases[m.base].gid))
	m.edit(t, dir)
	return dir
}

// A corpusPass is one judging of the whole corpus, and the file that keeps the shell's answer on every case, one line
// each in the corpus's order: "<exit> <verdict.json> <verdict.md> <stdout> <case>", each the first 16 hex of its
// sha256, or - when not written or empty. stdout is hashed with the run directory's path, which changes from run to
// run, replaced by "<dir>".
type corpusPass struct {
	version string // the version every case is judged under; "" for each case's base run's version
	file    string
}

// corpusPasses judge each case under its base run's version, then under v3, the version new runs get.
var corpusPasses = []corpusPass{
	{"", filepath.Join("testdata", "shell-corpus.txt")},
	{"v3", filepath.Join("testdata", "shell-corpus-v3.txt")},
}

// versionOf is the version pass p judges case m under.
func (p corpusPass) versionOf(m mutant) string {
	if p.version == "" {
		return corpusBases[m.base].version
	}
	return p.version
}

// An answer is what one judge makes of a run directory: its exit, its stdout, and verdict.json and verdict.md, nil
// when not written.
type answer struct {
	exit     int
	stdout   []byte
	json, md []byte
}

// goAnswer is Judge's answer on the run in dir under v: the summary, the files' line and the verdict, exit 0 or 1; on
// an error, exit 2 with nothing printed or written (ErrNoRunDir, a *StreamError or a *Failure).
func goAnswer(dir string, v Version) answer {
	res, err := Judge(os.DirFS(dir), v, Options{})
	if err != nil {
		return answer{exit: 2}
	}
	return answer{exit: res.Exit(), stdout: res.Stdout(dir), json: res.JSON, md: res.MD}
}

// answerLine is the line a pass's file keeps for case name with answer a on the run directory dir.
func answerLine(name string, a answer, dir string) string {
	short := func(b []byte) string {
		if len(b) == 0 {
			return "-" // not written, or nothing printed: a written verdict file is never empty
		}
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])[:16]
	}
	stdout := bytes.ReplaceAll(a.stdout, []byte(dir), []byte("<dir>"))
	return fmt.Sprintf("%d %s %s %s %s", a.exit, short(a.json), short(a.md), short(stdout), name)
}

// readCorpusFile is a pass's file's cases in order, and its answer lines by case.
func readCorpusFile(t *testing.T, file string) (names []string, lines map[string]string) {
	t.Helper()
	f, err := os.Open(file)
	must(t, err)
	defer f.Close()
	lines = map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 5)
		if len(fields) != 5 {
			t.Fatalf("%s: %q is not \"<exit> <json> <md> <stdout> <case>\"", file, line)
		}
		name := fields[4]
		if _, dup := lines[name]; dup {
			t.Fatalf("%s: case %q twice", file, name)
		}
		names = append(names, name)
		lines[name] = line
	}
	must(t, sc.Err())
	return names, lines
}

// TestCorpus holds the corpus's shape: every case name once and on one line, every edit applies to its base, and
// the cases number what each pass's file holds, in its order.
func TestCorpus(t *testing.T) {
	ms := mutants(t)
	seen := map[string]bool{}
	count := map[string]int{}
	for _, m := range ms {
		if seen[m.name] || strings.ContainsAny(m.name, "\n\r") {
			t.Errorf("case %q: twice, or not on one line", m.name)
		}
		seen[m.name] = true
		count[corpusBases[m.base].name]++
	}
	for _, b := range corpusBases {
		if v := recordedVersion(t, b.gid); v.Name != b.version {
			t.Errorf("%s records %s, not %s", b.gid, v.Name, b.version)
		}
	}
	for _, pass := range corpusPasses {
		names, _ := readCorpusFile(t, pass.file)
		if len(names) != len(ms) {
			t.Errorf("%s holds %d cases, the corpus %d: the file keeps the archived shell judges' answers, and no "+
				"test writes it anew", pass.file, len(names), len(ms))
		}
		for i := range min(len(names), len(ms)) {
			if names[i] != ms[i].name {
				t.Errorf("case %d is %q, and %s has %q", i+1, ms[i].name, pass.file, names[i])
				break
			}
		}
	}
	t.Logf("%d cases: %v", len(ms), count)
}

// TestCorpusAnswers judges every case of the corpus with Go alone, in each pass, and holds its exit, stdout and
// both files to the shell's answers in the pass's file, so the corpus guards the judge where no jq is.
func TestCorpusAnswers(t *testing.T) {
	lines := make([]map[string]string, len(corpusPasses))
	for p, pass := range corpusPasses {
		_, lines[p] = readCorpusFile(t, pass.file)
	}
	root := os.Geteuid() == 0
	for _, m := range mutants(t) {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			if m.perm && root {
				t.Skip("root reads a file whatever its mode")
			}
			for p, pass := range corpusPasses {
				v := mustNamed(t, pass.versionOf(m))
				dir := mutantDir(t, m)
				got := answerLine(m.name, goAnswer(dir, v), dir)
				if want, ok := lines[p][m.name]; !ok {
					t.Errorf("%s has no line for the case", pass.file)
				} else if got != want {
					t.Errorf("under %s, Go answers\n  %s\nand the shell answered\n  %s", v.Name, got, want)
				}
			}
		})
	}
}
