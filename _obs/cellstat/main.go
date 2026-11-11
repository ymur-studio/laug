// Package cellstat reads each laug container's cgroup, the Omes processes and the host, and writes one JSON line a sample:
// at start, then every -interval, then once more at -for, TERM or INT. With -report it turns a file of such lines,
// and a gate dir or a window, into tables per item: text, or JSON. With -listen it also serves the last sample on
// 127.0.0.1 as Prometheus text (metrics.go), for the observability stack (_obs/README.md); with -pids it
// reads the processes a directory of {pid, start, boot_id} records names, and the orchestrator's dev server and worker.
// Standard library only.
//
// Rules: a reading that was not read is absent, never 0; memory is anon and file, never memory.current; a group is
// read only when /proc/<pid>/cgroup names it and its last element holds the container's full id; the cgroup root is
// never listed; a line holds no path, command line or stderr text, and other projects' containers only as a count.
package cellstat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	projectLabel = "com.docker.compose.project"
	serviceLabel = "com.docker.compose.service"
	callTimeout  = 4 * time.Second        // each docker call; a slow one keeps the last list
	lockWait     = 100 * time.Millisecond // how long a busy -out lock is tried before it counts as another cellstat

	// The defaults of -project and -run-id. Run ids: the gate's <gate id>-<key>, the date form of the Makefile's
	// RUN_ID, and that form after raun- (laug raun) or fault-<fault>- (faults/run.sh).
	defaultProject = `^laug(-[0-9a-f]{8})?$`
	defaultRunID   = `^(g[0-9]{8}-[0-9]{6}-[a-z0-9]+|(raun-|fault-[a-z0-9-]+-)?[0-9]{8}-[0-9]{6})$`
)

// Line is one sample. v is the schema version; wall and mono are the gate's stamp clocks (gate/lib.sh, stamp), taken
// after the docker calls and before the first file read.
type Line struct {
	V           int         `json:"v"`
	Seq         int         `json:"seq"`
	Wall        float64     `json:"wall"`           // Unix seconds, to the ms
	Mono        *float64    `json:"mono,omitempty"` // /proc/uptime seconds
	SelfCPU     *uint64     `json:"self_cpu_us,omitempty"`
	ChildrenCPU *uint64     `json:"children_cpu_us,omitempty"` // its docker calls
	Host        Host        `json:"host"`
	Others      int         `json:"others"` // listed containers of other projects, never named
	Errors      []string    `json:"errors,omitempty"`
	Containers  []Container `json:"containers"`
	Procs       *[]Proc     `json:"procs,omitempty"`       // absent with -procs=false or when <root>/proc cannot be listed
	OtherProcs  *int        `json:"other_procs,omitempty"` // beside procs: Omes processes whose run id -run-id refuses, never named
	Pids        *[]Proc     `json:"pids,omitempty"`        // with -pids: each recorded process, then the orchestrator's dev server and worker
}

// lister finds compose containers: their ids, then each new id's pid, state and labels.
type lister interface {
	list() ([]string, error)
	inspect(ids []string) ([]inspected, error)
}

// inspected is the part of docker inspect's JSON that cellstat takes. Labels come from JSON: a template prints
// <no value> for a missing label (seen).
type inspected struct {
	ID    string `json:"Id"`
	State struct {
		Pid     int  `json:"Pid"`
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// dockerCLI runs the docker CLI, or the -cli stand-in. stderr is dropped: a warning there does not spoil stdout, and its text never
// reaches a line.
type dockerCLI struct {
	path    string
	timeout time.Duration
}

// run returns stdout and an error worded for the line: "<what> exit N", "<what> timeout" or "<what> failed".
// answered is false when the call timed out or did not start: then stdout holds nothing to parse. The timeout is the
// call's own, not TERM's: a TERM does not cut the last line's calls short, and they stay bounded as every call is.
func (d dockerCLI) run(what string, args ...string) (out []byte, answered bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.path, args...)
	cmd.WaitDelay = time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return nil, false, fmt.Errorf("%s timeout", what)
	case errors.As(err, &exit):
		return buf.Bytes(), true, fmt.Errorf("%s exit %d", what, exit.ExitCode())
	case err != nil:
		return nil, false, fmt.Errorf("%s failed", what)
	}
	return buf.Bytes(), true, nil
}

// list prints every running compose container's full id (64 hex, seen).
func (d dockerCLI) list() ([]string, error) {
	out, _, err := d.run("list", "ps", "-q", "--no-trunc", "--filter", "label="+projectLabel)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, f := range strings.Fields(string(out)) {
		if fullID.MatchString(f) {
			ids = append(ids, f)
		}
	}
	return ids, nil
}

// inspect parses stdout whatever the exit: docker exits 1 when an id left between the two calls and prints the rest
// (seen).
func (d dockerCLI) inspect(ids []string) ([]inspected, error) {
	out, answered, err := d.run("inspect", append([]string{"inspect"}, ids...)...)
	if !answered {
		return nil, err
	}
	var found []inspected
	if json.Unmarshal(out, &found) != nil {
		return nil, errors.New("inspect bad JSON")
	}
	return found, nil
}

var fullID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// NoCLI is the -cli that runs no container CLI: bin/obs usage passes it when its PATH holds no docker, since bin/obs never
// reads its own process's PATH.
const NoCLI = "none"

// noLister is -cli none. Its list fails as a docker that is not installed fails, so each line says "list failed" and
// holds no container.
type noLister struct{}

func (noLister) list() ([]string, error)               { return nil, errors.New("list failed") }
func (noLister) inspect([]string) ([]inspected, error) { return nil, errors.New("inspect failed") }

// known is what cellstat keeps of one container id between samples: its labels and its group, or why it has none.
// Another project's id keeps only that it is another's.
type known struct {
	other            bool
	project, service string
	dir, why         string
}

// omesProc is the verdict on one pid's cmdline. An empty role is an Omes process of another run id. An orchestrator
// role (orchestrator-server, orchestrator-worker) has no run id, and is kept only with -pids.
type omesProc struct{ role, runID string }

// seenProc is a pid's verdict; final once it came from the second sample that saw the pid.
type seenProc struct {
	o     *omesProc // nil: not an Omes process
	final bool
}

// sampler holds what persists between samples.
type sampler struct {
	root    string
	lister  lister
	project *regexp.Regexp
	runID   *regexp.Regexp
	procs   bool
	pidDir  string // -pids: "" reads no record and no orchestrator process
	now     func() time.Time
	usage   func() (self, children *uint64)
	seq     int
	listed  []string
	known   map[string]*known
	omes    map[int]seenProc
}

func newSampler(root string, l lister, project, runID *regexp.Regexp, procs bool) *sampler {
	return &sampler{root: root, lister: l, project: project, runID: runID, procs: procs, now: time.Now, usage: rusage,
		known: map[string]*known{}, omes: map[int]seenProc{}}
}

// sample takes one line. Ids new or unresolved last time get one inspect for all; the rest come from the cache.
func (s *sampler) sample() Line {
	line := Line{V: 1, Seq: s.seq, Containers: []Container{}}
	s.seq++
	ids, err := s.lister.list()
	if err != nil {
		line.Errors = append(line.Errors, err.Error())
		ids = s.listed
	} else {
		s.listed = ids
	}
	var ask []string
	for _, id := range ids {
		if k := s.known[id]; k == nil || (!k.other && k.dir == "") {
			ask = append(ask, id)
		}
	}
	var found []inspected
	if len(ask) > 0 {
		if found, err = s.lister.inspect(ask); err != nil {
			line.Errors = append(line.Errors, err.Error())
		}
	}
	line.Wall = float64(s.now().UnixMilli()) / 1000
	line.Mono = readUptime(s.root)

	asked := set(ask)
	for _, in := range found {
		if !asked[in.ID] {
			continue
		}
		p := in.Config.Labels[projectLabel]
		if !s.project.MatchString(p) {
			s.known[in.ID] = &known{other: true}
			continue
		}
		k := &known{project: p, service: in.Config.Labels[serviceLabel]}
		k.dir, k.why = s.resolve(in.ID, in.State.Pid, in.State.Running)
		s.known[in.ID] = k
	}
	listed := set(ids)
	for id, k := range s.known {
		if listed[id] {
			continue
		}
		if !k.other {
			line.Containers = append(line.Containers, Container{Project: k.project, Service: k.service, ID: id[:12],
				Via: via(k), State: "gone", Why: "left the list"})
		}
		delete(s.known, id)
	}
	for _, id := range ids {
		k := s.known[id]
		switch {
		case k == nil: // not inspected yet: asked again next time
			continue
		case k.other:
			line.Others++
			continue
		}
		c := Container{Project: k.project, Service: k.service, ID: id[:12], Via: via(k)}
		switch {
		case k.dir == "":
			c.State, c.Why = "unresolved", k.why
		case !isDir(k.dir):
			c.State, c.Why = "gone", "no cgroup dir"
		default:
			c.State = "ok"
			if !readCgroup(k.dir, &c) {
				continue
			}
		}
		line.Containers = append(line.Containers, c)
	}
	sort.Slice(line.Containers, func(i, j int) bool {
		a, b := line.Containers[i], line.Containers[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.ID < b.ID
	})
	line.Host = readHost(s.root)
	if s.procs || s.pidDir != "" {
		p, others, orch, ok := s.scanProcs()
		if ok && s.procs {
			line.Procs, line.OtherProcs = &p, &others
		}
		if s.pidDir != "" {
			pids := append(readRecords(s.root, s.pidDir), orch...)
			sort.Slice(pids, func(i, j int) bool {
				if pids[i].Role != pids[j].Role {
					return pids[i].Role < pids[j].Role
				}
				return pids[i].PID < pids[j].PID
			})
			line.Pids = &pids
		}
	}
	line.SelfCPU, line.ChildrenCPU = s.usage()
	return line
}

func via(k *known) string {
	if k.dir == "" {
		return ""
	}
	return "pid"
}

func set(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// resolve finds a container's group by the pid route: the 0:: line of /proc/<pid>/cgroup (0::/init.scope seen).
// The path must be absolute and clean, its last element must hold the full id, and the dir must exist. Both
// layouts, docker/<id> and system.slice/docker-<id>.scope, pass with nothing hard-coded. Anything else is
// unresolved, and a group not proven to be the container's is never read.
func (s *sampler) resolve(id string, pid int, running bool) (dir, why string) {
	if !running || pid <= 0 {
		return "", "not running"
	}
	b, err := os.ReadFile(filepath.Join(s.root, "proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", "no such pid"
	}
	p, ok := "", false
	for _, l := range strings.Split(string(b), "\n") {
		if p, ok = strings.CutPrefix(l, "0::"); ok {
			break
		}
	}
	if !ok || !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "", "bad path"
	}
	// An inspect pid from another pid namespace can name a live host process (pid_max 99999 on the dev host): its
	// group lacks the id, so it lands here.
	if !strings.Contains(path.Base(p), id) {
		return "", "path lacks id"
	}
	dir = filepath.Join(s.root, "sys", "fs", "cgroup", p)
	if !isDir(dir) {
		return "", "no such dir"
	}
	return dir, ""
}

// scanProcs lists <root>/proc for the Omes client and worker, and with -pids for the orchestrator's dev server and worker (orch).
// A pid's cmdline is read at the first two samples that see it, and the second verdict kept until the pid leaves: the
// first may catch the pid between its fork and its exec, as env before the worker it execs (make orchestrator-worker
// ran env so) or go run's child before the Omes binary, and a read between an exec's new comm and its new argv finds an
// empty cmdline (fs/exec.c; source only). In 20 background setsid starts, as gate/run.sh starts laugd, each pid
// ran its program by its parent's first read of it, 1.2 to 1.8 ms after the fork (seen on the dev host), so
// the second sample, an interval on, sees the program. A run id that -run-id refuses is counted, never kept.
func (s *sampler) scanProcs() (out []Proc, others int, orch []Proc, ok bool) {
	ents, err := os.ReadDir(filepath.Join(s.root, "proc"))
	if err != nil {
		return nil, 0, nil, false
	}
	out, orch = []Proc{}, []Proc{}
	live := map[int]bool{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		live[pid] = true
		c, seen := s.omes[pid]
		if !c.final {
			argv := readCmdline(s.root, pid)
			o := omesOf(argv)
			if o != nil && !s.runID.MatchString(o.runID) {
				o = &omesProc{}
			}
			if r := orchOf(argv); o == nil && r != "" && s.pidDir != "" {
				o = &omesProc{role: r}
			}
			c = seenProc{o: o, final: seen}
			s.omes[pid] = c
		}
		o := c.o
		switch {
		case o == nil:
			continue
		case o.role == "":
			others++
			continue
		case o.runID == "": // an orchestrator role
			if p, ok := readProc(s.root, pid); ok {
				p.Role, p.PID = o.role, pid
				readMemIO(s.root, pid, &p)
				orch = append(orch, p)
			}
			continue
		case !s.procs:
			continue
		}
		p, ok := readProc(s.root, pid)
		if !ok {
			continue
		}
		p.Role, p.RunID, p.PID = o.role, o.runID, pid
		out = append(out, p)
	}
	for pid := range s.omes {
		if !live[pid] {
			delete(s.omes, pid)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		if out[i].RunID != out[j].RunID {
			return out[i].RunID < out[j].RunID
		}
		return out[i].PID < out[j].PID
	})
	return out, others, orch, true
}

// orchOf names an orchestrator process by its argv, as omesOf names Omes: orchestrator-server is the dev server laug
// orchestrator server starts, `temporal server start-dev` with --port 7400 (its -orchestrator), and
// orchestrator-worker is `laug orchestrator worker`, which runs the worker in laug's own process: argv[0] named laug,
// bin/laug from the checkout's root or laug at any other path, then orchestrator and worker. Anything else is "".
func orchOf(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	if path.Base(argv[0]) == "laug" && len(argv) >= 3 && argv[1] == "orchestrator" && argv[2] == "worker" {
		return "orchestrator-worker"
	}
	if path.Base(argv[0]) != "temporal" || len(argv) < 3 || argv[1] != "server" || argv[2] != "start-dev" {
		return ""
	}
	for i, a := range argv[3:] {
		if a == "--port=7400" || (a == "--port" && 3+i+1 < len(argv) && argv[3+i+1] == "7400") {
			return "orchestrator-server"
		}
	}
	return ""
}

// omesOf names an Omes process by its argv. The client runs run-scenario-with-worker with --run-id <id>
// (load/run-omes.sh); the worker runs worker, then --task-queue omes-<run id> (omes f0de206,
// internal/workerctl/run.go; source only). argv[0] go is the go run parent:
// skipped.
func omesOf(argv []string) *omesProc {
	if len(argv) < 2 || path.Base(argv[0]) == "go" {
		return nil
	}
	after := func(from int, flag string) string {
		for i := from; i+1 < len(argv); i++ {
			if argv[i] == flag {
				return argv[i+1]
			}
		}
		return ""
	}
	for i, a := range argv[1:] {
		switch a {
		case "run-scenario-with-worker":
			if id := after(1, "--run-id"); id != "" {
				return &omesProc{"omes-client", id}
			}
			return nil
		case "worker":
			if q, ok := strings.CutPrefix(after(i+2, "--task-queue"), "omes-"); ok && q != "" {
				return &omesProc{"omes-worker", q}
			}
			return nil
		}
	}
	return nil
}

// rusage is cellstat's own CPU and its waited-for children's (the docker calls), in microseconds.
func rusage() (self, children *uint64) {
	us := func(who int) *uint64 {
		var ru syscall.Rusage
		if syscall.Getrusage(who, &ru) != nil {
			return nil
		}
		v := uint64(ru.Utime.Sec)*1e6 + uint64(ru.Utime.Usec) + uint64(ru.Stime.Sec)*1e6 + uint64(ru.Stime.Usec)
		return &v
	}
	return us(syscall.RUSAGE_SELF), us(syscall.RUSAGE_CHILDREN)
}

// bare drops the path from a file error, so a log line names no path.
func bare(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// run is cellstat with its arguments, as the tests run it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return Run(ctx, args, stdout, stderr, nil)
}

// Run is cellstat with its arguments, as bin/obs usage record and sample run it inside its own process. Cancelling
// ctx is TERM or INT: the sample in flight ends, then one last line, exit 0. ready, when not nil, is called once,
// after -out is locked and its pid file written and -listen is bound, before the first sample, so a caller's first
// line can say what started. Exit 2 on a bad flag, a -root with no kernel tree, an -out another cellstat holds or one
// that is not a new or empty file; 1 when -out cannot be opened or locked, its pid file cannot be written, a line
// cannot be written, a report cannot be read or its window holds fewer than 2 lines.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, ready func()) int {
	fl := flag.NewFlagSet("cellstat", flag.ContinueOnError)
	fl.SetOutput(stderr)
	interval := fl.Duration("interval", 5*time.Second, "time between samples; one that overruns skips the ticks it missed")
	forDur := fl.Duration("for", 0, "stop after this long with one last line; 0 waits for TERM or INT")
	out := fl.String("out", "", "append lines to this file, which must be new or empty and is locked until exit (empty: stdout); FILE.pid holds the pid until exit")
	once := fl.Bool("once", false, "write one line and exit")
	root := fl.String("root", "/", "the dir that holds the kernel's cgroup and proc trees (the tests' fake tree)")
	project := fl.String("project", defaultProject, "compose projects to read; every other one is counted in others, never named")
	procs := fl.Bool("procs", true, "also read the Omes client and worker processes")
	runID := fl.String("run-id", defaultRunID, "Omes run ids to read; every other one is counted in other_procs, never named")
	cli := fl.String("cli", "", "a stand-in for the container CLI (a test seam); empty runs the real one; "+NoCLI+
		" runs none, and each line says list failed, as with none installed")
	report := fl.String("report", "", "summarize this file of lines, and sample nothing")
	gate := fl.String("gate", "", "with -report: a gate result dir; its wave is the window, its items name the rows")
	from := fl.Float64("from", 0, "with -report: the window's first wall second (Unix)")
	to := fl.Float64("to", 0, "with -report: the window's last wall second (Unix)")
	asJSON := fl.Bool("json", false, "with -report: JSON, not text")
	listen := fl.String("listen", "", "also serve the last sample as Prometheus text on GET /metrics at 127.0.0.1:<port> (7710 for the observability stack), no other host; with no -out, no line is written")
	pids := fl.String("pids", "", "also read each process a {pid, start, boot_id} record in this dir names (<name>.pid; a record of another boot or start is not read), and the orchestrator's dev server and worker")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	given := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { given[f.Name] = true })
	bad := func(msg string) int { fmt.Fprintln(stderr, "cellstat: "+msg); return 2 }
	switch {
	case fl.NArg() > 0:
		return bad("no arguments past the flags")
	case *report == "" && (given["gate"] || given["from"] || given["to"] || given["json"]):
		return bad("-gate, -from, -to and -json go with -report")
	case *report != "" && (given["listen"] || given["pids"]):
		return bad("-listen and -pids sample; -report samples nothing")
	case *once && given["listen"]:
		return bad("-once and -listen, not both: -once exits before a scrape")
	case given["listen"] && !loopbackAddr(*listen):
		return bad("-listen must be 127.0.0.1:<port>: the listener has no auth, so no other host")
	case given["gate"] && (given["from"] || given["to"]):
		return bad("-gate or -from and -to, not both")
	case *interval <= 0 || *forDur < 0:
		return bad("-interval must be above 0 and -for at least 0")
	}
	logger := log.New(stderr, "", log.LstdFlags|log.LUTC)
	if *report != "" {
		w := window{gate: *gate}
		if given["from"] {
			w.from = from
		}
		if given["to"] {
			w.to = to
		}
		if err := runReport(*report, w, *asJSON, stdout); err != nil {
			logger.Printf("cellstat: report: %s", bare(err))
			return 1
		}
		return 0
	}
	re, err := regexp.Compile(*project)
	if err != nil {
		return bad("-project is not a regular expression")
	}
	runRe, err := regexp.Compile(*runID)
	if err != nil {
		return bad("-run-id is not a regular expression")
	}
	if _, err := os.Stat(filepath.Join(*root, "proc", "uptime")); err != nil || !isDir(filepath.Join(*root, "sys", "fs", "cgroup")) {
		return bad("-root holds no kernel tree")
	}
	if *cli == "" {
		*cli = "docker"
	}
	if *pids != "" {
		if fi, err := os.Stat(*pids); err == nil && !fi.IsDir() {
			return bad("-pids names a file; name the dir of records")
		}
	}

	w := stdout
	if *listen != "" && *out == "" {
		w = io.Discard // with -listen and no -out, cellstat writes no line: the listener is its output
	}
	if *out != "" {
		if fi, err := os.Stat(*out); err == nil && !fi.Mode().IsRegular() {
			return bad("-out names a dir or a device; name a file")
		}
		f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			logger.Printf("cellstat: -out: %s", bare(err))
			return 1
		}
		defer f.Close()
		// One cellstat a file. The lock belongs to this open file, so the kernel drops it when cellstat exits, however
		// it exits. It comes before the size: a file another cellstat writes is refused as that, with lines or none.
		if err := lockOut(f); errors.Is(err, syscall.EWOULDBLOCK) {
			return bad("another cellstat writes " + filepath.Base(*out) + "; name a new one")
		} else if err != nil {
			logger.Printf("cellstat: -out lock: %s", bare(err))
			return 1
		}
		if fi, err := f.Stat(); err != nil {
			logger.Printf("cellstat: -out: %s", bare(err))
			return 1
		} else if fi.Size() > 0 {
			return bad("-out names a file that is not empty; name a new one")
		}
		pidFile := *out + ".pid"
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			logger.Printf("cellstat: pid file: %s", bare(err))
			return 1
		}
		defer os.Remove(pidFile)
		w = f
	}

	// The listener binds before the first sample, so a port that is taken stops cellstat at once. Until the first line
	// it answers 503; then each scrape gets the bytes metrics.go rendered from the last line, and makes no docker call
	// and reads no file.
	var last atomic.Pointer[[]byte]
	if *listen != "" {
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			logger.Printf("cellstat: -listen: %s", bare(err))
			return 1
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /metrics", func(rw http.ResponseWriter, _ *http.Request) {
			b := last.Load()
			if b == nil {
				http.Error(rw, "no sample yet", http.StatusServiceUnavailable)
				return
			}
			rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			_, _ = rw.Write(*b)
		})
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()
		logger.Printf("cellstat: serving the last sample on %s, GET /metrics", ln.Addr())
		onListen(ln.Addr())
	}
	var l lister = dockerCLI{path: *cli, timeout: callTimeout}
	if *cli == NoCLI {
		l = noLister{}
	}
	if ready != nil {
		ready()
	}
	s := newSampler(*root, l, re, runRe, *procs)
	s.pidDir = *pids
	lines := 0
	write := func() bool {
		l := s.sample()
		b, err := json.Marshal(l)
		if err == nil {
			_, err = w.Write(append(b, '\n')) // one Write a line: a reader sees it whole or not at all
		}
		if err != nil {
			logger.Printf("cellstat: write: %s", bare(err))
			return false
		}
		if *listen != "" {
			m := renderMetrics(l)
			last.Store(&m)
		}
		lines++
		return true
	}
	if !write() {
		return 1
	}
	if *once {
		return 0
	}
	until := "TERM or INT"
	var end <-chan time.Time
	if *forDur > 0 {
		t := time.NewTimer(*forDur)
		defer t.Stop()
		end, until = t.C, forDur.String()+", TERM or INT"
	}
	logger.Printf("cellstat: a line every %s until %s", *interval, until)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
		case <-end:
		case <-tick.C:
			// After a sample longer than -interval a tick is ready, and select takes a ready case at random. A tick
			// must not win over a TERM, INT or -for that came during the sample: TERM costs that sample and one more.
			if ctx.Err() == nil && !fired(end) {
				if !write() {
					return 1
				}
				continue
			}
		}
		if !write() {
			return 1
		}
		logger.Printf("cellstat: stopped after %d lines", lines)
		return 0
	}
}

// lockOut takes -out's lock, or returns EWOULDBLOCK when another cellstat holds it for 100 ms. A run refused for a
// file with lines holds the lock too, from taking it to reading the size, so cellstats started at once on such a file
// see each other's lock for that moment, as one does bin/obs usage stop's check; a recording holds it until it exits.
func lockOut(f *os.File) error {
	for deadline := time.Now().Add(lockWait); ; time.Sleep(2 * time.Millisecond) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return err
		}
	}
}

// loopbackAddr says whether a -listen value is 127.0.0.1:<port>, the port a number from 0 to 65535 (0 takes a free
// one). localhost is refused with every other name: it may resolve to ::1 (inference).
func loopbackAddr(a string) bool {
	host, port, err := net.SplitHostPort(a)
	if err != nil || host != "127.0.0.1" || port == "" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 0 && n <= 65535 && strconv.Itoa(n) == port
}

// onListen is told the listener's address once it is bound: a test seam, a no-op in the binary.
var onListen = func(net.Addr) {}

// fired says whether a timer has fired, without waiting. A nil channel, -for 0, never has.
func fired(c <-chan time.Time) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
