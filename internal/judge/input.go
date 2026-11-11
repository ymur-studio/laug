package judge

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// An Input is a gate run as gather() hands it to the judge.
type Input struct {
	// Value is the gathered object, as the judge's jq reads it: {gate, clock, plan, thresholds,
	// thresholds_incumbent (Arm only), harness, polls, dirs: {<key>: {lease, exit, run, verdict}}}.
	Value jsonx.Value
	// Gid is the name the run and verdict files take, the shell's gid (gidOf), or "" for none. The judge's own
	// $gid is gate.json's .gate_id // null, a value and not this text.
	Gid string
}

// Options say how to gather a run.
type Options struct {
	// Arm reads thresholds-incumbent.json, as every version with Arm does; v1's gather() lacks
	// the line, and its input lacks the key.
	Arm bool
	// EmptyDirs are item directories to add, empty, in memory, for a run whose empty directory git does not carry;
	// internal/rejudge's explain makes them again in memory. Each must be a name explain would make, and
	// not be in the run already. Nothing is written.
	EmptyDirs []string
}

// ErrNoRunDir is a run directory that is not there, which the judge refuses before it gathers, exit 2.
var ErrNoRunDir = errors.New("no results directory")

// A StreamError is a run whose gathered input jq cannot parse. The judge then fails, exit 2, and writes
// nothing. Two things make one. A file that is there but cannot be read (a directory, or a file
// without read permission) makes jq print what it slurped from nothing and then exit 2, so load() prints
// "unreadable" twice and load_lines() prints [] and "unreadable" (seen on jq-1.7): two values where the object wants
// one. And a value nested close to jq's parser limit, 256 stack entries, reads alone but not inside the gathered
// object, where a top-level file starts 2 entries deep, poll.jsonl's values 3 and an item directory's files 6.
type StreamError struct {
	Err error // jsonx's error on the gathered text
}

func (e *StreamError) Error() string {
	return "the gathered input does not parse, so the judge fails: " + e.Err.Error()
}

func (e *StreamError) Unwrap() error { return e.Err }

// Gather reads the run in fsys, its results directory, as gather() reads it, and nothing else:
//
//   - gate.json, clock.json, plan.json, thresholds.json, thresholds-incumbent.json (Arm only) and harness.json, each
//     by load(): null when it is absent or a dangling symlink (`-e` is false); the value when the file
//     holds exactly one; the string "unreadable" when it holds none, more than one, or a parse error. A file that is
//     there but cannot be read, a directory among them, is a StreamError, not "unreadable".
//   - poll.jsonl by load_lines(): its values as one array, [] for an empty file, "unreadable" on a parse
//     error.
//   - Each item directory, its lease.json and exit.json, and, with a gid, <gid>-<key>.json and
//     <gid>-<key>.verdict.json, each by load(). An item directory is an entry that is a directory after symlinks,
//     dot names skipped, in the order of the `*/` glob under LC_ALL=C: by the bytes of the name and a slash, so
//     "a-b" comes before "a" (seen on bash 5.2). Its key is its name less trailing newlines, as $(basename "$d")
//     gives it, and two names with one key keep the first place and the last value, as jq reads the object.
//   - gid: when gate.json holds an object, its .gate_id // "" as jq -r prints it, less NUL bytes and
//     trailing newlines as $(...) drops them, kept only when it matches ^[A-Za-z0-9][A-Za-z0-9._-]*$. With none, no
//     run or verdict file is read, and both are null.
//
// Never read, though a run holds them: timeline.jsonl, cleanup.json, verdict.json, verdict.md, *.log, .state.json,
// .steps/, and in an item directory load.exit, .lease.json, item.log, known-slow.json and every other file
// (TestGatherReadsOnly).
//
// Gather builds the text gather() prints, byte for byte (TestGatherMatchesShell), then reads it as jq does: an error
// there is a *StreamError. A run directory that is not one is ErrNoRunDir.
//
// One limit of fs.FS: a name that is not UTF-8 cannot be opened (fs.ValidPath), so an entry by such a name that may
// be a directory is an error here, where the shell would read it. No committed run holds one.
func Gather(fsys fs.FS, opts Options) (*Input, error) {
	text, gid, err := gatherText(fsys, opts)
	if err != nil {
		return nil, err
	}
	v, err := jsonx.Parse(text)
	if err != nil {
		return nil, &StreamError{Err: err}
	}
	return &Input{Value: v, Gid: gid}, nil
}

// The texts load() and load_lines() print for a file that is absent, holds no single value, or cannot be read.
const (
	nullText        = "null\n"
	unreadableText  = "\"unreadable\"\n"
	cannotReadText  = unreadableText + unreadableText // jq slurps nothing, prints, exits 2; load echoes again
	cannotReadLines = "[]\n" + unreadableText
)

// gatherText is gather()'s output for the run in fsys, and the gid.
func gatherText(fsys fs.FS, opts Options) ([]byte, string, error) {
	if fi, err := fs.Stat(fsys, "."); err != nil || !fi.IsDir() {
		return nil, "", ErrNoRunDir // [[ -d "$dir" ]]
	}
	dirs, err := itemDirs(fsys, opts.EmptyDirs)
	if err != nil {
		return nil, "", err
	}
	gate := load(fsys, "gate.json", false)
	gid := gidOf(gate)

	b := append([]byte(`{"gate":`), gate...)
	b = append(append(b, `,"clock":`...), load(fsys, "clock.json", false)...)
	b = append(append(b, `,"plan":`...), load(fsys, "plan.json", false)...)
	b = append(append(b, `,"thresholds":`...), load(fsys, "thresholds.json", false)...)
	if opts.Arm {
		b = append(append(b, `,"thresholds_incumbent":`...), load(fsys, "thresholds-incumbent.json", false)...)
	}
	b = append(append(b, `,"harness":`...), load(fsys, "harness.json", false)...)
	b = append(append(b, `,"polls":`...), load(fsys, "poll.jsonl", true)...)
	b = append(b, `,"dirs":{`...)
	for i, d := range dirs {
		if i > 0 {
			b = append(b, ',')
		}
		k := strings.TrimRight(d.name, "\n")
		file := func(name string) []byte {
			if d.virtual {
				return []byte(nullText)
			}
			return load(fsys, d.name+"/"+name, false)
		}
		b = jsonx.AppendString(b, jsonx.Valid(k)) // jq -Rn --arg k "$k" '$k'
		b = append(append(b, `:{"lease":`...), file("lease.json")...)
		b = append(append(b, `,"exit":`...), file("exit.json")...)
		if gid != "" {
			b = append(append(b, `,"run":`...), file(gid+"-"+k+".json")...)
			b = append(append(b, `,"verdict":`...), file(gid+"-"+k+".verdict.json")...)
		} else {
			b = append(b, `,"run":null,"verdict":null`...)
		}
		b = append(b, '}')
	}
	return append(b, "}}\n"...), gid, nil
}

// load is what load() (lines false) or load_lines() (lines true) prints for the file name in fsys.
func load(fsys fs.FS, name string, lines bool) []byte {
	if _, err := fs.Stat(fsys, name); err != nil {
		return []byte(nullText) // [[ -e "$1" ]] is false: absent, a dangling symlink, or no way to stat it
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		if lines {
			return []byte(cannotReadLines)
		}
		return []byte(cannotReadText)
	}
	vs, err := jsonx.ParseStream(data)
	switch {
	case err != nil:
		return []byte(unreadableText)
	case lines:
		return jsonx.MarshalCompact(jsonx.Arr(vs)) // jq -cs .
	case len(vs) != 1:
		return []byte(unreadableText)
	}
	return jsonx.MarshalCompact(vs[0]) // jq -cs 'if length == 1 then .[0] else "unreadable" end'
}

// gidOf is the shell's gid, from what load() printed for gate.json:
//
//	gid="$(load "$dir/gate.json" | jq -r 'if type == "object" then .gate_id // "" else "" end')"
//	[[ "$gid" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || gid=""
//
// jq -r prints a string raw and any other value in its default form (true, a number's text, an array over lines),
// each with a newline, so gate_id true gives "true" and 1.50 gives "1.50". Bash drops NUL bytes and the trailing
// newlines (seen on jq-1.7 and bash 5.2).
func gidOf(gate []byte) string {
	vs, err := jsonx.ParseStream(gate)
	if err != nil {
		return "" // not reached: load prints JSON
	}
	var out []byte
	for _, v := range vs {
		x := jsonx.Str("")
		if o, ok := v.Obj(); ok {
			if g, _ := o.Get("gate_id"); !g.IsNull() && g.Kind() != jsonx.KindFalse {
				x = g // .gate_id // ""
			}
		}
		if s, ok := x.Str(); ok {
			out = append(out, s...)
		} else {
			out = jsonx.AppendIndent(out, x)
		}
		out = append(out, '\n')
	}
	gid := strings.TrimRight(strings.ReplaceAll(string(out), "\x00", ""), "\n")
	if !isName(gid) {
		return ""
	}
	return gid
}

// isName says s matches ^[A-Za-z0-9][A-Za-z0-9._-]*$ in the C locale: the gid rule and the names
// internal/rejudge's explain makes again.
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

// An itemDir is one directory the `*/` glob gives; a virtual one is an Options.EmptyDirs name, empty and in memory.
type itemDir struct {
	name    string
	virtual bool
}

// itemDirs lists the run's item directories as `for d in "$dir"/*/` does under LC_ALL=C: every entry that
// is a directory after symlinks, dot names skipped, sorted by the bytes of the name and a slash, the glob's own
// strings. A run directory that cannot be listed has none, as the glob then matches nothing. The empty names join
// the list in their place.
func itemDirs(fsys fs.FS, empty []string) ([]itemDir, error) {
	var dirs []itemDir
	held := map[string]bool{}
	entries, _ := fs.ReadDir(fsys, ".") // an error lists nothing, as the glob does
	for _, e := range entries {
		name := e.Name()
		held[name] = true
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !fs.ValidPath(name) {
			if e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
				return nil, fmt.Errorf("entry %q: its name is not UTF-8, so an fs.FS cannot open it, and it may be "+
					"an item directory", name)
			}
			continue // a file the glob does not match
		}
		if fi, err := fs.Stat(fsys, name); err == nil && fi.IsDir() {
			dirs = append(dirs, itemDir{name: name})
		}
	}
	for _, name := range empty {
		if !isName(name) {
			return nil, fmt.Errorf("empty directory %q: not a name laug gate rejudge makes again", name)
		}
		if _, err := fs.Stat(fsys, name); err == nil || held[name] {
			return nil, fmt.Errorf("empty directory %q: the run holds that name already", name)
		}
		held[name] = true
		dirs = append(dirs, itemDir{name: name, virtual: true})
	}
	slices.SortFunc(dirs, func(a, b itemDir) int { return strings.Compare(a.name+"/", b.name+"/") })
	return dirs, nil
}
