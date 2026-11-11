package main

// laug release: give a leased cell back; the pool destroys it.

import (
	"context"
	"fmt"

	"github.com/ymur-studio/laug/internal/poolapi"
)

// runRelease gives back the cell behind a lease, named by its id or by -last, and removes the last-lease file when it
// names that lease, so load and verdict never read a cell that is gone. A refused release leaves the file.
func runRelease(ctx context.Context, a *App, args []string) int {
	s, code, ok := parseSubject(a, "release", args)
	if !ok {
		return code
	}
	doc, err := s.client.Release(ctx, s.id)
	if err != nil {
		note := ""
		if l, rerr := poolapi.ReadLast(s.path); rerr == nil && l.ID == s.id {
			note = s.file + " still names it"
		}
		return poolFailed(ctx, a, "release", s.pool, s.id, note, err)
	}
	removed, err := poolapi.RemoveLast(s.path, s.id)
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · release: %s given back, but %s, which names it, stays: %v\n", s.id, s.file, a.errShown(err))
		return exitFailed
	}
	if s.json {
		return printed(a, "release", writeCompact(a.Stdout, doc))
	}
	file := ""
	switch l, err := poolapi.ReadLast(s.path); {
	case removed:
		file = "; " + s.file + " removed"
	case err == nil:
		file = "; " + s.file + " names " + l.ID + ", which stays"
	}
	fmt.Fprintf(a.Stdout, "laug · release: %s given back, its cell destroyed%s\n", s.id, file)
	return exitDone
}
