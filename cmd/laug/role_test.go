package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

// TestRoleNames: a role names the verb it runs for at the start of its stderr lines, "laug · <command>: ", as every
// laug error is named, and never a binary that no longer exists. runRole hands the role the command and verb as typed,
// and sdkRoles hands them on to the role's own Main: each of the six refuses before it dials with a line that starts
// so, health check's round trip under loop and record naming loop and record.
func TestRoleNames(t *testing.T) {
	t.Run("runRole hands on the command", func(t *testing.T) {
		for _, args := range [][]string{{"health", "check"}, {"health", "loop"}, {"health", "timer"}} {
			rec := &recorder{}
			code, _, errOut := runApp(t, &App{Runner: rec, Roles: rec}, args...)
			if want := []string{strings.Join(args, " ")}; code != 0 || !slices.Equal(rec.cmds, want) {
				t.Errorf("%q: exit %d, the role ran for %q, want %q\n%s", args, code, rec.cmds, want, errOut)
			}
		}
	})
	t.Run("the six roles", func(t *testing.T) {
		repo := t.TempDir()
		addr := []string{"TEMPORAL_ADDRESS=127.0.0.1:7400"}
		for _, c := range []struct {
			role, cmd     string
			args, environ []string
			code          int
			line          string
		}{
			{"healthcheck", "health check", []string{"x"}, nil, 2, `laug · health check: takes no arguments, got ["x"]`},
			{"healthcheck", "health loop", []string{"x"}, nil, 2, `laug · health loop: takes no arguments, got ["x"]`},
			{"healthcheck", "health record", []string{"x"}, nil, 2,
				`laug · health record: takes no arguments, got ["x"]`},
			{"timerprobe", "health timer", []string{"60s"}, nil, 2,
				`laug · health timer: takes no arguments, got ["60s"]`},
			{"worker", "orchestrator worker", nil, addr, 2,
				"laug · orchestrator worker: -repo is required: run laug orchestrator worker"},
			{"probe", "orchestrator probe", nil, addr, 2,
				"laug · orchestrator probe: run laug orchestrator probe [-idle 60s] [-unpolled]"},
			{"record", "orchestrator record", []string{"-repo", repo, "-gate", "g1", "-out", "x.json"}, addr, 2,
				`laug · orchestrator record: -gate "g1": a gate id, g<date>-<time>, as laug gate run -workflow printed it`},
			{"gate", "gate run", []string{"-repo", repo}, addr, 2,
				`laug · gate run: SERVER_TAG must be a release tag, x.y.z; got ""`},
			{"gate", "gate status", []string{"-status", "g20261016-120000"}, addr, 2,
				"laug · gate status: -repo is required: run laug gate run -workflow"},
			{"nope", "health check", nil, nil, 1, `laug · health check: no SDK role "nope"`},
		} {
			var stdout, stderr bytes.Buffer
			code := sdkRoles{}.Role(context.Background(), c.role, c.cmd, c.args, c.environ, &stdout, &stderr)
			if code != c.code || stderr.String() != c.line+"\n" || stdout.Len() != 0 {
				t.Errorf("%s for %s %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s\nwant:\n%s", c.role, c.cmd, c.args,
					code, c.code, stdout.String(), stderr.String(), c.line)
			}
		}
	})
}
