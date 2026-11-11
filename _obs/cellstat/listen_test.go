package cellstat

// The listener and -pids (SECURITY.md, the listener table): -listen takes 127.0.0.1
// alone; GET /metrics renders the last sample, and a scrape makes no docker call and reads no file; an absent reading
// is an absent series; the family names are fixed by testdata/metrics.txt; -pids reads a record tree's processes and
// the orchestrator's two roles, RSS and io included, and skips a record of another boot or another start.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestListenRefusesOtherHosts: -listen 0.0.0.0:7710 exits 2, as does every address but 127.0.0.1:<port>, before
// anything is bound or sampled; -listen goes with neither -once nor -report.
func TestListenRefusesOtherHosts(t *testing.T) {
	root := devTree(t)
	cli, dir := fakeDocker(t)
	for _, a := range []string{"0.0.0.0:7710", "localhost:7710", ":7710", "[::1]:7710", "[::]:7710", "127.0.0.2:7710",
		"127.0.0.1", "127.0.0.1:", "127.0.0.1:x", "127.0.0.1:65536", "127.0.0.1:07710", "10.254.0.2:7710"} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-listen", a, "-root", root, "-cli", cli.path, "-for", "1ms"}, &stdout, &stderr)
		if code != 2 || stdout.Len() > 0 || !strings.Contains(stderr.String(), "-listen must be 127.0.0.1:<port>") {
			t.Errorf("-listen %s: exit %d, %d bytes out, stderr %q; want 2 and the refusal", a, code, stdout.Len(), stderr.String())
		}
	}
	for name, args := range map[string][]string{"-once": {"-once", "-listen", "127.0.0.1:0"}, "-report": {"-report", "x", "-listen", "127.0.0.1:0"},
		"-report -pids": {"-report", "x", "-pids", t.TempDir()}} {
		if code := run(context.Background(), append(args, "-root", root, "-cli", cli.path), &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Errorf("%s with -listen or -pids: exit %d, want 2", name, code)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "calls.log")); len(b) > 0 {
		t.Errorf("a refused run called the container CLI: %q", b)
	}
}

// syncBuf is a stderr the test reads while run writes it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// listening runs cellstat with args and -listen 127.0.0.1:0, and returns its address once bound, a stop that cancels
// it and returns its exit code, and its stdout and stderr.
func listening(t *testing.T, args ...string) (addr string, stop func() int, stdout *syncBuf, stderr *syncBuf) {
	t.Helper()
	got := make(chan net.Addr, 1)
	onListen = func(a net.Addr) { got <- a }
	t.Cleanup(func() { onListen = func(net.Addr) {} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	stdout, stderr = &syncBuf{}, &syncBuf{}
	go func() { done <- run(ctx, append(args, "-listen", "127.0.0.1:0"), stdout, stderr) }()
	select {
	case a := <-got:
		addr = a.String()
	case code := <-done:
		cancel()
		t.Fatalf("cellstat exited %d before it listened; stderr %s", code, stderr.String())
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("no listener in 5 s; stderr %s", stderr.String())
	}
	var once sync.Once
	code := 0
	stop = func() int { once.Do(func() { cancel(); code = <-done }); return code }
	t.Cleanup(func() { stop() })
	return addr, stop, stdout, stderr
}

// scrape GETs /metrics until it answers 200, for 5 s at most.
func scrape(t *testing.T, addr string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if ct := resp.Header.Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
					t.Errorf("Content-Type %q", ct)
				}
				return string(b)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no 200 from /metrics in 5 s: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func listInspect(t *testing.T, dir string, cs ...fc) {
	t.Helper()
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.id)
	}
	write(t, filepath.Join(dir, "ps.out"), strings.Join(ids, "\n")+"\n")
	write(t, filepath.Join(dir, "inspect.out"), inspectJSON(t, cs...))
}

// TestListenServesLastSample: with -listen and no -out cellstat writes no line, and /metrics is the last sample. With
// -interval 1h only the first sample is taken: a cpu.stat changed on disk and a container CLI that would now list
// nothing change no byte of the body, and two scrapes make no call. With -interval 100ms the body follows the file.
func TestListenServesLastSample(t *testing.T) {
	root := devTree(t)
	cli, dir := fakeDocker(t)
	a := laug(idA, "temporal-matching", 101)
	listInspect(t, dir, a)
	addr, stop, stdout, _ := listening(t, "-root", root, "-cli", cli.path, "-interval", "1h", "-procs=false")
	first := scrape(t, addr)
	want := `laug_cell_cpu_seconds_total{project="laug-1dafb33b",service="temporal-matching"} `
	if !strings.Contains(first, want) {
		t.Fatalf("the body lacks container A's CPU: %s", first)
	}
	calls := lineCount(filepath.Join(dir, "calls.log"))
	cpu := filepath.Join(cgroupDir(root, "docker/"+idA), "cpu.stat")
	b, _ := os.ReadFile(cpu)
	write(t, cpu, strings.Replace(string(b), "usage_usec ", "usage_usec 9", 1))
	write(t, filepath.Join(dir, "ps.out"), "")
	for i := 0; i < 2; i++ {
		if got := scrape(t, addr); got != first {
			t.Errorf("scrape %d differs from the first sample's body", i+2)
		}
	}
	if n := lineCount(filepath.Join(dir, "calls.log")); n != calls {
		t.Errorf("the scrapes made %d container CLI calls, want 0", n-calls)
	}
	if code := stop(); code != 0 {
		t.Errorf("stopped: exit %d, want 0", code)
	}
	if stdout.String() != "" {
		t.Errorf("-listen with no -out wrote %d bytes to stdout", len(stdout.String()))
	}

	root = devTree(t)
	listInspect(t, dir, a)
	addr, _, _, _ = listening(t, "-root", root, "-cli", cli.path, "-interval", "100ms", "-procs=false")
	m := regexp.MustCompile(regexp.QuoteMeta(want) + `(\S+)`).FindStringSubmatch(scrape(t, addr))
	if m == nil {
		t.Fatal("no CPU sample for container A")
	}
	before, _ := strconv.ParseFloat(m[1], 64)
	cpu = filepath.Join(cgroupDir(root, "docker/"+idA), "cpu.stat")
	b, _ = os.ReadFile(cpu)
	usage := regexp.MustCompile(`usage_usec (\d+)`).FindStringSubmatch(string(b))[1]
	n, _ := strconv.ParseUint(usage, 10, 64)
	write(t, cpu, strings.Replace(string(b), "usage_usec "+usage, "usage_usec "+strconv.FormatUint(n+2500000, 10), 1))
	// A sample taken while the write above has cpu.stat truncated has no CPU line for A, so a body may not match yet.
	cpuRe := regexp.MustCompile(regexp.QuoteMeta(want) + `(\S+)`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		body := scrape(t, addr)
		last := "no match in:\n" + body
		if m := cpuRe.FindStringSubmatch(body); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v == before+2.5 {
				break
			}
			last = m[1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("the body did not follow cpu.stat in 5 s: still %s, want %v", last, before+2.5)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// parseExposition reads text format 0.0.4 into series → value, and checks each sample's family has HELP and TYPE.
func parseExposition(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	typed := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if f, ok := strings.CutPrefix(line, "# TYPE "); ok {
			p := strings.Fields(f)
			typed[p[0]] = p[1]
			continue
		}
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		series, v := line[:i], line[i+1:]
		name := series
		if j := strings.IndexByte(series, '{'); j >= 0 {
			name = series[:j]
		}
		typ := typed[name]
		switch {
		case typ == "":
			t.Errorf("%s has no TYPE line", name)
		case typ == "counter" && !strings.HasSuffix(name, "_total"), typ != "counter" && strings.HasSuffix(name, "_total"):
			t.Errorf("%s is a %s: _total goes on counters and only on them", name, typ)
		}
		if _, dup := out[series]; dup {
			t.Errorf("%s twice", series)
		}
		out[series] = v
	}
	return out
}

// TestMetricsNames: one sample of the dev tree, its four containers and two Omes processes, renders as
// testdata/metrics.txt byte for byte; every family has HELP and TYPE, and _total is on counters alone.
// LAUG_UPDATE_GOLDEN=1 writes the file anew.
func TestMetricsNames(t *testing.T) {
	root := devTree(t)
	l := &fakeLister{cs: []fc{laug(idA, "temporal-matching", 101), laug(idB, "temporal-history", 202), laug(idC, "temporal-ui", 303),
		laug(idD, "postgresql", 404), {strings.Repeat("f", 64), "not-laug", "web", 101, true}}}
	got := renderMetrics(testSampler(root, l, true).sample())
	if os.Getenv("LAUG_UPDATE_GOLDEN") == "1" {
		write(t, "testdata/metrics.txt", string(got))
	}
	want, err := os.ReadFile("testdata/metrics.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the body is not testdata/metrics.txt:\n%s", got)
	}
	parseExposition(t, string(got))
	for _, name := range []string{"laug_cell_cpu_seconds_total", "laug_cell_cpu_some_seconds_total", "laug_cell_io_writes_total",
		"laug_host_vcpu", "laug_host_cpu_seconds_total", "laug_proc_wait_seconds_total", "laug_proc_cpu_seconds_total"} {
		if !bytes.Contains(got, []byte("# TYPE "+name+" ")) {
			t.Errorf("no family %s, a name the plan's boards read", name)
		}
	}
}

// TestAbsentReadingAbsentSeries: container C has no controllers, so its line has cpu_us, user and system and the
// pressure totals, and its series are those and no others; a host reading that was not read and an Omes process
// with no thread read have no sample; a gone or unresolved container has none at all.
func TestAbsentReadingAbsentSeries(t *testing.T) {
	root := devTree(t)
	for _, f := range []string{"stat", "schedstat"} {
		if err := os.Remove(filepath.Join(root, "proc", f)); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []string{"4242", "4243"} {
		if err := os.Remove(filepath.Join(root, "proc/4242/task", task, "schedstat")); err != nil {
			t.Fatal(err)
		}
	}
	l := &fakeLister{cs: []fc{laug(idC, "temporal-ui", 303), {idD, "laug-1dafb33b", "postgresql", 0, false}}}
	line := testSampler(root, l, true).sample()
	series := parseExposition(t, string(renderMetrics(line)))
	var c map[string]json.RawMessage
	for _, x := range line.Containers {
		if x.Service == "temporal-ui" {
			b, _ := json.Marshal(x)
			json.Unmarshal(b, &c)
		}
	}
	keyOf := map[string]string{"cpu_us": "laug_cell_cpu_seconds_total", "cpu_user_us": "laug_cell_cpu_user_seconds_total",
		"cpu_system_us": "laug_cell_cpu_system_seconds_total", "cpu_some_us": "laug_cell_cpu_some_seconds_total",
		"cpu_full_us": "laug_cell_cpu_full_seconds_total", "io_some_us": "laug_cell_io_some_seconds_total",
		"io_full_us": "laug_cell_io_full_seconds_total", "mem_some_us": "laug_cell_memory_some_seconds_total",
		"mem_full_us": "laug_cell_memory_full_seconds_total"}
	var cSeries []string
	for s := range series {
		if strings.Contains(s, `service="temporal-ui"`) {
			cSeries = append(cSeries, s[:strings.IndexByte(s, '{')])
		}
		if strings.Contains(s, `service="postgresql"`) {
			t.Errorf("a container that is not ok has a series: %s", s)
		}
	}
	if len(cSeries) != len(c)-len([]string{"project", "service", "id", "via", "state", "missing"}) {
		t.Errorf("container C: %d series %v for the line's readings %v", len(cSeries), cSeries, c)
	}
	for _, s := range cSeries {
		found := false
		for k, name := range keyOf {
			if name == s {
				if _, ok := c[k]; !ok {
					t.Errorf("%s has a sample, and the line has no %s", s, k)
				}
				found = true
			}
		}
		if !found {
			t.Errorf("container C has %s, which its line holds no reading for", s)
		}
	}
	for s := range series {
		if strings.HasPrefix(s, "laug_host_cpu_seconds_total") || strings.HasPrefix(s, "laug_host_vcpu") || strings.HasPrefix(s, "laug_host_runq") {
			t.Errorf("no stat or schedstat was read, and %s has a sample", s)
		}
		if strings.Contains(s, `pid="4242"`) && !strings.HasPrefix(s, "laug_proc_cpu_seconds_total") {
			t.Errorf("the worker's threads were not read, and %s has a sample", s)
		}
	}
	if _, ok := series[`laug_proc_cpu_seconds_total{role="omes-worker",run_id="`+runID+`",pid="4242"}`]; !ok {
		t.Errorf("the worker's cpu_ticks were read, and it has no laug_proc_cpu_seconds_total")
	}
}

// pidsTree is a fake proc tree for -pids: this boot's id; prometheus 7001 and its record; jaeger 7002, recorded on
// another boot; grafana 7003, whose record names another start; the collector's record names a pid that is gone;
// The orchestrator's dev server 7101 on 7400 and its worker 7102; a dev server on 7500 (7103) and a bash (7104) that are neither.
func pidsTree(t *testing.T) (root, recs string) {
	t.Helper()
	root, recs = t.TempDir(), t.TempDir()
	const boot = "5b0a6d0e-2b6e-4c55-9d3e-0123456789ab"
	write(t, filepath.Join(root, "proc/sys/kernel/random/boot_id"), boot+"\n")
	write(t, filepath.Join(root, "proc/uptime"), "21037.95 400000.00\n")
	stat := func(pid, name string, utime, stime, start int) string {
		// 52 fields, as testdata/dev/proc/4241/stat: pid, (name), state, then fields 4 on; utime 14, stime 15, start 22.
		f := strings.Fields("S 1 1 1 0 -1 4194304 102 0 0 0 0 0 0 0 20 0 4 0 0 3354624 384 18446744073709551615 1 1 1 0 0 0 0 0 0 0 0 0 17 10 0 0 0 0 0 1 1 1 1 1 1 1 0")
		f[11], f[12], f[19] = strconv.Itoa(utime), strconv.Itoa(stime), strconv.Itoa(start)
		return pid + " (" + name + ") " + strings.Join(f, " ") + "\n"
	}
	proc := func(pid, name string, argv []string, utime, stime, start int, rssKB, hwmKB, rb, wb int) {
		d := filepath.Join(root, "proc", pid)
		write(t, filepath.Join(d, "stat"), stat(pid, name, utime, stime, start))
		write(t, filepath.Join(d, "cmdline"), strings.Join(argv, "\x00")+"\x00")
		write(t, filepath.Join(d, "status"), "Name:\t"+name+"\nVmPeak:\t  999999 kB\nVmHWM:\t  "+strconv.Itoa(hwmKB)+" kB\nVmRSS:\t  "+strconv.Itoa(rssKB)+" kB\nThreads:\t2\n")
		write(t, filepath.Join(d, "io"), "rchar: 5\nwchar: 6\nsyscr: 1\nsyscw: 1\nread_bytes: "+strconv.Itoa(rb)+"\nwrite_bytes: "+strconv.Itoa(wb)+"\ncancelled_write_bytes: 0\n")
		write(t, filepath.Join(d, "task", pid, "schedstat"), "1000000 2000000 30\n")
	}
	proc("7001", "prometheus", []string{"/obs/bin/prometheus-2.53.0/prometheus", "--config.file=x"}, 120, 30, 500, 78336, 80000, 4096, 1048576)
	proc("7002", "jaeger", []string{"/obs/bin/jaeger/jaeger"}, 10, 10, 501, 1, 1, 1, 1)
	proc("7003", "grafana", []string{"/obs/bin/grafana/grafana", "server"}, 10, 10, 502, 1, 1, 1, 1)
	proc("7101", "temporal", []string{"temporal", "server", "start-dev", "--ip", "127.0.0.1", "--port", "7400", "--http-port", "7401"}, 50, 11, 600, 180000, 190000, 0, 65536)
	proc("7102", "laug", []string{"bin/laug", "orchestrator", "worker", "-otlp", "127.0.0.1:7751"}, 3, 1, 601, 30000, 31000, 0, 0)
	proc("7103", "temporal", []string{"temporal", "server", "start-dev", "--port", "7500"}, 1, 1, 602, 1, 1, 1, 1)
	proc("7104", "bash", []string{"bash", "worker"}, 1, 1, 603, 1, 1, 1, 1)
	proc("7108", "laug", []string{"/opt/x/bin/laug", "orchestrator", "probe"}, 1, 1, 604, 1, 1, 1, 1)
	proc("7109", "laug", []string{"bin/laug", "gate", "run", "1.31.0", "-workflow"}, 1, 1, 605, 1, 1, 1, 1)
	proc("7110", "laugd", []string{"pool/laugd", "orchestrator", "worker"}, 1, 1, 606, 1, 1, 1, 1)
	rec := func(name string, pid, start int, boot string) {
		write(t, filepath.Join(recs, name+".pid"), `{"pid":`+strconv.Itoa(pid)+`,"start":`+strconv.Itoa(start)+`,"boot_id":"`+boot+`"}`+"\n")
	}
	rec("prometheus", 7001, 500, boot)
	rec("jaeger", 7002, 501, "00000000-0000-0000-0000-000000000000")
	rec("grafana", 7003, 999, boot)
	rec("collector", 7009, 503, boot)
	write(t, filepath.Join(recs, "prometheus.conf"), `{"sum":"x","commit":"y"}`+"\n")
	write(t, filepath.Join(recs, "follow.pid.new"), `{"pid":7104,"start":603,"boot_id":"`+boot+`"}`+"\n")
	return root, recs
}

// TestPidsRecordTree: -pids reads prometheus by its record, RSS, peak and io included, and the dev server on 7400 and
// the worker, laug orchestrator worker, by their command lines; it skips jaeger's record (another boot), grafana's
// (another start), the collector's (no such pid), files that are not <name>.pid, a dev server on another port, a
// process named worker, laug's other verbs and a laugd. Without -pids the line has no pids key, and the Omes procs stay
// as they were. A pid whose first sample saw env, or an empty cmdline, is the worker when the second sample sees the
// worker.
func TestPidsRecordTree(t *testing.T) {
	root, recs := pidsTree(t)
	s := testSampler(root, &fakeLister{}, true)
	s.pidDir = recs
	var v struct {
		Pids  json.RawMessage `json:"pids"`
		Procs json.RawMessage `json:"procs"`
	}
	if err := json.Unmarshal(marshal(t, s.sample()), &v); err != nil {
		t.Fatal(err)
	}
	want := `[{"role":"orchestrator-server","pid":7101,"threads":1,"run_ns":1000000,"wait_ns":2000000,"runs":30,"cpu_ticks":61,"rss_b":184320000,"rss_peak_b":194560000,"io_rbytes":0,"io_wbytes":65536},` +
		`{"role":"orchestrator-worker","pid":7102,"threads":1,"run_ns":1000000,"wait_ns":2000000,"runs":30,"cpu_ticks":4,"rss_b":30720000,"rss_peak_b":31744000,"io_rbytes":0,"io_wbytes":0},` +
		`{"role":"prometheus","pid":7001,"threads":1,"run_ns":1000000,"wait_ns":2000000,"runs":30,"cpu_ticks":150,"rss_b":80216064,"rss_peak_b":81920000,"io_rbytes":4096,"io_wbytes":1048576}]`
	if string(v.Pids) != want {
		t.Errorf("pids:\n got %s\nwant %s", v.Pids, want)
	}
	if string(v.Procs) != "[]" {
		t.Errorf("procs: %s, want [] (no Omes process in the tree)", v.Procs)
	}
	body := string(renderMetrics(s.sample()))
	for _, w := range []string{`laug_proc_rss_bytes{role="prometheus",pid="7001"} 80216064`, `laug_proc_io_write_bytes_total{role="prometheus",pid="7001"} 1048576`,
		`laug_proc_cpu_seconds_total{role="orchestrator-server",pid="7101"} 0.61`, `laug_proc_rss_peak_bytes{role="orchestrator-worker",pid="7102"} 31744000`} {
		if !strings.Contains(body, w+"\n") {
			t.Errorf("the body lacks %s", w)
		}
	}
	for _, p := range []string{"7002", "7003", "7009", "7103", "7104", "7108", "7109", "7110"} {
		if strings.Contains(body, `pid="`+p+`"`) {
			t.Errorf("pid %s has a series", p)
		}
	}
	// env GOMAXPROCS=2 bin/laug orchestrator worker runs env, which execs laug in the same pid. A first sample that saw
	// env, or the empty cmdline between an exec's new comm and its new argv, does not keep the pid from being the
	// worker; the second sample's verdict is kept.
	for _, f := range []string{"stat", "status", "io", "task/7102/schedstat"} {
		b, err := os.ReadFile(filepath.Join(root, "proc/7102", f))
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range []string{"7105", "7106", "7107"} {
			write(t, filepath.Join(root, "proc", pid, strings.Replace(f, "7102", pid, 1)), strings.Replace(string(b), "7102 (laug)", pid+" (laug)", 1))
		}
	}
	env := "env\x00GOMAXPROCS=2\x00bin/laug\x00orchestrator\x00worker\x00"
	write(t, filepath.Join(root, "proc/7105/cmdline"), env)
	write(t, filepath.Join(root, "proc/7106/cmdline"), "")
	write(t, filepath.Join(root, "proc/7107/cmdline"), env)
	s = testSampler(root, &fakeLister{}, true)
	s.pidDir = recs
	workers := func() (out []string) {
		b := marshal(t, s.sample())
		for _, pid := range []string{"7105", "7106", "7107"} {
			if bytes.Contains(b, []byte(`"role":"orchestrator-worker","pid":`+pid)) {
				out = append(out, pid)
			}
		}
		return out
	}
	if got := workers(); len(got) != 0 {
		t.Errorf("the first sample, env and an empty cmdline: workers %v, want none", got)
	}
	write(t, filepath.Join(root, "proc/7105/cmdline"), "bin/laug\x00orchestrator\x00worker\x00")
	write(t, filepath.Join(root, "proc/7106/cmdline"), "bin/laug\x00orchestrator\x00worker\x00")
	if got := workers(); strings.Join(got, " ") != "7105 7106" {
		t.Errorf("the second sample, 7105 and 7106 now the worker, 7107 still env: workers %v, want 7105 7106", got)
	}
	write(t, filepath.Join(root, "proc/7107/cmdline"), "bin/laug\x00orchestrator\x00worker\x00")
	if got := workers(); strings.Join(got, " ") != "7105 7106" {
		t.Errorf("the third sample: workers %v, want 7105 7106, the second sample's verdicts kept", got)
	}
	// A sampler with no -pids reads none of it, and a -pids dir that is not there yet gives an empty list.
	s = testSampler(root, &fakeLister{}, true)
	if b := marshal(t, s.sample()); bytes.Contains(b, []byte(`"pids"`)) || bytes.Contains(b, []byte("orchestrator")) {
		t.Errorf("no -pids, and the line has %s", b)
	}
	s = testSampler(root, &fakeLister{}, true)
	s.pidDir = filepath.Join(recs, "not-yet")
	if b := marshal(t, s.sample()); !bytes.Contains(b, []byte(`"pids":[{"role":"orchestrator-server"`)) {
		t.Errorf("a -pids dir not made yet: %s", b)
	}
	// -pids naming a file is refused before any sample.
	if code := run(context.Background(), []string{"-pids", filepath.Join(recs, "prometheus.pid"), "-once", "-root", devTree(t)}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("-pids naming a file: exit %d, want 2", code)
	}
}
