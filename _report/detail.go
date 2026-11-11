package report

// detail.go holds what the figures read of a raun's windows beyond the records: the four windows' names, in order, and
// a raun's stamp as faults/run.sh writes it.

import "github.com/ymur-studio/laug/internal/jsonx"

var windowNames = [4]string{"before", "during", "drain", "after"}

// stamp is a raun's stamp, which faults/run.sh writes as a string of whole seconds: its number, OK when it is one.
func stamp(v jsonx.Value) Number {
	if s, ok := v.Str(); ok {
		n, err := jsonx.Literal(s)
		if err != nil {
			return Number{}
		}
		return Number{n, true}
	}
	return num(v)
}
