package follow

// The fake cell (_obs/README.md): the store federates a cell, keeps its series under the labels that outlive
// it, reads kill-history's signal as the cell does when a series leaves, and lets a released cell's series go. make
// obs-test runs these two tests with OBS_BIN set to OBS_DIR/bin, where bin/obs up fetched and checked the releases;
// with OBS_BIN unset they are skipped, so make test-go and a fresh clone skip them. Each takes about 6 minutes and
// starts three listeners, each on 127.0.0.1, and nothing else, none while a gate is open: each test asks before it
// starts them, as bin/obs follow -gate-running does, and every 2 s after, and a gate that opens stops all three and fails
// the test:
//
//	127.0.0.1:7769  a canned exposition served by this test, standing for a cell's history role: the families the
//	                six readings read, each grown by a fixed count a scrape, and numshards_gauge at 512;
//	127.0.0.1:7768  Prometheus 2.53.0, the fetched binary, as the cell's own Prometheus: _obs/test/cell.yml scrapes
//	                7769 every 5 s as job temporal-history, as a cell's Prometheus scrapes its history role;
//	127.0.0.1:7730  the store under test: the same binary with _obs/prometheus.yml as bin/obs up writes it (OBS_DIR a
//	                temp directory), the stack's start flags and a TSDB of its own. 7730 is the stack's store's port,
//	                so the stack is down while these run; make obs-test says so before they start.
//
// The fake cell's target is follow's own: a copy of gate/results/g20261017-020417 cut after its leased steady line,
// whose steady/lease.json names the fake cell's Prometheus, and the gate's laugd in a fake proc tree, so follow's poll
// writes OBS_DIR/targets/cell-13880449.json as it does for a gate's cell, and removes it at the release line.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/gateopen"
)

const (
	storeAddr = "127.0.0.1:7730"
	cellAddr  = "127.0.0.1:7768"
	expoAddr  = "127.0.0.1:7769"
	fakeCell  = "13880449" // steady's cell in the copy
	interval  = 5 * time.Second
)

// The bucket edges of server 1.31.0, in seconds: the 20 le strings of each of five histograms on one lab-host cell,
// 120 s into g20261017-060633's wave (cells/results/2026-10-17-arm-pilot.txt; load/steady-state.yaml's
// le comment).
var leEdges = []string{"0.001", "0.002", "0.005", "0.01", "0.02", "0.05", "0.1", "0.2", "0.5", "1", "2", "5", "10",
	"20", "50", "100", "200", "500", "1000", "+Inf"}

// histo is one series set of a histogram family: base is the observations each scrape adds to a bucket, by its upper
// edge, and drift what each scrape adds on top in the second minute of every three (phase 1) and twice that in the
// third, so a 5 m window's quantile depends on which samples are in it.
type histo struct {
	family, labels string
	base, drift    map[string]float64
}

var hists = []histo{
	{"task_schedule_to_start_latency", `operation="TaskQueueMgr",service_name="matching",task_type="Workflow"`,
		map[string]float64{"0.002": 2, "0.005": 20, "0.01": 60, "0.02": 15, "0.05": 2, "0.1": 1}, map[string]float64{"0.02": 6, "0.05": 1}},
	{"task_schedule_to_start_latency", `operation="TaskQueueMgr",service_name="matching",task_type="Activity"`,
		map[string]float64{"0.002": 5, "0.005": 40, "0.01": 150, "0.02": 2}, map[string]float64{"0.02": 1}},
	{"workflow_schedule_to_close_latency", `namespace="default",service_name="history"`,
		map[string]float64{"0.02": 5, "0.05": 90, "0.1": 4, "0.2": 1}, map[string]float64{"0.1": 3}},
	{"persistence_latency", `operation="UpdateWorkflowExecution",service_name="history"`,
		map[string]float64{"0.001": 50, "0.002": 150, "0.005": 200, "0.01": 40, "0.02": 3}, map[string]float64{"0.02": 2, "0.05": 1}},
	{"persistence_latency", `operation="GetWorkflowExecution",service_name="history"`,
		map[string]float64{"0.001": 100, "0.002": 100, "0.005": 20}, nil},
	{"service_latency", `operation="StartWorkflowExecution",service_name="frontend"`,
		map[string]float64{"0.002": 10, "0.005": 40, "0.01": 45, "0.02": 4, "0.05": 1}, map[string]float64{"0.02": 2}},
	{"service_latency", `operation="PollWorkflowTaskQueue",service_name="frontend"`, // a long poll: the reading leaves it out
		map[string]float64{"50": 10, "100": 15}, nil},
	{"service_latency", `operation="RecordWorkflowTaskStarted",service_name="history"`,
		map[string]float64{"0.002": 300, "0.005": 100}, nil},
}

// counter is a counter series that grows by by every every-th scrape.
type counter struct {
	family, labels string
	every          int
	by             float64
}

var counters = []counter{
	{"service_requests", `operation="StartWorkflowExecution",service_name="frontend"`, 1, 100},
	{"service_requests", `operation="PollWorkflowTaskQueue",service_name="frontend"`, 1, 50},
	{"service_requests", `operation="RecordWorkflowTaskStarted",service_name="history"`, 1, 400},
	{"service_error_with_type", `error_type="serviceerror_NotFound",operation="StartWorkflowExecution",service_name="frontend"`, 4, 1},
	{"service_error_with_type", `error_type="serviceerror_Canceled",operation="AddWorkflowTask",service_name="history"`, 1, 30}, // left out by the reading
	{"workflow_success", `namespace="default",service_name="history"`, 1, 100},
}

func phase(k int) float64 { return float64((k / 12) % 3) }

// cannedBody is the exposition at the n-th scrape (from 0): every counter and bucket holds what n+1 scrapes added.
// numshards_gauge is there only while shards is.
func cannedBody(n int, shards bool) []byte {
	var b bytes.Buffer
	typed := map[string]bool{}
	head := func(family, typ string) {
		if !typed[family] {
			typed[family] = true
			fmt.Fprintf(&b, "# HELP %s the fake cell's %s\n# TYPE %s %s\n", family, family, family, typ)
		}
	}
	for _, h := range hists {
		head(h.family, "histogram")
		var cum, sum float64
		for _, le := range leEdges {
			var c float64
			for k := 0; k <= n; k++ {
				c += h.base[le] + h.drift[le]*phase(k)
			}
			cum += c
			if edge, err := strconv.ParseFloat(le, 64); err == nil {
				sum += c * edge
			}
			fmt.Fprintf(&b, "%s_bucket{%s,le=%q} %s\n", h.family, h.labels, le, fmtValue(cum))
		}
		fmt.Fprintf(&b, "%s_sum{%s} %s\n%s_count{%s} %s\n", h.family, h.labels, fmtValue(sum), h.family, h.labels, fmtValue(cum))
	}
	for _, c := range counters {
		head(c.family, "counter")
		fmt.Fprintf(&b, "%s{%s} %s\n", c.family, c.labels, fmtValue(float64(n/c.every+1)*c.by))
	}
	if shards {
		head("numshards_gauge", "gauge")
		b.WriteString("numshards_gauge{service_name=\"history\"} 512\n")
	}
	return b.Bytes()
}

// canned serves cannedBody on GET /metrics and counts the scrapes.
type canned struct {
	mu     sync.Mutex
	n      int
	shards bool
	last   time.Time
}

func (c *canned) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/metrics" {
		http.NotFound(w, r)
		return
	}
	c.mu.Lock()
	n, shards := c.n, c.shards
	c.n++
	c.last = time.Now()
	c.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(cannedBody(n, shards))
}

func (c *canned) setShards(on bool) {
	c.mu.Lock()
	c.shards = on
	c.mu.Unlock()
}

// switchShards waits for the cell's next scrape, then half an interval, and sets numshards_gauge in or out: the cell's
// part of the delay is then 2.5 s, its mean, and the store's part is what it is. It returns when it set it.
func (c *canned) switchShards(t *testing.T, on bool) time.Time {
	t.Helper()
	c.mu.Lock()
	n := c.n
	c.mu.Unlock()
	deadline := time.Now().Add(3 * interval)
	for {
		c.mu.Lock()
		m, last := c.n, c.last
		c.mu.Unlock()
		if m > n {
			time.Sleep(time.Until(last.Add(interval / 2)))
			c.setShards(on)
			return time.Now()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cell's Prometheus scraped 127.0.0.1:7769 not once in %s", 3*interval)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// obsBin is the fetched Prometheus 2.53.0 under OBS_BIN, from a tree bin/obs up checked; the test is skipped when
// OBS_BIN is unset, and fails when it is set and names no such binary.
func obsBin(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("OBS_BIN")
	if dir == "" {
		t.Skip("OBS_BIN unset: make obs-test sets it to OBS_DIR/bin, the releases bin/obs up fetched and checked")
	}
	tree := filepath.Join(dir, "prometheus-2.53.0")
	if _, err := os.Stat(filepath.Join(tree, ".checked")); err != nil {
		t.Fatalf("OBS_BIN=%s holds no checked prometheus-2.53.0 tree; bin/obs up fetches and checks it", dir)
	}
	bins, _ := filepath.Glob(filepath.Join(tree, "*", "prometheus"))
	if len(bins) != 1 {
		t.Fatalf("%s holds %d prometheus binaries, not 1", tree, len(bins))
	}
	out, err := exec.Command(bins[0], "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "version 2.53.0 ") {
		t.Fatalf("%s --version: %v, %q", bins[0], err, firstLine(out))
	}
	return bins[0]
}

func firstLine(b []byte) string { s, _, _ := strings.Cut(string(b), "\n"); return s }

// sock is one socket of /proc/net: tcp in state LISTEN, or udp in any state.
type sock struct {
	proto, ip string
	port      int
	inode     string
}

// procIP decodes /proc/net's hex address: IPv4 in host order, IPv6 as four 32-bit words in host order (x86).
func procIP(h string) string {
	b, err := hex.DecodeString(h)
	if err != nil {
		return h
	}
	for i := 0; i+4 <= len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b).String()
}

// sockets is every TCP listener and UDP socket of this host's network namespace, IPv4 and IPv6.
func sockets(t *testing.T) []sock {
	t.Helper()
	var out []sock
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := os.ReadFile("/proc/net/" + proto)
		if err != nil {
			t.Fatalf("/proc/net/%s: %v", proto, err)
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Scan() // the header
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 10 || (strings.HasPrefix(proto, "tcp") && f[3] != "0A") {
				continue
			}
			ip, port, _ := strings.Cut(f[1], ":")
			p, _ := strconv.ParseInt(port, 16, 32)
			out = append(out, sock{proto, procIP(ip), int(p), f[9]})
		}
	}
	return out
}

// socketInodes is the inodes of pid's sockets.
func socketInodes(pid int) map[string]bool {
	out := map[string]bool{}
	ents, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	for _, e := range ents {
		l, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, e.Name()))
		if in, ok := strings.CutPrefix(l, "socket:["); err == nil && ok {
			out[strings.TrimSuffix(in, "]")] = true
		}
	}
	return out
}

// portsFree fails when something listens on a port the fake cell takes.
func portsFree(t *testing.T) {
	t.Helper()
	for _, s := range sockets(t) {
		if strings.HasPrefix(s.proto, "tcp") && (s.port == 7730 || s.port == 7768 || s.port == 7769) {
			t.Fatalf("%s %s:%d is taken: the fake cell takes 127.0.0.1:7768 and 7769, and its store 7730, the stack's store's "+
				"port; bin/obs down first", s.proto, s.ip, s.port)
		}
	}
}

// gateEvery is how often the fake cell asks whether a gate is open, follow's own poll interval.
const gateEvery = 2 * time.Second

// hostGateOpen is this host's answer to whether a gate is open (docs/DESIGN.md section 9), -gate-running's own:
// gateopen.GateOpen on the real proc tree, the host lock in each place a gate takes it, and laugd's 127.0.0.1:7700.
func hostGateOpen() (why string, open bool) {
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	return gateopen.GateOpen("/", "127.0.0.1:7700", gateopen.LockPaths(xdg, os.Getuid()), xdg)
}

// gateWatch keeps the fake cell off while a gate is open: 7768 and 7769 are never up then, and the store under test on
// 7730 scrapes the stack's static targets, the orchestrator's dev server among them (_obs/prometheus.yml). It holds a
// stop for each of the three listeners.
type gateWatch struct {
	mu     sync.Mutex
	stops  []func()
	opened bool
}

// add gives the watch one more stop, run at once when a gate has opened already.
func (g *gateWatch) add(stop func()) {
	g.mu.Lock()
	opened := g.opened
	if !opened {
		g.stops = append(g.stops, stop)
	}
	g.mu.Unlock()
	if opened {
		stop()
	}
}

// watchGates fails the test at once when a gate is open, and otherwise asks again every gateEvery until the test
// ends. When a gate opens it stops what the watch holds, the newest first, and fails the test naming the gate's lock or
// laugd and when.
func watchGates(t *testing.T) *gateWatch {
	t.Helper()
	if why, open := hostGateOpen(); open {
		t.Fatalf("a gate is open: %s; run make obs-test again once the gate has ended", why)
	}
	g := &gateWatch{}
	done, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		tick := time.NewTicker(gateEvery)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
			}
			why, open := hostGateOpen()
			if !open {
				continue
			}
			seen := time.Now()
			g.mu.Lock()
			g.opened = true
			stops := g.stops
			g.stops = nil
			g.mu.Unlock()
			for i := len(stops) - 1; i >= 0; i-- {
				stops[i]()
			}
			t.Errorf("a gate opened, seen at %s UTC: %s; the fake cell's listeners were closed %s after; run make obs-test "+
				"again once the gate has ended", seen.UTC().Format("15:04:05.0"), why, secs(time.Since(seen)))
			return
		}
	}()
	t.Cleanup(func() { close(done); <-ended })
	return g
}

// auditListeners holds the three listeners to their ports (SECURITY.md): the
// store's one listener is 127.0.0.1:7730, the cell's 127.0.0.1:7768 and this test's 127.0.0.1:7769, each TCP; neither
// Prometheus has another listener or a UDP socket; and nothing else listens on those ports, on any address.
func auditListeners(t *testing.T, store, cell *prom) string {
	t.Helper()
	want := map[int]string{store.cmd.Process.Pid: storeAddr, cell.cmd.Process.Pid: cellAddr, os.Getpid(): expoAddr}
	names := map[int]string{store.cmd.Process.Pid: "the store", cell.cmd.Process.Pid: "the cell's Prometheus", os.Getpid(): "the exposition"}
	owner := map[string]int{}
	for pid := range want {
		for in := range socketInodes(pid) {
			owner[in] = pid
		}
	}
	got := map[int][]string{}
	for _, s := range sockets(t) {
		pid, ours := owner[s.inode]
		addr := net.JoinHostPort(s.ip, strconv.Itoa(s.port))
		switch {
		case ours && strings.HasPrefix(s.proto, "udp") && pid != os.Getpid():
			t.Errorf("%s has a UDP socket, %s", names[pid], addr)
		case ours && strings.HasPrefix(s.proto, "tcp"):
			got[pid] = append(got[pid], s.proto+" "+addr)
		case !ours && strings.HasPrefix(s.proto, "tcp") && (s.port == 7730 || s.port == 7768 || s.port == 7769):
			t.Errorf("%s %s listens beside the fake cell, and is not one of its three", s.proto, addr)
		}
	}
	var lines []string
	for pid, w := range want {
		if l := got[pid]; len(l) != 1 || l[0] != "tcp "+w {
			t.Errorf("%s listens on %v, want tcp %s alone", names[pid], l, w)
		}
		lines = append(lines, names[pid]+" "+strings.Join(got[pid], " "))
	}
	sort.Strings(lines)
	return strings.Join(lines, "; ")
}

// prom is a Prometheus the test started.
type prom struct {
	cmd  *exec.Cmd
	addr string
	log  string
	done chan struct{}
}

// startProm starts the fetched Prometheus on addr with config, its TSDB and log under dir, the stack's start flags
// (_obs/obs.sh, start_part) and an environment of PATH, HOME and GOMAXPROCS=2 only, in a process group of its own,
// killed if this test's process dies; it waits for /-/ready.
func startProm(t *testing.T, bin, dir, config, addr string) *prom {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	logf, err := os.Create(filepath.Join(dir, "prometheus.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--config.file="+config, "--web.listen-address="+addr, "--storage.tsdb.path="+filepath.Join(dir, "tsdb"),
		"--storage.tsdb.retention.time=90d", "--storage.tsdb.retention.size=20GB")
	cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GOMAXPROCS=2"}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &prom{cmd: cmd, addr: addr, log: logf.Name(), done: make(chan struct{})}
	go func() { _ = cmd.Wait(); logf.Close(); close(p.done) }()
	t.Cleanup(func() {
		p.stop()
		if t.Failed() {
			t.Logf("the last lines of %s's log:\n%s", addr, tail(p.log, 8))
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if resp, err := client.Get("http://" + addr + "/-/ready"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		select {
		case <-p.done:
			t.Fatalf("Prometheus on %s ended at its start:\n%s", addr, tail(p.log, 8))
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("Prometheus on %s was not ready in 30 s:\n%s", addr, tail(p.log, 8))
		}
	}
}

// stop sends TERM to the group, and KILL 15 s later.
func (p *prom) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

func tail(path string, n int) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

var client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}

// result is one element of an instant query's vector.
type result struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
}

func (r result) value() float64 {
	s, _ := r.Value[1].(string)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

// query is an instant query on addr at time at (now when zero).
func query(addr, q string, at time.Time) ([]result, error) {
	v := url.Values{"query": {q}}
	if !at.IsZero() {
		v.Set("time", strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64))
	}
	resp, err := client.Get("http://" + addr + "/api/v1/query?" + v.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Status, Error string
		Data          struct {
			ResultType string   `json:"resultType"`
			Result     []result `json:"result"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" || body.Data.ResultType != "vector" {
		return nil, fmt.Errorf("%s: %s %s", q, body.Status, body.Error)
	}
	return body.Data.Result, nil
}

// one is a query that must answer one sample, whose value it gives.
func one(t *testing.T, addr, q string, at time.Time) float64 {
	t.Helper()
	rs, err := query(addr, q, at)
	if err != nil {
		t.Fatalf("%s: %v", addr, err)
	}
	if len(rs) != 1 {
		t.Fatalf("%s on %s: %d samples, want 1: %v", q, addr, len(rs), rs)
	}
	return rs[0].value()
}

// selector matches a metric selector's name and the brace or bracket after it: rate(, sum( and by ( are no selectors.
var selector = regexp.MustCompile(`([a-zA-Z_:][a-zA-Z0-9_:]*)([{\[])`)

// withSelector adds matcher m to every metric selector of q, as a board's store reading does (the
// board's query with one selector added).
func withSelector(q, m string) string {
	return selector.ReplaceAllStringFunc(q, func(s string) string {
		if name, ok := strings.CutSuffix(s, "{"); ok {
			return name + "{" + m + ", "
		}
		return strings.TrimSuffix(s, "[") + "{" + m + "}["
	})
}

// sixReadings is load/steady-state.yaml's thresholds with a max, name and query, in file order.
func sixReadings(t *testing.T) [][2]string {
	t.Helper()
	var out [][2]string
	in := false
	var name, q string
	for _, line := range strings.Split(read(t, "../../load/steady-state.yaml"), "\n") {
		if line == "thresholds:" {
			in = true
			continue
		}
		if in && line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		s := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(s, "- name: "); in && ok {
			name, q = v, ""
		} else if v, ok := strings.CutPrefix(s, "query: "); in && ok {
			q = v
		} else if v, ok := strings.CutPrefix(s, "max: "); in && ok {
			if v, _, _ = strings.Cut(v, "#"); strings.TrimSpace(v) != "null" {
				out = append(out, [2]string{name, q})
			}
		}
	}
	if len(out) != 6 {
		t.Fatalf("load/steady-state.yaml: %d thresholds with a max, want the six", len(out))
	}
	return out
}

// killHistory is faults/catalog.yaml's kill-history signal, the cell's, and the store's with the cell's selector.
func killHistory(t *testing.T) (cellQ, storeQ string) {
	t.Helper()
	const q = `(512 - sum(numshards_gauge{job="temporal-history"})) or vector(512)`
	if !strings.Contains(read(t, "../../faults/catalog.yaml"), "signal_query: "+q+"\n") {
		t.Fatalf("faults/catalog.yaml no longer holds kill-history's signal_query %s", q)
	}
	return q, withSelector(q, `cell="`+fakeCell+`"`)
}

// world is one fake cell, its store and follow.
type world struct {
	expo        *canned
	cell, store *prom
	obs, gate   string
	leased      time.Time
	tTarget     time.Time // follow wrote the target file
	tSeries     time.Time // the store first answered with the fake cell's numshards_gauge
}

// storeConfig is _obs/prometheus.yml as bin/obs up writes it for OBS_DIR obs; with track false, the negative control,
// its one line track_timestamps_staleness: true says false, and every other line is the stack's.
func storeConfig(t *testing.T, obs string, track bool) string {
	t.Helper()
	src := read(t, "../prometheus.yml")
	if strings.Count(src, "track_timestamps_staleness: true") != 1 {
		t.Fatal("_obs/prometheus.yml does not set track_timestamps_staleness: true once")
	}
	conf := strings.ReplaceAll(src, "@OBS_DIR@", obs)
	if !track {
		ctl := strings.Replace(conf, "track_timestamps_staleness: true", "track_timestamps_staleness: false", 1)
		a, b := strings.Split(conf, "\n"), strings.Split(ctl, "\n")
		diff := 0
		for i := range a {
			if a[i] != b[i] {
				diff++
			}
		}
		if len(a) != len(b) || diff != 1 {
			t.Fatalf("the negative control's config differs from the stack's in %d lines, want 1", diff)
		}
		conf = ctl
	}
	path := filepath.Join(obs, "conf", "prometheus.yml")
	write(t, path, conf)
	return path
}

// fakeGateCopy is the copy of g20261017-020417 under results, cut after its leased steady line, with pl's lease gone and
// steady's naming the fake cell's Prometheus, leased now for 30 minutes.
func fakeGateCopy(t *testing.T, results string, now time.Time) string {
	t.Helper()
	dir := filepath.Join(results, gateID)
	copyTree(t, filepath.Join("testdata", gateID), dir)
	var tl bytes.Buffer
	for _, l := range wholeLines([]byte(read(t, filepath.Join(dir, "timeline.jsonl")))) {
		tl.Write(append(l, '\n'))
		if bytes.Contains(l, []byte(`"step":"leased steady"`)) {
			break
		}
	}
	write(t, filepath.Join(dir, "timeline.jsonl"), tl.String())
	if err := os.Remove(filepath.Join(dir, "pl", "lease.json")); err != nil {
		t.Fatal(err)
	}
	var lease map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "steady", "lease.json"))), &lease); err != nil {
		t.Fatal(err)
	}
	lease["prometheus"] = "http://" + cellAddr
	lease["leased_at"] = now.UTC().Format(time.RFC3339Nano)
	lease["expires_at"] = now.Add(30 * time.Minute).UTC().Format(time.RFC3339Nano)
	b, _ := json.MarshalIndent(lease, "", "  ")
	write(t, filepath.Join(dir, "steady", "lease.json"), string(b)+"\n")
	write(t, filepath.Join(dir, "laugd.log"), "")
	return dir
}

// appendLine adds a timeline line by a rename, as a whole file, so follow never reads it half written.
func appendLine(t *testing.T, dir, step string, at time.Time) {
	t.Helper()
	path := filepath.Join(dir, "timeline.jsonl")
	s := read(t, path) + fmt.Sprintf(`{"step":%q,"wall":%.3f,"mono":0}`, step, float64(at.UnixMilli())/1000) + "\n"
	write(t, path+".new", s)
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

// setUp fails when a gate is open, and keeps asking every 2 s while the test runs (watchGates); starts the exposition,
// the cell's Prometheus, the store (the stack's config, or the negative control's) and follow's poll every 2 s; checks
// the three listeners; and waits for the store to hold the fake cell's numshards_gauge.
func setUp(t *testing.T, track bool) *world {
	t.Helper()
	bin := obsBin(t)
	portsFree(t)
	gw := watchGates(t)
	tmp := t.TempDir()
	w := &world{expo: &canned{shards: true}, obs: filepath.Join(tmp, "obs")}

	ln, err := net.Listen("tcp", expoAddr)
	if err != nil {
		t.Fatalf("the exposition: %v", err)
	}
	srv := &http.Server{Handler: w.expo, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	gw.add(func() { srv.Close() })

	cellConf := filepath.Join(tmp, "cell.yml")
	write(t, cellConf, read(t, "../test/cell.yml"))
	w.cell = startProm(t, bin, filepath.Join(tmp, "cell"), cellConf, cellAddr)
	gw.add(w.cell.stop)
	// OBS_DIR/targets before the store, as bin/obs up makes it: file_sd watches the directory, and with one made after
	// the store started, the store had no target 30 s after follow wrote the file (seen on the dev host,
	// cells/results/2026-10-17-obs-offline.txt section 4e); it reads the file at its refresh_interval then, 5 m by
	// default (Prometheus 2.53.0 file_sd docs).
	if err := os.MkdirAll(filepath.Join(w.obs, "targets"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.store = startProm(t, bin, filepath.Join(w.obs, "prometheus"), storeConfig(t, w.obs, track), storeAddr)
	gw.add(w.store.stop)
	t.Logf("listeners: %s", auditListeners(t, w.store, w.cell))

	w.leased = time.Now()
	w.gate = fakeGateCopy(t, filepath.Join(tmp, "results"), w.leased)
	root := fakeRoot(t)
	addLaugd(t, root, 4321, "127.0.0.1:7700", filepath.Join(w.gate, "laugd.log"))
	f, err := newFollower(w.obs, root, "127.0.0.1:7700", nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			f.poll()
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-stopped })

	target := filepath.Join(w.obs, "targets", "cell-"+fakeCell+".json")
	w.tTarget = waitFor(t, 10*time.Second, "follow's target file", func() bool { _, err := os.Stat(target); return err == nil })
	t.Logf("follow wrote targets/cell-%s.json %.1f s after the lease:\n%s", fakeCell, w.tTarget.Sub(w.leased).Seconds(), read(t, target))
	deadline := time.Now().Add(30 * time.Second)
	for {
		if rs, err := query(storeAddr, `numshards_gauge{cell="`+fakeCell+`"}`, time.Time{}); err == nil && len(rs) == 1 {
			w.tSeries = time.Now()
			return w
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake cell's numshards_gauge was not in the store 30 s after the target file; the cell's target: %s; "+
				"the store's: %s", targetHealth(cellAddr, "temporal-history"), targetHealth(storeAddr, "laug-cells"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// targetHealth is what a Prometheus says of its targets of job: health, last error and the last scrape's samples.
func targetHealth(addr, job string) string {
	resp, err := client.Get("http://" + addr + "/api/v1/targets?state=active")
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	var v struct {
		Data struct {
			ActiveTargets []struct {
				ScrapePool string `json:"scrapePool"`
				ScrapeURL  string `json:"scrapeUrl"`
				Health     string `json:"health"`
				LastError  string `json:"lastError"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return err.Error()
	}
	var out []string
	for _, a := range v.Data.ActiveTargets {
		if a.ScrapePool == job {
			out = append(out, fmt.Sprintf("%s %s %q", a.ScrapeURL, a.Health, a.LastError))
		}
	}
	if len(out) == 0 {
		return "none of job " + job
	}
	return strings.Join(out, ", ")
}

// waitFor polls cond every 100 ms for up to d, and gives when it first held.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) time.Time {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s", what, d)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return time.Now()
}

// labelSet is a series' labels as PromQL writes them, the name first, the rest by name.
func labelSet(m map[string]string) string {
	var ls []string
	for k, v := range m {
		if k != "__name__" {
			ls = append(ls, k+"="+strconv.Quote(v))
		}
	}
	sort.Strings(ls)
	return m["__name__"] + "{" + strings.Join(ls, ", ") + "}"
}

func secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s" }

// TestFakeCellInTheStore: with the stack's config the store federates the fake cell under the labels follow's target
// gives it, cell, life, block, backend, server_tag, gate_id and item, the cell's job and instance kept; reads the six
// readings with the cell's selector over a 5 m window as the cell reads them, to 3 significant digits; reads
// kill-history's 512 missing within 10 s of numshards_gauge leaving the exposition and at every read for 4 minutes
// after, and 0 again within one 5 s pass of the cell when it is back; and lets the cell's series go stale once follow
// removes the target at the release line.
func TestFakeCellInTheStore(t *testing.T) {
	w := setUp(t, true)
	cellQ, storeQ := killHistory(t)

	// The labels.
	rs, err := query(storeAddr, `numshards_gauge{cell="`+fakeCell+`"}`, time.Time{})
	if err != nil || len(rs) != 1 {
		t.Fatalf("numshards_gauge in the store: %v %v", rs, err)
	}
	wantLabels := map[string]string{"__name__": "numshards_gauge", "cell": fakeCell, "life": strconv.FormatInt(w.leased.Unix(), 10),
		"block": "0", "backend": "compose", "server_tag": "1.31.0", "gate_id": gateID, "item": "steady",
		"job": "temporal-history", "instance": expoAddr, "service_name": "history"}
	if fmt.Sprint(rs[0].Metric) != fmt.Sprint(wantLabels) {
		t.Errorf("the store's numshards_gauge: %v, want %v", rs[0].Metric, wantLabels)
	}
	t.Logf("the store held %s 512, %s after follow wrote the target", labelSet(rs[0].Metric), secs(w.tSeries.Sub(w.tTarget)))
	all := one(t, storeAddr, `count({job="temporal-history"})`, time.Time{})
	labelled := one(t, storeAddr, `count({job="temporal-history", cell="`+fakeCell+`", gate_id="`+gateID+`", item="steady"})`, time.Time{})
	if all != labelled {
		t.Errorf("%v series of job temporal-history in the store, %v of them with cell, gate_id and item", all, labelled)
	}
	fams, _ := query(storeAddr, `count by (__name__) ({cell="`+fakeCell+`"})`, time.Time{})
	var names []string
	for _, r := range fams {
		names = append(names, r.Metric["__name__"])
	}
	sort.Strings(names)
	t.Logf("%v series of job temporal-history in the store, each with cell, gate_id and item; the names with the cell's "+
		"label, the store's own scrape_* and up of its target among them: %s", all, strings.Join(names, " "))
	if v := one(t, storeAddr, `up{job="laug-cells", cell="`+fakeCell+`"}`, time.Time{}); v != 1 {
		t.Errorf("up of the fake cell's target: %v", v)
	}

	// Staleness: numshards_gauge leaves the exposition, as when history dies.
	if c, s := one(t, cellAddr, cellQ, time.Time{}), one(t, storeAddr, storeQ, time.Time{}); c != 0 || s != 0 {
		t.Fatalf("before: kill-history reads %v on the cell and %v in the store, want 0 and 0", c, s)
	}
	tOut := w.expo.switchShards(t, false)
	var dCell, dStore time.Duration = -1, -1
	reads := 0
	for {
		now := time.Now()
		d := now.Sub(tOut)
		if d > 4*time.Minute {
			break
		}
		c, s := one(t, cellAddr, cellQ, now), one(t, storeAddr, storeQ, now)
		if dCell < 0 && c == 512 {
			dCell = d
		}
		if dStore < 0 && s == 512 {
			dStore = d
		}
		if dStore >= 0 {
			reads++
			if s != 512 {
				t.Fatalf("the store read %v missing at +%s, after it read 512 at +%s", s, secs(d), secs(dStore))
			}
		}
		if dStore < 0 && d > 15*time.Second {
			t.Fatalf("the store still read %v missing 15 s after numshards_gauge left the exposition (the cell: %v)", s, c)
		}
		if dStore < 0 {
			time.Sleep(250 * time.Millisecond)
		} else {
			time.Sleep(time.Second)
		}
	}
	if dCell < 0 || dStore > 10*time.Second {
		t.Errorf("kill-history read 512 missing on the cell at +%s and in the store at +%s; the store's must be within 10 s", secs(dCell), secs(dStore))
	}
	t.Logf("staleness: numshards_gauge left the exposition at %s UTC; kill-history read 512 missing on the cell at +%s, "+
		"in the store at +%s, %s after the cell; the store read 512 at each of its %d reads to +%s", tOut.UTC().Format("15:04:05.0"),
		secs(dCell), secs(dStore), secs(dStore-dCell), reads, secs(time.Since(tOut)))
	tIn := w.expo.switchShards(t, true)
	var bCell, bStore time.Duration = -1, -1
	for bStore < 0 {
		now := time.Now()
		d := now.Sub(tIn)
		if d > 20*time.Second {
			t.Fatalf("20 s after numshards_gauge came back kill-history read 0 on the cell at +%s and not in the store", secs(bCell))
		}
		if bCell < 0 && one(t, cellAddr, cellQ, now) == 0 {
			bCell = d
		}
		if bStore < 0 && one(t, storeAddr, storeQ, now) == 0 {
			bStore = d
		}
		time.Sleep(250 * time.Millisecond)
	}
	if bCell < 0 || bStore-bCell > 6*time.Second {
		t.Errorf("back: the cell read 0 at +%s, the store at +%s; want the store within one 5 s pass of the cell", secs(bCell), secs(bStore))
	}
	t.Logf("numshards_gauge back at %s UTC: kill-history read 0 on the cell at +%s, in the store at +%s, %s after the cell",
		tIn.UTC().Format("15:04:05.0"), secs(bCell), secs(bStore), secs(bStore-bCell))

	// The six over a 5 m window that begins 10 s after the store's first sample of the cell.
	at := w.tSeries.Truncate(time.Second).Add(310 * time.Second)
	time.Sleep(time.Until(at.Add(8 * time.Second)))
	n := 0
	for _, r := range sixReadings(t) {
		cq := strings.ReplaceAll(r[1], "[1m]", "[300s]")
		sq := withSelector(cq, `cell="`+fakeCell+`"`)
		c, s := one(t, cellAddr, cq, at), one(t, storeAddr, sq, at)
		same := "bit for bit"
		if c != s {
			same = "to 3 significant digits"
		}
		if math.IsNaN(c) || strconv.FormatFloat(c, 'g', 3, 64) != strconv.FormatFloat(s, 'g', 3, 64) {
			t.Errorf("%s: the cell %v, the store %v (%s)", r[0], c, s, sq)
			continue
		}
		n++
		t.Logf("%-40s the cell %-22s the store %-22s equal %s", r[0], fmtValue(c), fmtValue(s), same)
	}
	cs := one(t, cellAddr, `count_over_time(workflow_success[300s])`, at)
	ss := one(t, storeAddr, `count_over_time(workflow_success{cell="`+fakeCell+`"}[300s])`, at)
	t.Logf("%d of 6 equal, over (%s, %s] UTC, %v samples a series in the cell and %v in the store", n,
		at.Add(-300*time.Second).UTC().Format("15:04:05"), at.UTC().Format("15:04:05"), cs, ss)

	// The release line: follow removes the target, and the store lets the cell's series go.
	tRel := time.Now()
	appendLine(t, w.gate, "release", tRel)
	target := filepath.Join(w.obs, "targets", "cell-"+fakeCell+".json")
	tGone := waitFor(t, 5*time.Second, "follow removing the target", func() bool { _, err := os.Stat(target); return os.IsNotExist(err) })
	tStale := waitFor(t, 60*time.Second, "the store letting the cell's series go", func() bool {
		rs, err := query(storeAddr, `count({cell="`+fakeCell+`"})`, time.Time{})
		return err == nil && len(rs) == 0
	})
	t.Logf("released: follow removed the target %s after the release line; the store held no series of the cell %s after that", secs(tGone.Sub(tRel)), secs(tStale.Sub(tGone)))
	if v := one(t, cellAddr, `count({job="temporal-history"})`, time.Time{}); v == 0 {
		t.Errorf("the cell itself lost its series")
	}
}

// TestFakeCellStalenessControl: the negative control. The store's config is the stack's with track_timestamps_staleness
// false, one line apart; when numshards_gauge leaves the exposition the cell reads 512 missing, and the store keeps the
// last sample and reads 0, recovered, at every read for at least 4 minutes, until the 5 m lookback lets it go.
func TestFakeCellStalenessControl(t *testing.T) {
	w := setUp(t, false)
	cellQ, storeQ := killHistory(t)
	if c, s := one(t, cellAddr, cellQ, time.Time{}), one(t, storeAddr, storeQ, time.Time{}); c != 0 || s != 0 {
		t.Fatalf("before: kill-history reads %v on the cell and %v in the store, want 0 and 0", c, s)
	}
	tOut := w.expo.switchShards(t, false)
	var dCell, dFlip, lastZero time.Duration = -1, -1, -1
	reads := 0
	for dFlip < 0 {
		now := time.Now()
		d := now.Sub(tOut)
		if d > 330*time.Second {
			t.Fatalf("the store still read 0 at +%s", secs(d))
		}
		c, s := one(t, cellAddr, cellQ, now), one(t, storeAddr, storeQ, now)
		if dCell < 0 && c == 512 {
			dCell = d
		}
		if dCell >= 0 && c != 512 {
			t.Fatalf("the cell read %v missing at +%s, after 512 at +%s", c, secs(d), secs(dCell))
		}
		switch {
		case s == 0:
			reads++
			lastZero = d
		case s == 512 && d > 4*time.Minute:
			dFlip = d
		default:
			t.Fatalf("the store read %v missing at +%s, inside 4 minutes", s, secs(d))
		}
		time.Sleep(time.Second)
	}
	if dCell < 0 || dCell > 10*time.Second {
		t.Errorf("the cell read 512 missing at +%s, want within 10 s", secs(dCell))
	}
	t.Logf("negative control: numshards_gauge left the exposition at %s UTC; kill-history read 512 missing on the cell at "+
		"+%s; the store read 0, recovered, at each of its %d reads to +%s, and first read 512 at +%s, when the 5 m lookback "+
		"let the last sample go", tOut.UTC().Format("15:04:05.0"), secs(dCell), reads, secs(lastZero), secs(dFlip))
}
