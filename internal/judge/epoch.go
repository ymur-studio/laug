package judge

import (
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// epoch is `epoch`:
//
//	def epoch: if type == "string" then (try fromdateiso8601 catch null) else null end;
//
// a time in seconds since 1970, or null when v is not a string or does not parse.
func epoch(v jsonx.Value) jsonx.Value {
	s, ok := v.Str()
	if !ok {
		return null
	}
	t, ok := fromdateiso8601(s)
	if !ok {
		return null // try ... catch null
	}
	return jsonx.Float(float64(t))
}

// fromdateiso8601 is jq 1.7's fromdateiso8601 on glibc, `strptime("%Y-%m-%dT%H:%M:%SZ") | mktime`, and false where
// jq raises an error:
//
//   - jq hands the string to glibc's strptime as a C string, so it ends at a NUL byte.
//   - strptime reads each number as glibc's get_number does: white space skipped first, then at least one digit,
//     more while the width allows and the value times 10 stays in range, then the range checked: %Y 0 to 9999 in
//     up to 4 digits, %m 1 to 12, %d 1 to 31, %H 0 to 23, %M 0 to 59 and %S 0 to 61, each in up to 2. The `-`,
//     `T`, `:` and `Z` between must be the next byte exactly.
//   - jq then checks the one byte after `Z` (src/builtin.c, f_strptime): the end or white space, and the rest is
//     not read. Anything else does not match the format.
//   - mktime is timegm, which normalizes a day or a second out of range (Feb 31 is Mar 3), as time.Date does. jq
//     calls -1 "invalid gmtime representation" and -2 "mktime not supported on this platform": both errors.
//
// White space is C's isspace in the C locale, as the judge's jq ran under LC_ALL=C: space, \t, \n, \v, \f and \r.
// Seen on jq-1.7: 2026-10-18T9:29:26Z, a leading space, ...Z xyz and ...Z\njunk parse; ...Zxyz, fractions and
// +00:00 do not; 1969-12-31T23:59:59Z and :58Z error, :57Z gives -3 (TestEpochMatchesJQ).
func fromdateiso8601(s string) (int64, bool) {
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	p := 0
	number := func(lo, hi, width int) (int, bool) { // glibc's get_number
		for p < len(s) && isCSpace(s[p]) {
			p++
		}
		if p == len(s) || !isDigit(s[p]) {
			return 0, false
		}
		v := 0
		for {
			v = v*10 + int(s[p]-'0')
			p++
			width--
			if width == 0 || v*10 > hi || p == len(s) || !isDigit(s[p]) {
				break
			}
		}
		return v, lo <= v && v <= hi
	}
	char := func(c byte) bool {
		if p < len(s) && s[p] == c {
			p++
			return true
		}
		return false
	}
	var f [6]int
	ok := true
	for i, step := range []struct {
		lo, hi, width int
		after         byte
	}{{0, 9999, 4, '-'}, {1, 12, 2, '-'}, {1, 31, 2, 'T'}, {0, 23, 2, ':'}, {0, 59, 2, ':'}, {0, 61, 2, 'Z'}} {
		if f[i], ok = number(step.lo, step.hi, step.width); !ok || !char(step.after) {
			return 0, false
		}
	}
	if p < len(s) && !isCSpace(s[p]) {
		return 0, false // date "..." does not match format "%Y-%m-%dT%H:%M:%SZ"
	}
	t := time.Date(f[0], time.Month(f[1]), f[2], f[3], f[4], f[5], 0, time.UTC).Unix()
	if t == -1 || t == -2 {
		return 0, false
	}
	return t, true
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// isCSpace is isspace in the C locale.
func isCSpace(c byte) bool {
	return c == ' ' || '\t' <= c && c <= '\r'
}
