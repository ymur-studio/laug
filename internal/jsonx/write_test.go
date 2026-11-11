package jsonx

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestJQStrings pins jq's string escaping, seen on jq-1.7 (`jq -Rs .` of the raw bytes): `"` and `\` take a
// backslash, \b \t \n \f \r their short forms, other bytes under 0x20 and 0x7f a lowercase \u00xx, and nothing
// else is escaped, U+2028, U+2029 and `< > & /` included. Bad UTF-8 becomes U+FFFD per jq unit. With jq present,
// each case is checked against jq too.
func TestJQStrings(t *testing.T) {
	ascii := make([]byte, 128)
	for i := range ascii {
		ascii[i] = byte(i)
	}
	cases := []struct{ in, want string }{
		{string(ascii), `"\u0000\u0001\u0002\u0003\u0004\u0005\u0006\u0007\b\t\n\u000b\f\r\u000e\u000f\u0010\u0011` +
			`\u0012\u0013\u0014\u0015\u0016\u0017\u0018\u0019\u001a\u001b\u001c\u001d\u001e\u001f !\"#$%&'()*+,-./` +
			"0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\\\]^_`abcdefghijklmnopqrstuvwxyz{|}~\\u007f\""},
		{"\u2028\u2029<>&/é😀·", "\"\u2028\u2029<>&/é😀·\""},
		{">= 5700", `">= 5700"`},
		{"", `""`},
		{"a\xe2\x82b\xff\xfe\xf0A", "\"a\ufffdb\ufffd\ufffd\ufffd\""},
		{"\xed\xa0\x80\xc0\x80\xf4\x90\x80\x80", "\"\ufffd\ufffd\ufffd\ufffd\""},
	}
	jq := maybeJQ(t)
	for _, c := range cases {
		got := string(AppendString(nil, Valid(c.in)))
		if got != c.want {
			t.Errorf("%q: jsonx writes %s, want %s", c.in, got, c.want)
		}
		if s, _ := Str(c.in).Str(); string(AppendString(nil, s)) != got {
			t.Errorf("%q: Str and Valid differ", c.in)
		}
		if jq != "" {
			if out := runJQ(t, jq, []byte(c.in), "-Rs", "."); string(out) != c.want+"\n" {
				t.Errorf("%q: jq writes %s, the table %s", c.in, out, c.want)
			}
		}
	}

	// Keys escape as values do, in both forms; encoding/json would escape U+2028 and < > &.
	o := NewObject()
	o.Set("k\x01\"", Str("\x7f"))
	o.Set("\u2028<", Float(1))
	v := Obj(o)
	if got, want := string(MarshalCompact(v)), "{\"k\\u0001\\\"\":\"\\u007f\",\"\u2028<\":1}\n"; got != want {
		t.Errorf("compact: %s, want %s", got, want)
	}
	if got, want := string(Marshal(v)), "{\n  \"k\\u0001\\\"\": \"\\u007f\",\n  \"\u2028<\": 1\n}\n"; got != want {
		t.Errorf("indent: %s, want %s", got, want)
	}
	if b, _ := json.Marshal("\u2028<\x7f"); bytes.Equal(b, AppendString(nil, "\u2028<\x7f")) {
		t.Errorf("encoding/json now writes %s as jq does: the reason for a writer of its own is gone", b)
	}
}

// TestIndentForms pins jq's layout for empty and nested containers, seen on jq-1.7.
func TestIndentForms(t *testing.T) {
	v, err := Parse([]byte(`{"a":[],"b":{},"c":[1,[2,{}],{"d":null}],"e":"x","f":true,"g":false}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "a": [],
  "b": {},
  "c": [
    1,
    [
      2,
      {}
    ],
    {
      "d": null
    }
  ],
  "e": "x",
  "f": true,
  "g": false
}
`
	if got := string(Marshal(v)); got != want {
		t.Errorf("indent:\n%s\nwant:\n%s", got, want)
	}
	if got, want := string(MarshalCompact(v)), `{"a":[],"b":{},"c":[1,[2,{}],{"d":null}],"e":"x","f":true,"g":false}`+"\n"; got != want {
		t.Errorf("compact: %s, want %s", got, want)
	}
	if got := v.String(); got+"\n" != string(MarshalCompact(v)) {
		t.Errorf("String is not -c's form without the newline: %s", got)
	}
	for _, c := range []struct {
		v    Value
		want string
	}{{Null(), "null\n"}, {Arr(nil), "[]\n"}, {Obj(nil), "{}\n"}, {Num(Number{}), "0\n"}} {
		if got := string(Marshal(c.v)); got != c.want {
			t.Errorf("Marshal = %q, want %q", got, c.want)
		}
	}
}
