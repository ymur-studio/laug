package main

// laug reset: destroy the cell behind a lease and build it again, under the same lease.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/ymur-studio/laug/internal/poolapi"
)

// runReset rebuilds the cell behind a lease, named by its id or by -last. The id, the port block and the expiry stay,
// so the last-lease file stays as it is. A reset whose create fails ends the lease, so the file may go (failedReset).
func runReset(ctx context.Context, a *App, args []string) int {
	s, code, ok := parseSubject(a, "reset", args)
	if !ok {
		return code
	}
	l, doc, err := s.client.Reset(ctx, s.id)
	if err != nil {
		return poolFailed(ctx, a, "reset", s.pool, s.id, a.failedReset(ctx, s, err), err)
	}
	if s.json {
		return printed(a, "reset", writeCompact(a.Stdout, doc))
	}
	fmt.Fprintf(a.Stdout, "laug · reset: %s rebuilt under the same lease, for %s until %s; %s gives it back\n", l.ID,
		plain(l.Requester), utc(l.ExpiresAt), a.releaseLine(l.ID, s.pool, s.file))
	return printed(a, "reset", a.writeIndented(a.Stdout, doc))
}

// failedReset is what a failed reset (err) did to the last-lease file, as the end of its line; empty when the file
// names another lease or none. A reset whose create fails ends the lease: the pool forgets it, and may build on its
// port block at once, or keeps its cell as stuck (internal/pool, Reset). So when the pool answered with anything but
// 404, and its status then shows the lease neither leased nor busy, the file is removed, as release removes it. Then
// load and verdict never read a cell that is gone or stuck. A 404, which a pool other than the lease's gives too, no
// answer, or a signal leaves the file, as a refused release does. After a signal the pool finishes the reset on its
// own.
func (a *App) failedReset(ctx context.Context, s subject, err error) string {
	if l, rerr := poolapi.ReadLast(s.path); rerr != nil || l.ID != s.id {
		return ""
	}
	still := s.file + " still names it"
	var pe *poolapi.Error
	if ctx.Err() != nil || !errors.As(err, &pe) || pe.Code == http.StatusNotFound {
		return still
	}
	st, _, serr := s.client.Status(ctx)
	if serr != nil {
		return still
	}
	for _, c := range st.Cells {
		if c.ID == s.id && (c.State == "leased" || c.State == "busy") {
			return still
		}
	}
	if _, rerr := poolapi.RemoveLast(s.path, s.id); rerr != nil {
		return "the pool no longer has it leased, but " + s.file + ", which names it, stays: " + a.errShown(rerr)
	}
	return s.file + " removed, since the pool no longer has it leased"
}
