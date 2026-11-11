package report

// timeofday.go keeps times of day out of the tables' stamp columns: a column that holds when something happened holds
// its date, or the seconds from another stamp of its record, and a duration stays as recorded. The record keeps the
// time itself. The columns cut so are each table's in docs/tables/README.md; values a figure table keeps of a record's
// lines are cut where the figure's reader says so (fig-staleness, fig-toxic-fuse).

import (
	"regexp"
	"strconv"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// rfc3339 is a time as the records write one: a date, T, a time of day, a fraction, and Z or an offset.
var rfc3339 = regexp.MustCompile(`([0-9]{4}-[0-9]{2}-[0-9]{2})T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?` +
	`(?:Z|[+-][0-9]{2}:[0-9]{2})`)

// dateOnly is s with each RFC 3339 time in it cut to its date.
func dateOnly(s string) string { return rfc3339.ReplaceAllString(s, "$1") }

// dated is v with every string in it, at any depth, cut by dateOnly; members and their order stay.
func dated(v jsonx.Value) jsonx.Value {
	switch v.Kind() {
	case jsonx.KindString:
		s, _ := v.Str()
		return jsonx.Str(dateOnly(s))
	case jsonx.KindArray:
		a, _ := v.Arr()
		out := make([]jsonx.Value, len(a))
		for i, x := range a {
			out[i] = dated(x)
		}
		return jsonx.Arr(out)
	case jsonx.KindObject:
		o, _ := v.Obj()
		out := jsonx.NewObject()
		for i := range o.Len() {
			k, x := o.Member(i)
			out.Set(k, dated(x))
		}
		return jsonx.Obj(out)
	}
	return v
}

// stampSecs is v as a Unix time in seconds, a number or a string of one, as a raun writes its stamps; ok is false for
// anything else.
func stampSecs(v jsonx.Value) (float64, bool) {
	n := stamp(v)
	if !n.OK {
		return 0, false
	}
	f, err := strconv.ParseFloat(n.String(), 64)
	return f, err == nil
}

// unixDate is the UTC date of v, a Unix time; null when v is not one.
func unixDate(v jsonx.Value) jsonx.Value {
	f, ok := stampSecs(v)
	if !ok {
		return jsonx.Null()
	}
	return jsonx.Str(time.Unix(int64(f), 0).UTC().Format(time.DateOnly))
}

// secondsAfter is v less zero, both Unix times, in seconds; null when either is not one.
func secondsAfter(v, zero jsonx.Value) jsonx.Value {
	a, ok1 := stampSecs(v)
	b, ok2 := stampSecs(zero)
	if !ok1 || !ok2 {
		return jsonx.Null()
	}
	return jsonx.Float(a - b)
}

// raunStamps are a raun's stamps after its inject_at, each a Unix time; a rauns row holds each as <stamp>_s.
var raunStamps = []string{"landed_at", "remove_at", "recovered_at", "after_start", "after_end"}

// mechanismsAfter is a raun's mechanisms with each one's at, a Unix time, as at_s, its seconds after inject, in at's
// place; anything that is not a list of objects stays as it is.
func mechanismsAfter(ms, inject jsonx.Value) jsonx.Value {
	a, ok := ms.Arr()
	if !ok {
		return ms
	}
	out := make([]jsonx.Value, len(a))
	for i, m := range a {
		o, ok := m.Obj()
		if !ok {
			out[i] = m
			continue
		}
		n := jsonx.NewObject()
		for j := range o.Len() {
			k, x := o.Member(j)
			if k == "at" {
				n.Set("at_s", secondsAfter(x, inject))
				continue
			}
			n.Set(k, x)
		}
		out[i] = jsonx.Obj(n)
	}
	return jsonx.Arr(out)
}

// isRaunRow says whether v is a rauns row, whose stamps are seconds after the inject, rather than a raun JSON.
func isRaunRow(v jsonx.Value) bool {
	o, ok := v.Obj()
	if !ok {
		return false
	}
	_, row := o.Get("landed_at_s")
	return row
}
