package jsonx

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestTotalOrder holds values in jq's order, one rank per line, values of one rank equal: kinds first, then
// literals as exact decimals, strings by bytes, arrays element by element, objects by sorted keys then values.
// Compare must give every pair its ranks' order, Sort must restore the order from a shuffle, and with jq present
// `<` and `==` over every pair and `sort` must agree.
func TestTotalOrder(t *testing.T) {
	ranks := [][]string{
		{`null`},
		{`false`},
		{`true`},
		{`-1e1000000000`, `-2e1000000000`},
		{`-1e400`},
		{`-1.5`, `-15e-1`},
		{`-1`, `-1.0`, `-1.00`},
		{`-0.10000000000000000555`},
		{`-0.1`},
		{`0`, `-0`, `0.0`, `0E+10`, `-0e-5`},
		{`1e-400`},
		{`0.1`, `0.10`},
		{`0.10000000000000000555`},
		{`1`, `1.0`, `10e-1`},
		{`1.0000000000000000001`},
		{`9007199254740992`},
		{`9007199254740993`},
		{`1e400`},
		{`9e999999999`},
		{`1e1000000000`, `5e1000000000`},
		{`""`},
		{`"A"`},
		{`"a"`},
		{`"a\u0000"`},
		{`"ab"`},
		{`"b"`},
		{`"é"`},
		{`"😀"`},
		{`[]`},
		{`[null]`},
		{`[false]`},
		{`[0]`, `[-0.0]`},
		{`[0,null]`},
		{`[1]`, `[1.0]`},
		{`["a"]`},
		{`[[]]`},
		{`[{}]`},
		{`{}`},
		{`{"a":null}`},
		{`{"a":1}`, `{"a":1.00}`},
		{`{"a":2}`},
		{`{"a":1,"b":0}`, `{"b":0,"a":1}`},
		{`{"a":1,"b":1}`},
		{`{"b":0}`},
	}
	type item struct {
		text string
		v    Value
		rank int
	}
	var items []item
	for r, texts := range ranks {
		for _, s := range texts {
			v, err := Parse([]byte(s))
			if err != nil {
				t.Fatalf("%s: %v", s, err)
			}
			items = append(items, item{s, v, r})
		}
	}
	sign := func(x int) int { return min(max(x, -1), 1) }
	for _, a := range items {
		for _, b := range items {
			if got, want := Compare(a.v, b.v), sign(a.rank-b.rank); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a.text, b.text, got, want)
			}
		}
	}

	rng := rand.New(rand.NewPCG(7, 22))
	perm := rng.Perm(len(items))
	shuffled := make([]Value, len(items))
	for i, j := range perm {
		shuffled[i] = items[j].v
	}
	sorted := append([]Value(nil), shuffled...)
	Sort(sorted)
	for i := 1; i < len(sorted); i++ {
		if Compare(sorted[i-1], sorted[i]) > 0 {
			t.Errorf("Sort left %s before %s", sorted[i-1], sorted[i])
		}
	}

	// NaN, which only arithmetic makes: below every number and itself, above null (seen on jq-1.7:
	// nan < nan, nan < 0 and nan > null are true; nan == nan and nan > 0 false).
	nan := Float(math.NaN())
	for _, c := range []struct {
		a, b Value
		want int
	}{{nan, nan, -1}, {nan, Float(0), -1}, {Float(math.Inf(-1)), nan, 1}, {nan, Null(), 1}, {Null(), nan, -1},
		{Num(lit(t, "-1e1000000000")), nan, 1}, {Arr([]Value{nan}), Arr([]Value{nan}), -1}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if Equal(nan, nan) {
		t.Errorf("nan == nan, want not")
	}

	jq := maybeJQ(t)
	if jq == "" {
		return
	}
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.text
	}
	all := "[" + strings.Join(texts, ",") + "]"
	matrix := runJQ(t, jq, []byte(all), "-c",
		`. as $all | [$all[] as $a | [$all[] as $b | if $a < $b then -1 elif $a == $b then 0 else 1 end]]`)
	m, err := Parse(matrix)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := m.Arr()
	for i, row := range rows {
		cells, _ := row.Arr()
		for j, c := range cells {
			n, _ := c.Num()
			if got := Compare(items[i].v, items[j].v); float64(got) != n.F {
				t.Errorf("%s against %s: jsonx %d, jq %s", items[i].text, items[j].text, got, n)
			}
		}
	}
	var in []string
	for _, j := range perm {
		in = append(in, items[j].text)
	}
	jqSorted := strings.TrimSuffix(string(runJQ(t, jq, []byte("["+strings.Join(in, ",")+"]"), "-c", "sort")), "\n")
	if got := Arr(sorted).String(); got != jqSorted {
		t.Errorf("Sort gives %s\njq's sort gives %s", got, jqSorted)
	}
}
