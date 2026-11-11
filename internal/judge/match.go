package judge

import (
	"regexp"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// The judge's regular expressions, as Go matches what jq 1.7's Oniguruma matched. Two differences matter, both
// seen on jq-1.7 (TestRegexMatchesJQ): jq's `$` also matches before one final newline, so "abc\n" matches
// "^[a-z]*$" and "abc\n\n" does not, where Go's `$` matches at the end only; each pattern that ends in `$` ends in
// `\n?\z` here. And `^` matches at the start of the string only, in both. `.` matches anything but a newline in both.

// A pattern is one of JUDGE's test() patterns: jq's text and Go's.
type pattern struct {
	jq string
	re *regexp.Regexp
}

var (
	// keyName is the plan key check.
	keyName = pattern{`^[a-z0-9][a-z0-9-]*$`, regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\n?\z`)}
	// overMsEnd and msEnd are scale_of's.
	overMsEnd = pattern{`_over_[0-9]+ms$`, regexp.MustCompile(`_over_[0-9]+ms\n?\z`)}
	msEnd     = pattern{`_ms$`, regexp.MustCompile(`_ms\n?\z`)}
	// sweepFailed is the sweep test on a deciding row's result, unanchored.
	sweepFailed = pattern{`the sweep .* failed`, regexp.MustCompile(`the sweep .* failed`)}
)

// patterns is every test() pattern above, for TestRegexMatchesJQ.
var patterns = []pattern{keyName, overMsEnd, msEnd, sweepFailed}

// test is jq's test($re) on v: true when re matches somewhere in v. A v that is not a string is jq's error.
func test(v jsonx.Value, p pattern) bool {
	s, ok := v.Str()
	if !ok {
		fail("%s (%s) cannot be matched, as it is not a string", v.Type(), v)
	}
	return p.re.MatchString(s)
}

// recoveredIn is class_table's `^recovered in [0-9]`.
func recoveredIn(s string) bool {
	const prefix = "recovered in "
	return len(s) > len(prefix) && s[:len(prefix)] == prefix && isDigit(s[len(prefix)])
}

// failedIterations is class_table's `^[0-9]+ failed iteration\(s\) `, a blank at its end.
func failedIterations(s string) bool {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	const rest = " failed iteration(s) "
	return i > 0 && len(s)-i >= len(rest) && s[i:i+len(rest)] == rest
}
