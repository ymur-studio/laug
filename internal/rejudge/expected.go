// Package rejudge judges committed gate runs again, each under the judge version its gate.json records, and says how
// each compares with the verdict.json and verdict.md it holds. It sits apart from internal/judge so that its code is
// not part of the judge's identity. The standard library only.
//
// expected.go lists the runs and reads the expected file, which names every run that is not judged identical as it
// stands, with a kind whose proof is code (committed_test.go). A run it does not name must judge to both of its
// files byte for byte. s1.go is S1, which laug gate rejudge runs: a run judged again, and
// identical, known, re-rendered, unexplained or not judged, with its line and the counts. s2.go is its S2: the
// signature, which laug gate signature prints and laug gate compare compares field by field.
package rejudge

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/judge"
)

// A Run is one committed gate run: a directory of gate/results, and the judge its gate.json records.
type Run struct {
	Gid string // the directory's name
	// SHA256 is the gate/combine.sh sha256 the run's gate.json records, as judge.RecordedSHA256 reads it,
	// or "" for none.
	SHA256 string
	// Recorded is all the run's gate.json records of its judge (ReadRecorded): SHA256, or with none the version of
	// the internal/judge entry a run bin/laug judge judged records.
	Recorded Recorded
	// Version is the registry's version for Recorded; ok is false when the registry has none, and the run is not
	// judged.
	Version judge.Version
	ok      bool
}

// Registered says the registry names the run's recorded judge.
func (r Run) Registered() bool { return r.ok }

// Runs lists the runs in results, gate/results or a copy of it, as a shell lists them with `for p in
// "$r"/*` and `[[ -d "$p" ]]` under LC_ALL=C: every entry that is a directory after
// symlinks, dot names skipped, by name in byte order, each with its recorded judge. A run whose gate.json cannot be
// read records none. An entry whose name is not UTF-8 is an error when it is a directory or a link, since an fs.FS
// cannot open it and the shell would count it.
func Runs(results fs.FS) ([]Run, error) {
	entries, err := fs.ReadDir(results, ".")
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // the glob skips dot names, with no dotglob
		}
		if !fs.ValidPath(name) {
			if e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
				return nil, fmt.Errorf("entry %q: its name is not UTF-8, so an fs.FS cannot open it, and it may be "+
					"a run", name)
			}
			continue // a file, which -d skips
		}
		if fi, err := fs.Stat(results, name); err != nil || !fi.IsDir() {
			continue // -d follows a link, and is false for a dangling one
		}
		r := Run{Gid: name}
		if data, err := fs.ReadFile(results, name+"/gate.json"); err == nil {
			r.Recorded = ReadRecorded(data)
			r.SHA256 = r.Recorded.SHA256
		}
		r.Version, r.ok = r.Recorded.Version()
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b Run) int { return strings.Compare(a.Gid, b.Gid) })
	return runs, nil
}

// A Kind is why a run is not judged identical as it stands. Each kind's proof is code: a test that reproduces the
// run's committed bytes another way.
type Kind string

const (
	// EmptyDirs is a run that held empty item directories, which git does not carry. Judged as a clone holds it, it
	// differs; judged with those directories made again in memory (judge.Options.EmptyDirs), it equals both files.
	// Counted known.
	EmptyDirs Kind = "empty-dirs"
	// Trap is a trap verdict, which gate/run.sh or gate/lib.sh wrote when the gate stopped before its judge: re-rendered
	// by its form, T1, T2 or T3 (judge.RerenderTrap), from the run's own step and reason, and never rejudged. Counted
	// re-rendered.
	Trap Kind = "trap"
)

// An Expectation is one line of the expected file: "<gid>  <kind>  <arg>".
type Expectation struct {
	Gid  string
	Kind Kind
	// Arg is, for EmptyDirs, the item keys whose directories were empty, joined by commas; for Trap, the form.
	Arg string
}

// Dirs is an EmptyDirs line's item keys.
func (x Expectation) Dirs() []string {
	if x.Kind != EmptyDirs {
		return nil
	}
	return strings.Split(x.Arg, ",")
}

// Expected is the expected file read: its lines by gid.
type Expected map[string]Expectation

// ParseExpected reads the expected file. Each line is a gid, a kind and its argument, separated by spaces; a line
// that starts with # and a blank line say nothing. The gids are names as the judge takes a gid, in byte order,
// each once. An EmptyDirs argument is item keys joined by commas, each a name too; a Trap argument is T1, T2 or T3.
func ParseExpected(text []byte) (Expected, error) {
	ex := Expected{}
	last := ""
	for i, line := range strings.Split(string(text), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bad := func(why string) error { return fmt.Errorf("expected file, line %d: %s: %q", i+1, why, line) }
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, bad("want <gid> <kind> <arg>")
		}
		x := Expectation{Gid: f[0], Kind: Kind(f[1]), Arg: f[2]}
		switch {
		case !isName(x.Gid):
			return nil, bad("the gid is not a name")
		case x.Gid <= last:
			return nil, bad("the gid is not after the line before's, in byte order")
		}
		switch x.Kind {
		case EmptyDirs:
			for _, k := range x.Dirs() {
				if !isName(k) {
					return nil, bad("an item key is not a name")
				}
			}
		case Trap:
			if !slices.Contains(judge.Forms, judge.Form(x.Arg)) {
				return nil, bad("a trap's form is T1, T2 or T3")
			}
		default:
			return nil, bad("no such kind")
		}
		ex[x.Gid] = x
		last = x.Gid
	}
	return ex, nil
}

// ReadExpected reads the expected file at name in fsys; a file that is not there is an empty one.
func ReadExpected(fsys fs.FS, name string) (Expected, error) {
	data, err := fs.ReadFile(fsys, name)
	if errors.Is(err, fs.ErrNotExist) {
		return Expected{}, nil
	}
	if err != nil {
		return nil, err
	}
	if bytes.ContainsRune(data, '\r') {
		return nil, fmt.Errorf("%s: a carriage return", name)
	}
	return ParseExpected(data)
}

// isName says s matches ^[A-Za-z0-9][A-Za-z0-9._-]*$, the rule internal/judge holds a gid to and
// explain an item key it makes again.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}
