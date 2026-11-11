package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"testing"
)

func TestColourNeverOnABuffer(t *testing.T) {
	a := &App{Environ: []string{"TERM=xterm-256color", "NO_COLOR="}}
	for _, v := range []string{"ADVANCE", "BLOCK"} {
		var b bytes.Buffer
		if err := a.writeVerdict(&b, v, false); err != nil || b.String() != v+"\n" {
			t.Errorf("%s on a buffer: %q, %v", v, b.String(), err)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for name, wr := range map[string]io.Writer{"a buffer": &bytes.Buffer{}, "a file": f, "a pipe": w} {
		if isTerminal(wr, "xterm-256color") {
			t.Errorf("%s is a terminal", name)
		}
	}
	// /dev/null is a character device, so it stands in for a terminal: TERM decides.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	for term, want := range map[string]bool{"xterm-256color": true, "dumb": false, "": false} {
		if got := isTerminal(null, term); got != want {
			t.Errorf("%s with TERM %q: %t, want %t", os.DevNull, term, got, want)
		}
	}
}

// TestColourOnATerminal fakes the terminal: moss and ember, bold, on their own line, unless NO_COLOR or -json. The
// App's environment decides, not the process's: the process's NO_COLOR is set throughout.
func TestColourOnATerminal(t *testing.T) {
	saved := isTerminal
	t.Cleanup(func() { isTerminal = saved })
	isTerminal = func(_ io.Writer, term string) bool { return term == "xterm-256color" }
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	for _, c := range []struct {
		name    string
		environ []string
		verdict string
		json    bool
		want    string
	}{
		{"ADVANCE", []string{"TERM=xterm-256color"}, "ADVANCE", false, "\x1b[1;38;2;91;155;106mADVANCE\x1b[0m\n"},
		{"BLOCK", []string{"TERM=xterm-256color"}, "BLOCK", false, "\x1b[1;38;2;196;75;50mBLOCK\x1b[0m\n"},
		{"NO_COLOR empty", []string{"NO_COLOR=", "TERM=xterm-256color"}, "BLOCK", false, "\x1b[1;38;2;196;75;50mBLOCK\x1b[0m\n"},
		{"NO_COLOR", []string{"TERM=xterm-256color", "NO_COLOR=1"}, "ADVANCE", false, "ADVANCE\n"},
		{"NO_COLOR, BLOCK", []string{"NO_COLOR=yes", "TERM=xterm-256color"}, "BLOCK", false, "BLOCK\n"},
		{"no TERM", nil, "ADVANCE", false, "ADVANCE\n"},
		{"the first TERM", []string{"TERM=dumb", "TERM=xterm-256color"}, "ADVANCE", false, "ADVANCE\n"},
		{"-json", []string{"TERM=xterm-256color"}, "BLOCK", true, "BLOCK\n"},
		{"another word", []string{"TERM=xterm-256color"}, "no verdict", false, "no verdict\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			a := &App{Environ: c.environ}
			if err := a.writeVerdict(&b, c.verdict, c.json); err != nil || b.String() != c.want {
				t.Errorf("got %q, %v; want %q", b.String(), err, c.want)
			}
		})
	}
}

// TestVerdictColoursMatchBrand ties the escapes to the hex values in docs/BRAND.md's Colour table.
func TestVerdictColoursMatchBrand(t *testing.T) {
	brand, err := os.ReadFile("../../docs/BRAND.md")
	if err != nil {
		t.Fatal(err)
	}
	for token, sgr := range map[string]string{"moss": sgrAdvance, "ember": sgrBlock} {
		m := regexp.MustCompile("(?m)^\\| " + token + " \\| `#([0-9A-Fa-f]{6})`").FindSubmatch(brand)
		if m == nil {
			t.Fatalf("BRAND.md has no %s row", token)
		}
		n, _ := strconv.ParseUint(string(m[1]), 16, 32)
		if want := fmt.Sprintf("\x1b[1;38;2;%d;%d;%dm", n>>16, n>>8&0xff, n&0xff); sgr != want {
			t.Errorf("%s: %q, BRAND.md says #%s, %q", token, sgr, m[1], want)
		}
	}
}
