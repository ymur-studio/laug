package script

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Stray is a knob the caller's environment held that the verb maps, and that does not reach the script as it was:
// the verb sets another value, or none. By is what sets the knob in that verb when it is not the table's Flag, as
// gate run's <tag> sets SERVER_TAG; empty, the table's Flag says.
type Stray struct {
	Name, Value, By string
}

// String says what happened to the stray copy and what sets the knob instead, on one line: a value with a control
// character in it is quoted.
func (s Stray) String() string {
	by := "laug sets it"
	if k, ok := Lookup(s.Name); s.By != "" {
		by = s.By + " sets it"
	} else if ok && k.Flag != "" {
		by = k.Flag + " sets it"
	}
	v := s.Value
	if strings.ContainsFunc(v, unicode.IsControl) {
		v = strconv.Quote(v)
	}
	return s.Name + "=" + v + " in the environment is not passed on; " + by
}

// Env is the environment c's script runs with, built from parent, the caller's, one KEY=value a string. It holds the
// names Base, Tools and Seams match, as parent has them, the first of a name winning as os.Getenv's does; then c.Pass
// and c.Set. Every other name in parent is dropped, every knob among them, so a SERVER_TAG or a RUN_ID exported in the
// caller's shell never reaches a script. The strays are the knobs c maps (Set, Pass or Clear) that parent holds with
// another value, by name, each with what c.By says sets it.
func Env(parent []string, c Command) (env []string, strays []Stray) {
	env = []string{}
	seen := map[string]bool{}
	var maps []string
	set := map[string]string{}
	for _, v := range slices.Concat(c.pass(), c.Set) {
		maps = append(maps, v.Name)
		set[v.Name] = v.Text
	}
	maps = append(maps, c.Clear...)
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if passes(k) {
			env = append(env, kv)
			continue
		}
		if want, isSet := set[k]; slices.Contains(maps, k) && (!isSet || want != v) {
			strays = append(strays, Stray{k, v, c.By[k]})
		}
	}
	for _, v := range slices.Concat(c.pass(), c.Set) {
		env = append(env, v.Name+"="+v.Text)
	}
	slices.SortFunc(strays, func(a, b Stray) int { return strings.Compare(a.Name, b.Name) })
	return env, strays
}
