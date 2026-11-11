package jsonx

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestReaderDifferences pins what the reader does where jq 1.7 or encoding/json might do otherwise. Each case
// gives the input; what jsonx reads, as `jq -c .` would print it ("error" for a refusal); what jq 1.7 prints, seen
// on jq-1.7 and checked again with jq present; and, where it is the reason for a reader of its own, what
// encoding/json reads. The reader differs from jq only on number spellings RFC 8259 refuses. A NUL byte ends a
// number token in both, as jq reads the token as a C string.
func TestReaderDifferences(t *testing.T) {
	const ff = "\ufffd"
	deep := func(open, close string, n int) string { return strings.Repeat(open, n) + strings.Repeat(close, n) }
	cases := []struct {
		name, in, ours, jq, goJSON string
	}{
		{"a byte order mark, skipped", "\xef\xbb\xbf{\"a\":1}", "{\"a\":1}\n", "", "error"},
		{"two byte order marks", "\xef\xbb\xbf\xef\xbb\xbf1", "error", "", ""},
		{"a byte order mark after a value", "1\xef\xbb\xbf", "error", "", ""},
		{"a cut sequence is one unit", "\"a\xe2\x82b\"", "\"a" + ff + "b\"\n", "", "\"a" + ff + ff + "b\"\n"},
		{"two stray bytes are two units", "\"a\xff\xfeb\"", "\"a" + ff + ff + "b\"\n", "", "\"a" + ff + ff + "b\"\n"},
		{"a sequence cut by the string's end takes the rest", "\"a\xf0A\"", "\"a" + ff + "\"\n", "", "\"a" + ff + "A\"\n"},
		{"an escape inside a cut sequence", "\"a\xf0\\n\"", "\"a" + ff + "\"\n", "", "\"a" + ff + "\\n\"\n"},
		{"an escape after a cut sequence", "\"\xf0\\u0041bc\"", "\"" + ff + "Abc\"\n", "", ""},
		{"an encoded surrogate is one unit", "\"\xed\xa0\x80\"", "\"" + ff + "\"\n", "", "\"" + ff + ff + ff + "\"\n"},
		{"an overlong form is one unit", "\"\xe0\x80\x80\"", "\"" + ff + "\"\n", "", "\"" + ff + ff + ff + "\"\n"},
		{"0xC0 leads nothing", "\"\xc0\x80\"", "\"" + ff + ff + "\"\n", "", ""},
		{"past U+10FFFF is one unit", "\"\xf4\x90\x80\x80\"", "\"" + ff + "\"\n", "", "\"" + ff + ff + ff + ff + "\"\n"},
		{"bad UTF-8 in a key", "{\"\xff\":1}", "{\"" + ff + "\":1}\n", "", ""},
		{"a duplicate key keeps its place and its last value", `{"b":1,"a":2,"b":3}`, `{"b":3,"a":2}` + "\n", "",
			`{"a":2,"b":3}` + "\n"},
		{"a lone high surrogate escape", `"\ud800"`, "error", "", "\"" + ff + "\"\n"},
		{"a high surrogate escape before a letter", `"\ud800x"`, "error", "", ""},
		{"a high surrogate escape before another escape", `"\ud800\u0041"`, "error", "", "\"" + ff + "A\"\n"},
		{"a lone low surrogate escape", `"\udc00"`, "\"" + ff + "\"\n", "", "\"" + ff + "\"\n"},
		{"a surrogate pair", `"\ud83d\ude00"`, "\"😀\"\n", "", ""},
		{"escapes jq leaves alone when printing", `"\u00E9\/\u2028"`, "\"é/\u2028\"\n", "", ""},
		{"a short \\u escape", `"\u12"`, "error", "", ""},
		{"a bad escape", `"\x"`, "error", "", ""},
		{"a raw NUL", "\"a\x00b\"", "error", "", "error"},
		{"a raw 0x1f", "\"a\x1fb\"", "error", "", "error"},
		{"a raw tab", "\"a\tb\"", "error", "", "error"},
		{"a raw DEL", "\"a\x7fb\"", "\"a\\u007fb\"\n", "", ""},
		{"form feed is not white space", "\x0c1", "error", "", "error"},
		{"a record separator is not white space", "1\x1e2", "error", "", ""},
		{"01", "01", "error", "1\n", "0\n1\n"},
		{"[01]", "[01]", "error", "[1]\n", "error"},
		{"1.", "1.", "error", "1\n", "error"},
		{".5", ".5", "error", "0.5\n", "error"},
		{"+1", "+1", "error", "1\n", "error"},
		{"-01", "-01", "error", "-1\n", "-0\n1\n"},
		{"1.e5", "1.e5", "error", "1E+5\n", "error"},
		{"nan", "nan", "error", "null\n", "error"},
		{"-nan", "-nan", "error", "null\n", "error"},
		{"NaN", "NaN", "error", "null\n", "error"},
		{"sNaN", "sNaN", "error", "null\n", "error"},
		{"inf", "inf", "error", "1.7976931348623157e+308\n", "error"},
		{"-Inf", "-Inf", "error", "-1.7976931348623157e+308\n", "error"},
		{"Infinity", "Infinity", "error", "1.7976931348623157e+308\n", "error"},
		{"a literal past decNumber's range", "1e1000000000", "1.7976931348623157e+308\n", "", ""},
		{"a NUL ends a number", "1\x00x", "1\n", "", "error"},
		{"a NUL ends a number in an array", "[1,2\x003]", "[1,2]\n", "", "error"},
		{"a NUL ends a number, then white space and a value", "1\x00x 2", "1\n2\n", "", ""},
		{"a NUL before an exponent", "1.5\x00e3", "1.5\n", "", ""},
		{"a NUL inside an exponent", "1e\x002", "error", "", ""},
		{"a NUL before a number", "\x001", "error", "", ""},
		{"a NUL alone", "\x00", "error", "", ""},
		{"a NUL after true", "true\x00", "error", "", ""},
		{"a NUL inside null", "nu\x00ll", "error", "", ""},
		{"a NUL in a three-byte token after n", "n\x00l", "error", "", ""},
		{"two objects, no space", "{}{}", "{}\n{}\n", "", ""},
		{"a number, then a string", `1"a"`, "1\n\"a\"\n", "", ""},
		{"an array, then false", "[true]false", "[true]\nfalse\n", "", ""},
		{"two literals, no space", "truefalse", "error", "", ""},
		{"two numbers", "1 2", "1\n2\n", "", ""},
		{"a trailing comma", "[1,]", "error", "", "error"},
		{"an unfinished array", "[1,2", "error", "", "error"},
		{"single quotes", "'a'", "error", "", "error"},
		{"nothing", "", "", "", ""},
		{"white space only", " \n\t\r", "", "", ""},
		{"256 arrays deep", deep("[", "]", 256), deep("[", "]", 256) + "\n", "", ""},
		{"257 arrays deep", deep("[", "]", 257), "error", "", deep("[", "]", 257) + "\n"},
		{"128 objects deep", deep(`{"a":`, "}", 128)[:128*5] + "1" + strings.Repeat("}", 128),
			deep(`{"a":`, "}", 128)[:128*5] + "1" + strings.Repeat("}", 128) + "\n", "", ""},
		{"129 objects deep", deep(`{"a":`, "}", 129)[:129*5] + "1" + strings.Repeat("}", 129), "error", "",
			deep(`{"a":`, "}", 129)[:129*5] + "1" + strings.Repeat("}", 129) + "\n"},
	}
	jq := maybeJQ(t)
	for _, c := range cases {
		wantJQ := c.jq
		if wantJQ == "" {
			wantJQ = c.ours
		}
		if got := readAll([]byte(c.in)); got != c.ours {
			t.Errorf("%s: jsonx reads %q, want %q", c.name, got, c.ours)
		}
		if jq != "" {
			got := "error"
			if out, ok := tryJQ(jq, []byte(c.in), "-c", "."); ok {
				got = string(out)
			}
			if got != wantJQ {
				t.Errorf("%s: jq reads %q, the table says %q", c.name, got, wantJQ)
			}
		}
		if c.goJSON != "" {
			if got := readGo([]byte(c.in)); got != c.goJSON {
				t.Errorf("%s: encoding/json reads %q, the table says %q", c.name, got, c.goJSON)
			}
		}
	}

	// Parse keeps exactly one value, as load() does: none, or two, is unreadable.
	for _, c := range []struct {
		in string
		ok bool
	}{{"", false}, {" \n", false}, {"1 2", false}, {"{}{}", false}, {"1 x", false}, {"\xef\xbb\xbf1\n", true},
		{"\n{\"a\":[1]}\n\n", true}, {"null", true}} {
		if _, err := Parse([]byte(c.in)); (err == nil) != c.ok {
			t.Errorf("Parse(%q): error %v, want ok %t", c.in, err, c.ok)
		}
	}
}

// readAll is the stream as jsonx reads it, printed as `jq -c .` prints a stream, or "error".
func readAll(in []byte) string {
	vs, err := ParseStream(in)
	if err != nil {
		return "error"
	}
	var b []byte
	for _, v := range vs {
		b = append(b, MarshalCompact(v)...)
	}
	return string(b)
}

// readGo is the stream as encoding/json reads it, numbers kept as text, printed compact with HTML left alone, or
// "error".
func readGo(in []byte) string {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for {
		var v any
		if err := dec.Decode(&v); err == io.EOF {
			return b.String()
		} else if err != nil {
			return "error"
		}
		if err := enc.Encode(v); err != nil {
			return "error"
		}
	}
}
