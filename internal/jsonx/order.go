package jsonx

import (
	"cmp"
	"math"
	"slices"
	"strings"
)

// Compare is jq 1.7's total order (src/jv.c, jv_cmp), which sort, group_by, unique, keys, min, max and the
// comparison operators use: null, false, true, numbers, strings, arrays, objects. Strings go by bytes, the shorter
// first on a common prefix; arrays element by element, the shorter first; objects by their sorted keys, compared as
// arrays, then by their values in that key order. Two literals compare as exact decimals, any other pair of numbers
// as doubles (jvp_number_cmp), so 0.1 and 0.10000000000000000555 differ as literals and not as doubles. A NaN is
// below every number, itself included, as jq 1.7 orders it: nan < nan is true and nan == nan false (seen on jq-1.7).
// It returns -1, 0 or 1.
func Compare(a, b Value) int {
	if a.kind != b.kind {
		return cmp.Compare(a.kind, b.kind)
	}
	switch a.kind {
	case KindNumber:
		return compareNumbers(a.num, b.num)
	case KindString:
		return strings.Compare(a.str, b.str)
	case KindArray:
		for i := range min(len(a.arr), len(b.arr)) {
			if c := Compare(a.arr[i], b.arr[i]); c != 0 {
				return c
			}
		}
		return cmp.Compare(len(a.arr), len(b.arr))
	case KindObject:
		ka, kb := a.obj.SortedKeys(), b.obj.SortedKeys()
		if c := slices.Compare(ka, kb); c != 0 {
			return c
		}
		for _, k := range ka {
			x, _ := a.obj.Get(k)
			y, _ := b.obj.Get(k)
			if c := Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return 0
}

// Equal is jq's ==, which is Compare's 0.
func Equal(a, b Value) bool { return Compare(a, b) == 0 }

// CompareNumbers is Compare for two numbers.
func CompareNumbers(a, b Number) int { return compareNumbers(a, b) }

func compareNumbers(a, b Number) int {
	switch {
	case math.IsNaN(a.F):
		return -1
	case math.IsNaN(b.F):
		return 1
	case a.Lit && b.Lit:
		return compareDecimals(a, b)
	}
	return cmp.Compare(a.F, b.F)
}

// Sort sorts vs by Compare, keeping equal values in their order, as jq's sort does.
func Sort(vs []Value) {
	slices.SortStableFunc(vs, Compare)
}
