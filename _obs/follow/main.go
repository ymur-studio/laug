// Package follow writes each leased cell's federation target, with the labels that outlive the cell, and serves each gate's
// own records as Prometheus series, for the observability stack (_obs/README.md). Standard library only.
//
//	follow -listen 127.0.0.1:7720 -pool 127.0.0.1:7700 -dir <OBS_DIR>   every -every (2 s), until TERM or INT
//	follow -openmetrics <gate dir> [-dir <OBS_DIR>]                     one gate's records with its own times, on stdout
//	follow -gate-running                                                   exit 0 and why when a gate runs, else 1
//	follow -decode <OBS_DIR> -out <dir>                                 the stores' compressed bytes, decoded, for obs pack
//
// Each poll: find the laugd on -pool and, when its stdout is a gate directory's laugd.log, follow that gate; read the
// gate directories it follows (records.go); while the laugd on -pool is a gate's, ask it nothing and take targets from
// the gate's files: each item's lease.json until the gate's release line or the lease's expiry, and each item's Omes
// from the wave line to its ended line; outside a gate, read laugd's /status, take each leased cell, and drop every
// hand target after 3 failed reads (targets.go). A gate's records are served from first sight until 10 minutes after
// its done line (render.go). It reads OBS_DIR/conf/aa/*.txt, the committed A/A lists, and OBS_DIR/conf/catalog.json,
// faults/catalog.yaml as bin/obs up made it JSON, and writes OBS_DIR/targets and OBS_DIR/follow, the gate directories
// it follows: absolute paths, private to this host, never served. Its command line is never a pool serve's, so laug
// pool down (cells/pool-down.sh) never stops it.
package follow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ymur-studio/laug/internal/gateopen"
)

// keepAfterDone is how long a gate's records are served after its done line; a gate with no done line that no laugd
// names is dropped once its timeline has not changed for as long.
const keepAfterDone = 10 * time.Minute

// loopback says whether a is 127.0.0.1:<port>, port 0 to 65535 written plainly.
func loopback(a string) bool {
	host, port, err := net.SplitHostPort(a)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 0 && n <= 65535 && strconv.Itoa(n) == port
}

// onListen is told the listener's address once bound: a test seam, a no-op in the binary.
var onListen = func(net.Addr) {}

// run is follow with its arguments, as Main and the tests run it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return Run(ctx, args, stdout, stderr, nil)
}

// Run is follow with its arguments, as bin/obs follow runs it inside its own process. Cancelling ctx is TERM or
// INT: the poll in flight ends, then exit 0. ready, when not nil, is called once with -listen's address, after the
// listener is bound and before the first poll, so a caller's first line can say what started. Exit 2 on a bad flag;
// with -gate-running 0 when a gate runs and 1 when none does; with -openmetrics 1 when the directory holds no timeline;
// with -decode 2 when a file cannot be placed or read; 1 when the listener cannot bind or OBS_DIR cannot be written. A
// file that does not parse is never an exit: it is skipped for that pass and counted.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, ready func(net.Addr)) int {
	fl := flag.NewFlagSet("follow", flag.ContinueOnError)
	fl.SetOutput(stderr)
	listen := fl.String("listen", "", "serve GET /metrics on 127.0.0.1:<port> (7720 for the stack), no other host")
	pool := fl.String("pool", "127.0.0.1:7700", "the address the gate's laugd, and any other, listens on: 127.0.0.1:<port>")
	dir := fl.String("dir", "", "the stack's OBS_DIR: targets and follow are written in it, conf/aa and conf/catalog.json read")
	every := fl.Duration("every", 2*time.Second, "time between polls")
	om := fl.String("openmetrics", "", "print this gate directory's records as OpenMetrics with the gate's own times, and exit")
	gateOpenFlag := fl.Bool("gate-running", false, "exit 0 and say why when a gate runs on this host, 1 when none does")
	decode := fl.String("decode", "", "decode OBS_DIR/prometheus's WAL and OBS_DIR/jaeger's tables into -out, for bin/obs pack's check, and exit (decode.go)")
	decodeOut := fl.String("out", "", "with -decode: an empty directory, outside OBS_DIR/prometheus and OBS_DIR/jaeger, for the decoded files")
	root := fl.String("root", "/", "the directory that holds the proc tree (the tests' fake tree)")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	bad := func(msg string) int { fmt.Fprintln(stderr, "follow: "+msg); return 2 }
	given := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { given[f.Name] = true })
	modes := 0
	for _, m := range []string{"listen", "openmetrics", "gate-running", "decode"} {
		if given[m] {
			modes++
		}
	}
	switch {
	case fl.NArg() > 0:
		return bad("no arguments past the flags")
	case modes != 1:
		return bad("one of -listen, -openmetrics, -gate-running and -decode")
	case given["out"] && !given["decode"]:
		return bad("-out goes with -decode")
	case !loopback(*pool):
		return bad("-pool must be 127.0.0.1:<port>: laugd listens on loopback only")
	case given["listen"] && !loopback(*listen):
		return bad("-listen must be 127.0.0.1:<port>: the listener has no auth, so no other host")
	case given["listen"] && *dir == "":
		return bad("-listen needs -dir, the stack's OBS_DIR")
	case *every <= 0:
		return bad("-every must be above 0")
	}
	if given["decode"] {
		return runDecode(*decode, *decodeOut, stdout, stderr)
	}
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	locks := gateopen.LockPaths(xdg, os.Getuid())

	if *gateOpenFlag {
		if why, open := gateopen.GateOpen(*root, *pool, locks, xdg); open {
			fmt.Fprintln(stdout, why)
			return 0
		}
		return 1
	}
	if *om != "" {
		abs, err := filepath.Abs(*om)
		if err != nil {
			return bad("-openmetrics: not a directory")
		}
		g := newGateDir(filepath.Base(abs), abs)
		g.refresh()
		if len(g.timeline) == 0 {
			fmt.Fprintln(stderr, "follow: -openmetrics: "+g.id+" has no timeline line to give the records a time")
			return 1
		}
		aa, budget := readConf(*dir)
		b, dropped := openMetrics(g, aa, budget)
		if g.skipped > 0 || dropped > 0 {
			fmt.Fprintf(stderr, "follow: -openmetrics: %s: %d files or lines skipped (they did not parse), %d series left out (a label would hold a slash or a control character, or a label name twice)\n", g.id, g.skipped, dropped)
		}
		if _, err := stdout.Write(b); err != nil {
			return 1
		}
		return 0
	}

	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	f, err := newFollower(*dir, *root, *pool, locks, logger)
	if err != nil {
		logger.Printf("follow: %v", err)
		return 1
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Printf("follow: -listen: %v", errors.Unwrap(err))
		return 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		b := f.body.Load()
		if b == nil {
			http.Error(w, "no poll yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write(*b)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	logger.Printf("follow: serving on %s, GET /metrics; a poll every %s until TERM or INT", ln.Addr(), *every)
	onListen(ln.Addr())
	if ready != nil {
		ready(ln.Addr())
	}
	tick := time.NewTicker(*every)
	defer tick.Stop()
	for {
		f.poll()
		select {
		case <-ctx.Done():
			logger.Printf("follow: stopped after %d polls", f.polls)
			return 0
		case <-tick.C:
		}
	}
}

// readConf reads the A/A lists, OBS_DIR/conf/aa/<list>.txt with one gate id a line and # comments, into each gate
// id's lists, and the catalog's budget_s by fault. A missing file gives nothing.
func readConf(dir string) (aa map[string][]string, budget map[string]float64) {
	aa, budget = map[string][]string{}, map[string]float64{}
	if dir == "" {
		return
	}
	files, _ := filepath.Glob(filepath.Join(dir, "conf", "aa", "*.txt"))
	sort.Strings(files)
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		list := strings.TrimSuffix(filepath.Base(file), ".txt")
		for _, line := range strings.Split(string(b), "\n") {
			if id := strings.TrimSpace(line); gateopen.IsGateID(id) {
				aa[id] = append(aa[id], list)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "conf", "catalog.json")); err == nil {
		var c struct {
			Faults map[string]struct {
				Budget *float64 `json:"budget_s"`
			} `json:"faults"`
		}
		if json.Unmarshal(b, &c) == nil {
			for name, f := range c.Faults {
				if f.Budget != nil {
					budget[name] = *f.Budget
				}
			}
		}
	}
	return
}

// follower is what persists between polls.
type follower struct {
	dir, root, pool string
	sc              *gateopen.Scanner
	locks           []string
	client          *http.Client
	now             func() time.Time
	log             *log.Logger
	gates           map[string]*gateDir
	hand            map[string]target // by file name
	handFails       int
	polls           uint64
	laugdErrors     uint64
	skipped         uint64
	dropped         int // series the last render left out
	targets         []target
	stale           int
	body            atomic.Pointer[[]byte]
}

// newFollower makes OBS_DIR's targets and follow directories and takes up what a follow before it followed.
func newFollower(dir, root, pool string, locks []string, logger *log.Logger) (*follower, error) {
	f := &follower{dir: dir, sc: &gateopen.Scanner{Root: root, Pool: pool}, root: root, pool: pool, locks: locks,
		client: &http.Client{Timeout: time.Second}, now: time.Now, gates: map[string]*gateDir{}, hand: map[string]target{}, log: logger}
	for _, d := range []string{"targets", "follow"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return nil, fmt.Errorf("cannot make OBS_DIR/%s", d)
		}
	}
	f.load()
	return f, nil
}

// load takes up the gate directories a follow before this one followed, from OBS_DIR/follow/<gate id>.json.
func (f *follower) load() {
	files, _ := filepath.Glob(filepath.Join(f.dir, "follow", "*.json"))
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".json")
		var v struct {
			Dir string `json:"dir"`
		}
		b, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(b, &v) != nil || !gateopen.IsGateID(id) || filepath.Base(v.Dir) != id {
			continue
		}
		f.gates[id] = newGateDir(id, v.Dir)
	}
}

// see follows a gate directory from now on, and records it in OBS_DIR/follow for the next follow.
func (f *follower) see(id, dir string) {
	if f.gates[id] != nil {
		return
	}
	f.gates[id] = newGateDir(id, dir)
	b, _ := json.Marshal(map[string]string{"dir": dir})
	if err := writeIfChanged(filepath.Join(f.dir, "follow", id+".json"), append(b, '\n')); err != nil {
		f.log.Printf("follow: cannot record gate %s in OBS_DIR/follow", id)
	}
	f.log.Printf("follow: gate %s found by its laugd", id)
}

// expired says a gate's records are no longer served: 10 minutes after its done line; with no done line, when no
// laugd names it and its timeline has not changed for 10 minutes; or when its directory is gone.
func (f *follower) expired(g *gateDir, named bool, now time.Time) bool {
	if named {
		return false
	}
	fi, err := os.Stat(filepath.Join(g.dir, "timeline.jsonl"))
	if _, gerr := os.Stat(filepath.Join(g.dir, "gate.json")); gerr != nil {
		return true
	}
	if d := g.lineAt("done", ""); !math.IsNaN(d) {
		return now.Sub(time.Unix(0, int64(d*1e9))) > keepAfterDone
	}
	return err != nil || now.Sub(fi.ModTime()) > keepAfterDone
}

func (f *follower) forget(id string) {
	delete(f.gates, id)
	os.Remove(filepath.Join(f.dir, "follow", id+".json"))
	f.log.Printf("follow: gate %s no longer followed", id)
}

// poll is one pass: find the gate, read what changed, write the targets, render the body.
func (f *follower) poll() {
	now := f.now()
	f.polls++
	var gate *gateopen.Laugd
	hand := false
	ls := f.sc.Laugds()
	for i := range ls {
		if ls[i].Dir != "" {
			gate = &ls[i]
		} else {
			hand = true
		}
	}
	if gate != nil {
		f.see(gate.ID, gate.Dir)
	}
	for id, g := range f.gates {
		f.skipped += uint64(g.refresh())
		if f.expired(g, gate != nil && gate.ID == id, now) {
			f.forget(id)
		}
	}
	switch {
	case gate != nil:
		// The laugd on -pool is a gate's: no hand lease can be there, and follow asks it nothing.
		f.hand, f.handFails = map[string]target{}, 0
	case hand:
		if why, held := gateopen.HeldLock(f.root, f.locks); held {
			_ = why // a gate holds the host lock: it is open, and follow asks no laugd
			break
		}
		cells, err := readStatus(f.client, f.pool)
		if err != nil {
			f.laugdErrors++
			f.handFail()
			break
		}
		f.handFails = 0
		f.hand = map[string]target{}
		for _, c := range cells {
			if c.State != "leased" || !c.ExpiresAt.After(now) {
				continue
			}
			if t, ok := cellTarget(c.ID, c.Prom, c.Block, c.ServerTag, c.LeasedAt, c.ExpiresAt, "", "", ""); ok {
				f.hand[t.file] = t
			}
		}
	default:
		f.handFail() // no laugd on -pool: its leases end with it
	}
	f.targets = f.gateTargets(now)
	for _, t := range f.hand {
		f.targets = append(f.targets, t)
	}
	sortTargets(f.targets)
	stale, err := writeTargets(filepath.Join(f.dir, "targets"), f.targets, now)
	if err != nil {
		f.log.Printf("follow: cannot write OBS_DIR/targets: %v", errors.Unwrap(err))
	}
	f.stale = stale
	b := f.render()
	f.body.Store(&b)
}

// handFail counts a poll that read no /status; the third in a row drops every hand target.
func (f *follower) handFail() {
	f.handFails++
	if f.handFails >= 3 {
		f.hand = map[string]target{}
	}
}

// gateTargets is each followed gate's targets now: an item's cell from its lease.json until the gate's release line
// or the lease's expires_at, whichever is first, and its Omes, worker and client from gate.json's wiring, from the wave
// line until the item's ended line.
func (f *follower) gateTargets(now time.Time) []target {
	var out []target
	for _, g := range f.gates {
		if !math.IsNaN(g.lineAt("release", "")) {
			continue
		}
		wave := !math.IsNaN(g.lineAt("wave", ""))
		arms := g.arms()
		for _, k := range g.keys() {
			it := g.items[k]
			if it == nil || it.lease == nil {
				continue
			}
			leased, err1 := parseTime(str(it.lease, "leased_at"))
			expires, err2 := parseTime(str(it.lease, "expires_at"))
			block, ok := num(it.lease, "block")
			if err1 != nil || err2 != nil || !ok || float64(now.Unix()) >= expires {
				continue
			}
			t, ok := cellTarget(str(it.lease, "id"), str(it.lease, "prometheus"), int(block), str(it.lease, "server_tag"),
				time.Unix(int64(leased), 0), time.Unix(int64(expires), 0), g.id, k, arms[k])
			if !ok {
				continue
			}
			out = append(out, t)
			om := obj0(g.wiring(k)["omes_metrics"])
			if !wave || !math.IsNaN(g.lineAt("ended", k)) {
				continue
			}
			var groups []group
			for _, role := range []string{"worker", "client"} {
				if a := str(om, role); loopback(a) {
					groups = append(groups, group{a, [][][2]string{nonEmpty([][2]string{{"gate_id", g.id}, {"item", k}, {"arm", arms[k]},
						{"role", role}, {"cell", str(it.lease, "id")}})}})
				}
			}
			if len(groups) > 0 {
				out = append(out, target{file: "omes-" + g.id + "-" + k + ".json", groups: groups, expires: int64(expires)})
			}
		}
	}
	return out
}

// parseTime reads laugd's and the run JSON's RFC 3339 times as Unix seconds, fractions kept.
func parseTime(s string) (float64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, err
	}
	return float64(t.UnixNano()) / 1e9, nil
}

// render is the live body: every followed gate's records as they stand, the newest poll of each at its own time
// (milliseconds, the text format's), the lease labels of each cell target, and follow's own counters.
func (f *follower) render() []byte {
	aa, budget := readConf(f.dir)
	type sample struct {
		labels, ts string
		v          float64
	}
	byFam := map[string][]sample{}
	seen := map[string]bool{}
	left := map[string]bool{}
	put := func(name string, ls [][2]string, v float64, ts string) {
		if !clean(ls) {
			left[name+labelText(ls)] = true
			return
		}
		lt := labelText(ls)
		if seen[name+lt] {
			return
		}
		seen[name+lt] = true
		byFam[name] = append(byFam[name], sample{lt, ts, v})
	}
	ids := make([]string, 0, len(f.gates))
	for id := range f.gates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		g := f.gates[id]
		all := g.series(aa, budget)
		newest := map[string]float64{}
		for _, s := range all {
			if s.ts != 0 {
				k := s.name + labelText(s.labels)
				if s.ts > newest[k] {
					newest[k] = s.ts
				}
			}
		}
		for _, s := range all {
			switch {
			case s.ts != 0 && s.ts == newest[s.name+labelText(s.labels)]:
				put(s.name, s.labels, s.value, strconv.FormatInt(int64(math.Round(s.ts*1000)), 10))
			case s.ts == 0 && !math.IsNaN(s.at):
				put(s.name, s.labels, s.value, "")
			}
		}
		for _, s := range g.stepActive(math.Inf(1)) {
			put(s.name, s.labels, s.value, "")
		}
		put("laug_follow_gate_dir_found", [][2]string{{"gate_id", id}}, 1, "")
	}
	for _, t := range f.targets {
		if t.lease != nil {
			put("laug_lease_info", t.lease, 1, "")
		}
	}
	put("laug_follow_polls_total", nil, float64(f.polls), "")
	put("laug_follow_laugd_errors_total", nil, float64(f.laugdErrors), "")
	put("laug_follow_targets", nil, float64(len(f.targets)), "")
	put("laug_follow_stale_targets", nil, float64(f.stale), "")
	put("laug_follow_skipped_files_total", nil, float64(f.skipped), "")
	f.dropped = len(left)
	put("laug_follow_dropped_series", nil, float64(f.dropped), "")
	var b bytes.Buffer
	for _, fam := range families {
		ss := byFam[fam.name]
		if len(ss) == 0 {
			continue
		}
		b.WriteString("# HELP " + fam.name + " " + fam.help + "\n# TYPE " + fam.name + " " + fam.typ + "\n")
		for _, s := range ss {
			b.WriteString(fam.name + s.labels + " " + fmtValue(s.v))
			if s.ts != "" {
				b.WriteString(" " + s.ts)
			}
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}
