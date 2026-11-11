package health

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// CheckMain and TimerMain take their own flags, as the binaries did, and refuse before they dial: -h exits 0, a flag
// they do not take or an argument exits 2, with a line on stderr and nothing on stdout. A line names the laug command
// the role runs for, as every laug error does: "laug · <command>: ".
func TestMainsTakeTheirFlags(t *testing.T) {
	for _, c := range []struct {
		name string
		main func(context.Context, string, []string, []string, io.Writer, io.Writer) int
		cmd  string
		args []string
		code int
		line string
	}{
		{"check -h", CheckMain, "health check", []string{"-h"}, 0, "Usage of health check:"},
		{"check, a bad flag", CheckMain, "health check", []string{"-nope"}, 2, "flag provided but not defined: -nope"},
		{"check, an argument", CheckMain, "health check", []string{"x"}, 2,
			`laug · health check: takes no arguments, got ["x"]`},
		{"loop, an argument", CheckMain, "health loop", []string{"-loop", "1s", "x"}, 2,
			`laug · health loop: takes no arguments, got ["x"]`},
		{"timer -h", TimerMain, "health timer", []string{"-h"}, 0, "Usage of health timer:"},
		{"timer, a bad flag", TimerMain, "health timer", []string{"-timer", "soon"}, 2,
			`invalid value "soon" for flag -timer`},
		{"timer, an argument", TimerMain, "health timer", []string{"60s"}, 2,
			`laug · health timer: takes no arguments, got ["60s"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := c.main(context.Background(), c.cmd, c.args, nil, &stdout, &stderr)
			if code != c.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), c.line) {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, stdout.String(), stderr.String())
			}
		})
	}
}
