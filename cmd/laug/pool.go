package main

// laug pool: serve runs the warm pool manager, internal/pool, in the foreground; status reads its GET /status through
// the client, internal/poolapi; down stops it and removes its cells, compose's by cells/pool-down.sh and microVMs by
// cells/backend-firecracker.sh sweep; bench measures a running pool, cells/pool-bench.sh.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ymur-studio/laug/internal/gateopen"
	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// pidFile is where pool serve keeps its pid while it runs, under the checkout, so pool down can stop it.
const pidFile = "cells/run/pool.pid"

// The scripts pool serve -backend compose and firecracker, pool down and pool bench run, and the checkout's bin/laug,
// whose health check both backends' health verbs run.
const (
	composeBackend = "cells/backend-compose.sh"
	fcBackend      = "cells/backend-firecracker.sh"
	poolDownScript = "cells/pool-down.sh"
	benchScript    = "cells/pool-bench.sh"
	laugBin        = "bin/laug"
)

// fcMinDefault and fcMaxDefault are -min and -max under -backend firecracker, FC_POOL_MIN and FC_POOL_MAX in
// internal/script's knob table: a clone takes 4 vCPU, fixed in the snapshot, and the host every microVM number ran on
// had 8.
const (
	fcMinDefault = 1
	fcMaxDefault = 2
)

// poolBlocks is the most cells pool serve's -max takes on a backend that publishes host ports, a port block each.
// Block N publishes each of cellPorts at 10N over block 0's, as cells/backend-compose.sh lays a pool cell out. Two
// listeners run beside the cells: the orchestrator's dev server from 7400 (orchAddressDefault), and laug pool serve
// on 7700 (pool.DefaultListen). A cell port that starts below one of them must stay below it. The frontend, 7233 plus
// 10N, reaches 7400 first: blocks 0 to 16 keep it below, the last at 7393, so 17. A microVM cell publishes no host
// port, so -backend firecracker is not held to the cap. TestPoolBlocks holds every port of the 17 blocks clear of the
// dev server's four (7400, HTTP 7401, metrics 7402, UI 8400) and of 7700.
var poolBlocks = portBlocks(portOf(orchAddressDefault), portOf(pool.DefaultListen))

// portBlocks is the most port blocks, from block 0, whose ports stay below each listener that block 0's port is below:
// at most cellBlockMax+1, the blocks whose ports never meet each other.
func portBlocks(listeners ...int) int {
	for n := 0; n <= cellBlockMax; n++ {
		for _, p := range cellPorts {
			for _, l := range listeners {
				if p.base < l && (cellCompose{block: n}).port(p.name) >= l {
					return n
				}
			}
		}
	}
	return cellBlockMax + 1
}

// portOf is the port of addr, one of the address constants: 7400 of 127.0.0.1:7400; 0 when it names none.
func portOf(addr string) int {
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	return n
}

// geteuid is os.Geteuid: only root runs the Firecracker pool. Tests replace it.
var geteuid = os.Geteuid

// poolDefault is the pool pool status, lease, reset and release talk to by default. Tests point it at a fake pool.
var poolDefault = pool.DefaultListen

// poolServeOpts is pool serve's flags. The defaults are those of POOL_MIN, POOL_MAX, POOL_QUOTA, POOL_MAX_LEASE and
// POOL_LISTEN, and the cell's SERVER_TAG, VISIBILITY and BIND_ADDR, as internal/script's knob table holds them;
// TestPoolDefaultsMatchKnobs reads them there.
type poolServeOpts struct {
	listen                string
	backend               string
	tag, visibility, bind string
	pidFile               string
	log                   string
	cfg                   pool.Config
}

// pidNone is -pid-file's value for a pool that keeps no pid file: a gate's own, which gate/run.sh and gate/step.sh
// start beside the checkout's pool, on a port of their own, and stop by their own records.
const pidNone = "none"

// newPoolServeFlags is pool serve's flags.
func (a *App) newPoolServeFlags(o *poolServeOpts) *flags {
	f := a.newFlags("pool serve", "[flags]")
	f.StringVar(&o.listen, "listen", pool.DefaultListen,
		"the `address` to listen on, loopback and a port: the API has no auth, so any other host is refused")
	f.StringVar(&o.backend, "backend", "fake", "fake, which answers at once and starts nothing; compose, "+
		composeBackend+", one compose cell a port block; firecracker, "+fcBackend+", one microVM a cell, as "+
		"root; or a backend `script`: <script> create|destroy|health <id>, with LAUG_BLOCK in its environment")
	f.StringVar(&o.tag, "server-tag", serverTagDefault, "the temporalio/server `tag` a script backend's cells "+
		"run, x.y.z; a lease may name another")
	f.StringVar(&o.visibility, "visibility", visibilityDefault, "a script backend's cells' visibility `store`: "+
		"postgres, or es for Elasticsearch")
	f.StringVar(&o.bind, "bind", bindDefault, "the `interfaces` a script backend's cell ports listen on: "+
		"loopback, 127.0.0.1, or all, 0.0.0.0; nothing in a cell has auth")
	f.StringVar(&o.pidFile, "pid-file", pidFile, "where serve keeps its pid while it runs, in a checkout: "+
		pidFile+", where "+a.self()+" pool down finds it, or "+pidNone+", for a pool beside the checkout's own, as a "+
		"gate's is; no other `path`")
	f.StringVar(&o.log, "log", "", "a `file` to append the pool's log and the cells' output to, not stderr; a "+
		"relative path starts at the checkout's root")
	f.IntVar(&o.cfg.MinWarm, "min", 3, "warm cells to keep ready; "+strconv.Itoa(fcMinDefault)+
		" under -backend firecracker")
	f.IntVar(&o.cfg.MaxCells, "max", 6, "cells on this host, warm plus leased plus in-flight, and stuck; also the "+
		"number of port blocks, "+strconv.Itoa(poolBlocks)+" at most, the blocks whose frontends stay below "+
		strconv.Itoa(portOf(orchAddressDefault))+" and "+strconv.Itoa(portOf(pool.DefaultListen))+"; under -backend "+
		"firecracker, whose cells publish no host port, not held to "+strconv.Itoa(poolBlocks)+", and "+
		strconv.Itoa(fcMaxDefault)+" by default")
	f.IntVar(&o.cfg.Quota, "quota", 2,
		"concurrent leases per requester; a requester names itself, so this guards against mistakes only")
	f.DurationVar(&o.cfg.MaxLease, "max-lease", 2*time.Hour, "longest lease allowed; a longer request is clamped")
	f.DurationVar(&o.cfg.CreateTimeout, "create-timeout", 5*time.Minute,
		"time allowed for the backend to create a healthy cell; also bounds a destroy")
	f.DurationVar(&o.cfg.HealthPoll, "health-poll", 2*time.Second,
		"how often to ask the backend for health after a create")
	f.DurationVar(&o.cfg.ReapInterval, "reap-interval", 10*time.Second,
		"how often expired leases are released and a failed create or a stuck cell's destroy is tried again")
	return f
}

// runPoolServe runs the pool until ctx is done, then stops within 2 s and exits 0. Under -backend firecracker it runs
// the backend's prep first, once it holds the address. Exit 2 on a flag, an argument or a value it refuses, a backend
// it cannot run, a pid file that names a process that runs, or a prep that refused; 1 when the address cannot be bound,
// prep fails, the pid file cannot be written or the listener fails.
func runPoolServe(ctx context.Context, a *App, args []string) int {
	var o poolServeOpts
	f := a.newPoolServeFlags(&o)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	fail := func(code int, format string, v ...any) int {
		fmt.Fprintf(a.Stderr, "laug · pool serve: %s\n", a.textShown(fmt.Sprintf(format, v...)))
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(a.Stderr, "laug · pool serve: takes no arguments, got %q\n", pos)
		f.mistake(a.Stderr)
		return exitUsage
	}
	fc := o.backend == "firecracker"
	if fc && !given(f, "min") {
		o.cfg.MinWarm = fcMinDefault
	}
	if fc && !given(f, "max") {
		o.cfg.MaxCells = fcMaxDefault
	}
	if err := checkServe(o, a.self()); err != nil {
		return fail(exitUsage, "%v", err)
	}
	logw, logNote := a.Stderr, ""
	if o.log != "" {
		path := o.log
		if !filepath.IsAbs(path) && a.Root != "" {
			path = filepath.Join(a.Root, path)
		}
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		var lf *os.File
		if err == nil {
			lf, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		}
		if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
			err = pe.Err // the path as typed, not the absolute one, which can hold a home path
		}
		if err != nil {
			return fail(exitUsage, "-log %s: %v", o.log, err)
		}
		// Not closed here. Serve does not wait for a create, reset or destroy under way. After Serve returns, a
		// replenish can still start its destroy script with this file as stderr, then log a line. The file
		// closes when the process exits, or once nothing holds it, by os.File's finalizer.
		logw, logNote = lf, "; its log and the cells' output go to "+a.wordShown(o.log)
	}
	backend, name, env, err := a.poolBackend(o, logw)
	if err != nil {
		return fail(exitUsage, "%v", err)
	}
	pidPath, pidNote := "", "not in a laug checkout, so no pid file"
	switch {
	case o.pidFile == pidNone:
		pidNote = "-pid-file " + pidNone + ", so no pid file"
	case a.Root != "":
		pidPath = filepath.Join(a.Root, pidFile)
		if pid, live := livePid(pidPath); live {
			return fail(exitUsage, "%s names pid %d, which runs: one pool serve a checkout; %s pool down "+
				"-remove-cells stops it, or remove the file if pid %d is no pool", pidFile, pid, a.self(), pid)
		}
	}
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return fail(exitFailed, "%v", err)
	}
	if fc {
		if code := a.fcPrep(env, logw); code != exitDone {
			ln.Close()
			return code
		}
	}
	if pidPath != "" {
		pid := os.Getpid()
		if err := writePid(pidPath, a.Root, pid); err != nil {
			ln.Close()
			return fail(exitFailed, "writing %s: %v", pidFile, err)
		}
		defer removePid(pidPath, pid)
		pidNote = fmt.Sprintf("pid %d in %s", pid, pidFile)
	}
	c := o.cfg
	fmt.Fprintf(a.Stdout, "laug · pool serve: listening on %s (min %d, max %d, quota %d, max lease %s, backend %s); "+
		"%s%s; Ctrl-C or SIGTERM stops it\n", ln.Addr(), c.MinWarm, c.MaxCells, c.Quota, c.MaxLease, name, pidNote,
		logNote)
	// Log lines go to stderr, or -log's file, in UTC, like every result file.
	cells, err := pool.Serve(ctx, ln, backend, c, log.New(logw, "", log.LstdFlags|log.LUTC))
	if err != nil {
		return fail(exitFailed, "%v", err)
	}
	removes := a.self() + " pool down -remove-cells removes the compose backend's"
	if fc {
		removes = "sudo bin/laug pool down -backend firecracker -remove-cells removes them"
	}
	fmt.Fprintf(a.Stdout, "laug · pool serve: stopped with %d cells in its table, none destroyed (%s)\n", cells, removes)
	return exitDone
}

// fcPrep runs cells/backend-firecracker.sh prep as a child with env, the backend's environment: no pool runs, root's
// tools and the snapshot are there, then the disks volume, a sweep of what a last pool left, the deny rules and the
// guest key. Its lines go to logw, beside the pool's log. Exit 2 when prep refused (its exit 2), 1 when it failed any
// other way or could not start.
func (a *App) fcPrep(env []string, logw io.Writer) int {
	if a.Runner == nil {
		fmt.Fprintf(a.Stderr, "laug · pool serve: no runner, so %s prep did not run\n", fcBackend)
		return exitFailed
	}
	c := script.Command{Root: a.Root, Script: fcBackend, Args: []script.Value{{Text: "prep"}}}
	code, err := a.Runner.Run(c, env, logw, logw)
	switch {
	case err != nil:
		fmt.Fprintf(a.Stderr, "laug · pool serve: could not run %s prep: %v\n", fcBackend, a.errShown(err))
		return exitFailed
	case code == exitDone:
		return exitDone
	}
	fmt.Fprintf(a.Stderr, "laug · pool serve: %s prep exited %d, so no pool serves\n", fcBackend, code)
	if code == exitUsage {
		return exitUsage
	}
	return exitFailed
}

// poolBackend is the backend -backend names, its name for the first line, and a script backend's environment. fake, or
// empty, is the pool's own fake. compose is cells/backend-compose.sh in the checkout, and firecracker
// cells/backend-firecracker.sh, which root alone runs: each runs the checkout's bin/laug health check, so needs it. Any
// other value is a script by its path. Under pool serve a script runs with the environment internal/script builds, as a
// verb's script does: the base names, the tools' and the seams as the caller has them, and SERVER_TAG, VISIBILITY and
// BIND_ADDR from the flags, after one stderr line for each stray copy; the pool sets LAUG_BLOCK, and a lease's own
// SERVER_TAG, per call. A script's stderr, docker compose's lines among them, goes to logw.
func (a *App) poolBackend(o poolServeOpts, logw io.Writer) (pool.Backend, string, []string, error) {
	path, rel := o.backend, o.backend
	switch o.backend {
	case "", "fake":
		return pool.Fake(), "fake", nil, nil
	case "compose", "firecracker":
		fc := o.backend == "firecracker"
		rel, build := composeBackend, "make build builds it"
		if fc {
			rel, build = fcBackend, "make build, as yourself, builds it"
		}
		var err error
		switch {
		case a.Root == "":
			err = fmt.Errorf("-backend %s runs %s, so it needs a laug checkout: no directory above this one holds "+
				"go.mod and gate/run.sh", o.backend, rel)
		case fc && geteuid() != 0:
			err = errors.New("-backend firecracker runs as root, for /dev/kvm and each cell's network and mount " +
				"namespaces: sudo bin/laug pool serve -backend firecracker")
		case !a.executable(laugBin):
			err = fmt.Errorf("-backend %s: %s, whose health check a new cell's health verb runs, is not built: %s",
				o.backend, laugBin, build)
		}
		if err != nil {
			return nil, "", nil, err
		}
		path = filepath.Join(a.Root, rel)
	}
	c := script.Command{Script: rel, Set: []script.Var{script.Bare("SERVER_TAG", o.tag),
		script.Bare("VISIBILITY", o.visibility), script.Quoted("BIND_ADDR", bindAddrs[o.bind])}}
	env, strays := a.scriptEnv(a.Environ, c)
	for _, s := range strays {
		fmt.Fprintf(a.Stderr, "laug · pool serve: %s\n", s)
	}
	name := fmt.Sprintf("%s, server %s, visibility %s, on %s", o.backend, o.tag, o.visibility, bindAddrs[o.bind])
	if o.backend == "firecracker" {
		name = fmt.Sprintf("firecracker, server %s, visibility %s, on host-only addresses", o.tag, o.visibility)
	}
	return pool.Script(path, env, logw), name, env, nil
}

// checkServe refuses what the pool cannot run with: a listener off loopback, a negative or empty pool, a -max past the
// port blocks (poolBlocks) on a backend other than firecracker, a quota under 1, more warm cells than cells, or a
// duration of 0 or less; a server tag, visibility or bind that laug cell up refuses, a -pid-file other than
// cells/run/pool.pid or none, or, under -backend firecracker, a value a microVM cell or its pool cannot have
// (checkFirecracker). self starts the line to type it names.
func checkServe(o poolServeOpts, self string) error {
	if err := loopbackAddr(o.listen); err != nil {
		return fmt.Errorf("-listen %s: %w", o.listen, err)
	}
	cc := cellComposeDefault
	cc.tag, cc.visibility, cc.bind = o.tag, o.visibility, o.bind
	if err := cc.check(); err != nil {
		return err
	}
	if o.pidFile != pidFile && o.pidFile != pidNone {
		return fmt.Errorf("-pid-file %s: %s, where %s pool down finds the pool, or %s", shellWord(o.pidFile),
			pidFile, self, pidNone)
	}
	if o.backend == "firecracker" {
		if err := checkFirecracker(o); err != nil {
			return err
		}
	}
	c := o.cfg
	switch {
	case c.MinWarm < 0:
		return fmt.Errorf("-min %d: under 0", c.MinWarm)
	case c.MaxCells < 1:
		return fmt.Errorf("-max %d: under 1", c.MaxCells)
	case c.MaxCells > poolBlocks && o.backend != "firecracker":
		return fmt.Errorf("-max %d: over %d: port block N's frontend is %d plus 10N, and blocks 0 to %d keep it below "+
			"%d and %d, the orchestrator's dev server and laug pool serve", c.MaxCells, poolBlocks,
			cellComposeDefault.port("FRONTEND_PORT"), poolBlocks-1, portOf(orchAddressDefault),
			portOf(pool.DefaultListen))
	case c.MinWarm > c.MaxCells:
		return fmt.Errorf("-min %d is over -max %d", c.MinWarm, c.MaxCells)
	case c.Quota < 1:
		return fmt.Errorf("-quota %d: under 1", c.Quota)
	}
	for _, d := range []struct {
		name string
		d    time.Duration
	}{{"max-lease", c.MaxLease}, {"create-timeout", c.CreateTimeout}, {"health-poll", c.HealthPoll},
		{"reap-interval", c.ReapInterval}} {
		if d.d <= 0 {
			return fmt.Errorf("-%s %s: 0 or less", d.name, d.d)
		}
	}
	return nil
}

// checkFirecracker refuses a server tag, visibility or bind other than a microVM cell's: the image bakes server 1.31.0
// and Postgres visibility (cells/rootfs/build-image.sh, snapshot.sh), and a cell answers on a host-only address,
// 10.254.<block>.2, whatever BIND_ADDR says (cells/backend-firecracker.sh). It refuses -pid-file none too: the backend
// finds its pool by the pid file (cells/rootfs/fc-lib.sh).
func checkFirecracker(o poolServeOpts) error {
	switch {
	case o.tag != serverTagDefault:
		return fmt.Errorf("-backend firecracker: microVM cells run server %s, the tag the image bakes; -server-tag %s "+
			"is for compose cells", serverTagDefault, o.tag)
	case o.visibility != visibilityDefault:
		return fmt.Errorf("-backend firecracker: microVM cells run %s visibility, as the image bakes it; -visibility "+
			"%s is for compose cells", visibilityDefault, o.visibility)
	case o.bind != bindDefault:
		return fmt.Errorf("-backend firecracker: microVM cells answer on a host-only address, 10.254.<block>.2; "+
			"-bind %s is for compose cells", o.bind)
	case o.pidFile == pidNone:
		return fmt.Errorf("-backend firecracker -pid-file %s: the Firecracker pool keeps its pid in %s, where pool "+
			"down -backend firecracker and the backend's prep and sweep find it", pidNone, pidFile)
	}
	return nil
}

// loopbackAddr refuses an address whose host is not a loopback IP, 127.0.0.0/8 or ::1 (poolapi.LoopbackHost, the rule
// a client of the pool keeps too), or whose port is not a number from 0 to 65535. An empty host would listen on every
// interface, and a name may resolve anywhere.
func loopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("not host:port")
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || strconv.FormatUint(n, 10) != port {
		return fmt.Errorf("port %q is not a number from 0 to 65535", port)
	}
	return poolapi.LoopbackHost(host)
}

// livePid reads a pid file and reports whether its pid runs. A file that is missing or does not hold a pid is not
// live. kill -0 answers for a process of another user too (EPERM).
func livePid(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	err = syscall.Kill(pid, 0)
	return pid, err == nil || errors.Is(err, syscall.EPERM)
}

// writePid writes pid to path, its directory made first (mkdirOwned, owner the checkout), through a temp file and a
// rename.
func writePid(path, owner string, pid int) error {
	if err := mkdirOwned(filepath.Dir(path), owner); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mkdirOwned makes dir and its missing parents, 0755. As root it gives each one it made owner's owner and group, so a
// pool serve under sudo (-backend firecracker) leaves no cells/run that the checkout's user cannot write in.
func mkdirOwned(dir, owner string) error {
	var made []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || d == filepath.Dir(d) {
			break
		}
		made = append(made, d)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if geteuid() != 0 || len(made) == 0 {
		return nil
	}
	fi, err := os.Stat(owner)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: no owner to give %s", owner, dir)
	}
	for _, d := range made {
		if err := os.Chown(d, int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	return nil
}

// removePid removes path when it still holds pid, so a stop never removes another pool's file.
func removePid(path string, pid int) {
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(pid) {
		_ = os.Remove(path)
	}
}

// runPoolStatus prints the pool's settings and cells: a line and a table, or with -json the pool's /status document
// as it came, on one line. Exit 1 when nothing answers on the address, or what answers is not a pool; 2 on a flag, an
// argument or an address off loopback.
func runPoolStatus(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("pool status", "[-pool <address>] [-json]")
	addr := f.String("pool", poolDefault, "the pool's `address`: a loopback host:port, or an http:// URL")
	jsonOut := f.Bool("json", false, "print the pool's /status document as one JSON object")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(a.Stderr, "laug · pool status: takes no arguments, got %q\n", pos)
		f.mistake(a.Stderr)
		return exitUsage
	}
	c, err := poolapi.New(*addr)
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · pool status: -pool %s: %v\n", *addr, a.errShown(err))
		return exitUsage
	}
	fail := func(format string, v ...any) int {
		fmt.Fprintf(a.Stderr, "laug · pool status: %s\n", a.textShown(fmt.Sprintf(format, v...)))
		return exitFailed
	}
	st, body, err := c.Status(ctx)
	switch {
	case errors.Is(err, poolapi.ErrNotPool):
		return fail("what answers on %s is %v", *addr, err)
	case errors.As(err, new(*poolapi.Error)):
		return fail("no pool answers on %s: %v", *addr, err)
	case err != nil:
		return fail("no pool answers on %s%s; %s", *addr, noAnswer(err), a.poolServe(*addr))
	}
	if *jsonOut {
		if err := writeCompact(a.Stdout, body); err != nil {
			return fail("writing JSON: %v", err)
		}
		return exitDone
	}
	writePoolStatus(a.Stdout, *addr, st)
	return exitDone
}

// writePoolStatus writes the status line and, when the pool holds cells, one row a cell. A lease's expiry is in UTC.
// Stuck cells, whose destroy failed (a release's, or the one after a failed create or reset; internal/pool's Release
// and keepStuck), are counted in the line only when there are any.
func writePoolStatus(w io.Writer, addr string, st poolapi.Status) {
	count := map[string]int{}
	for _, c := range st.Cells {
		count[c.State]++
	}
	stuck := ""
	if n := count["stuck"]; n > 0 {
		stuck = fmt.Sprintf(", %d stuck", n)
	}
	fmt.Fprintf(w, "laug · pool status: %s, %d cells: %d warm, %d leased, %d busy%s; "+
		"min %d, max %d, quota %d, max lease %s\n", addr, len(st.Cells), count["warm"], count["leased"], count["busy"],
		stuck, st.MinWarm, st.MaxCells, st.Quota, st.MaxLease)
	if len(st.Cells) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\tblock\tstate\ttag\trequester\texpires (UTC)\taddress")
	for _, c := range st.Cells {
		expires := "-"
		if c.State == "leased" && !c.ExpiresAt.IsZero() {
			expires = c.ExpiresAt.UTC().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", plain(c.ID), c.Block, plain(c.State), plain(c.ServerTag),
			plain(c.Requester), expires, plain(c.Address))
	}
	tw.Flush()
}

// plain is a field as one word on one line: "-" when empty, quoted when it holds a space, a tab or a control
// character, as a requester may.
func plain(s string) string {
	switch {
	case s == "":
		return "-"
	case strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return strconv.Quote(s)
	}
	return s
}

// plural is n and noun, with an s past 1 or at 0.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "s") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// poolGone is what laug pool down -remove-cells stops and removes, as cells/pool-down.sh finds it: this checkout's
// pool serve by its pid file, unless it runs the Firecracker backend, every gate's own pool, each laug-<8 hex> compose
// project's containers, volumes and networks, the per-cell dirs in cells/run and the last lease's file.
type poolGone struct {
	serve       int      // pool serve's pid, 0 when the pid file names no pool serve that runs, or a Firecracker one
	gatePools   []int    // every gate's own pool: laug pool serve -pid-file none, by its command line
	projects    []string // laug-<8 hex> compose projects, from their containers
	containers  []string
	volumes     []string
	networks    []string
	dirs        []string // cells/run/laug-*
	lease       bool     // .last-lease.json is there
	firecracker bool     // the Firecracker pool's pool serve runs, or /run/laug/b<N>/ is there: neither goes
}

// poolListWait bounds the three docker calls that list what pool down removes.
const poolListWait = 20 * time.Second

// procDir is where pool down reads command lines, and fcRunDir where a Firecracker pool keeps its cells: tests point
// both at temp dirs.
var (
	procDir  = "/proc"
	fcRunDir = "/run/laug"
)

// poolProject is a pool cell's compose project name at the start of a container's name, as pool-down.sh's grep -oE
// takes it; serveCmdline is pool serve's command line, and gateCmdline a gate's own pool's, as the script matches
// them (its serve_re and gate_re; TestPoolDownMatchesScript).
var (
	poolProject  = regexp.MustCompile(`^laug-[0-9a-f]{8}`)
	serveCmdline = regexp.MustCompile(`^([^ ]*/)?laug pool serve( |$)`)
	gateCmdline  = regexp.MustCompile(`^([^ ]*/)?laug pool serve( .*)? --?pid-file[ =]none( |$)`)
)

// gateOpenNow says whether a gate runs on this host, and why, as bin/obs follow -gate-running answers it, with the
// same arguments (internal/gateopen): a host lock held, or a gate's laugd on 127.0.0.1:7700 whose timeline has no done
// line. why names the lock or the gate's id, never a path in the checkout. Tests replace it.
var gateOpenNow = func(context.Context) (why string, open bool) {
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	return gateopen.GateOpen("/", pool.DefaultListen, gateopen.LockPaths(xdg, os.Getuid()), xdg)
}

func runPoolDown(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("pool down", "-remove-cells [-while-gate-runs] [-backend compose|firecracker] [-dry-run]")
	remove := f.Bool("remove-cells", false, "stop the pool and remove every pool cell, its data with it, which no "+
		"command brings back: under compose every gate's own pool too, and each cell's volumes, Postgres's and "+
		"Prometheus's; "+
		"under firecracker each microVM's disk; unset, compose lists what would go and removes nothing, and "+
		"firecracker stops its pool alone")
	whileGate := f.Bool("while-gate-runs", false, "with -remove-cells, stop and remove them even while a gate "+
		"runs, which it refuses otherwise: that gate's pool stops and its cells go mid-wave")
	backend := f.String("backend", "compose", "the `backend` whose pool and cells go: compose, or firecracker, "+
		"whose pool and cells are root's: sudo bin/laug pool down -backend firecracker")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) > 0:
		return a.usageError(f, "takes no arguments, got %q", pos)
	case *whileGate && !*remove:
		return a.usageError(f, "-while-gate-runs goes with -remove-cells, which it lets run while a gate runs")
	case *whileGate && *backend != "compose":
		return a.usageError(f, "-while-gate-runs goes with -backend compose: a gate's pool and cells are compose's")
	}
	switch *backend {
	case "compose":
	case "firecracker":
		return a.poolDownFirecracker(*remove, *dry)
	default:
		return a.refused("pool down", fmt.Errorf("-backend %s: compose or firecracker", shellWord(*backend)))
	}
	c := script.Command{Script: poolDownScript}
	if *dry && *remove {
		return a.runScript("pool down", c, true, script.Exec, "")
	}
	if !a.inCheckout("pool down", c.Script) {
		return exitUsage
	}
	gone, code, ok := a.poolGone(ctx)
	if !ok {
		return code
	}
	// The script stops every gate's own pool and removes every pool cell, so while a gate runs it would end that
	// gate mid-wave (gate/run.sh's header, step 1 lock). It refuses then, as obs down does, unless -while-gate-runs.
	why, open := gateOpenNow(ctx)
	if !*remove {
		gone.report(a.Stderr, a.self())
		if open {
			fmt.Fprintf(a.Stderr, "A gate runs (%s): -remove-cells refuses until it ends, and -while-gate-runs "+
				"removes them all the same\n", why)
		}
		return exitUsage
	}
	first := "laug · pool down: " + gone.summary() + " go"
	if open {
		if !*whileGate {
			return a.refused("pool down", fmt.Errorf("a gate runs (%s): -remove-cells would stop its pool and "+
				"remove its cells mid-wave; wait for it to end, or add -while-gate-runs to remove them anyway", why))
		}
		first += ", while a gate runs (" + why + ")"
	}
	return a.runScript("pool down", c, false, script.Exec, first)
}

// summary counts what goes, as pool down's first line and bare report say it.
func (g poolGone) summary() string {
	serve := "no pool serve"
	if g.serve != 0 {
		serve = fmt.Sprintf("pool serve, pid %d", g.serve)
	}
	return fmt.Sprintf("%s, %s; %s: %s, %s and %s, their data with them; %s in cells/run", serve,
		plural(len(g.gatePools), "gate pool"), plural(len(g.projects), "pool cell"), plural(len(g.containers), "container"),
		plural(len(g.volumes), "volume"), plural(len(g.networks), "network"), plural(len(g.dirs), "per-cell dir"))
}

// report writes what pool down -remove-cells would stop and remove, with counts and names, and the line that does
// it, starting with self: bare pool down's answer.
func (g poolGone) report(w io.Writer, self string) {
	if g.serve == 0 && !g.lease &&
		len(g.gatePools)+len(g.projects)+len(g.containers)+len(g.volumes)+len(g.networks)+len(g.dirs) == 0 {
		fmt.Fprintln(w, "laug · pool down: no pool serve, gate pool or pool cell, and no per-cell dir or last lease: "+
			"nothing to stop or remove")
	} else {
		fmt.Fprintf(w, "laug · pool down: would stop and remove %s\n", g.summary())
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		pids := make([]string, 0, len(g.gatePools))
		for _, p := range g.gatePools {
			pids = append(pids, strconv.Itoa(p))
		}
		lease := "none"
		if g.lease {
			lease = poolapi.LastLeaseFile + ", removed"
		}
		for _, l := range []struct {
			name  string
			names []string
		}{{"gate pools", pids}, {"pool cells", g.projects}, {"containers", g.containers}, {"volumes", g.volumes},
			{"networks", g.networks}, {"dirs", g.dirs}, {"last lease", []string{lease}}} {
			names := "none"
			if len(l.names) > 0 {
				names = strings.Join(l.names, ", ")
			}
			fmt.Fprintf(tw, "  %s\t%s\n", l.name, names)
		}
		tw.Flush()
		fmt.Fprintln(w, self+" pool down -remove-cells stops and removes them")
	}
	if g.firecracker {
		fmt.Fprintln(w, "The Firecracker pool is up as well, its cells in /run/laug: sudo bin/laug pool down -backend "+
			"firecracker -remove-cells removes it, and this does not")
	}
}

// poolGone finds what pool down removes. With ok false it printed why: 2 with no docker on PATH, 1 when a listing
// fails.
func (a *App) poolGone(ctx context.Context) (poolGone, int, bool) {
	var g poolGone
	if pid, backend := a.poolServeOf(); backend == "firecracker" {
		g.firecracker = true
	} else {
		g.serve = pid
	}
	procs, _ := filepath.Glob(filepath.Join(procDir, "[0-9]*", "cmdline"))
	for _, p := range procs {
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(p)))
		if err == nil && pid != os.Getpid() && gateCmdline.MatchString(cmdline(pid)) {
			g.gatePools = append(g.gatePools, pid)
		}
	}
	slices.Sort(g.gatePools)
	g.dirs, _ = filepath.Glob(filepath.Join(a.Root, "cells", "run", "laug-*"))
	for i, d := range g.dirs {
		g.dirs[i] = filepath.Base(d)
	}
	_, err := os.Stat(filepath.Join(a.Root, poolapi.LastLeaseFile))
	g.lease = err == nil
	fc, _ := filepath.Glob(filepath.Join(fcRunDir, "b[0-9]*"))
	for _, d := range fc {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			g.firecracker = true
		}
	}
	docker := a.lookPath("docker")
	if docker == "" {
		fmt.Fprintln(a.Stderr, "laug · pool down: no docker on PATH, so nothing can say which pool cells go, and "+
			"cells/pool-down.sh cannot remove them")
		return g, exitUsage, false
	}
	ctx, cancel := context.WithTimeout(ctx, poolListWait)
	defer cancel()
	list := func(name string, args ...string) ([]string, bool) {
		cmd := exec.CommandContext(ctx, docker, args...)
		cmd.Dir, cmd.Env = a.Root, append([]string{}, a.Environ...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			why, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
			if why == "" {
				why = err.Error()
			}
			fmt.Fprintf(a.Stderr, "laug · pool down: %s could not say what goes, so nothing was stopped or removed: "+
				"%s\n", name, a.textShown(why))
			return nil, false
		}
		return strings.Fields(string(out)), true
	}
	containers, ok := list("docker ps", "ps", "-a", "--format", "{{.Names}}")
	if !ok {
		return g, exitFailed, false
	}
	volumes, ok := list("docker volume ls", "volume", "ls", "-q")
	if !ok {
		return g, exitFailed, false
	}
	networks, ok := list("docker network ls", "network", "ls", "--format", "{{.Name}}")
	if !ok {
		return g, exitFailed, false
	}
	for _, c := range containers {
		if p := poolProject.FindString(c); p != "" && !slices.Contains(g.projects, p) {
			g.projects = append(g.projects, p)
		}
	}
	slices.Sort(g.projects)
	// docker compose down -v removes a project's containers, <project>-<service>-<n>, and its volumes and networks,
	// <project>_<name> (docker compose's naming).
	of := func(names []string, sep string) []string {
		var out []string
		for _, n := range names {
			for _, p := range g.projects {
				if strings.HasPrefix(n, p+sep) {
					out = append(out, n)
					break
				}
			}
		}
		slices.Sort(out)
		return out
	}
	g.containers, g.volumes, g.networks = of(containers, "-"), of(volumes, "_"), of(networks, "_")
	return g, exitDone, true
}

// poolServeOf is the pool serve the checkout's pid file names, when that pid runs laug pool serve: its pid and the
// -backend its command line names, fake when it names none, "" when its flags do not parse back. A pid that is gone,
// a zombie's empty command line, or a pid another program took reads as none, pid 0.
func (a *App) poolServeOf() (int, string) {
	pid, live := livePid(filepath.Join(a.Root, pidFile))
	line := cmdline(pid)
	if !live || !serveCmdline.MatchString(line) {
		return 0, ""
	}
	var o poolServeOpts
	f := a.newPoolServeFlags(&o)
	if _, _, ok := f.parse(strings.Fields(line)[3:], io.Discard, io.Discard); !ok {
		return pid, ""
	}
	if o.backend == "" {
		return pid, "fake"
	}
	return pid, o.backend
}

// fcGone is what laug pool down -backend firecracker stops, and with -remove-cells removes, as
// cells/backend-firecracker.sh sweep finds it: this checkout's pool serve by its pid file, when it runs that backend,
// and the microVM cells in /run/laug, by their block dirs and their VMMs.
type fcGone struct {
	serve  int      // pool serve -backend firecracker's pid; 0 when the pid file names none that runs
	other  int      // the pid of a pool serve on another backend, which this refuses to stop
	blocks []string // /run/laug/b<N>, one a cell
	vmms   []int    // processes whose command line holds --api-sock /run/laug/b<N>/fc.sock, or --id laug-b<N> (jailed)
}

// fcGoneNow finds what pool down -backend firecracker stops and removes.
func (a *App) fcGoneNow() fcGone {
	var g fcGone
	if pid, backend := a.poolServeOf(); backend == "firecracker" {
		g.serve = pid
	} else {
		g.other = pid
	}
	dirs, _ := filepath.Glob(filepath.Join(fcRunDir, "b[0-9]*"))
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			g.blocks = append(g.blocks, filepath.Base(d))
		}
	}
	// A VMM started as root names its socket; one the jailer started names its id, --id and the id as two arguments
	// (Firecracker v1.17.0 src/jailer/src/env.rs), or --id=<id> as the jailer's docs write it.
	vmm := regexp.MustCompile(`--api-sock ` + regexp.QuoteMeta(fcRunDir) + `/b[0-9]+/fc\.sock( |$)` +
		`|--id[ =]laug-b[0-9]+( |$)`)
	procs, _ := filepath.Glob(filepath.Join(procDir, "[0-9]*", "cmdline"))
	for _, p := range procs {
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(p)))
		if err == nil && vmm.MatchString(cmdline(pid)) {
			g.vmms = append(g.vmms, pid)
		}
	}
	slices.Sort(g.vmms)
	return g
}

// serveText names the pool serve that stops; cells counts the microVM cells, which stay or go.
func (g fcGone) serveText() string {
	if g.serve == 0 {
		return "no Firecracker pool serve runs"
	}
	return fmt.Sprintf("pool serve -backend firecracker, pid %d", g.serve)
}

func (g fcGone) cells() string {
	return fmt.Sprintf("%s in %s and %s", plural(len(g.blocks), "microVM cell"), fcRunDir, plural(len(g.vmms), "VMM"))
}

// fcStopWait is how long pool down -backend firecracker waits for pool serve to go after SIGTERM: pool serve stops
// within 2 s. fcStopPoll is how often it looks.
var fcStopWait, fcStopPoll = 5 * time.Second, 100 * time.Millisecond

// poolDownFirecracker is pool down -backend firecracker. Bare, it stops the checkout's pool serve -backend firecracker
// and leaves the cells; with remove it then execs cells/backend-firecracker.sh sweep, which removes them. Both run as
// root alone. -dry-run with remove prints the sweep's line; alone it says what would stop and stay, exit 2.
func (a *App) poolDownFirecracker(remove, dry bool) int {
	c := script.Command{Script: fcBackend, Args: []script.Value{{Text: "sweep"}}}
	if dry && remove {
		return a.runScript("pool down", c, true, script.Exec, "")
	}
	if !a.inCheckout("pool down", c.Script) {
		return exitUsage
	}
	g := a.fcGoneNow()
	stop := g.serveText()
	if g.serve == 0 {
		stop = "nothing: " + stop
	}
	if dry {
		fmt.Fprintf(a.Stderr, "laug · pool down: would stop %s; %s would stay\n", stop, g.cells())
		fmt.Fprintln(a.Stderr, "sudo bin/laug pool down -backend firecracker -remove-cells removes them as well, each "+
			"cell's disk, link and dir, with prep's deny rules and the guest key")
		return exitUsage
	}
	if geteuid() != 0 {
		line := "sudo bin/laug pool down -backend firecracker"
		if remove {
			line += " -remove-cells"
		}
		return a.refused("pool down", errors.New("-backend firecracker runs as root, since the pool and its cells are "+
			"root's: "+line))
	}
	if g.other != 0 {
		return a.refused("pool down", fmt.Errorf("%s names pid %d, a pool serve on another backend: %s pool down "+
			"-remove-cells stops it; -backend firecracker stops only the Firecracker pool", pidFile, g.other, a.self()))
	}
	var env []string
	if remove {
		var code int
		var ok bool
		if c, env, code, ok = a.prepare("pool down", c, false); !ok {
			return code
		}
		fmt.Fprintf(a.Stdout, "laug · pool down: %s; %s: each cell's disk, its data with it, its link and its dir; "+
			"prep's deny rules and the guest key go\n", g.serveText(), g.cells())
	} else {
		fmt.Fprintf(a.Stdout, "laug · pool down: stopping %s; %s stay: sudo bin/laug pool down -backend firecracker "+
			"-remove-cells removes them\n", stop, g.cells())
	}
	if g.serve != 0 {
		if err := stopServe(g.serve); err != nil {
			why := ""
			if remove {
				why = ", so the sweep did not run and nothing was removed"
			}
			fmt.Fprintf(a.Stderr, "laug · pool down: %v%s\n", a.errShown(err), why)
			return exitFailed
		}
		fmt.Fprintf(a.Stdout, "pool serve, pid %d, stopped\n", g.serve)
	}
	if !remove {
		return exitDone
	}
	return a.start("pool down", c, env, script.Exec, "", a.Stdout)
}

// stopServe sends pool serve's pid SIGTERM and waits fcStopWait at most for it to go: gone, a zombie whose command line
// is empty, or a pid that no longer runs laug pool serve.
func stopServe(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("pool serve, pid %d: SIGTERM: %w", pid, err)
	}
	for end := time.Now().Add(fcStopWait); ; time.Sleep(fcStopPoll) {
		err := syscall.Kill(pid, 0)
		if err != nil && !errors.Is(err, syscall.EPERM) || !serveCmdline.MatchString(cmdline(pid)) {
			return nil
		}
		if time.Now().After(end) {
			return fmt.Errorf("pool serve, pid %d, still runs %s after SIGTERM", pid, fcStopWait)
		}
	}
}

// cmdline is a process's command line, its arguments joined by blanks, as tr '\0' ' ' makes it; "" when it cannot be
// read.
func cmdline(pid int) string {
	b, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.ReplaceAll(string(b), "\x00", " "), " ")
}

func runPoolBench(_ context.Context, a *App, args []string) int {
	f := a.newFlags("pool bench", "<host:port> [-cycles 20] [-ttl-short 30s] [-dry-run]")
	cycles := f.Int("cycles", 20, "lease and release `cycles` from warm, for the lease p50 and p95")
	ttl := f.Duration("ttl-short", 30*time.Second, "the short lease's TTL, whole seconds: the time from its lease to "+
		"its cell gone from /status")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch len(pos) {
	case 0:
		return a.usageError(f, "names no pool: its address, %s for %s pool serve's default", pool.DefaultListen,
			a.self())
	case 1:
	default:
		return a.usageError(f, "takes one pool, got %q", pos)
	}
	c, err := poolapi.New(pos[0])
	if err != nil {
		return a.refused("pool bench", fmt.Errorf("%s: %w", shellWord(pos[0]), err))
	}
	cmd := script.Command{Script: benchScript, Pass: []script.Var{script.Bare("POOL_ADDR", c.Base())},
		By: map[string]string{"POOL_ADDR": "<host:port>"}}
	if given(f, "cycles") {
		if err := checkCount("-cycles", *cycles); err != nil {
			return a.refused("pool bench", err)
		}
		cmd.Pass = append(cmd.Pass, script.Bare("CYCLES", strconv.Itoa(*cycles)))
	} else {
		cmd.Clear = append(cmd.Clear, "CYCLES")
	}
	if given(f, "ttl-short") {
		s, err := seconds("-ttl-short", *ttl)
		if err == nil && *ttl == 0 {
			err = fmt.Errorf("-ttl-short %s: whole seconds, over 0", *ttl)
		}
		if err != nil {
			return a.refused("pool bench", err)
		}
		cmd.Pass = append(cmd.Pass, script.Bare("TTL_SHORT", s))
	} else {
		cmd.Clear = append(cmd.Clear, "TTL_SHORT")
	}
	first := fmt.Sprintf("laug · pool bench: timing the pool at %s: its time to warm, %d lease cycles from warm, "+
		"one cold lease and a %s TTL; it leases and releases that pool's cells; Ctrl-C stops it",
		c.Base(), *cycles, *ttl)
	return a.runScript("pool bench", cmd, *dry, script.Exec, first)
}
