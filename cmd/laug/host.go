package main

// laug host: what a gate checks about this host. type declares the host's type and prints what a gate would say of
// it (gate/host-type.sh); clock measures the wall clock against the uptime clock (cells/clock-check.sh).

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// hostTypeRE is a host type as EC2 names one, as gate/host-type.sh and gate/lib.sh's host_check take it.
var hostTypeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,31}$`)

func runHostType(_ context.Context, a *App, args []string) int {
	f := a.newFlags("host type", "[-declare <type>] [-json] [-dry-run]")
	typ := f.String("declare", "", "declare this host's `type`, as EC2 names one (m8i.8xlarge), in "+
		"$XDG_CONFIG_HOME/laug/host-type, then print; unset, it prints and writes nothing")
	jsonOut := f.Bool("json", false, "print one JSON object: declared, vcpu, mem_gib, virtualized, set, passes and reason")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -declare declares a type", pos)
	}
	c := script.Command{Script: "gate/host-type.sh"}
	first := ""
	if given(f, "declare") {
		if !hostTypeRE.MatchString(*typ) {
			return a.refused("host type", fmt.Errorf("-declare %s: a type name, lowercase letters, digits, . and -, "+
				"32 at most, as EC2 names one (m8i.8xlarge)", shellWord(*typ)))
		}
		c.Args = []script.Value{{Text: *typ, Quote: true}}
	} else {
		first = "laug · host type: the type this host declares, its size and the threshold set's host, as a gate " +
			"checks them"
	}
	if !*jsonOut {
		c.Clear = []string{"JSON"}
		return a.runScript("host type", c, *dry, script.Exec, first)
	}
	// -json keeps the script a child, as every -json does: JSON=1 has it print one object in place of its lines.
	c.Pass = []script.Var{script.Bare("JSON", "1")}
	c, env, code, ok := a.prepare("host type", c, *dry)
	if !ok {
		return code
	}
	if *dry {
		fmt.Fprintln(a.Stdout, script.DryRun(c))
		return exitDone
	}
	// gate/host-type.sh's own codes are 0 printed and 2 refused (its header).
	return a.childExit("host type", "nothing printed", c.Script, a.start("host type", c, env, script.Run, "",
		a.Stdout), exitDone, exitUsage)
}

func runHostClock(_ context.Context, a *App, args []string) int {
	f := a.newFlags("host clock", "[-for 60s] [-dry-run]")
	length := f.Duration("for", clockSecondsDefault*time.Second, "how long to measure, one sample a second, whole "+
		"seconds, 2s or more")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -for sets how long", pos)
	}
	secs, err := seconds("-for", *length)
	if err == nil && *length < 2*time.Second {
		err = fmt.Errorf("-for %s: 2s or more", *length)
	}
	if err != nil {
		return a.refused("host clock", err)
	}
	c := script.Command{Script: "cells/clock-check.sh", Args: []script.Value{{Text: secs, Quote: true}},
		Clear: []string{"SECONDS"}, By: map[string]string{"SECONDS": "-for"}}
	return a.runScript("host clock", c, *dry, script.Exec, "")
}
