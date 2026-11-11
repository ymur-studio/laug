package main

import (
	"context"
	"fmt"
)

// runHelp prints the help; with a command, that command's verbs, or its flags when it has one verb, as its -h does;
// with a command and a verb, that verb's flags, as the verb's -h does.
func runHelp(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("help", "[<command> [<verb>]]")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		usage(a.Stdout, a.self())
		return exitDone
	case len(pos) > 2:
		fmt.Fprintf(a.Stderr, "laug · help: takes a command and a verb at most, got %q\n", pos)
		f.mistake(a.Stderr)
		return exitUsage
	}
	c, found := lookup(pos[0])
	if !found {
		return unknownCommand(a, "laug · help: ", pos[0])
	}
	if len(pos) == 1 {
		return c.dispatch(ctx, a, []string{"-h"})
	}
	if len(c.verbs) == 1 && c.verbs[0].name == "" {
		fmt.Fprintf(a.Stderr, "laug · help: %s has no verbs; %s help %s prints its flags\n", c.name, a.self(), c.name)
		f.mistake(a.Stderr)
		return exitUsage
	}
	return c.dispatch(ctx, a, []string{pos[1], "-h"})
}
