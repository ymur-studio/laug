package main

// laug lease: a cell from the pool, kept as the last lease. Beside runLease, what reset and release share with it: the
// lease they act on (parseSubject), where the last-lease file is, the line that gives a lease back, the pool's answer
// printed, and its errors.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/poolapi"
)

// leaseOpts is lease's flags. -ttl defaults to 30m, and -requester to laug, not the caller's login name, which would
// reach every lease and the results that copy it.
type leaseOpts struct {
	pool      string
	ttl       time.Duration
	requester string
	serverTag string
	file      string
	json      bool
}

func runLease(ctx context.Context, a *App, args []string) int {
	var o leaseOpts
	f := a.newFlags("lease", "[-pool <address>] [-ttl 30m] [-requester laug] [-server-tag <x.y.z>] [-file <path>] [-json]")
	f.StringVar(&o.pool, "pool", poolDefault, "the pool's `address`: a loopback host:port, or an http:// URL")
	f.DurationVar(&o.ttl, "ttl", 30*time.Minute, "how long the lease lasts, in whole seconds; the pool's max lease cuts it")
	f.StringVar(&o.requester, "requester", "laug", "the `name` the pool's quota counts this lease under")
	f.StringVar(&o.serverTag, "server-tag", "", "the server `tag` the cell runs, a release, x.y.z; empty takes any warm cell")
	f.StringVar(&o.file, "file", poolapi.LastLeaseFile,
		"the last-lease `file` the scripts read; a relative path starts at the checkout's root")
	f.BoolVar(&o.json, "json", false, "print the pool's answer, the lease, as one JSON object")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(a.Stderr, "laug · lease: takes no arguments, got %q\n", pos)
		f.mistake(a.Stderr)
		return exitUsage
	}
	refuse := func(format string, v ...any) int {
		fmt.Fprintf(a.Stderr, "laug · lease: "+format+"\n", v...)
		return exitUsage
	}
	c, err := poolapi.New(o.pool)
	if err != nil {
		return refuse("-pool %s: %v", o.pool, err)
	}
	req, err := leaseRequest(o.ttl, o.requester, o.serverTag)
	if err != nil {
		return refuse("%v", err)
	}
	l, doc, err := a.leaseSaying(ctx, "lease", o.pool, c, req)
	if err != nil {
		return poolFailed(ctx, a, "lease", o.pool, "", "", err)
	}
	// The pool's answer goes to the file as it came, as curl -o wrote it: the scripts read it with jq.
	if err := poolapi.WriteLast(lastLeasePath(a, o.file), doc); err != nil {
		fmt.Fprintf(a.Stderr, "laug · lease: leased %s but could not keep it in %s: %v; %s gives it back\n", l.ID,
			a.wordShown(o.file), a.errShown(err), a.releaseLine(l.ID, o.pool, o.file))
		return exitFailed
	}
	if o.json {
		return printed(a, "lease", writeCompact(a.Stdout, doc))
	}
	cut := ""
	if got := l.ExpiresAt.Sub(l.LeasedAt); got < o.ttl {
		cut = fmt.Sprintf(", cut from -ttl %s to the pool's max lease, %s", o.ttl, got)
	}
	fmt.Fprintf(a.Stdout, "laug · lease: %s for %s until %s%s; kept in %s; %s gives it back\n", l.ID,
		plain(l.Requester), utc(l.ExpiresAt), cut, a.wordShown(o.file), a.releaseLine(l.ID, o.pool, o.file))
	return printed(a, "lease", a.writeIndented(a.Stdout, doc))
}

// waitSay is how long a lease waits for the pool's answer before it says that it waits. Tests shorten it.
var waitSay = time.Second

// leaseSaying asks the pool on addr for a cell, for verb: lease, raun or candidate. A warm cell comes back at once. A
// cold one takes the pool's -create-timeout at most, 5 minutes by default, so when no answer has come in waitSay, one
// stderr line says the verb waits and how to stop the wait. The line is written before leaseSaying returns.
func (a *App) leaseSaying(ctx context.Context, verb, addr string, c *poolapi.Client,
	req poolapi.LeaseRequest) (poolapi.Lease, []byte, error) {
	said := make(chan struct{})
	t := time.AfterFunc(waitSay, func() {
		defer close(said)
		fmt.Fprintf(a.Stderr, "laug · %s: waiting for a cell from the pool on %s; Ctrl-C stops the wait\n", verb,
			addr)
	})
	l, doc, err := c.Lease(ctx, req)
	if !t.Stop() {
		<-said
	}
	return l, doc, err
}

// leaseRequest is the cell lease, raun and candidate ask the pool for, from their -ttl, -requester and -server-tag, or
// an error that names the flag it refuses: a ttl of 0 or less or not whole seconds, an empty requester, a tag that is
// not a release.
func leaseRequest(ttl time.Duration, requester, tag string) (poolapi.LeaseRequest, error) {
	switch {
	case ttl <= 0:
		return poolapi.LeaseRequest{}, fmt.Errorf("-ttl %s: 0 or less", ttl)
	case ttl%time.Second != 0:
		return poolapi.LeaseRequest{}, fmt.Errorf("-ttl %s: not a whole number of seconds", ttl)
	case requester == "":
		return poolapi.LeaseRequest{}, errors.New("-requester is empty: the pool would count the lease as anonymous's")
	case tag != "" && !poolapi.TagRE.MatchString(tag):
		return poolapi.LeaseRequest{}, fmt.Errorf("-server-tag %s: not a release tag, x.y.z", tag)
	}
	return poolapi.LeaseRequest{TTLSeconds: int(ttl / time.Second), Requester: requester, ServerTag: tag}, nil
}

// lastLeasePath is where file is: as given when absolute or outside a checkout, else under the checkout's root, where
// the scripts read it.
func lastLeasePath(a *App, file string) string {
	if filepath.IsAbs(file) || a.Root == "" {
		return file
	}
	return filepath.Join(a.Root, file)
}

// utc is a time as laug prints one: in UTC, to the second, with the zone named.
func utc(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05") + " UTC"
}

// releaseLine is the command that gives lease id back, with -pool and -file when they are not the defaults, so the
// release reaches the same pool and removes the same file.
func (a *App) releaseLine(id, pool, file string) string {
	return a.verbLine("release", id, pool, file)
}

// verbLine is the command that runs verb on lease id, with -pool and -file when they are not the defaults, -file as
// wordShown writes it.
func (a *App) verbLine(verb, id, pool, file string) string {
	line := a.self() + " " + verb + " " + shellWord(id)
	if pool != poolDefault {
		line += " -pool " + shellWord(pool)
	}
	if file != poolapi.LastLeaseFile {
		line += " -file " + a.wordShown(file)
	}
	return line
}

// subject is what reset and release act on: a lease, by its id or by -last, and the client of the pool that holds it.
type subject struct {
	id     string
	pool   string // -pool as given
	file   string // -file as given
	path   string // -file, under the checkout's root when relative
	json   bool
	client *poolapi.Client
}

// parseSubject parses reset's or release's arguments: a lease id, or -last for the one the last-lease file names.
// Bare, it prints on stderr that it names no lease, the last lease's id and the line that acts on it, and its synopsis,
// and returns 2. With ok false the verb returns code.
func parseSubject(a *App, verb string, args []string) (s subject, code int, ok bool) {
	f := a.newFlags(verb, "<id> | -last [-pool <address>] [-file <path>] [-json]")
	f.StringVar(&s.pool, "pool", poolDefault, "the pool's `address`: a loopback host:port, or an http:// URL")
	fileHelp := "the last-lease `file` -last reads; a relative path starts at the checkout's root"
	if verb == "release" {
		fileHelp = "the last-lease `file` -last reads, removed when its lease is released; a relative path starts at " +
			"the checkout's root"
	}
	f.StringVar(&s.file, "file", poolapi.LastLeaseFile, fileHelp)
	last := f.Bool("last", false, "act on the lease the last-lease file names, the one "+a.self()+" lease kept last")
	f.BoolVar(&s.json, "json", false, "print the pool's answer as one JSON object")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return s, code, false
	}
	usageError := func(format string, v ...any) (subject, int, bool) {
		fmt.Fprintf(a.Stderr, "laug · %s: %s\n", verb, a.textShown(fmt.Sprintf(format, v...)))
		f.mistake(a.Stderr)
		return s, exitUsage, false
	}
	var err error
	if s.client, err = poolapi.New(s.pool); err != nil {
		fmt.Fprintf(a.Stderr, "laug · %s: -pool %s: %v\n", verb, s.pool, a.errShown(err))
		return s, exitUsage, false
	}
	s.path = lastLeasePath(a, s.file)
	switch {
	case len(pos) > 1:
		return usageError("takes one lease id, got %q", pos)
	case len(pos) == 1 && *last:
		return usageError("takes a lease id or -last, not both")
	case len(pos) == 1 && pos[0] == "":
		return usageError("the lease id is empty")
	case len(pos) == 1:
		s.id = pos[0]
		return s, exitDone, true
	case *last:
		l, err := poolapi.ReadLast(s.path)
		if err != nil {
			fmt.Fprintf(a.Stderr, "laug · %s: -last: %s\n", verb, a.noLease(s.file, err))
			return s, exitUsage, false
		}
		s.id = l.ID
		return s, exitDone, true
	}
	fmt.Fprintf(a.Stderr, "laug · %s: names no lease: give it a lease id, or -last\n", verb)
	if l, err := poolapi.ReadLast(s.path); err != nil {
		fmt.Fprintln(a.Stderr, a.noLease(s.file, err))
	} else {
		fmt.Fprintf(a.Stderr, "the last lease, in %s, is %s, for %s until %s; to %s it:\n  %s\n", s.file, l.ID,
			plain(l.Requester), utc(l.ExpiresAt), verb, a.verbLine(verb, l.ID, s.pool, s.file))
	}
	f.mistake(a.Stderr)
	return s, exitUsage, false
}

// noLease says why the last-lease file names no lease.
func (a *App) noLease(file string, err error) string {
	if errors.Is(err, poolapi.ErrNoLease) {
		return file + " holds no lease; " + a.self() + " lease keeps one there"
	}
	return file + " holds no lease: " + a.errShown(err)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// shellWord quotes s for a shell when it holds anything but letters, digits and _./:@%+=,-.
func shellWord(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeCompact writes doc, a JSON document as the pool sent it, the way -json prints one: on one line, nothing else.
func writeCompact(w io.Writer, doc []byte) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, doc); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// writeIndented writes doc indented by two spaces a level, as jq . prints it, with each path in it shown as laug shows
// one, from the checkout's root or as ~/: a compose cell's dynconfig is its file's absolute path. The last-lease file
// and -json keep the pool's answer as it came, for the scripts that read it.
func (a *App) writeIndented(w io.Writer, doc []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(doc), "", "  "); err != nil {
		return err
	}
	_, err := io.WriteString(w, a.textShown(buf.String())+"\n")
	return err
}

// printed turns the error of writing a verb's output into its exit: 0, or 1 with one line.
func printed(a *App, verb string, err error) int {
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · %s: writing the pool's answer: %v\n", verb, a.errShown(err))
		return exitFailed
	}
	return exitDone
}

// poolFailed prints why a call to the pool on addr failed, one line, and returns 1: the context ended, as a signal
// ends it, before the answer; the pool refused, with its reason; what answers is not a pool; or nothing answers, and
// for a lease the line that starts a pool. id is the lease the call named, "" for a lease; note, when not empty, ends
// the line.
func poolFailed(ctx context.Context, a *App, verb, addr, id, note string, err error) int {
	return poolFailedAs(ctx, a, verb, verb, addr, id, note, err)
}

// poolFailedAs is poolFailed for a call, op (lease, reset or release), that verb made on its way: raun's lease and its
// release, candidate's lease and the release it makes when its script cannot start.
func poolFailedAs(ctx context.Context, a *App, verb, op, addr, id, note string, err error) int {
	var pe *poolapi.Error
	named := ""
	if id != "" {
		named = " " + id
	}
	if note != "" {
		note = "; " + note
	}
	switch {
	case ctx.Err() != nil:
		// The pool runs a release or a reset on its own once asked, and gives back a cell whose lease it could not
		// hand over (internal/pool, handleLease).
		after := map[string]string{"lease": "the pool gives back a cell it could not hand over",
			"reset": "the pool finishes the reset on its own", "release": "the pool finishes the release on its own"}[op]
		fmt.Fprintf(a.Stderr, "laug · %s: stopped before the pool on %s answered; %s%s\n", verb, addr, after, note)
	case errors.As(err, &pe) && pe.Text != "":
		fmt.Fprintf(a.Stderr, "laug · %s: the pool on %s refused%s: %s (%s)%s\n", verb, addr, named, pe.Text, pe.Status,
			note)
	case errors.As(err, &pe):
		fmt.Fprintf(a.Stderr, "laug · %s: the pool on %s refused%s: %s%s\n", verb, addr, named, pe.Status, note)
	case errors.Is(err, poolapi.ErrNotPool):
		fmt.Fprintf(a.Stderr, "laug · %s: what answers on %s is %v%s\n", verb, addr, a.errShown(err), note)
	case op == "lease":
		fmt.Fprintf(a.Stderr, "laug · %s: no pool answers on %s%s; %s%s\n", verb, addr, noAnswer(err), a.poolServe(addr),
			note)
	default:
		fmt.Fprintf(a.Stderr, "laug · %s: no pool answers on %s%s%s\n", verb, addr, noAnswer(err), note)
	}
	return exitFailed
}

// noAnswer is why nothing answered, as the end of "no pool answers on <addr>": in two or three words for a port that
// refuses and for a wait that ran out, else Go's error after a colon.
func noAnswer(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return " (connection refused)"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		return " (no answer in time)"
	}
	return ": " + err.Error()
}

// poolServe is the line that starts a pool on addr, and what it starts: -listen only for an address that is not the
// default.
func (a *App) poolServe(addr string) string {
	line := a.self() + " pool serve"
	if c, err := poolapi.New(addr); err == nil {
		if _, host, _ := strings.Cut(c.Base(), "://"); host != pool.DefaultListen {
			line += " -listen " + host
		}
	}
	return line + " starts one, on fake cells by default"
}
