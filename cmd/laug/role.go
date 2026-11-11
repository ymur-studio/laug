package main

// The edge a verb that runs a Temporal SDK role crosses: runRole hands the role, in laug's own process, the
// environment internal/script builds, as runScript hands a script its own. This file imports no package that links the
// SDK: health.go and orchestrator.go hold the roles (healthRoles, orchRoles).

import (
	"context"
	"fmt"

	"github.com/ymur-studio/laug/internal/script"
)

// role is one SDK role as a verb runs it: its name, its own flags, the knobs laug sets in the environment it reads, the
// knobs the verb maps that this run leaves unset, and what sets a knob when it is not the table's flag.
type role struct {
	name  string // healthcheck, timerprobe, worker, gate, probe or record
	args  []string
	set   []script.Var
	clear []string
	by    map[string]string
}

// runRole runs r for cmd, a command and its verb as the user typed them ("health check", "gate run"), in laug's own
// process: with the environment scriptEnv builds from environ, script.Env's and LAUG, after one stderr line for each
// stray copy of a knob r maps, then first on stdout when it is not empty. The role gets cmd too, and names its stderr
// lines with it, "laug · <cmd>: ". It returns the role's exit code; 1 when r sets a name that is not a knob, or the App
// has no Roles.
func (a *App) runRole(ctx context.Context, cmd string, r role, environ []string, first string) int {
	c := script.Command{Script: r.name, Set: r.set, Clear: r.clear, By: r.by}
	if err := c.Check(); err != nil {
		fmt.Fprintf(a.Stderr, "laug · %s: %v\n", cmd, a.errShown(err))
		return exitFailed
	}
	env, strays := a.scriptEnv(environ, c)
	for _, s := range strays {
		fmt.Fprintf(a.Stderr, "laug · %s: %s\n", cmd, s)
	}
	if a.Roles == nil {
		fmt.Fprintf(a.Stderr, "laug · %s: no SDK roles, so %s did not run\n", cmd, r.name)
		return exitFailed
	}
	if first != "" {
		fmt.Fprintln(a.Stdout, first)
	}
	return a.Roles.Role(ctx, r.name, cmd, r.args, env, a.Stdout, a.Stderr)
}
