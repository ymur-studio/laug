package jsonx

import "unicode/utf8"

// Marshal is v as `jq .` prints it: two-space indent, `"key": value`, one element or member per line, [] and {}
// when empty, and a final newline.
func Marshal(v Value) []byte {
	return append(AppendIndent(nil, v), '\n')
}

// MarshalCompact is v as `jq -c .` prints it: no space at all, and a final newline.
func MarshalCompact(v Value) []byte {
	return append(AppendCompact(nil, v), '\n')
}

// AppendIndent appends v in jq's default form, without the final newline.
func AppendIndent(b []byte, v Value) []byte { return appendValue(b, v, 0, true) }

// AppendCompact appends v in jq's -c form, which is also tojson's.
func AppendCompact(b []byte, v Value) []byte { return appendValue(b, v, 0, false) }

func appendValue(b []byte, v Value, depth int, pretty bool) []byte {
	switch v.kind {
	case KindNull:
		return append(b, "null"...)
	case KindFalse:
		return append(b, "false"...)
	case KindTrue:
		return append(b, "true"...)
	case KindNumber:
		return append(b, v.num.String()...)
	case KindString:
		return AppendString(b, v.str)
	case KindArray:
		if len(v.arr) == 0 {
			return append(b, "[]"...)
		}
		b = append(b, '[')
		for i, e := range v.arr {
			if i > 0 {
				b = append(b, ',')
			}
			b = newline(b, depth+1, pretty)
			b = appendValue(b, e, depth+1, pretty)
		}
		b = newline(b, depth, pretty)
		return append(b, ']')
	case KindObject:
		if v.obj.Len() == 0 {
			return append(b, "{}"...)
		}
		b = append(b, '{')
		for i, k := range v.obj.keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = newline(b, depth+1, pretty)
			b = AppendString(b, k)
			b = append(b, ':')
			if pretty {
				b = append(b, ' ')
			}
			b = appendValue(b, v.obj.vals[i], depth+1, pretty)
		}
		b = newline(b, depth, pretty)
		return append(b, '}')
	}
	panic("jsonx: a value of no kind")
}

func newline(b []byte, depth int, pretty bool) []byte {
	if !pretty {
		return b
	}
	b = append(b, '\n')
	for range depth {
		b = append(b, ' ', ' ')
	}
	return b
}

// AppendString appends s quoted as jq's dump quotes it (src/jv_print.c, jvp_dump_string): `"` and `\` take a
// backslash; \b, \t, \n, \f and \r their short forms; any other byte under 0x20, and 0x7f, \u00xx in lowercase hex;
// everything else, `/ < > &`, U+2028 and U+2029 among it, stays as it is. encoding/json differs: it escapes U+2028
// and U+2029 always and `< > &` unless told not to, and leaves 0x7f. A byte of bad UTF-8, which no Value holds,
// is written as U+FFFD.
func AppendString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				b = utf8.AppendRune(b, utf8.RuneError)
			} else {
				b = append(b, s[i:i+n]...)
			}
			i += n
			continue
		}
		i++
		switch {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\t':
			b = append(b, '\\', 't')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c < 0x20 || c == 0x7f:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}
