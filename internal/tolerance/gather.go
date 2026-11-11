package tolerance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// itemKey is an item key as gather checks the two it reads in each gate, and pairRE PAIR's two, in the
// C locale; bash's =~ is POSIX's, whose $ is the end of the string only, as Go's is.
var itemKey = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// gather is the text the script's gather() printed for the gates ids in dir, the results directory, read
// with pair, PAIR as given: one array, a gate an object, by id in byte order, as `sort` gives them under LC_ALL=C:
//
//	{"id", "a", "b", "verdict", "driver", "th", "thi", "slow", "va", "vb"}
//
// a and b are the two items it reads, PAIR's, else plan.json's item of arm incumbent and its pair. verdict, th, thi,
// va and vb are verdict.json, thresholds.json, thresholds-incumbent.json and the two items' verdict JSONs, each by
// load. driver is gate.json's .driver // "run.sh", by its own jq. slow lists the item directories that
// hold known-slow.json, by find.
//
// The text is built byte for byte, so it fails to parse where jq's read of it fails: a file
// that is there and cannot be read makes load() print null twice, and an empty gate.json gives driver nothing. It
// reads through os, not an fs.FS, which cannot open a name that is not UTF-8 where find walks into one.
//
// errs is what gather() prints on stderr, its other commands' stderr going to /dev/null: find's line for each
// directory it cannot read, in the order find meets them, each path as the script forms it, shown standing for
// GATE_RESULTS as given (slow).
func gather(dir, shown string, ids []string, pair string) (text []byte, errs string) {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	b := []byte("[")
	var e strings.Builder
	for i, g := range sorted {
		if i > 0 {
			b = append(b, ',')
		}
		d := filepath.Join(dir, g)
		var a, c string // a and b, as the script names them
		if pair != "" {
			a, c, _ = strings.Cut(pair, ":")
		} else {
			a = planItem(filepath.Join(d, "plan.json"), "key")
			c = planItem(filepath.Join(d, "plan.json"), "pair")
		}
		if a+c != "" && !(itemKey.MatchString(a) && itemKey.MatchString(c)) {
			a, c = "", ""
		}
		b = append(b, `{"id":"`+g+`","a":"`+a+`","b":"`+c+`","verdict":`...)
		b = append(b, load(filepath.Join(d, "verdict.json"))...)
		b = append(append(b, `,"driver":`...), driver(filepath.Join(d, "gate.json"))...)
		b = append(append(b, `,"th":`...), load(filepath.Join(d, "thresholds.json"))...)
		b = append(append(b, `,"thi":`...), load(filepath.Join(d, "thresholds-incumbent.json"))...)
		listed, found := slow(d, shown+"/"+g)
		b = append(append(b, `,"slow":`...), listed...)
		e.WriteString(found)
		b = append(append(b, `,"va":`...), itemVerdict(d, g, a)...)
		b = append(append(b, `,"vb":`...), itemVerdict(d, g, c)...)
		b = append(b, '}')
	}
	return append(b, "]\n"...), e.String()
}

// The texts load() prints for a file that is absent or holds no single value, and for one that cannot be read.
const (
	nullText       = "null\n"
	cannotReadText = nullText + nullText // jq slurps nothing, prints null, exits 2; load echoes null again
)

// load is what this line, here in two, prints for the file at path:
//
//	load() { [[ -e "$1" ]] || { echo null; return 0; };
//	  jq -cs 'if length == 1 then .[0] else null end' "$1" 2>/dev/null || echo null; }
//
// null when it is absent or a dangling symlink; the value when the file holds exactly one; null when it holds none,
// more than one, or a parse error. A file that is there but cannot be read, a directory among them, prints null twice
// (seen on jq-1.7: jq -s runs the program on what it slurped, nothing, then exits 2). Unlike internal/judge's load,
// a bad file is null here, not "unreadable".
func load(path string) []byte {
	if _, err := os.Stat(path); err != nil {
		return []byte(nullText)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []byte(cannotReadText)
	}
	vs, err := jsonx.ParseStream(data)
	if err != nil || len(vs) != 1 {
		return []byte(nullText)
	}
	return jsonx.MarshalCompact(vs[0])
}

// itemVerdict is "va" or "vb": load() of <key>/<gid>-<key>.verdict.json in the gate's directory d, or null
// when key is "".
func itemVerdict(d, g, key string) []byte {
	if key == "" {
		return []byte(nullText)
	}
	return load(filepath.Join(d, key, g+"-"+key+".verdict.json"))
}

// driver is what this line prints for gate.json:
//
//	jq -c '.driver // "run.sh"' "$d/gate.json" 2>/dev/null || echo '"run.sh"'
//
// jq runs the program on each value it reads, in turn, as its parser completes it, and goes on past a value the
// program fails on; it stops at a parse error. It exits 2 when it cannot read the file, 5 on a parse error, and else
// with the status of the last value, 5 when the program failed on it (seen on jq-1.7: `5 {"driver":"y"}` prints "y"
// and exits 0). On any exit but 0 the script echoes "run.sh" after what jq printed. So one object gives its driver;
// an empty file gives nothing, and two values or a value before a parse error give two lines, which the rule's jq
// then cannot read.
func driver(path string) []byte {
	run := []byte(`"run.sh"` + "\n")
	data, err := os.ReadFile(path)
	if err != nil {
		return run
	}
	vs, parsed := stream(data)
	var out []byte
	failed := false
	for _, v := range vs {
		var d jsonx.Value
		failed = evaluate(func() { d = alt(field(v, "driver"), str("run.sh")) }) != nil
		if !failed {
			out = append(out, jsonx.MarshalCompact(d)...)
		}
	}
	if !parsed || failed {
		out = append(out, run...)
	}
	return out
}

// planItem is a or b without PAIR, the key or the pair of plan.json's first item of arm incumbent:
//
//	a="$(jq -r '[.items[]? | select(.arm == "incumbent") | .key][0] // ""' "$d/plan.json" 2>/dev/null)" || a=""
//
// "" when jq exits other than 0 (as driver says when), else what it printed for each value: a string raw, anything
// else in jq's default form, each with a newline; then $(...) drops NUL bytes, as bash does, and the newlines at the
// end. bash says so on stderr ("warning: command substitution: ignored null byte in input"); this does not.
func planItem(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	vs, parsed := stream(data)
	if !parsed {
		return ""
	}
	var out []byte
	for i, v := range vs {
		var got jsonx.Value
		if evaluate(func() { got = incumbentItem(v, key) }) != nil {
			if i == len(vs)-1 {
				return ""
			}
			continue
		}
		if s, ok := got.Str(); ok {
			out = append(out, s...)
		} else {
			out = jsonx.AppendIndent(out, got)
		}
		out = append(out, '\n')
	}
	return strings.TrimRight(strings.ReplaceAll(string(out), "\x00", ""), "\n")
}

// incumbentItem is `[.items[]? | select(.arm == "incumbent") | .<key>][0] // ""` on one value of plan.json.
func incumbentItem(plan jsonx.Value, key string) jsonx.Value {
	var keys []jsonx.Value
	for _, it := range eachOpt(field(plan, "items")) {
		if is(field(it, "arm"), "incumbent") {
			keys = append(keys, field(it, key))
		}
	}
	if len(keys) == 0 {
		return str("")
	}
	return alt(keys[0], str(""))
}

// slow is what this line, here in two, prints for the gate whose directory is d:
//
//	find "$d" -mindepth 2 -maxdepth 2 -name known-slow.json -printf '%P\n' | sort |
//	  jq -Rcs 'split("\n") | map(select(. != ""))'
//
// GNU find walks without following symlinks (-P), so it descends into the gate's real directories, dot names among
// them, and not into a symlink to one; a gate directory that is itself a symlink gives nothing. Each entry named
// known-slow.json there counts, whatever its type. sort orders the lines by bytes; jq reads them raw, bad UTF-8 made
// U+FFFD.
//
// A directory find cannot open, the gate's or an item's (mode 000, or no read or no search permission where it
// needs one), it lists as empty, and says so on stderr: found is that line for each, in the order find meets them,
// the order the directory holds its entries, shown being the gate's directory as the script names it,
// "$GATE_RESULTS/<gate id>" (seen on GNU find 4.9.0: "find: 'gate/results/g1/x': Permission denied").
func slow(d, shown string) (listed []byte, found string) {
	var lines []string
	var errs strings.Builder
	if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
		entries, err := readDir(d)
		if err != nil {
			errs.WriteString(findLine(shown, err))
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			inner, err := readDir(filepath.Join(d, e.Name()))
			if err != nil {
				errs.WriteString(findLine(shown+"/"+e.Name(), err))
				continue
			}
			if slices.ContainsFunc(inner, func(f os.DirEntry) bool { return f.Name() == "known-slow.json" }) {
				lines = append(lines, e.Name()+"/known-slow.json")
			}
		}
	}
	slices.Sort(lines)
	text := strings.Join(lines, "\n")
	vs := []jsonx.Value{}
	for _, s := range strings.Split(text, "\n") {
		if s != "" {
			vs = append(vs, str(s))
		}
	}
	return jsonx.MarshalCompact(jsonx.Arr(vs)), errs.String()
}

// readDir is the entries of the directory d in the order it holds them, as readdir gives them to find, which does not
// sort them; os.ReadDir would.
func readDir(d string) ([]os.DirEntry, error) {
	f, err := os.Open(d)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// findLine is the line GNU find prints on stderr for a directory at path it cannot read, under LC_ALL=C, with err the
// reason: "find: '<path>': <strerror>". The path is quoted as gnulib's locale quoting style quotes it in the C
// locale: in single quotes, with a backslash before a quote or a backslash, \a \b \f \n \r \t and \v for those bytes,
// and any other byte that is not printable ASCII as a backslash and three octal digits (seen on GNU find 4.9.0:
// "it\'s", "a\\b", "x\377y", "del\177x"). The reason is glibc's strerror text: Go's errno text, which is glibc's in
// lower case, its first letter raised.
func findLine(path string, err error) string {
	var b strings.Builder
	b.WriteString("find: '")
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '\a', '\b', '\f', '\n', '\r', '\t', '\v':
			b.WriteByte('\\')
			b.WriteByte("abfnrtv"[strings.IndexByte("\a\b\f\n\r\t\v", c)])
		case '\'', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			if c < 0x20 || c > 0x7e {
				fmt.Fprintf(&b, "\\%03o", c)
				continue
			}
			b.WriteByte(c)
		}
	}
	why := err.Error()
	var errno syscall.Errno
	if errors.As(err, &errno) {
		why = errno.Error()
	}
	if why != "" {
		why = strings.ToUpper(why[:1]) + why[1:]
	}
	return b.String() + "': " + why + "\n"
}

// stream is the values jq's parser completes in data, in order, and whether it read all of data. jq hands each value
// to the program as its parser completes it: an array or an object at its closing bracket, a string at its closing
// quote, any other token at the byte that ends it. So the values before a parse error still run (seen on jq-1.7:
// `{"driver":"x"} garbage` prints "x", then fails). One byte order mark at the start is skipped, as jq skips it.
func stream(data []byte) ([]jsonx.Value, bool) {
	if vs, err := jsonx.ParseStream(data); err == nil {
		return vs, true
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	var vs []jsonx.Value
	pos := 0
	for {
		for pos < len(data) && isSpace(data[pos]) {
			pos++
		}
		end := valueEnd(data, pos)
		if pos == len(data) || end <= pos || data[pos] == 0xEF {
			return vs, false // the end, a byte no value starts with, or a mark jq does not skip here
		}
		v, err := jsonx.Parse(data[pos:end])
		if err != nil {
			return vs, false
		}
		vs = append(vs, v)
		pos = end
	}
}

// valueEnd is where the value that starts at pos ends, as jq's scanner finds it: past the bracket that closes an
// array or an object, counting brackets outside strings; past a string's closing quote; else at the first white
// space, quote or structural byte. A value with no end gives len(data); whether it is a value is Parse's to say.
func valueEnd(data []byte, pos int) int {
	if pos >= len(data) {
		return pos
	}
	switch data[pos] {
	case '"':
		return stringEnd(data, pos)
	case '[', '{':
		depth := 0
		for i := pos; i < len(data); i++ {
			switch data[i] {
			case '"':
				i = stringEnd(data, i) - 1
			case '[', '{':
				depth++
			case ']', '}':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(data)
	}
	i := pos
	for i < len(data) && !isSpace(data[i]) && !strings.ContainsRune(`"[]{},:`, rune(data[i])) {
		i++
	}
	return i
}

// stringEnd is the end of the string whose opening quote is at pos: past its closing quote, a backslash escaping
// the byte after it; len(data) when it does not close.
func stringEnd(data []byte, pos int) int {
	for i := pos + 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(data)
}

// isSpace is white space as jq's scanner reads it.
func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
