package main

import (
	"fmt"
	"io"
	"os"
)

// The verdict colors of docs/BRAND.md's Colour table, moss #5B9B6A for ADVANCE and ember #C44B32 for BLOCK, as
// bold ANSI 24-bit foreground escapes.
const (
	sgrAdvance = "\x1b[1;38;2;91;155;106m"
	sgrBlock   = "\x1b[1;38;2;196;75;50m"
	sgrReset   = "\x1b[0m"
)

// isTerminal reports whether w is a terminal: an *os.File on a character device, with term, TERM's value, set and
// not "dumb". The standard library has no isatty, so /dev/null, a character device, passes too. Tests replace it.
var isTerminal = func(w io.Writer, term string) bool {
	f, ok := w.(*os.File)
	if !ok || term == "" || term == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// colour reports whether a verdict written to w is colored: -json is off, the App's NO_COLOR is unset or empty
// (no-color.org, which ignores an empty value) and w is a terminal by the App's TERM.
func (a *App) colour(w io.Writer, jsonOut bool) bool {
	return !jsonOut && a.getenv("NO_COLOR") == "" && isTerminal(w, a.getenv("TERM"))
}

// writeVerdict writes a verdict on its own line: ADVANCE bold in moss and BLOCK bold in ember when colour holds,
// the plain word otherwise. Any other word is written plain.
func (a *App) writeVerdict(w io.Writer, verdict string, jsonOut bool) error {
	sgr := map[string]string{"ADVANCE": sgrAdvance, "BLOCK": sgrBlock}[verdict]
	if sgr == "" || !a.colour(w, jsonOut) {
		_, err := fmt.Fprintln(w, verdict)
		return err
	}
	_, err := fmt.Fprintf(w, "%s%s%s\n", sgr, verdict, sgrReset)
	return err
}
