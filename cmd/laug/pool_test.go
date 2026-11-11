package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/script"
)

// TestMain points pool status's default address at a fake pool for every test in this package: the bare and -json
// rules then read a pool that answers, and no test reads whatever runs on 127.0.0.1:7700. At the end it removes the
// binary laugBinary built (binary_test.go), if a test asked for one.
func TestMain(m *testing.M) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := pool.Config{MinWarm: 1, MaxCells: 2, Quota: 1, MaxLease: time.Hour, CreateTimeout: time.Second,
		HealthPoll: 10 * time.Millisecond, ReapInterval: time.Second}
	go pool.Serve(ctx, ln, pool.Fake(), cfg, log.New(io.Discard, "", 0))
	poolDefault = ln.Addr().String()
	code := m.Run()
	cancel()
	if built != "" {
		os.RemoveAll(built)
	}
	os.Exit(code)
}

// syncBuffer is a buffer a serve in another goroutine writes while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// checkout is a temp dir that findRoot takes for a laug checkout.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "gate/run.sh"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// serving is a pool serve running in a goroutine.
type serving struct {
	stdout, stderr *syncBuffer
	cancel         context.CancelFunc
	done           chan struct{} // closed when it returns, code its exit code
	code           int
	addr           string // where it listens, from its first line
}

var listeningOn = regexp.MustCompile(`^laug · pool serve: listening on (\S+) \(`)

// startServe runs pool serve with args under root and waits for its first line.
func startServe(t *testing.T, root string, args ...string) *serving {
	t.Helper()
	return startServeEnv(t, root, []string{"PATH=" + os.Getenv("PATH")}, args...)
}

// startServeEnv is startServe with environ for the App's environment.
func startServeEnv(t *testing.T, root string, environ []string, args ...string) *serving {
	t.Helper()
	return startServeApp(t, &App{Root: root, Environ: environ}, args...)
}

// startServeApp is startServe on a, its two streams made buffers.
func startServeApp(t *testing.T, a *App, args ...string) *serving {
	t.Helper()
	s := &serving{stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	a.Stdout, a.Stderr = s.stdout, s.stderr
	go func() {
		s.code = runPoolServe(ctx, a, args)
		close(s.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
		}
	})
	waitUntil(t, "pool serve's first line", func() bool { return strings.Contains(s.stdout.String(), "\n") })
	m := listeningOn.FindStringSubmatch(firstLine(s.stdout.String()))
	if m == nil {
		t.Fatalf("first line %q\nstderr:\n%s", firstLine(s.stdout.String()), s.stderr.String())
	}
	s.addr = m[1]
	return s
}

// stop cancels the serve's context, as a signal does, and returns its exit code and how long it took.
func (s *serving) stop(t *testing.T) (int, time.Duration) {
	t.Helper()
	start := time.Now()
	s.cancel()
	select {
	case <-s.done:
		return s.code, time.Since(start)
	case <-time.After(3 * time.Second):
		t.Fatal("pool serve did not return within 3 s of its context's end")
	}
	return 0, 0
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s within 3 s", what)
		}
	}
}

// TestPool drives pool serve and pool status together in a checkout: the first line says where, with what, the pid
// file and how to stop; the pid file holds this pid while it runs, so a second serve in the checkout is refused, but
// one with -pid-file none, a gate's own pool, runs beside it and leaves the file alone;
// status reads the cells, a lease among them, in text and -json; the end of the context, a signal in main, stops it
// within 2 s with exit 0, and the pid file goes.
func TestPool(t *testing.T) {
	root := checkout(t)
	s := startServe(t, root, "-listen", "127.0.0.1:0", "-min", "2", "-max", "4")
	pidPath := filepath.Join(root, "cells", "run", "pool.pid")
	pid := strconv.Itoa(os.Getpid())
	want := "laug · pool serve: listening on " + s.addr + " (min 2, max 4, quota 2, max lease 2h0m0s, backend fake); " +
		"pid " + pid + " in cells/run/pool.pid; Ctrl-C or SIGTERM stops it"
	if got := firstLine(s.stdout.String()); got != want {
		t.Errorf("first line\n got %q\nwant %q", got, want)
	}
	if b, err := os.ReadFile(pidPath); err != nil || string(b) != pid+"\n" {
		t.Errorf("cells/run/pool.pid: %q, %v; want %s", b, err, pid)
	}

	code, out, errOut := runApp(t, &App{Root: root}, "pool", "serve", "-listen", "127.0.0.1:0")
	if code != 2 || out != "" || firstLine(errOut) != "laug · pool serve: cells/run/pool.pid names pid "+pid+
		", which runs: one pool serve a checkout; laug pool down -remove-cells stops it, or remove the file if pid "+pid+
		" is no pool" {
		t.Errorf("a second serve in the checkout: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	// A gate's own pool runs beside the checkout's: -pid-file none neither reads nor writes the file.
	g := startServe(t, root, "-listen", "127.0.0.1:0", "-min", "0", "-pid-file", "none")
	if got, want := firstLine(g.stdout.String()), "laug · pool serve: listening on "+g.addr+" (min 0, max 6, quota 2, "+
		"max lease 2h0m0s, backend fake); -pid-file none, so no pid file; Ctrl-C or SIGTERM stops it"; got != want {
		t.Errorf("-pid-file none beside the checkout's pool: first line\n got %q\nwant %q", got, want)
	}
	if code, took := g.stop(t); code != 0 || took >= 2*time.Second {
		t.Errorf("-pid-file none: stop: exit %d after %s, want 0 under 2 s", code, took)
	}
	if b, err := os.ReadFile(pidPath); err != nil || string(b) != pid+"\n" {
		t.Errorf("cells/run/pool.pid after a -pid-file none serve stopped: %q, %v; want %s still", b, err, pid)
	}

	waitUntil(t, "2 warm cells", func() bool {
		_, out, _ := runArgs(t, "pool", "status", "-pool", s.addr)
		return strings.Contains(out, ", 2 cells: 2 warm, ")
	})
	resp, err := http.Post("http://"+s.addr+"/lease", "application/json", strings.NewReader(`{"requester":"laug","ttl_seconds":600}`))
	if err != nil {
		t.Fatal(err)
	}
	var lease struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err = json.NewDecoder(resp.Body).Decode(&lease)
	resp.Body.Close()
	if err != nil || lease.ID == "" {
		t.Fatalf("lease: %v %+v", err, lease)
	}
	waitUntil(t, "a cell to replace the leased one", func() bool {
		_, out, _ := runArgs(t, "pool", "status", "-pool", "http://"+s.addr)
		return strings.Contains(out, ", 3 cells: 2 warm, 1 leased, 0 busy; ")
	})
	code, out, errOut = runArgs(t, "pool", "status", "-pool", s.addr)
	if code != 0 || errOut != "" || firstLine(out) != "laug · pool status: "+s.addr+
		", 3 cells: 2 warm, 1 leased, 0 busy; min 2, max 4, quota 2, max lease 2h0m0s" {
		t.Errorf("pool status: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(rows) != 5 || !regexp.MustCompile(`^id +block +state +tag +requester +expires \(UTC\) +address$`).MatchString(rows[1]) {
		t.Fatalf("pool status rows:\n%s", out)
	}
	leased := regexp.MustCompile(`^` + lease.ID + ` +[0-3] +leased +- +laug +` +
		regexp.QuoteMeta(lease.ExpiresAt.UTC().Format("2006-01-02 15:04:05")) + ` +fake-` + lease.ID + `:72[0-9]3$`)
	warm := regexp.MustCompile(`(?m)^[0-9a-f]{8} +[0-3] +warm +- +- +- +fake-[0-9a-f]{8}:72[0-9]3$`)
	if n := len(warm.FindAllString(out, -1)); n != 2 || !leasedRow(rows[2:], leased) {
		t.Errorf("pool status: %d warm rows, want 2, and a leased row like %s:\n%s", n, leased, out)
	}

	code, out, errOut = runArgs(t, "pool", "status", "-pool", s.addr, "-json")
	var st map[string]any
	if code != 0 || errOut != "" || strings.Count(out, "\n") != 1 || json.Unmarshal([]byte(out), &st) != nil ||
		st["min_warm"] != 2.0 || st["max_cells"] != 4.0 || len(st["cells"].([]any)) != 3 {
		t.Errorf("pool status -json: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	code, took := s.stop(t)
	if code != 0 || took >= 2*time.Second {
		t.Errorf("stop: exit %d after %s, want 0 under 2 s\nstderr:\n%s", code, took, s.stderr.String())
	}
	if last := lastLine(s.stdout.String()); last != "laug · pool serve: stopped with 3 cells in its table, none destroyed "+
		"(laug pool down -remove-cells removes the compose backend's)" {
		t.Errorf("last line %q", last)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("cells/run/pool.pid is still there after the stop: %v", err)
	}
}

func leasedRow(rows []string, re *regexp.Regexp) bool {
	for _, r := range rows {
		if re.MatchString(r) {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// runApp runs laug with args on a, its two streams made buffers.
func runApp(t *testing.T, a *App, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a.Stdout, a.Stderr = &stdout, &stderr
	code := run(context.Background(), a, args)
	return code, stdout.String(), stderr.String()
}

// Outside a checkout serve writes no pid file, and says why.
func TestPoolServeOutsideCheckout(t *testing.T) {
	s := startServe(t, "", "-listen", "127.0.0.1:0")
	if !strings.Contains(firstLine(s.stdout.String()), "; not in a laug checkout, so no pid file; ") {
		t.Errorf("outside a checkout: %q", firstLine(s.stdout.String()))
	}
}

// TestPoolBlocks holds -max's cap, 17, to the ports a pool's cells publish. No port of blocks 0 to 16 is one of the dev
// server's four (7400, 7401, 7402, UI 8400) or pool serve's 7700. A port of block 0 below 7400 or 7700 stays below
// it; block 17's frontend is 7403. cellPorts and the 10N are cells/backend-compose.sh's (TestCellDefaultsMatchKnobs).
// checkServe refuses -max 18 on every backend but firecracker, whose cells publish no host port, and pool serve takes
// -max 17.
func TestPoolBlocks(t *testing.T) {
	if poolBlocks != 17 {
		t.Fatalf("poolBlocks %d, want 17", poolBlocks)
	}
	listeners := []int{portOf(orchAddressDefault), portOf(pool.DefaultListen)}
	taken := map[int]string{portOf(orchAddressDefault): "the dev server", orchHTTPPortDefault: "its HTTP",
		orchMetricsPortDefault: "its metrics", orchUIPortDefault: "its UI", portOf(pool.DefaultListen): "pool serve"}
	if !slices.Equal(listeners, []int{7400, 7700}) || len(taken) != 5 {
		t.Fatalf("listeners %v and taken ports %v, want 7400 and 7700 and five ports", listeners, taken)
	}
	for n := range poolBlocks {
		c := cellCompose{block: n}
		for _, p := range cellPorts {
			port := c.port(p.name)
			if what, ok := taken[port]; ok {
				t.Errorf("block %d's %s, %d, is %s's", n, p.name, port, what)
			}
			for _, l := range listeners {
				if p.base < l && port >= l {
					t.Errorf("block %d's %s, %d, is not below %d", n, p.name, port, l)
				}
			}
		}
	}
	last, next := (cellCompose{block: 16}).port("FRONTEND_PORT"), (cellCompose{block: 17}).port("FRONTEND_PORT")
	if last != 7393 || next != 7403 {
		t.Errorf("block 16's frontend %d and block 17's %d, want 7393 and 7403", last, next)
	}
	var o poolServeOpts
	(&App{}).newPoolServeFlags(&o)
	for _, c := range []struct {
		backend string
		max     int
		refused bool
	}{{"fake", 17, false}, {"fake", 18, true}, {"compose", 18, true}, {"my-backend.sh", 99, true},
		{"firecracker", 18, false}} {
		o.backend, o.cfg.MaxCells = c.backend, c.max
		if err := checkServe(o, "laug"); (err != nil) != c.refused ||
			c.refused && !strings.HasPrefix(err.Error(), "-max "+strconv.Itoa(c.max)+": over 17: ") {
			t.Errorf("-backend %s -max %d: %v, want refused %v", c.backend, c.max, err, c.refused)
		}
	}
	s := startServe(t, "", "-listen", "127.0.0.1:0", "-min", "0", "-max", "17")
	if !strings.Contains(firstLine(s.stdout.String()), " (min 0, max 17, quota 2, ") {
		t.Errorf("-max 17: %q", firstLine(s.stdout.String()))
	}
}

// pool serve refuses, exit 2 and one line, a listener off loopback, a value the pool cannot run with, a -pid-file
// other than cells/run/pool.pid or none, an argument and a flag it does not take; under -backend firecracker, a run
// that is not root's, a tag, visibility or bind a microVM cell cannot have, and -pid-file none, its -min and -max at 1
// and 2 unless given; an address taken is exit 1. None writes a pid file.
func TestPoolServeRefusals(t *testing.T) {
	saved := geteuid
	geteuid = func() int { return 1000 }
	defer func() { geteuid = saved }()
	for _, c := range []struct {
		args  []string
		code  int
		first string
	}{
		{[]string{"-listen", "0.0.0.0:7700"}, 2, `laug · pool serve: -listen 0.0.0.0:7700: host "0.0.0.0" is not a loopback address`},
		{[]string{"-listen", ":7700"}, 2, `laug · pool serve: -listen :7700: host "" is not a loopback address`},
		{[]string{"-listen", "localhost:7700"}, 2, `laug · pool serve: -listen localhost:7700: host "localhost" is not`},
		{[]string{"-listen", "127.0.0.1"}, 2, "laug · pool serve: -listen 127.0.0.1: not host:port"},
		{[]string{"-min", "-1"}, 2, "laug · pool serve: -min -1: under 0"},
		{[]string{"-max", "0", "-min", "0"}, 2, "laug · pool serve: -max 0: under 1"},
		{[]string{"-max", "18"}, 2, "laug · pool serve: -max 18: over 17: port block N's frontend is 7233 plus 10N, and " +
			"blocks 0 to 16 keep it below 7400 and 7700, the orchestrator's dev server and laug pool serve"},
		{[]string{"-max", "18", "-backend", "compose"}, 2, "laug · pool serve: -max 18: over 17: port block N's"},
		{[]string{"-min", "7"}, 2, "laug · pool serve: -min 7 is over -max 6"},
		{[]string{"-quota", "0"}, 2, "laug · pool serve: -quota 0: under 1"},
		{[]string{"-reap-interval", "0s"}, 2, "laug · pool serve: -reap-interval 0s: 0 or less"},
		{[]string{"-health-poll", "-1s"}, 2, "laug · pool serve: -health-poll -1s: 0 or less"},
		{[]string{"now"}, 2, `laug · pool serve: takes no arguments, got ["now"]`},
		{[]string{"-dev"}, 2, "laug · pool serve: no flag -dev"},
		{[]string{"-server-tag", "1.32"}, 2, "laug · pool serve: -server-tag 1.32: not a release tag, x.y.z"},
		{[]string{"-visibility", "mysql"}, 2, "laug · pool serve: -visibility mysql: postgres or es"},
		{[]string{"-bind", "10.0.0.1"}, 2, "laug · pool serve: -bind 10.0.0.1: loopback, 127.0.0.1, or all, 0.0.0.0"},
		{[]string{"-pid-file", "cells/run/gate.pid"}, 2, "laug · pool serve: -pid-file cells/run/gate.pid: " +
			"cells/run/pool.pid, where laug pool down finds the pool, or none"},
		{[]string{"-pid-file", ""}, 2, "laug · pool serve: -pid-file '': cells/run/pool.pid, where laug pool down"},
		{[]string{"-backend", "firecracker"}, 2, "laug · pool serve: -backend firecracker runs as root, for /dev/kvm " +
			"and each cell's network and mount namespaces: sudo bin/laug pool serve -backend firecracker"},
		{[]string{"-backend", "firecracker", "-server-tag", "1.32.1"}, 2, "laug · pool serve: -backend firecracker: " +
			"microVM cells run server 1.31.0, the tag the image bakes; -server-tag 1.32.1 is for compose cells"},
		{[]string{"-backend", "firecracker", "-visibility", "es"}, 2, "laug · pool serve: -backend firecracker: " +
			"microVM cells run postgres visibility, as the image bakes it; -visibility es is for compose cells"},
		{[]string{"-backend", "firecracker", "-bind", "all"}, 2, "laug · pool serve: -backend firecracker: microVM " +
			"cells answer on a host-only address, 10.254.<block>.2; -bind all is for compose cells"},
		{[]string{"-backend", "firecracker", "-pid-file", "none"}, 2, "laug · pool serve: -backend firecracker " +
			"-pid-file none: the Firecracker pool keeps its pid in cells/run/pool.pid, where pool down -backend " +
			"firecracker and the backend's prep and sweep find it"},
		{[]string{"-backend", "firecracker", "-listen", "0.0.0.0:7700"}, 2, `laug · pool serve: -listen 0.0.0.0:7700: ` +
			`host "0.0.0.0" is not a loopback address`},
		{[]string{"-backend", "firecracker", "-min", "3"}, 2, "laug · pool serve: -min 3 is over -max 2"},
		{[]string{"-backend", "firecracker", "-max", "0"}, 2, "laug · pool serve: -max 0: under 1"},
		{[]string{"-backend", "compose"}, 2, "laug · pool serve: -backend compose: bin/laug, whose health check a " +
			"new cell's health verb runs, is not built: make build builds it"},
		{[]string{"-listen", poolDefault}, 1, "laug · pool serve: listen tcp " + poolDefault + ": bind: address already in use"},
	} {
		root := checkout(t)
		code, out, errOut := runApp(t, &App{Root: root}, append([]string{"pool", "serve"}, c.args...)...)
		if code != c.code || out != "" || !strings.HasPrefix(firstLine(errOut), c.first) {
			t.Errorf("pool serve %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.args, code, c.code, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(root, "cells")); !os.IsNotExist(err) {
			t.Errorf("pool serve %q made cells/: %v", c.args, err)
		}
	}
}

// loopbackAddr takes a loopback IP and a port, and refuses every other host, a name included, and a port that is
// not a plain number in range.
func TestLoopbackAddr(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:7700": true, "127.0.0.1:0": true, "127.1.2.3:65535": true, "[::1]:7700": true,
		"0.0.0.0:7700": false, ":7700": false, "[::]:7700": false, "localhost:7700": false, "example.com:7700": false,
		"127.0.0.1:http": false, "127.0.0.1:65536": false, "127.0.0.1:07700": false, "127.0.0.1:": false,
		"127.0.0.1": false, "": false,
	} {
		if err := loopbackAddr(addr); (err == nil) != ok {
			t.Errorf("loopbackAddr(%q) = %v, want ok %t", addr, err, ok)
		}
	}
}

// refusedAddr is a loopback address that refuses every connection while the test runs: a socket bound to a port
// and never listening. The kernel answers a connect there with a reset, and binds no other socket to the port while
// this one holds it, so no server a test starts after can answer there. A listener closed at once frees its port,
// and the next httptest server could get it (seen once: pool status -pool <gone> met a server that is not a pool).
func refusedAddr(t *testing.T) string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return "127.0.0.1:" + strconv.Itoa(sa.(*syscall.SockaddrInet4).Port)
}

// pool status refuses an address it cannot read or one off loopback (2), and fails, one line and exit 1, when nothing
// answers, when what answers is not a pool, or answers with an error. A requester with a space is quoted, so a row
// stays one row.
func TestPoolStatus(t *testing.T) {
	gone := refusedAddr(t)
	notPool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			w.Write([]byte(`{"cells":[]}`))
		default:
			http.Error(w, "no", http.StatusTeapot)
		}
	}))
	t.Cleanup(notPool.Close)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	odd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"min_warm":0,"max_cells":1,"quota":1,"max_lease":"1h0m0s","cells":[{"id":"0a0b0c0d",` +
			`"address":"fake-0a0b0c0d:7233","block":0,"state":"leased","requester":"two words",` +
			`"expires_at":"2026-10-22T12:30:00+02:00"}]}`))
	}))
	t.Cleanup(odd.Close)
	stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"min_warm":1,"max_cells":2,"quota":1,"max_lease":"1h0m0s","cells":[{"id":"0a0b0c0d",` +
			`"address":"fake-0a0b0c0d:7233","block":0,"state":"stuck","requester":"ci"},{"id":"0e0f0a0b",` +
			`"address":"fake-0e0f0a0b:7243","block":1,"state":"warm"}]}`))
	}))
	t.Cleanup(stuck.Close)
	for _, c := range []struct {
		args        []string
		code        int
		first, more string
	}{
		{[]string{"-pool", "ftp://" + gone}, 2, "laug · pool status: -pool ftp://" + gone + ": not host:port or an http:// URL", ""},
		{[]string{"-pool", gone + "/status"}, 2, "laug · pool status: -pool " + gone + "/status: not host:port", ""},
		{[]string{"-pool", "0.0.0.0:7700"}, 2, `laug · pool status: -pool 0.0.0.0:7700: host "0.0.0.0" is not a loopback`, ""},
		{[]string{"x"}, 2, `laug · pool status: takes no arguments, got ["x"]`, ""},
		{[]string{"-pool", gone}, 1, "laug · pool status: no pool answers on " + gone + " (connection refused); laug " +
			"pool serve -listen " + gone + " starts one, on fake cells by default", ""},
		{[]string{"-pool", notPool.URL}, 1, "laug · pool status: what answers on " + notPool.URL + " is not a pool", ""},
		{[]string{"-pool", broken.URL, "-json"}, 1, "laug · pool status: no pool answers on " + broken.URL +
			": GET /status answered 500 Internal Server Error", ""},
		{[]string{"-pool", odd.URL + "/"}, 0, "laug · pool status: " + odd.URL + "/, 1 cells: 0 warm, 1 leased, 0 busy; " +
			"min 0, max 1, quota 1, max lease 1h0m0s", `0a0b0c0d  0      leased  -    "two words"  2026-10-22 10:30:00  fake-0a0b0c0d:7233`},
		{[]string{"-pool", stuck.URL}, 0, "laug · pool status: " + stuck.URL + ", 2 cells: 1 warm, 0 leased, 0 busy, 1 stuck; " +
			"min 1, max 2, quota 1, max lease 1h0m0s", "0a0b0c0d  0      stuck  -    ci         -              fake-0a0b0c0d:7233"},
	} {
		code, out, errOut := runArgs(t, append([]string{"pool", "status"}, c.args...)...)
		got, quiet := errOut, out // the stream that holds the answer, and the one that stays empty
		if c.code == 0 {
			got, quiet = out, errOut
		}
		if code != c.code || quiet != "" || !strings.HasPrefix(firstLine(got), c.first) || !strings.Contains(got, c.more) ||
			c.code == 1 && strings.Count(errOut, "\n") != 1 {
			t.Errorf("pool status %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.args, code, c.code, out, errOut)
		}
	}
}

// TestPoolDefaultsMatchKnobs: pool serve's defaults are the ones make pool passed, POOL_MIN, POOL_MAX, POOL_QUOTA,
// POOL_MAX_LEASE and POOL_LISTEN, and a cell's SERVER_TAG, VISIBILITY and BIND_ADDR, as internal/script's knob table
// keeps them; under -backend firecracker -min and -max are make fc-pool's FC_POOL_MIN and FC_POOL_MAX;
// pool bench's -cycles and -ttl-short are cells/pool-bench.sh's own CYCLES and TTL_SHORT.
func TestPoolDefaultsMatchKnobs(t *testing.T) {
	var o poolServeOpts
	(&App{}).newPoolServeFlags(&o)
	for name, want := range map[string]string{
		"POOL_MIN": strconv.Itoa(o.cfg.MinWarm), "POOL_MAX": strconv.Itoa(o.cfg.MaxCells),
		"POOL_QUOTA": strconv.Itoa(o.cfg.Quota), "POOL_LISTEN": o.listen, "SERVER_TAG": o.tag,
		"VISIBILITY": o.visibility, "BIND_ADDR": bindAddrs[o.bind], "POOL_MAX_LEASE": "2h",
		"FC_POOL_MIN": strconv.Itoa(fcMinDefault), "FC_POOL_MAX": strconv.Itoa(fcMaxDefault),
	} {
		if k, ok := script.Lookup(name); !ok || k.Default != want {
			t.Errorf("knob %s: default %q, pool serve's %q", name, k.Default, want)
		}
	}
	if o.cfg.MaxLease != 2*time.Hour {
		t.Errorf("pool serve's -max-lease default %s, the knob's 2h", o.cfg.MaxLease)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", benchScript))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`CYCLES="${CYCLES:-20}"`, `TTL_SHORT="${TTL_SHORT:-30}"`,
		`POOL_ADDR="${POOL_ADDR:-http://127.0.0.1:7700}"`} {
		if !strings.Contains(string(b), want+"\n") {
			t.Errorf("%s lacks %s", benchScript, want)
		}
	}
}

// TestPoolServeBackend: a script backend under pool serve runs with the environment internal/script builds, as a
// verb's script does: SERVER_TAG, VISIBILITY and BIND_ADDR from the flags, a stray copy of each named on stderr and
// not passed on; the base names, the tools' and the seams as the caller has them; USER, TEMPORAL_* and OTEL_* dropped;
// LAUG_BLOCK set per call. -backend compose needs the checkout's cells/backend-compose.sh and bin/laug.
func TestPoolServeBackend(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, "backend.sh")
	// The env file appears whole: sort writes a file of its own, which mv renames, so a reader that sees env sees all
	// of it.
	writeExec(t, backend, "#!/bin/sh\n[ \"$1\" = create ] && { env | sort > '"+dir+"/env.part' && "+
		"mv '"+dir+"/env.part' '"+dir+"/env'; printf '{\"id\":\"%s\"}\\n' \"$2\"; }\nexit 0\n")
	environ := []string{"PATH=" + os.Getenv("PATH"), "HOME=/h/x", "USER=someone", "SERVER_TAG=9.9.9",
		"VISIBILITY=es", "BIND_ADDR=0.0.0.0", "TEMPORAL_ADDRESS=127.0.0.1:7233", "OTEL_SERVICE_NAME=x",
		"FAKE_GATE_DIR=/tmp/fake", "LAUG_BLOCK=7", "GOFLAGS=-mod=mod"}
	read := func() map[string]string {
		t.Helper()
		waitUntil(t, "the backend's create", func() bool { _, err := os.Stat(filepath.Join(dir, "env")); return err == nil })
		b, err := os.ReadFile(filepath.Join(dir, "env"))
		if err != nil {
			t.Fatal(err)
		}
		env := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			k, v, _ := strings.Cut(l, "=")
			env[k] = v
		}
		os.Remove(filepath.Join(dir, "env"))
		return env
	}

	s := startServeEnv(t, "", environ, "-listen", "127.0.0.1:0", "-min", "1", "-max", "1", "-backend",
		backend, "-server-tag", "1.32.1")
	env := read()
	for k, want := range map[string]string{"SERVER_TAG": "1.32.1", "VISIBILITY": "postgres", "BIND_ADDR": "127.0.0.1",
		"LAUG_BLOCK": "0", "HOME": "/h/x", "FAKE_GATE_DIR": "/tmp/fake", "GOFLAGS": "-mod=mod",
		"USER": "", "TEMPORAL_ADDRESS": "", "OTEL_SERVICE_NAME": ""} {
		if env[k] != want {
			t.Errorf("pool serve's backend: %s=%q, want %q", k, env[k], want)
		}
	}
	if want := "laug · pool serve: listening on " + s.addr + " (min 1, max 1, quota 2, max lease 2h0m0s, backend " +
		backend + ", server 1.32.1, visibility postgres, on 127.0.0.1); not in a laug checkout"; !strings.HasPrefix(
		firstLine(s.stdout.String()), want) {
		t.Errorf("first line\n got %q\nwant %q...", firstLine(s.stdout.String()), want)
	}
	for _, line := range []string{
		"laug · pool serve: BIND_ADDR=0.0.0.0 in the environment is not passed on; -bind sets it",
		"laug · pool serve: SERVER_TAG=9.9.9 in the environment is not passed on; -server-tag sets it",
		"laug · pool serve: VISIBILITY=es in the environment is not passed on; -visibility sets it",
	} {
		if !strings.Contains(s.stderr.String(), line+"\n") {
			t.Errorf("stderr lacks %q:\n%s", line, s.stderr.String())
		}
	}
	s.stop(t)
	if code, _, errOut := runApp(t, &App{}, "pool", "serve", "-backend", "compose"); code != 2 ||
		!strings.HasPrefix(errOut, "laug · pool serve: -backend compose runs cells/backend-compose.sh, so it needs "+
			"a laug checkout") {
		t.Errorf("-backend compose outside a checkout: exit %d\n%s", code, errOut)
	}
}

// TestPoolServeLog: -log <file> takes the pool's log lines and a script backend's own output, docker compose's lines
// in a compose cell, off stderr and appends them to the file, a relative one at the checkout's root, its directory
// made; the first line names it. A file that cannot be opened is refused, exit 2, with the path as typed.
func TestPoolServeLog(t *testing.T) {
	root := checkout(t)
	backend := filepath.Join(t.TempDir(), "backend.sh")
	// Its create fails, so the pool logs a line of its own too.
	writeExec(t, backend, "#!/bin/sh\necho \"compose: $1 $2\" >&2\n[ \"$1\" = create ] && exit 1\nexit 0\n")
	args := []string{"-listen", "127.0.0.1:0", "-min", "1", "-max", "1", "-reap-interval", "1h", "-backend", backend,
		"-log", "cells/run/pool.log"}
	s := startServe(t, root, args...)
	if !strings.Contains(firstLine(s.stdout.String()), "; its log and the cells' output go to cells/run/pool.log; "+
		"Ctrl-C or SIGTERM stops it") {
		t.Errorf("first line %q", firstLine(s.stdout.String()))
	}
	path := filepath.Join(root, "cells", "run", "pool.log")
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	logLine := regexp.MustCompile(`(?m)^[0-9/]{10} [0-9:]{8} replenish: `)
	waitUntil(t, "the backend's create and the pool's line in the log", func() bool {
		return strings.Contains(read(), "compose: create ") && logLine.MatchString(read())
	})
	s.stop(t)
	if errOut := s.stderr.String(); errOut != "" {
		t.Errorf("stderr under -log:\n%s", errOut)
	}
	// A second serve appends.
	before := read()
	s = startServe(t, root, args...)
	waitUntil(t, "a second create in the log", func() bool { return strings.Count(read(), "compose: create ") == 2 })
	s.stop(t)
	if got := read(); !strings.HasPrefix(got, before) {
		t.Errorf("a second serve did not append:\n%s", got)
	}
	if errOut := s.stderr.String(); errOut != "" {
		t.Errorf("stderr under -log, a second serve:\n%s", errOut)
	}
	writeFile(t, filepath.Join(root, "a-file"), "")
	code, out, errOut := runApp(t, &App{Root: root}, "pool", "serve", "-listen", "127.0.0.1:0", "-log", "a-file/x.log")
	if code != 2 || out != "" || errOut != "laug · pool serve: -log a-file/x.log: not a directory\n" {
		t.Errorf("a -log under a file: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// poolDocker is a docker that lists one pool cell, laug-0123abcd, beside the single cell, project laug, which pool
// down leaves alone; with STUB_FAIL set, its ps fails.
const poolDocker = `#!/bin/sh
case "$1" in
ps) [ -n "$STUB_FAIL" ] && { echo "Cannot connect to the Docker daemon" >&2; exit 1; }
  printf '%s\n' laug-0123abcd-history-1 laug-frontend-1 laug-0123abcd-frontend-1 ;;
volume) printf '%s\n' laug-0123abcd_promdata laug_pgdata laug-0123abcd_pgdata ;;
network) printf '%s\n' bridge laug-0123abcd_default laug_default ;;
esac
exit 0
`

// poolDownWorld is a checkout and an App for pool down: a docker that lists poolDocker's world first on PATH, a proc
// dir with this test's pid as laug pool serve, pid 4242 as a gate's own pool, pid 4444 as another checkout's pool
// serve and 4545 as follow, which pool down leaves alone, the checkout's pid file naming this test, a per-cell dir and
// a last lease; procDir and fcRunDir point at the test's dirs.
func poolDownWorld(t *testing.T) (*App, *recorder) {
	t.Helper()
	root := checkout(t)
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "docker"), poolDocker)
	proc := t.TempDir()
	pid := strconv.Itoa(os.Getpid())
	writeFile(t, filepath.Join(proc, pid, "cmdline"), "../bin/laug\x00pool\x00serve\x00-listen\x00127.0.0.1:7700\x00")
	writeFile(t, filepath.Join(proc, "4242", "cmdline"),
		"bin/laug\x00pool\x00serve\x00-pid-file\x00none\x00-listen\x00127.0.0.1:7700\x00-min\x000\x00")
	writeFile(t, filepath.Join(proc, "4444", "cmdline"), "/x/bin/laug\x00pool\x00serve\x00-listen\x00127.0.0.1:7701\x00")
	writeFile(t, filepath.Join(proc, "4545", "cmdline"), "bin/laug\x00obs\x00follow\x00-pool\x00127.0.0.1:7700\x00")
	writeFile(t, filepath.Join(proc, "4343", "cmdline"), "bash\x00-c\x00sleep 1\x00")
	writeFile(t, filepath.Join(root, pidFile), pid+"\n")
	writeFile(t, filepath.Join(root, "cells", "run", "laug-0123abcd", "cell-id"), "0123abcd\n")
	writeFile(t, filepath.Join(root, ".last-lease.json"), "{}\n")
	savedProc, savedFC := procDir, fcRunDir
	procDir, fcRunDir = proc, filepath.Join(t.TempDir(), "laug")
	t.Cleanup(func() { procDir, fcRunDir = savedProc, savedFC })
	gateOpenIs(t, "")
	rec := &recorder{}
	return &App{Root: root, Environ: []string{"PATH=" + bin + ":/usr/bin:/bin"}, Runner: rec}, rec
}

// gateOpenIs makes gateOpenNow answer for the length of the test: a gate runs, for why, unless why is "".
func gateOpenIs(t *testing.T, why string) {
	t.Helper()
	saved := gateOpenNow
	t.Cleanup(func() { gateOpenNow = saved })
	gateOpenNow = func(context.Context) (string, bool) { return why, why != "" }
}

// TestPoolDownGateOpen: while a gate runs, pool down -remove-cells refuses, exit 2 with nothing run, since it would
// stop that gate's pool and remove its cells mid-wave; -while-gate-runs runs it, and its first line says a gate is
// open. Bare says so after its list. -while-gate-runs goes with -remove-cells and the compose backend alone, and
// -dry-run asks nothing.
func TestPoolDownGateOpen(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())
	counts := "pool serve, pid " + pid + ", 1 gate pool; 1 pool cell: 2 containers, 2 volumes and 1 network, their data " +
		"with them; 1 per-cell dir in cells/run"
	const why = "/tmp/laug-gate.lock is held"
	for _, c := range []struct {
		name   string
		open   bool
		args   []string
		code   int
		calls  int
		out    string
		errOut string // all of it, or its last line when it starts with "..."
	}{
		{name: "open, bare", open: true, code: 2, errOut: "...A gate runs (" + why + "): -remove-cells refuses " +
			"until it ends, and -while-gate-runs removes them all the same"},
		{name: "open, -remove-cells", open: true, args: []string{"-remove-cells"}, code: 2,
			errOut: "laug · pool down: a gate runs (" + why + "): -remove-cells would stop its pool and remove its " +
				"cells mid-wave; wait for it to end, or add -while-gate-runs to remove them anyway"},
		{name: "open, -remove-cells -while-gate-runs", open: true, args: []string{"-remove-cells", "-while-gate-runs"},
			calls: 1, out: "laug · pool down: " + counts + " go, while a gate runs (" + why + ")"},
		{name: "closed, -remove-cells -while-gate-runs", args: []string{"-remove-cells", "-while-gate-runs"}, calls: 1,
			out: "laug · pool down: " + counts + " go"},
		{name: "closed, bare", code: 2, errOut: "...laug pool down -remove-cells stops and removes them"},
		{name: "open, -remove-cells -dry-run", open: true, args: []string{"-remove-cells", "-dry-run"},
			out: "cells/pool-down.sh"},
		{name: "-while-gate-runs alone", args: []string{"-while-gate-runs"}, code: 2,
			errOut: "laug · pool down: -while-gate-runs goes with -remove-cells, which it lets run while a gate runs"},
		{name: "-while-gate-runs, firecracker", args: []string{"-remove-cells", "-while-gate-runs", "-backend",
			"firecracker"}, code: 2, errOut: "laug · pool down: -while-gate-runs goes with -backend compose: a gate's " +
			"pool and cells are compose's"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := poolDownWorld(t)
			if c.open {
				gateOpenIs(t, why)
			}
			code, out, errOut := runApp(t, a, append([]string{"pool", "down"}, c.args...)...)
			got := errOut
			if strings.HasPrefix(c.errOut, "...") {
				got = "..." + lastLine(errOut)
			} else if c.errOut != "" {
				got = firstLine(errOut)
			}
			if code != c.code || len(rec.calls) != c.calls || strings.TrimSuffix(out, "\n") != c.out ||
				(c.errOut != "" && got != c.errOut) || (c.errOut == "" && errOut != "") {
				t.Errorf("exit %d, %d calls\nstdout:\n%s\nstderr:\n%s\nwant exit %d, %d calls, %q, %q", code,
					len(rec.calls), out, errOut, c.code, c.calls, c.out, c.errOut)
			}
		})
	}
}

// TestGateOpenNow: gateOpenNow asks internal/gateopen, as bin/obs follow -gate-running does: a host lock held in
// XDG_RUNTIME_DIR is a gate open, named without its path.
func TestGateOpenNow(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	f, err := os.OpenFile(filepath.Join(xdg, "laug-gate.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	why, open := gateOpenNow(context.Background())
	if !open || why != "laug-gate.lock in XDG_RUNTIME_DIR is held" {
		t.Errorf("a lock held: open %t, why %q", open, why)
	}
}

// TestPoolDown: bare, pool down lists what -remove-cells would stop and remove, with counts and names, and exits 2,
// running nothing; -remove-cells execs cells/pool-down.sh after a first line that counts the same. With no docker it
// refuses, 2; a docker that cannot list refuses, 1; both run nothing. Firecracker cells are named, and left.
func TestPoolDown(t *testing.T) {
	gateOpenIs(t, "")
	pid := strconv.Itoa(os.Getpid())
	counts := "pool serve, pid " + pid + ", 1 gate pool; 1 pool cell: 2 containers, 2 volumes and 1 network, their data " +
		"with them; 1 per-cell dir in cells/run"
	a, rec := poolDownWorld(t)
	code, out, errOut := runApp(t, a, "pool", "down")
	want := "laug · pool down: would stop and remove " + counts + "\n" +
		"  gate pools  4242\n" +
		"  pool cells  laug-0123abcd\n" +
		"  containers  laug-0123abcd-frontend-1, laug-0123abcd-history-1\n" +
		"  volumes     laug-0123abcd_pgdata, laug-0123abcd_promdata\n" +
		"  networks    laug-0123abcd_default\n" +
		"  dirs        laug-0123abcd\n" +
		"  last lease  .last-lease.json, removed\n" +
		"laug pool down -remove-cells stops and removes them\n"
	if code != 2 || out != "" || errOut != want || len(rec.calls) != 0 {
		t.Errorf("bare: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s\nwant:\n%s", code, len(rec.calls), out, errOut, want)
	}

	a, rec = poolDownWorld(t)
	code, out, errOut = runApp(t, a, "pool", "down", "-remove-cells")
	if code != 0 || errOut != "" || out != "laug · pool down: "+counts+" go\n" || len(rec.calls) != 1 ||
		rec.calls[0].Script != poolDownScript || rec.hows[0] != script.Exec || len(rec.calls[0].Set) != 0 {
		t.Errorf("-remove-cells: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", code, len(rec.calls), out, errOut)
	}

	a, rec = poolDownWorld(t)
	writeFile(t, filepath.Join(fcRunDir, "b1", "cell"), "x")
	fcLine := "\nThe Firecracker pool is up as well, its cells in /run/laug: sudo bin/laug pool down -backend firecracker " +
		"-remove-cells removes it, and this does not\n"
	if code, _, errOut := runApp(t, a, "pool", "down", "-dry-run"); code != 2 || len(rec.calls) != 0 ||
		!strings.HasSuffix(errOut, fcLine) {
		t.Errorf("-dry-run alone, Firecracker cells up: exit %d\n%s", code, errOut)
	}

	// The pid file names a pool serve on -backend firecracker: it is not this pool down's to stop, so it is not counted,
	// and the Firecracker line says it is up.
	a, rec = poolDownWorld(t)
	writeFile(t, filepath.Join(procDir, pid, "cmdline"), "bin/laug\x00pool\x00serve\x00-backend\x00firecracker\x00")
	code, _, errOut = runApp(t, a, "pool", "down")
	if code != 2 || len(rec.calls) != 0 || !strings.HasPrefix(errOut, "laug · pool down: would stop and remove no "+
		"pool serve, 1 gate pool; ") || !strings.HasSuffix(errOut, fcLine) {
		t.Errorf("a Firecracker pool serve in the pid file: exit %d\n%s", code, errOut)
	}

	root := checkout(t)
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "docker"), "#!/bin/sh\nexit 0\n")
	savedProc, savedFC := procDir, fcRunDir
	procDir, fcRunDir = t.TempDir(), t.TempDir()
	defer func() { procDir, fcRunDir = savedProc, savedFC }()
	rec = &recorder{}
	code, _, errOut = runApp(t, &App{Root: root, Environ: []string{"PATH=" + bin}, Runner: rec}, "pool", "down")
	if code != 2 || len(rec.calls) != 0 || errOut != "laug · pool down: no pool serve, gate pool or pool cell, and no "+
		"per-cell dir or last lease: nothing to stop or remove\n" {
		t.Errorf("nothing there: exit %d\n%s", code, errOut)
	}

	for _, c := range []struct {
		name    string
		environ []string
		args    []string
		code    int
		err     string
	}{
		{"no docker", []string{"PATH=" + t.TempDir()}, []string{"-remove-cells"}, 2, "laug · pool down: no docker on " +
			"PATH, so nothing can say which pool cells go, and cells/pool-down.sh cannot remove them\n"},
		{"docker fails", nil, []string{"-remove-cells"}, 1, "laug · pool down: docker ps could not say what goes, so " +
			"nothing was stopped or removed: Cannot connect to the Docker daemon\n"},
		{"another backend", nil, []string{"-remove-cells", "-backend", "fake"}, 2, "laug · pool down: -backend " +
			"fake: compose or firecracker\n"},
	} {
		a, rec := poolDownWorld(t)
		if c.environ != nil {
			a.Environ = c.environ
		} else {
			a.Environ = append(a.Environ, "STUB_FAIL=1")
		}
		code, out, errOut := runApp(t, a, append([]string{"pool", "down"}, c.args...)...)
		if code != c.code || out != "" || errOut != c.err || len(rec.calls) != 0 {
			t.Errorf("%s: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", c.name, code, len(rec.calls), out, errOut)
		}
	}
	rec = &recorder{}
	if code, _, errOut := runApp(t, &App{Runner: rec}, "pool", "down", "-remove-cells"); code != 2 ||
		len(rec.calls) != 0 || !strings.Contains(errOut, "runs cells/pool-down.sh, so it needs a laug checkout") {
		t.Errorf("outside a checkout: exit %d\n%s", code, errOut)
	}
}

// TestPoolBench: bench execs cells/pool-bench.sh with POOL_ADDR the pool it names, as an http:// URL, and CYCLES and
// TTL_SHORT only when their flags are given; a stray copy of each is named and not passed on. Bare, two pools, a pool
// off loopback or a count it cannot run are refused, exit 2, with nothing run.
func TestPoolBench(t *testing.T) {
	root := checkout(t)
	environ := []string{"PATH=/usr/bin:/bin", "POOL_ADDR=http://127.0.0.1:1", "CYCLES=3", "TTL_SHORT=5"}
	rec := &recorder{}
	code, out, errOut := runApp(t, &App{Root: root, Environ: environ, Runner: rec}, "pool", "bench", "127.0.0.1:7700")
	if code != 0 || len(rec.calls) != 1 || rec.hows[0] != script.Exec || rec.calls[0].Script != benchScript ||
		!slices.Contains(rec.envs[0], "POOL_ADDR=http://127.0.0.1:7700") || slices.ContainsFunc(rec.envs[0],
		func(kv string) bool { return strings.HasPrefix(kv, "CYCLES=") || strings.HasPrefix(kv, "TTL_SHORT=") }) {
		t.Fatalf("bare flags: exit %d, %d calls, env %q\n%s%s", code, len(rec.calls), rec.envs, out, errOut)
	}
	if want := "laug · pool bench: timing the pool at http://127.0.0.1:7700: its time to warm, 20 lease cycles from " +
		"warm, one cold lease and a 30s TTL; it leases and releases that pool's cells; Ctrl-C stops it\n"; out != want {
		t.Errorf("first line\n got %q\nwant %q", out, want)
	}
	for _, s := range []string{"CYCLES=3 in the environment is not passed on; -cycles sets it",
		"POOL_ADDR=http://127.0.0.1:1 in the environment is not passed on; <host:port> sets it",
		"TTL_SHORT=5 in the environment is not passed on; -ttl-short sets it"} {
		if !strings.Contains(errOut, "laug · pool bench: "+s+"\n") {
			t.Errorf("stderr lacks %q:\n%s", s, errOut)
		}
	}
	rec = &recorder{}
	if code, _, _ := runApp(t, &App{Root: root, Runner: rec}, "pool", "bench", "http://127.0.0.1:17700", "-cycles",
		"5", "-ttl-short", "10s"); code != 0 || len(rec.calls) != 1 || !slices.Equal(rec.envs[0],
		[]string{"POOL_ADDR=http://127.0.0.1:17700", "CYCLES=5", "TTL_SHORT=10", "LAUG=laug"}) {
		t.Errorf("flags: exit %d, env %q", code, rec.envs)
	}
	for _, c := range []struct {
		args []string
		err  string
	}{
		{nil, "laug · pool bench: names no pool: its address, 127.0.0.1:7700 for laug pool serve's default"},
		{[]string{"127.0.0.1:1", "127.0.0.1:2"}, `laug · pool bench: takes one pool, got ["127.0.0.1:1" "127.0.0.1:2"]`},
		{[]string{"10.0.0.1:7700"}, `laug · pool bench: 10.0.0.1:7700: host "10.0.0.1" is not a loopback address`},
		{[]string{"127.0.0.1:7700", "-cycles", "0"}, "laug · pool bench: -cycles 0: a whole number over 0"},
		{[]string{"127.0.0.1:7700", "-ttl-short", "0s"}, "laug · pool bench: -ttl-short 0s: whole seconds, over 0"},
		{[]string{"127.0.0.1:7700", "-ttl-short", "1.5s"}, "laug · pool bench: -ttl-short 1.5s: whole seconds, 0 or " +
			"more"},
	} {
		rec := &recorder{}
		code, out, errOut := runApp(t, &App{Root: root, Runner: rec}, append([]string{"pool", "bench"}, c.args...)...)
		if code != 2 || out != "" || len(rec.calls) != 0 || !strings.HasPrefix(errOut, c.err) {
			t.Errorf("%q: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", c.args, code, len(rec.calls), out, errOut)
		}
	}
}

// fakeRoot makes geteuid answer uid for the rest of the test.
func fakeRoot(t *testing.T, uid int) {
	t.Helper()
	saved := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = saved })
}

// fcCheckout is a checkout for -backend firecracker: a fake cells/backend-firecracker.sh that writes its environment
// to <dir>/env on its first create and answers create, health and destroy at once, and a bin/laug that is executable.
func fcCheckout(t *testing.T) (root, envFile string) {
	t.Helper()
	root = checkout(t)
	envFile = filepath.Join(t.TempDir(), "env")
	// The first create's env file appears whole, by a rename of a file of the create's own, as in TestPoolServeBackend.
	writeExec(t, filepath.Join(root, fcBackend), "#!/bin/sh\nif [ \"$1\" = create ]; then\n"+
		"  [ -e '"+envFile+"' ] || { env | sort > '"+envFile+".'$$ && mv '"+envFile+".'$$ '"+envFile+"'; }\n"+
		"  printf '{\"id\":\"%s\"}\\n' \"$2\"\nfi\nexit 0\n")
	writeExec(t, filepath.Join(root, laugBin), "#!/bin/sh\nexit 0\n")
	return root, envFile
}

// TestPoolServeFirecracker: as root, in a checkout with bin/laug built, pool serve -backend firecracker runs
// cells/backend-firecracker.sh prep as a child with the backend's environment, then serves that script with -min 1
// and -max 2 unless given; its pid file is the checkout's, cells/run made with the checkout's owner; its last line
// names the verb that removes the cells. A prep that refuses exits 2, one that fails 1, and neither serves, writes a
// pid file or keeps the address. As root, it still refuses outside a checkout, and without bin/laug.
func TestPoolServeFirecracker(t *testing.T) {
	fakeRoot(t, 0)
	root, envFile := fcCheckout(t)
	environ := []string{"PATH=" + os.Getenv("PATH"), "HOME=/h/root", "USER=root", "SUDO_USER=someone",
		"FC_DIR=/opt/fc-test", "SERVER_TAG=9.9.9", "TEMPORAL_ADDRESS=127.0.0.1:7233"}
	rec := &recorder{run: func(_ script.Command, _ []string, stdout, _ io.Writer) int {
		io.WriteString(stdout, "laug · fc pool left: 0 firecracker, 0 laugfc links, 0 cell disks, 0 block dirs, "+
			"1 deny rules\n")
		return 0
	}}
	s := startServeApp(t, &App{Root: root, Environ: environ, Runner: rec}, "-listen", "127.0.0.1:0",
		"-backend", "firecracker")
	pid := strconv.Itoa(os.Getpid())
	want := "laug · pool serve: listening on " + s.addr + " (min 1, max 2, quota 2, max lease 2h0m0s, backend " +
		"firecracker, server 1.31.0, visibility postgres, on host-only addresses); pid " + pid + " in " +
		"cells/run/pool.pid; Ctrl-C or SIGTERM stops it"
	if got := firstLine(s.stdout.String()); got != want {
		t.Errorf("first line\n got %q\nwant %q", got, want)
	}
	if len(rec.calls) != 1 || rec.hows[0] != script.Run || rec.calls[0].Script != fcBackend ||
		rec.calls[0].Root != root || len(rec.calls[0].Args) != 1 || rec.calls[0].Args[0].Text != "prep" {
		t.Fatalf("prep: %d calls %+v, hows %v", len(rec.calls), rec.calls, rec.hows)
	}
	for _, kv := range []string{"SERVER_TAG=1.31.0", "VISIBILITY=postgres", "BIND_ADDR=127.0.0.1", "SUDO_USER=someone",
		"FC_DIR=/opt/fc-test", "HOME=/h/root"} {
		if !slices.Contains(rec.envs[0], kv) {
			t.Errorf("prep's environment lacks %s: %q", kv, rec.envs[0])
		}
	}
	if slices.ContainsFunc(rec.envs[0], func(kv string) bool {
		return strings.HasPrefix(kv, "USER=") || strings.HasPrefix(kv, "TEMPORAL_") || kv == "SERVER_TAG=9.9.9"
	}) {
		t.Errorf("prep's environment keeps USER, TEMPORAL_* or the stray SERVER_TAG: %q", rec.envs[0])
	}
	for _, line := range []string{"laug · fc pool left: 0 firecracker, 0 laugfc links, 0 cell disks, 0 block dirs, " +
		"1 deny rules", "laug · pool serve: SERVER_TAG=9.9.9 in the environment is not passed on; -server-tag sets it"} {
		if !strings.Contains(s.stderr.String(), line+"\n") {
			t.Errorf("stderr lacks %q:\n%s", line, s.stderr.String())
		}
	}
	waitUntil(t, "the backend's first create", func() bool { _, err := os.Stat(envFile); return err == nil })
	b, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range []string{"LAUG_BLOCK=0", "SERVER_TAG=1.31.0", "SUDO_USER=someone", "FC_DIR=/opt/fc-test"} {
		if !strings.Contains(string(b), "\n"+kv+"\n") && !strings.HasPrefix(string(b), kv+"\n") {
			t.Errorf("the backend's create lacks %s:\n%s", kv, b)
		}
	}
	pidPath := filepath.Join(root, pidFile)
	if b, err := os.ReadFile(pidPath); err != nil || string(b) != pid+"\n" {
		t.Errorf("%s: %q, %v", pidFile, b, err)
	}
	if fi, err := os.Stat(filepath.Join(root, "cells", "run")); err != nil ||
		fi.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		t.Errorf("cells/run: %v, %v", fi, err)
	}
	if code, took := s.stop(t); code != 0 || took >= 2*time.Second {
		t.Errorf("stop: exit %d after %s", code, took)
	}
	if last := lastLine(s.stdout.String()); !strings.HasSuffix(last, "none destroyed (sudo bin/laug pool down "+
		"-backend firecracker -remove-cells removes them)") {
		t.Errorf("last line %q", last)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("%s is still there after the stop: %v", pidFile, err)
	}

	// -min and -max given win over the backend's defaults. -max is not held to the 17 port blocks: a microVM cell
	// publishes no host port.
	s = startServeApp(t, &App{Root: root, Environ: environ, Runner: &recorder{}}, "-listen", "127.0.0.1:0",
		"-backend", "firecracker", "-min", "0", "-max", "18")
	if !strings.Contains(firstLine(s.stdout.String()), " (min 0, max 18, quota 2, ") {
		t.Errorf("-min 0 -max 18: %q", firstLine(s.stdout.String()))
	}
	s.stop(t)

	// A prep that refuses or fails: nothing serves, no pid file, and the address is free again.
	for _, c := range []struct {
		code, want int
	}{{2, 2}, {1, 1}, {7, 1}} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		root, _ := fcCheckout(t)
		rec := &recorder{run: func(_ script.Command, _ []string, _, stderr io.Writer) int {
			io.WriteString(stderr, "no /opt/fc/snap/vm.snap; run make fc-snapshot first\n")
			return c.code
		}}
		code, out, errOut := runApp(t, &App{Root: root, Environ: environ, Runner: rec}, "pool", "serve", "-listen",
			addr, "-backend", "firecracker")
		if code != c.want || out != "" || !strings.Contains(errOut, "no /opt/fc/snap/vm.snap; run make fc-snapshot "+
			"first\nlaug · pool serve: cells/backend-firecracker.sh prep exited "+strconv.Itoa(c.code)+", so no pool "+
			"serves\n") {
			t.Errorf("prep exits %d: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.code, code, c.want, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(root, "cells", "run")); !os.IsNotExist(err) {
			t.Errorf("prep exits %d: cells/run made: %v", c.code, err)
		}
		if ln, err := net.Listen("tcp", addr); err != nil {
			t.Errorf("prep exits %d: %s still held: %v", c.code, addr, err)
		} else {
			ln.Close()
		}
	}

	noLaug := checkout(t)
	environ = []string{"PATH=" + os.Getenv("PATH"), "SUDO_USER=someone"}
	for _, c := range []struct {
		name string
		a    *App
		err  string
	}{
		{"no runner", &App{Root: root, Environ: environ}, "laug · pool serve: no runner, so " +
			"cells/backend-firecracker.sh prep did not run\n"},
		{"outside a checkout", &App{Environ: environ, Runner: &recorder{}}, "laug · pool serve: -backend " +
			"firecracker runs cells/backend-firecracker.sh, so it needs a laug checkout: no directory above this one " +
			"holds go.mod and gate/run.sh\n"},
		{"no bin/laug", &App{Root: noLaug, Environ: environ, Runner: &recorder{}}, "laug · pool serve: " +
			"-backend firecracker: bin/laug, whose health check a new cell's health verb runs, is not built: make " +
			"build, as yourself, builds it\n"},
	} {
		var stdout, stderr bytes.Buffer
		c.a.Stdout, c.a.Stderr = &stdout, &stderr
		want := 2
		if c.name == "no runner" {
			want = 1
		}
		code := runPoolServe(context.Background(), c.a, []string{"-listen", "127.0.0.1:0", "-backend", "firecracker"})
		if code != want || stdout.String() != "" || stderr.String() != c.err {
			t.Errorf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.name, code, want, stdout.String(),
				stderr.String())
		}
	}
}

// TestMkdirOwned: as root, every directory mkdirOwned makes gets the owner's owner; one that was there is left.
func TestMkdirOwned(t *testing.T) {
	fakeRoot(t, 0)
	root := t.TempDir()
	if err := mkdirOwned(filepath.Join(root, "cells", "run"), root); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"cells", "cells/run"} {
		fi, err := os.Stat(filepath.Join(root, d))
		if err != nil || !fi.IsDir() || fi.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
			t.Errorf("%s: %v, %v", d, fi, err)
		}
	}
	if err := mkdirOwned(filepath.Join(root, "cells", "run"), filepath.Join(root, "gone")); err != nil {
		t.Errorf("a directory that was there: %v", err)
	}
	if err := mkdirOwned(filepath.Join(root, "new"), filepath.Join(root, "gone")); err == nil {
		t.Errorf("an owner that is not there: no error")
	}
}

// TestPoolServeOf: the pid file names a pool serve only while its pid runs laug pool serve, by any path; its backend
// is what its -backend flag says, in any of Go's spellings, and fake without one.
func TestPoolServeOf(t *testing.T) {
	root := checkout(t)
	saved := procDir
	procDir = t.TempDir()
	defer func() { procDir = saved }()
	pid := strconv.Itoa(os.Getpid())
	a := &App{Root: root}
	if p, b := a.poolServeOf(); p != 0 || b != "" {
		t.Errorf("no pid file: %d %q", p, b)
	}
	writeFile(t, filepath.Join(root, pidFile), pid+"\n")
	for _, c := range []struct {
		cmdline string
		pid     int
		backend string
	}{
		{"bin/laug\x00pool\x00serve\x00-backend\x00firecracker\x00", os.Getpid(), "firecracker"},
		{"/x/bin/laug\x00pool\x00serve\x00-listen\x00127.0.0.1:7701\x00--backend=firecracker\x00", os.Getpid(),
			"firecracker"},
		{"laug\x00pool\x00serve\x00", os.Getpid(), "fake"},
		{"../bin/laug\x00pool\x00serve\x00-backend\x00compose\x00-min\x001\x00", os.Getpid(), "compose"},
		// An empty argument is lost in the joined command line, so its flags do not parse back.
		{"bin/laug\x00pool\x00serve\x00-backend\x00\x00", os.Getpid(), ""},
		{"pool/laugd\x00-listen\x00127.0.0.1:7700\x00", 0, ""},
		{"bin/laug\x00pool\x00status\x00", 0, ""},
		{"", 0, ""},
	} {
		writeFile(t, filepath.Join(procDir, pid, "cmdline"), c.cmdline)
		if p, b := a.poolServeOf(); p != c.pid || b != c.backend {
			t.Errorf("%q: pid %d, backend %q; want %d, %q", c.cmdline, p, b, c.pid, c.backend)
		}
	}
	writeFile(t, filepath.Join(procDir, pid, "cmdline"), "bin/laug\x00pool\x00serve\x00-backend\x00firecracker\x00")
	writeFile(t, filepath.Join(root, pidFile), "999999999\n")
	if p, b := a.poolServeOf(); p != 0 || b != "" {
		t.Errorf("a pid that does not run: %d %q", p, b)
	}
}

// TestPoolDownMatchesScript holds pool down's two command line patterns to cells/pool-down.sh's serve_re and gate_re,
// and runs gate_re through pgrep -f, as the script's pkill does, against stand-ins: sleeps whose argv[0] is a command
// line. It finds a gate's own pool, -pid-file none in either spelling, and leaves a checkout's pool serve, a pool
// status and follow alone.
func TestPoolDownMatchesScript(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", poolDownScript))
	if err != nil {
		t.Fatal(err)
	}
	re := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)\b(serve_re|gate_re)='([^']*)'`).FindAllStringSubmatch(string(b), -1) {
		re[m[1]] = m[2]
	}
	if re["serve_re"] != serveCmdline.String() || re["gate_re"] != gateCmdline.String() {
		t.Fatalf("%s: serve_re %q and gate_re %q; pool down's %q and %q", poolDownScript, re["serve_re"],
			re["gate_re"], serveCmdline, gateCmdline)
	}
	needTools(t, "the test runs gate_re through pgrep -f against sleep stand-ins", "pgrep", "sleep")
	pgrep, _ := exec.LookPath("pgrep")
	sleep, _ := exec.LookPath("sleep")
	want := map[string]bool{
		"bin/laug pool serve -pid-file none -listen 127.0.0.1:17901 -min 0": true,
		"/x/bin/laug pool serve -listen 127.0.0.1:17902 --pid-file=none":    true,
		"bin/laug pool serve -listen 127.0.0.1:17903":                       false,
		"bin/laug pool status -pool 127.0.0.1:17904 -pid-file none":         false,
		"/x/bin/obs follow -pool 127.0.0.1:17905 -dir /x/none":              false,
		"bin/laug pool serve -pid-file nonesuch -listen 127.0.0.1:17906":    false,
	}
	pids := map[string]string{}
	for argv0 := range want {
		cmd := &exec.Cmd{Path: sleep, Args: []string{argv0, "30"}}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		pids[strconv.Itoa(cmd.Process.Pid)] = argv0
		waitUntil(t, "the stand-in's command line", func() bool {
			return strings.HasPrefix(cmdlineOf(cmd.Process.Pid), argv0)
		})
	}
	out, _ := exec.Command(pgrep, "-f", re["gate_re"]).Output()
	found := map[string]bool{}
	for _, p := range strings.Fields(string(out)) {
		if argv0, ok := pids[p]; ok {
			found[argv0] = true
		}
	}
	for argv0, w := range want {
		if found[argv0] != w || gateCmdline.MatchString(argv0+" 30") != w {
			t.Errorf("%q: pgrep -f gate_re %t, gateCmdline %t; want %t", argv0, found[argv0],
				gateCmdline.MatchString(argv0+" 30"), w)
		}
	}
}

// cmdlineOf is /proc/<pid>/cmdline, its arguments joined by spaces, as pgrep -f reads it.
func cmdlineOf(pid int) string {
	b, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	return strings.TrimRight(strings.ReplaceAll(string(b), "\x00", " "), " ")
}

// fcDownWorld is a checkout for pool down -backend firecracker as root: a proc dir with four pool VMMs, two by socket
// (one in a block dir and one not) and two by the jailer's id (--id and the id as two arguments, and --id=<id>), and
// processes the matcher must pass over: one on another socket, one with another id, one whose id runs on past the
// number; /run/laug's stand-in with blocks b0 and b1; a recorder.
func fcDownWorld(t *testing.T) (*App, *recorder) {
	t.Helper()
	fakeRoot(t, 0)
	root := checkout(t)
	savedProc, savedFC := procDir, fcRunDir
	procDir, fcRunDir = t.TempDir(), filepath.Join(t.TempDir(), "laug")
	t.Cleanup(func() { procDir, fcRunDir = savedProc, savedFC })
	writeFile(t, filepath.Join(procDir, "5151", "cmdline"), "/opt/fc/firecracker\x00--api-sock\x00"+fcRunDir+
		"/b0/fc.sock\x00--enable-pci\x00")
	writeFile(t, filepath.Join(procDir, "5252", "cmdline"), "/opt/fc/firecracker\x00--api-sock\x00"+fcRunDir+
		"/b7/fc.sock\x00")
	writeFile(t, filepath.Join(procDir, "5353", "cmdline"), "/opt/fc/firecracker\x00--api-sock\x00/opt/fc/fc.sock\x00")
	writeFile(t, filepath.Join(procDir, "5454", "cmdline"), "/firecracker\x00--id\x00laug-b3\x00--start-time-us\x001\x00"+
		"--start-time-cpu-us\x002\x00--parent-cpu-time-us\x003\x00--api-sock\x00/run/firecracker.socket\x00")
	writeFile(t, filepath.Join(procDir, "5555", "cmdline"), "/firecracker\x00--id=laug-b250\x00")
	writeFile(t, filepath.Join(procDir, "5656", "cmdline"), "/firecracker\x00--id\x00other-b3\x00")
	writeFile(t, filepath.Join(procDir, "5757", "cmdline"), "/firecracker\x00--id\x00laug-b3x\x00")
	writeFile(t, filepath.Join(fcRunDir, "b0", "id"), "0123abcd\n")
	writeFile(t, filepath.Join(fcRunDir, "b1", "id"), "4567cdef\n")
	writeFile(t, filepath.Join(fcRunDir, "b1.lock"), "")
	rec := &recorder{}
	return &App{Root: root, Environ: []string{"PATH=/usr/bin:/bin", "FC_DIR=/opt/fc"}, Runner: rec}, rec
}

// fcServe starts a real process to stand in for pool serve -backend firecracker: shell, its command line in procDir
// that of laug pool serve, and the checkout's pid file naming it. It is reaped as soon as it exits; done closes then.
func fcServe(t *testing.T, a *App, shell string) (int, chan struct{}) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", shell)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	pid := cmd.Process.Pid
	// The shell's trap and exec come before the TERM: wait for sleep to show in its real command line.
	waitUntil(t, "the stand-in's sleep", func() bool {
		b, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		return strings.HasPrefix(string(b), "sleep\x00")
	})
	writeFile(t, filepath.Join(procDir, strconv.Itoa(pid), "cmdline"),
		"bin/laug\x00pool\x00serve\x00-backend\x00firecracker\x00")
	writeFile(t, filepath.Join(a.Root, pidFile), strconv.Itoa(pid)+"\n")
	return pid, done
}

// TestFcGoneVMMs: pool down's matcher finds a VMM by its block's socket and a jailed one by its id in either form, and
// passes over another socket, another id and an id that runs on past the block number.
func TestFcGoneVMMs(t *testing.T) {
	a, _ := fcDownWorld(t)
	if got, want := a.fcGoneNow().vmms, []int{5151, 5252, 5454, 5555}; !slices.Equal(got, want) {
		t.Errorf("vmms %v, want %v", got, want)
	}
}

// TestPoolDownFirecracker: as root, pool down -backend firecracker stops the pool serve the pid file names, SIGTERM,
// and leaves the cells; with -remove-cells it then execs cells/backend-firecracker.sh sweep, after a first line that
// counts what goes. -dry-run needs no root: with -remove-cells it prints the sweep's line, alone it says what would
// stop and stay, exit 2. Not as root, or with a pool serve of another backend in the pid file, it refuses, exit 2; a
// pool serve that outlives the wait is exit 1, and the sweep does not run.
func TestPoolDownFirecracker(t *testing.T) {
	// cells counts what fcDownWorld leaves in its /run/laug stand-in, a new one each time.
	cells := func() string { return "2 microVM cells in " + fcRunDir + " and 4 VMMs" }
	a, rec := fcDownWorld(t)
	pid, done := fcServe(t, a, "exec sleep 30")
	p := strconv.Itoa(pid)
	code, out, errOut := runApp(t, a, "pool", "down", "-backend", "firecracker", "-remove-cells")
	want := "laug · pool down: pool serve -backend firecracker, pid " + p + "; " + cells() + ": each cell's disk, its " +
		"data with it, its link and its dir; prep's deny rules and the guest key go\npool serve, pid " + p +
		", stopped\n"
	if code != 0 || out != want || errOut != "" {
		t.Errorf("-remove-cells: exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", code, out, want, errOut)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Errorf("the stand-in pool serve still runs")
	}
	if len(rec.calls) != 1 || rec.hows[0] != script.Exec || rec.calls[0].Script != fcBackend ||
		len(rec.calls[0].Args) != 1 || rec.calls[0].Args[0].Text != "sweep" || !slices.Contains(rec.envs[0],
		"FC_DIR=/opt/fc") {
		t.Errorf("the sweep: %d calls %+v, hows %v, envs %q", len(rec.calls), rec.calls, rec.hows, rec.envs)
	}

	a, rec = fcDownWorld(t)
	pid, done = fcServe(t, a, "exec sleep 30")
	p = strconv.Itoa(pid)
	code, out, errOut = runApp(t, a, "pool", "down", "-backend", "firecracker")
	want = "laug · pool down: stopping pool serve -backend firecracker, pid " + p + "; " + cells() + " stay: sudo " +
		"bin/laug pool down -backend firecracker -remove-cells removes them\npool serve, pid " + p + ", stopped\n"
	if code != 0 || out != want || errOut != "" || len(rec.calls) != 0 {
		t.Errorf("bare -backend firecracker: exit %d, %d calls\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", code,
			len(rec.calls), out, want, errOut)
	}
	<-done

	a, rec = fcDownWorld(t)
	code, out, errOut = runApp(t, a, "pool", "down", "-backend", "firecracker")
	if code != 0 || errOut != "" || len(rec.calls) != 0 || out != "laug · pool down: stopping nothing: no "+
		"Firecracker pool serve runs; "+cells()+" stay: sudo bin/laug pool down -backend firecracker -remove-cells "+
		"removes them\n" {
		t.Errorf("nothing to stop: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// A pool serve that ignores SIGTERM outlives the wait: exit 1, and no sweep.
	a, rec = fcDownWorld(t)
	savedWait := fcStopWait
	fcStopWait = 300 * time.Millisecond
	defer func() { fcStopWait = savedWait }()
	pid, _ = fcServe(t, a, "trap '' TERM; exec sleep 30")
	code, _, errOut = runApp(t, a, "pool", "down", "-backend", "firecracker", "-remove-cells")
	if code != 1 || len(rec.calls) != 0 || errOut != "laug · pool down: pool serve, pid "+strconv.Itoa(pid)+", still "+
		"runs 300ms after SIGTERM, so the sweep did not run and nothing was removed\n" {
		t.Errorf("a pool serve that stays: exit %d, %d calls\n%s", code, len(rec.calls), errOut)
	}
	fcStopWait = savedWait

	// -dry-run, and the refusals: nothing is stopped or run.
	for _, c := range []struct {
		name       string
		uid        int
		other      bool
		args       []string
		code       int
		out, err   string
		serveAlive bool
	}{
		{"-dry-run -remove-cells, not root", 1000, false, []string{"-remove-cells", "-dry-run"}, 0,
			"cells/backend-firecracker.sh sweep\n", "", true},
		{"-dry-run alone, not root", 1000, false, []string{"-dry-run"}, 2, "", "laug · pool down: would stop pool " +
			"serve -backend firecracker, pid PID; " + "CELLS would stay\nsudo bin/laug pool down -backend firecracker " +
			"-remove-cells removes them as well, each cell's disk, link and dir, with prep's deny rules and the guest " +
			"key\n", true},
		{"not root", 1000, false, []string{"-remove-cells"}, 2, "", "laug · pool down: -backend firecracker runs " +
			"as root, since the pool and its cells are root's: sudo bin/laug pool down -backend firecracker " +
			"-remove-cells\n", true},
		{"not root, bare", 1000, false, nil, 2, "", "laug · pool down: -backend firecracker runs as root, since the " +
			"pool and its cells are root's: sudo bin/laug pool down -backend firecracker\n", true},
		{"another backend's pool serve", 0, true, []string{"-remove-cells"}, 2, "", "laug · pool down: " +
			"cells/run/pool.pid names pid PID, a pool serve on another backend: laug pool down -remove-cells stops " +
			"it; -backend firecracker stops only the Firecracker pool\n", true},
		{"an argument", 0, false, []string{"now"}, 2, "", `laug · pool down: takes no arguments, got ["now"]`, true},
	} {
		a, rec := fcDownWorld(t)
		fakeRoot(t, c.uid)
		pid, done := fcServe(t, a, "exec sleep 30")
		if c.other {
			writeFile(t, filepath.Join(procDir, strconv.Itoa(pid), "cmdline"),
				"bin/laug\x00pool\x00serve\x00-backend\x00compose\x00")
		}
		code, out, errOut := runApp(t, a, append([]string{"pool", "down", "-backend", "firecracker"}, c.args...)...)
		wantErr := strings.NewReplacer("PID", strconv.Itoa(pid), "CELLS", cells()).Replace(c.err)
		if code != c.code || out != c.out || !strings.HasPrefix(errOut, wantErr) || len(rec.calls) != 0 {
			t.Errorf("%s: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s\nwant:\n%s", c.name, code, len(rec.calls), out,
				errOut, wantErr)
		}
		select {
		case <-done:
			t.Errorf("%s: the stand-in pool serve was stopped", c.name)
		default:
		}
	}
}
