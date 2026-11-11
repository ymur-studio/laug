package judge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// fileSHA256 is the sha256 of the file at path, as sha256sum prints it.
func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestRegistryMatchesArchive holds versions.txt to its committed lines. The four committed lines stay as they are,
// and a new version adds a line after them. Each version is found by its sha256 and by its name, the one judge not
// ported is not found by its sha256, and Current is a version.
func TestRegistryMatchesArchive(t *testing.T) {
	pinned := []string{
		"v1 284a759c54557c18bcbdfc257414199c9c94bfb573e3234e2265ecf18ab5366c",
		"v2 33b4e89dc2ecef502e73ea88136a482fbf8694986789712fd5126e31f5faf6f0",
		"v2-r7off d7278dabbb8552875c8cae1f4cce5bf57fbcea1bd3ac0d863ec193c8ae937715",
		"v3 ece65b42476220bb0a07e89aa94b3a51d4b0c8ecdd0f7eedbaf3e0fe73008b33",
	}
	data, err := os.ReadFile("versions.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != versionsTxt {
		t.Fatal("versions.txt on disk is not the one embedded")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) < len(pinned) || !slices.Equal(lines[:len(pinned)], pinned) {
		t.Fatalf("versions.txt starts\n%s\nwant the committed lines\n%s", strings.Join(lines, "\n"),
			strings.Join(pinned, "\n"))
	}
	if len(versions) != len(lines) {
		t.Fatalf("%d versions from %d lines", len(versions), len(lines))
	}
	for _, v := range versions {
		if w, ok := Lookup(v.SHA256); !ok || w != v {
			t.Errorf("Lookup(%s) = %v, %v", v.SHA256[:12], w, ok)
		}
		if w, ok := Named(v.Name); !ok || w != v {
			t.Errorf("Named(%s) = %v, %v", v.Name, w, ok)
		}
	}
	for _, sha := range notPorted {
		if _, ok := Lookup(sha); ok {
			t.Errorf("Lookup finds %s, which is not ported", sha[:12])
		}
	}
	if _, ok := Named(Current); !ok {
		t.Fatalf("Current %s is not in versions.txt", Current)
	}
	if _, ok := Lookup(strings.Repeat("0", 64)); ok {
		t.Error("Lookup finds 64 zeros")
	}
}

// TestParseVersions refuses each malformed versions.txt.
func TestParseVersions(t *testing.T) {
	const h = "284a759c54557c18bcbdfc257414199c9c94bfb573e3234e2265ecf18ab5366c"
	const h2 = "33b4e89dc2ecef502e73ea88136a482fbf8694986789712fd5126e31f5faf6f0"
	if vs, err := parseVersions("v1 " + h + "\nv2 " + h2 + "\n"); err != nil || len(vs) != 2 || vs[1].Name != "v2" ||
		!vs[1].Arm || vs[0].Arm {
		t.Errorf("two good lines: %v, %v", vs, err)
	}
	for _, bad := range []string{
		"", "\n", "v1 " + h, "v1 " + h + "\n\n", "v1  " + h + "\n", "v1\t" + h + "\n", "v1 " + strings.ToUpper(h) + "\n",
		"v1 " + h[:63] + "\n", "v1 " + h + " \n", " " + h + "\n", "v1 " + h + "\nv1 " + h2 + "\n",
		"v1 " + h + "\nv2 " + h + "\n", "v9 " + h + "\n",
	} {
		if vs, err := parseVersions(bad); err == nil {
			t.Errorf("parseVersions(%q) = %v, want an error", bad, vs)
		}
	}
}

// TestRecordedSHA256 reads gate.json texts as laug gate rejudge does: the table's and the 158 committed runs'. The 21
// runs that record none record an internal/judge entry in its place: the judge's version and its sources' sha256,
// judge.Self's. gate rejudge and S2 pick the judge by the version; gate rejudge prints the sha256 beside laug's own
// and compares none. That sha256 covers the .go files of internal/judge and internal/jsonx that are not tests, and
// versions.txt. Boot 4's 19 runs record eb37b18a8bbe, from a build whose identity covered the tests as well; boot
// 6's 2 record af315e7661ab.
func TestRecordedSHA256(t *testing.T) {
	const h = "284a759c54557c18bcbdfc257414199c9c94bfb573e3234e2265ecf18ab5366c"
	const h2 = "33b4e89dc2ecef502e73ea88136a482fbf8694986789712fd5126e31f5faf6f0"
	entry := func(sha string) string { return `{"path":"gate/combine.sh","sha256":` + sha + `}` }
	cases := []struct{ name, gate, want string }{
		{"one entry", `{"inputs":[{"path":"gate/plan.yaml","sha256":"` + h2 + `"},` + entry(`"`+h+`"`) + `]}`, h},
		{"no entry", `{"inputs":[{"path":"gate/plan.yaml","sha256":"` + h + `"}]}`, ""},
		{"no inputs", `{}`, ""},
		{"inputs a string", `{"inputs":"x"}`, ""},
		{"inputs an object", `{"inputs":{"a":` + entry(`"`+h+`"`) + `}}`, h},
		{"the first string of two entries", `{"inputs":[` + entry(`5`) + `,1,[],` + entry(`"`+h2+`"`) + `,` +
			entry(`"`+h+`"`) + `]}`, h2},
		{"a path not exactly gate/combine.sh", `{"inputs":[{"path":"./gate/combine.sh","sha256":"` + h + `"}]}`, ""},
		{"upper case hex", `{"inputs":[` + entry(`"`+strings.ToUpper(h)+`"`) + `]}`, ""},
		{"63 hex", `{"inputs":[` + entry(`"`+h[:63]+`"`) + `]}`, ""},
		{"a trailing newline", `{"inputs":[` + entry(`"`+h+`\n"`) + `]}`, h},
		{"a NUL byte", `{"inputs":[` + entry(`"`+h[:10]+`\u0000`+h[10:]+`"`) + `]}`, h},
		{"null", `null`, ""},
		{"an array", `[` + entry(`"`+h+`"`) + `]`, ""},
		{"null, then one entry", `null {"inputs":[` + entry(`"`+h+`"`) + `]}`, h},
		{"one entry, then an array", `{"inputs":[` + entry(`"`+h+`"`) + `]} [1]`, ""},
		{"two values with one each", `{"inputs":[` + entry(`"`+h+`"`) + `]} {"inputs":[` + entry(`"`+h+`"`) + `]}`, ""},
		{"a parse error", `{"inputs":[` + entry(`"`+h+`"`) + `]} {`, ""},
		{"an empty file", ``, ""},
	}
	for _, c := range cases {
		if got := RecordedSHA256([]byte(c.gate)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	entries := 0
	for _, run := range committedRuns(t) {
		data, err := os.ReadFile(filepath.Join(results, run, "gate.json"))
		if err != nil {
			t.Fatal(err)
		}
		got := RecordedSHA256(data)
		if got == "" && entryVersion(data) == "" {
			t.Errorf("%s: gate.json records no gate/combine.sh sha256 and no internal/judge entry", run)
		}
		if got == "" {
			entries++
		}
	}
	cs := census(t)
	none := func(row jsonx.Value) bool { return rowMember(row, "combine_sha256").IsNull() }
	if all := cs.Count(nil, none); all != 21 {
		t.Errorf("the gates table records %d runs with no gate/combine.sh sha256, want 21", all)
	}
	if want := cs.Count(cs.Here, none); entries != want {
		t.Errorf("%d committed runs record no gate/combine.sh sha256, want %d", entries, want)
	}
}
