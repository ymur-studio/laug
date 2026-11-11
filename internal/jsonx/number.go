package jsonx

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// Number is a JSON number as jq 1.7 holds one, in one of two forms.
//
// A literal (Lit true) is a number jq read, from input or as a constant in the jq source. Text is the decNumber
// canonical form of its token (to-scientific-string), which jq 1.7 prints and interpolates in place of the token:
// 8.460 stays 8.460, 1e-05 becomes 0.00001, 1E2 becomes 1E+2 and 0.000000123 becomes 1.23E-7. F is its value as jq
// converts it to a double: the decimal rounded to 17 significant digits, half to even, then to the nearest double
// (seen on jq-1.7: 1.00000000000000011102230246251565404236316680908203126 converts to 1, and 18-digit ties go to
// the even 17th digit). A literal whose adjusted exponent passes 999999999 is decNumber's infinity: Text "Infinity"
// or "-Infinity", F infinite, and it prints as a double does.
//
// A computed number (Lit false) is any arithmetic result. Only F counts, and it prints in jq 1.7's dtoa form
// (FormatFloat). The zero Number is the computed 0.
type Number struct {
	Text string
	F    float64
	Lit  bool
}

// jq 1.7 reads a literal in a decNumber context of 147483648 digits that rounds half up, with exponents from
// -999999999 to 999999999 (seen on jq-1.7: 1e-1147483646 is the smallest power of ten a literal keeps, 0e1000000000
// prints 0E+999999999 and 1e1000000000 overflows).
const (
	litDigits = 147483648
	litEmax   = 999999999
	litEmin   = -999999999
	litEtiny  = litEmin - (litDigits - 1)
	dblDigits = 17
)

// Literal is the literal jq 1.7 reads from the token tok, which must be an RFC 8259 number. jq takes more spellings
// (01, 1., .5, +1, nan, Infinity); Literal refuses them, as the reader does.
func Literal(tok string) (Number, error) {
	d, ok := scanDecimal(tok)
	if !ok {
		return Number{}, errors.New("invalid numeric literal: " + strconv.Quote(tok))
	}
	d = d.finalize()
	return Number{Text: d.text(), F: d.float(), Lit: true}, nil
}

// Computed is the number an arithmetic result f is in jq.
func Computed(f float64) Number {
	return Number{F: f}
}

// String is n as jq 1.7 prints it, the same in JSON output, tostring, tojson and string interpolation: a literal's
// Text, else the dtoa form of F.
func (n Number) String() string {
	if n.Lit && !n.infinite() {
		return n.Text
	}
	return FormatFloat(n.F)
}

// infinite says n is a literal past decNumber's range.
func (n Number) infinite() bool {
	return n.Lit && (n.Text == "Infinity" || n.Text == "-Infinity")
}

// FormatFloat is f in jq 1.7's dtoa form (src/jv_dtoa.c, jvp_dtoa_fmt): the shortest digits that read back as f,
// in exponent form when the decimal point sits 4 or more places before the first digit or more than 15 places past
// the last, with a signed exponent of at least two digits; else plain. -0 is "-0". NaN prints null, and an
// infinity the largest double of its sign, as jq's dump does (seen on jq-1.7: 1e-05, 1e+16, 123456789012345680,
// 1.7976931348623157e+308).
func FormatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "null"
	case f > math.MaxFloat64:
		f = math.MaxFloat64
	case f < -math.MaxFloat64:
		f = -math.MaxFloat64
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // [-]d[.ddd]e±dd: the exponent form already
	mant, exp, _ := strings.Cut(e, "e")
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	decpt := x + 1 // the value is 0.<digits> times 10^decpt, as dtoa returns it
	if decpt <= -4 || decpt > len(digits)+15 {
		return e
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch {
	case decpt <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -decpt))
		b.WriteString(digits)
	case decpt < len(digits):
		b.WriteString(digits[:decpt])
		b.WriteByte('.')
		b.WriteString(digits[decpt:])
	default:
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", decpt-len(digits)))
	}
	return b.String()
}

// decimal is a number as decNumber holds it: a sign, the coefficient's digits with no leading zero ("0" for zero)
// and the exponent, or an infinity.
type decimal struct {
	neg  bool
	inf  bool
	coef string
	exp  int64
}

// scanDecimal reads an RFC 8259 number token. A literal's Text is one too, but for an infinity's (decimalOf).
func scanDecimal(tok string) (d decimal, ok bool) {
	s := tok
	if strings.HasPrefix(s, "-") {
		d.neg, s = true, s[1:]
	}
	i := 0
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && isDigit(s[i]):
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	default:
		return d, false
	}
	digits := s[:i]
	frac := 0
	if i < len(s) && s[i] == '.' {
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if j == i+1 {
			return d, false
		}
		digits += s[i+1 : j]
		frac = j - i - 1
		i = j
	}
	var exp int64
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		neg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			neg = s[i] == '-'
			i++
		}
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if j == i {
			return d, false
		}
		exp = exponent(s[i:j])
		if neg {
			exp = -exp
		}
		i = j
	}
	if i != len(s) {
		return d, false
	}
	d.coef = strings.TrimLeft(digits, "0")
	if d.coef == "" {
		d.coef = "0"
	}
	d.exp = exp - int64(frac)
	return d, true
}

// exponent reads an exponent's digits as decNumber's from-string does: past 10 significant digits, or at 10 that
// start above 1, it is 1999999998, which no context holds.
func exponent(s string) int64 {
	s = strings.TrimLeft(s, "0")
	if len(s) > 10 || len(s) == 10 && s[0] > '1' {
		return 2 * 999999999
	}
	n, _ := strconv.ParseInt("0"+s, 10, 64)
	return n
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// finalize fits d to jq's literal context, as decNumber's from-string does: a zero's exponent is clamped to
// [litEtiny, litEmax]; a coefficient past litDigits digits, or an exponent under litEtiny, is rounded half up, once;
// an adjusted exponent past litEmax is an infinity.
func (d decimal) finalize() decimal {
	if d.inf {
		return d
	}
	if d.coef == "0" {
		d.exp = min(max(d.exp, litEtiny), litEmax)
		return d
	}
	drop := max(int64(len(d.coef))-litDigits, litEtiny-d.exp)
	if drop > 0 {
		d.coef = roundCoef(d.coef, drop, false)
		d.exp += drop
		if int64(len(d.coef)) > litDigits { // 999...9 carried to 10^litDigits
			d.coef = d.coef[:len(d.coef)-1]
			d.exp++
		}
	}
	if d.coef != "0" && d.exp+int64(len(d.coef))-1 > litEmax {
		return decimal{neg: d.neg, inf: true}
	}
	return d
}

// roundCoef drops the last n digits of coef, rounding half up, or half to even when even is set, and returns the
// digits kept with no leading zero: "0" when nothing is left. A carry can add one digit.
func roundCoef(coef string, n int64, even bool) string {
	if n <= 0 {
		return coef
	}
	if n > int64(len(coef)) {
		return "0" // the first dropped digit is a leading zero
	}
	keep, rest := coef[:int64(len(coef))-n], coef[int64(len(coef))-n:]
	up := rest[0] > '5' || rest[0] == '5' && !even
	if even && rest[0] == '5' {
		up = strings.TrimRight(rest[1:], "0") != "" || keep != "" && (keep[len(keep)-1]-'0')%2 == 1
	}
	if !up {
		if keep == "" {
			return "0"
		}
		return keep
	}
	b := []byte(keep)
	i := len(b) - 1
	for ; i >= 0 && b[i] == '9'; i-- {
		b[i] = '0'
	}
	if i < 0 {
		return "1" + string(b)
	}
	b[i]++
	return string(b)
}

// text is d's decNumber to-scientific-string: plain when the exponent is at most 0 and the adjusted exponent at
// least -6, else one digit, the rest after a point, and E with a sign and the adjusted exponent.
func (d decimal) text() string {
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	if d.inf {
		b.WriteString("Infinity")
		return b.String()
	}
	n := int64(len(d.coef))
	adj := d.exp + n - 1
	switch {
	case d.exp == 0:
		b.WriteString(d.coef)
	case d.exp < 0 && adj >= -6:
		if -d.exp < n {
			b.WriteString(d.coef[:n+d.exp])
			b.WriteByte('.')
			b.WriteString(d.coef[n+d.exp:])
		} else {
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", int(-d.exp-n)))
			b.WriteString(d.coef)
		}
	default:
		b.WriteString(d.coef[:1])
		if n > 1 {
			b.WriteByte('.')
			b.WriteString(d.coef[1:])
		}
		b.WriteByte('E')
		if adj >= 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.FormatInt(adj, 10))
	}
	return b.String()
}

// float is d as jq 1.7 converts a literal to a double: rounded to 17 digits, half to even, then to the nearest
// double, which strconv does as jq's strtod does. Past the double's range it is an infinity, under it a zero, each
// with d's sign.
func (d decimal) float() float64 {
	if d.inf {
		if d.neg {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	coef, exp := d.coef, d.exp
	if n := int64(len(coef)) - dblDigits; n > 0 {
		coef = roundCoef(coef, n, true)
		exp += n
	}
	s := coef + "e" + strconv.FormatInt(exp, 10)
	if d.neg {
		s = "-" + s
	}
	f, _ := strconv.ParseFloat(s, 64) // a range error still gives the infinity or the zero
	return f
}

// compareDecimals orders two literals as exact decimals, as decNumberCompare does: every zero is equal, and an
// infinity is past every finite number of its sign.
func compareDecimals(a, b Number) int {
	x, y := decimalOf(a.Text), decimalOf(b.Text)
	sx, sy := x.sign(), y.sign()
	if sx != sy {
		if sx < sy {
			return -1
		}
		return 1
	}
	return sx * compareMagnitudes(x, y)
}

// decimalOf reads a literal's Text.
func decimalOf(text string) decimal {
	if text == "Infinity" || text == "-Infinity" {
		return decimal{neg: text[0] == '-', inf: true}
	}
	d, _ := scanDecimal(text)
	return d
}

func (d decimal) sign() int {
	switch {
	case !d.inf && d.coef == "0":
		return 0
	case d.neg:
		return -1
	}
	return 1
}

// compareMagnitudes orders the absolute values of two decimals of one sign, neither zero unless both are.
func compareMagnitudes(x, y decimal) int {
	switch {
	case x.inf && y.inf:
		return 0
	case x.inf:
		return 1
	case y.inf:
		return -1
	case x.coef == "0" && y.coef == "0":
		return 0
	}
	ax, ay := x.exp+int64(len(x.coef)), y.exp+int64(len(y.coef))
	if ax != ay {
		if ax < ay {
			return -1
		}
		return 1
	}
	cx, cy := strings.TrimRight(x.coef, "0"), strings.TrimRight(y.coef, "0")
	return strings.Compare(cx, cy) // same leading position: digit by digit, and a longer tail is larger
}
