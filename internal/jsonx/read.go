package jsonx

import (
	"errors"
	"fmt"
	"strings"
)

// Parse reads data as exactly one JSON value, as internal/judge's load keeps a file: no value, or more than one, is an
// error, as is anything the stream reader refuses.
func Parse(data []byte) (Value, error) {
	s := scanner{data: trimBOM(data)}
	s.space()
	if s.pos == len(s.data) {
		return Value{}, errors.New("no JSON value")
	}
	v, err := s.value(0)
	if err != nil {
		return Value{}, err
	}
	s.space()
	if s.pos < len(s.data) {
		return Value{}, s.errorf("more than one JSON value")
	}
	return v, nil
}

// ParseStream reads data as the stream of JSON values jq reads from one file, as load_lines() slurps poll.jsonl
// (internal/judge, load): none is an empty list. As in jq, two values need white space between them only when
// both are numbers, true, false or null: {}{} and 1"a" are two values each, 12 is one and truefalse an error.
//
// The reader takes what jq 1.7 takes, but for the spellings RFC 8259 refuses (TestReaderDifferences):
//   - One UTF-8 byte order mark at the start is skipped, as jq skips it. encoding/json refuses it.
//   - Bad UTF-8 in a string becomes U+FFFD per unit of jq's jvp_utf8_next, cut after escapes are read (Valid).
//     encoding/json gives one U+FFFD per byte.
//   - \uXXXX escapes as jq 1.7 reads them: a high surrogate must start a pair, and a lone low surrogate is U+FFFD.
//     encoding/json reads a lone high surrogate as U+FFFD.
//   - A duplicate key keeps its first place and its last value (Object.Set).
//   - Arrays nest 256 deep, objects 128, as jq's parser stack allows: it counts an array 1 and an object 2.
//   - A number token ends at its first NUL byte, as jq hands it to decNumber as a C string (src/jv_parse.c,
//     check_literal): 1\x00x reads 1, [1,2\x003] reads [1,2], and \x001 is no number. true, false and null are
//     compared whole, so a NUL in one is an error in both. encoding/json refuses a NUL outside a string.
//   - Numbers are RFC 8259's only, each read by Literal. jq also takes 01, 1., .5, +1, nan, NaN, Infinity and
//     decNumber's other spellings; the reader refuses them, so a file holding one is unreadable here and not in jq.
//     No committed input holds one: jq or yq wrote each.
func ParseStream(data []byte) ([]Value, error) {
	s := scanner{data: trimBOM(data)}
	vs := []Value{}
	for {
		s.space()
		if s.pos == len(s.data) {
			return vs, nil
		}
		v, err := s.value(0)
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
}

func trimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// maxStack is the depth of jq 1.7's parser stack (src/jv_parse.c, MAX_PARSING_DEPTH): an array takes one entry, an
// object two, its key the second, and a [ or { that finds the stack full is an error (seen on jq-1.7: 256 nested
// arrays read, 257 do not; 128 nested objects read, 129 do not).
const maxStack = 256

type scanner struct {
	data []byte
	pos  int
}

func (s *scanner) errorf(format string, a ...any) error {
	return fmt.Errorf("byte %d: %s", s.pos, fmt.Sprintf(format, a...))
}

func (s *scanner) space() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\r', '\n':
			s.pos++
		default:
			return
		}
	}
}

// value reads one value at s.pos, past any white space; stack is the depth of jq's parser stack there.
func (s *scanner) value(stack int) (Value, error) {
	s.space()
	if s.pos == len(s.data) {
		return Value{}, s.errorf("unfinished JSON term")
	}
	switch s.data[s.pos] {
	case '[':
		return s.array(stack)
	case '{':
		return s.object(stack)
	case '"':
		str, err := s.string()
		if err != nil {
			return Value{}, err
		}
		return Value{kind: KindString, str: str}, nil
	case ']', '}', ',', ':':
		return Value{}, s.errorf("unexpected %q", s.data[s.pos])
	}
	start := s.pos
	for s.pos < len(s.data) && !delimiter(s.data[s.pos]) {
		s.pos++
	}
	switch tok := string(s.data[start:s.pos]); tok {
	case "null":
		return Null(), nil
	case "true":
		return Bool(true), nil
	case "false":
		return Bool(false), nil
	default:
		// jq's check_literal hands a number token to decNumber as a C string, which ends at the first NUL (seen
		// on jq-1.7: 1\x00x reads 1). true, false and null are compared whole above, and Literal refuses any token
		// that starts with a letter, so a NUL in one of them stays an error, as in jq.
		if i := strings.IndexByte(tok, 0); i >= 0 {
			tok = tok[:i]
		}
		n, err := Literal(tok)
		if err != nil {
			s.pos = start
			return Value{}, s.errorf("%v", err)
		}
		return Num(n), nil
	}
}

// delimiter says c ends a token that is not a string: white space, a quote or a structural character, as jq's
// scanner classifies bytes. Any other byte belongs to the token.
func delimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '"', '[', ']', '{', '}', ',', ':':
		return true
	}
	return false
}

func (s *scanner) array(stack int) (Value, error) {
	if stack >= maxStack {
		return Value{}, s.errorf("exceeds depth limit for parsing")
	}
	s.pos++
	elems := []Value{}
	s.space()
	if s.pos < len(s.data) && s.data[s.pos] == ']' {
		s.pos++
		return Arr(elems), nil
	}
	for {
		v, err := s.value(stack + 1)
		if err != nil {
			return Value{}, err
		}
		elems = append(elems, v)
		s.space()
		if s.pos == len(s.data) {
			return Value{}, s.errorf("unfinished JSON term")
		}
		switch s.data[s.pos] {
		case ',':
			s.pos++
		case ']':
			s.pos++
			return Arr(elems), nil
		default:
			return Value{}, s.errorf("expected , or ] after an array element")
		}
	}
}

func (s *scanner) object(stack int) (Value, error) {
	if stack >= maxStack {
		return Value{}, s.errorf("exceeds depth limit for parsing")
	}
	s.pos++
	o := NewObject()
	s.space()
	if s.pos < len(s.data) && s.data[s.pos] == '}' {
		s.pos++
		return Obj(o), nil
	}
	for {
		s.space()
		if s.pos == len(s.data) || s.data[s.pos] != '"' {
			return Value{}, s.errorf("object keys must be strings")
		}
		k, err := s.string()
		if err != nil {
			return Value{}, err
		}
		s.space()
		if s.pos == len(s.data) || s.data[s.pos] != ':' {
			return Value{}, s.errorf("expected : after an object key")
		}
		s.pos++
		v, err := s.value(stack + 2)
		if err != nil {
			return Value{}, err
		}
		o.Set(k, v)
		s.space()
		if s.pos == len(s.data) {
			return Value{}, s.errorf("unfinished JSON term")
		}
		switch s.data[s.pos] {
		case ',':
			s.pos++
		case '}':
			s.pos++
			return Obj(o), nil
		default:
			return Value{}, s.errorf("expected , or } after an object member")
		}
	}
}

// string reads the string at s.pos as jq does: the token runs to the first quote no backslash escapes, then its
// escapes are read (src/jv_parse.c, found_string), then its bad UTF-8 is replaced (Valid).
func (s *scanner) string() (string, error) {
	end := s.pos + 1
	for end < len(s.data) && s.data[end] != '"' {
		if s.data[end] == '\\' {
			end++
		}
		end++
	}
	if end >= len(s.data) {
		return "", s.errorf("unfinished string")
	}
	tok := s.data[s.pos+1 : end]
	out := make([]byte, 0, len(tok))
	for i := 0; i < len(tok); {
		c := tok[i]
		i++
		if c != '\\' {
			if c < 0x20 {
				return "", s.errorf("control characters from U+0000 through U+001F must be escaped")
			}
			out = append(out, c)
			continue
		}
		e := tok[i]
		i++
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if i+4 > len(tok) {
				return "", s.errorf(`invalid \uXXXX escape`)
			}
			cp, ok := hex4(tok[i : i+4])
			if !ok {
				return "", s.errorf(`invalid characters in \uXXXX escape`)
			}
			i += 4
			if 0xD800 <= cp && cp <= 0xDBFF {
				if i+6 > len(tok) || tok[i] != '\\' || tok[i+1] != 'u' {
					return "", s.errorf(`invalid \uXXXX\uXXXX surrogate pair escape`)
				}
				lo, ok := hex4(tok[i+2 : i+6])
				if !ok || lo < 0xDC00 || lo > 0xDFFF {
					return "", s.errorf(`invalid \uXXXX\uXXXX surrogate pair escape`)
				}
				i += 6
				cp = 0x10000 + ((cp-0xD800)<<10 | (lo - 0xDC00))
			}
			out = appendCodePoint(out, cp)
		default:
			return "", s.errorf("invalid escape")
		}
	}
	s.pos = end + 1
	return Valid(string(out)), nil
}

func hex4(b []byte) (rune, bool) {
	var r rune
	for _, c := range b {
		switch {
		case '0' <= c && c <= '9':
			r = r<<4 | rune(c-'0')
		case 'a' <= c && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case 'A' <= c && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// appendCodePoint encodes cp in UTF-8's bit pattern, a lone surrogate too, as jq's jvp_utf8_encode does; Valid
// then reads such a surrogate as one bad unit.
func appendCodePoint(b []byte, cp rune) []byte {
	switch {
	case cp < 0x80:
		return append(b, byte(cp))
	case cp < 0x800:
		return append(b, 0xC0|byte(cp>>6), 0x80|byte(cp&0x3F))
	case cp < 0x10000:
		return append(b, 0xE0|byte(cp>>12), 0x80|byte(cp>>6&0x3F), 0x80|byte(cp&0x3F))
	}
	return append(b, 0xF0|byte(cp>>18), 0x80|byte(cp>>12&0x3F), 0x80|byte(cp>>6&0x3F), 0x80|byte(cp&0x3F))
}
