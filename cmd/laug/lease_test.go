package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/pool"
)

// tagBackend answers at once, as the fake one does, and prints the tag a lease named as the cell's server_tag.
type tagBackend struct{}

func (tagBackend) Create(_ context.Context, id string, block int, tag string) (pool.Cell, error) {
	return pool.Cell{ID: id, Address: fmt.Sprintf("fake-%s:%d", id, 7233+10*block), ServerTag: tag, Backend: "fake"}, nil
}
func (tagBackend) Destroy(context.Context, string, int, string) error { return nil }
func (tagBackend) Health(context.Context, string, int, string) error  { return nil }
func (tagBackend) Fault(context.Context, string, int, string, string, string) error {
	return nil
}

// leasePool is internal/pool's API under httptest: no warm cell to start with, 4 cells at most, 2 leases a requester,
// 1h at most. It returns the pool's address, host:port, and a count of the requests that reached it.
func leasePool(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	cfg := pool.Config{MinWarm: 0, MaxCells: 4, Quota: 2, MaxLease: time.Hour, CreateTimeout: time.Second,
		HealthPoll: 10 * time.Millisecond, ReapInterval: time.Second}
	h := pool.Handler(tagBackend{}, cfg, log.New(io.Discard, "", 0))
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &n
}

// leaseDoc is the last-lease file as a map, and its bytes.
func leaseDoc(t *testing.T, path string) (map[string]any, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return m, string(b)
}

func keysOf(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// leaseKeys are the keys of a lease of the fake backend's cell: the pool's entry, with no key the backend left empty;
// backend is never left empty.
var leaseKeys = []string{"address", "backend", "block", "expires_at", "id", "leased_at", "requester", "state"}

// The first line of a lease in text: the id, the requester, the expiry in UTC, the file, and the line that gives it
// back.
var leaseLine = regexp.MustCompile(`^laug · lease: ([0-9a-f]{8}) for laug until ([0-9]{4}-[0-9]{2}-[0-9]{2} ` +
	`[0-9]{2}:[0-9]{2}:[0-9]{2}) UTC(.*); kept in (\S+); laug release ([0-9a-f]{8})(.*) gives it back$`)

// TestLease leases from internal/pool in a checkout, as laug, never as the login name in the environment: the first
// line names the lease, its expiry in UTC, the file and the line that gives it back; the lease follows, indented as
// jq . printed it; .last-lease.json at the root holds the pool's answer byte for byte, 0600, with no temp file
// beside it. -json prints the answer alone, on one line, the file's bytes. A TTL over the pool's max lease is cut and
// says so; -server-tag reaches the backend. A lease past the quota is refused, exit 1, and the file stays as it was.
func TestLease(t *testing.T) {
	addr, _ := leasePool(t)
	root := checkout(t)
	app := func() *App { return &App{Root: root, Environ: []string{"USER=someone-else", "LOGNAME=someone-else"}} }
	path := filepath.Join(root, ".last-lease.json")

	code, out, errOut := runApp(t, app(), "lease", "-pool", addr)
	lines := strings.SplitN(out, "\n", 2)
	m := leaseLine.FindStringSubmatch(lines[0])
	if code != 0 || errOut != "" || m == nil || m[3] != "" || m[4] != ".last-lease.json" || m[5] != m[1] ||
		m[6] != " -pool "+addr {
		t.Fatalf("lease: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	doc, raw := leaseDoc(t, path)
	var indented strings.Builder
	app().writeIndented(&indented, []byte(raw))
	if lines[1] != indented.String() || !strings.HasPrefix(lines[1], "{\n  \"id\": \""+m[1]+"\",\n") {
		t.Errorf("lease: the document after the first line\n%s\nwant\n%s", lines[1], indented.String())
	}
	leased, _ := time.Parse(time.RFC3339Nano, doc["leased_at"].(string))
	expires, _ := time.Parse(time.RFC3339Nano, doc["expires_at"].(string))
	if doc["id"] != m[1] || doc["requester"] != "laug" || doc["state"] != "leased" || expires.Sub(leased) != 30*time.Minute ||
		expires.UTC().Format("2006-01-02 15:04:05") != m[2] || !reflect.DeepEqual(keysOf(doc), leaseKeys) ||
		strings.Count(raw, "\n") != 1 {
		t.Errorf("lease: the file\n%s\nthe line %q", raw, lines[0])
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("lease: the file's mode: %v, %v", fi.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".lease.*")); left != nil {
		t.Errorf("lease: temp files left: %q", left)
	}

	code, out, errOut = runApp(t, app(), "lease", "-json", "-pool", "http://"+addr, "-ttl", "3h", "-server-tag", "1.32.1")
	doc, raw = leaseDoc(t, path)
	if code != 0 || errOut != "" || out != raw || strings.Contains(out, "laug ·") || doc["server_tag"] != "1.32.1" ||
		!reflect.DeepEqual(keysOf(doc), append(slices.Clone(leaseKeys[:7]), "server_tag", "state")) {
		t.Errorf("lease -json: exit %d\nstdout:\n%s\nstderr:\n%s\nthe file:\n%s", code, out, errOut, raw)
	}
	leased, _ = time.Parse(time.RFC3339Nano, doc["leased_at"].(string))
	expires, _ = time.Parse(time.RFC3339Nano, doc["expires_at"].(string))
	if expires.Sub(leased) != time.Hour {
		t.Errorf("lease -ttl 3h: %s, want the pool's max lease, 1h", expires.Sub(leased))
	}

	// A third lease as laug passes the quota of 2: refused, exit 1, the file as it was.
	code, out, errOut = runApp(t, app(), "lease", "-pool", addr, "-ttl", "3h")
	if code != 1 || out != "" || errOut != "laug · lease: the pool on "+addr+" refused: quota: laug already holds 2 "+
		"lease(s) (409 Conflict)\n" {
		t.Errorf("a third lease: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if _, after := leaseDoc(t, path); after != raw {
		t.Errorf("a refused lease changed the file:\n%s\nwas\n%s", after, raw)
	}

	// Another requester has its own quota, and a TTL over the max lease is cut and says so.
	code, out, _ = runApp(t, app(), "lease", "-pool", addr, "-ttl", "3h", "-requester", "ci", "-file", "ci.json")
	if code != 0 || !strings.Contains(firstLine(out), " for ci until ") ||
		!strings.Contains(firstLine(out), " UTC, cut from -ttl 3h0m0s to the pool's max lease, 1h0m0s; kept in ci.json; ") ||
		!strings.HasSuffix(firstLine(out), " -pool "+addr+" -file ci.json gives it back") {
		t.Errorf("lease -requester ci -ttl 3h: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "ci.json")); err != nil {
		t.Errorf("-file ci.json: not at the root: %v", err)
	}
}

// dynBackend answers at once, as tagBackend does, with a cell whose dynamic config file is at an absolute path under
// dir, as a compose cell's is under the checkout: cells/run/laug-<id>/dynamicconfig/laug.yaml.
type dynBackend struct{ dir string }

func (b dynBackend) Create(_ context.Context, id string, block int, _ string) (pool.Cell, error) {
	return pool.Cell{ID: id, Address: fmt.Sprintf("127.0.0.1:%d", 7233+10*block),
		DynConfig: filepath.Join(b.dir, "cells", "run", "laug-"+id, "dynamicconfig", "laug.yaml")}, nil
}
func (dynBackend) Destroy(context.Context, string, int, string) error { return nil }
func (dynBackend) Health(context.Context, string, int, string) error  { return nil }
func (dynBackend) Fault(context.Context, string, int, string, string, string) error {
	return nil
}

// TestLeasePathsShown: a compose cell's lease names its dynamic config by its absolute path, under the checkout, and
// the checkout is under $HOME on most hosts. lease and reset print the lease with that path from the checkout's root,
// and one under $HOME outside it as ~/; the last-lease file and -json keep the path whole, as the scripts read it.
func TestLeasePathsShown(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "laug")
	for _, f := range []string{"go.mod", "gate/run.sh"} {
		writeFile(t, filepath.Join(root, f), "")
	}
	for _, c := range []struct {
		name, dir, shown string // the backend's dir, and the start of dynconfig as printed
	}{
		{"under the checkout", root, "cells/run/laug-"},
		{"under $HOME, outside the checkout", filepath.Join(home, "elsewhere"), "~/elsewhere/cells/run/laug-"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := pool.Config{MinWarm: 0, MaxCells: 2, Quota: 2, MaxLease: time.Hour, CreateTimeout: time.Second,
				HealthPoll: 10 * time.Millisecond, ReapInterval: time.Second}
			srv := httptest.NewServer(pool.Handler(dynBackend{c.dir}, cfg, log.New(io.Discard, "", 0)))
			t.Cleanup(srv.Close)
			addr := strings.TrimPrefix(srv.URL, "http://")
			app := func() *App { return &App{Root: root, Environ: []string{"HOME=" + home}} }
			code, out, errOut := runApp(t, app(), "lease", "-pool", addr)
			doc, raw := leaseDoc(t, filepath.Join(root, ".last-lease.json"))
			id, _ := doc["id"].(string)
			whole := filepath.Join(c.dir, "cells", "run", "laug-"+id, "dynamicconfig", "laug.yaml")
			line := "  \"dynconfig\": \"" + c.shown + id + "/dynamicconfig/laug.yaml\","
			if code != 0 || errOut != "" || strings.Contains(out, home) || !slices.Contains(strings.Split(out, "\n"), line) {
				t.Errorf("lease: exit %d, want 0, no %s and the line %q\nstdout:\n%s\nstderr:\n%s", code, home, line,
					out, errOut)
			}
			if doc["dynconfig"] != whole {
				t.Errorf("the last-lease file's dynconfig: %v, want %s\n%s", doc["dynconfig"], whole, raw)
			}
			code, out, errOut = runApp(t, app(), "reset", "-last", "-pool", addr)
			if code != 0 || errOut != "" || strings.Contains(out, home) || !slices.Contains(strings.Split(out, "\n"), line) {
				t.Errorf("reset: exit %d, want 0, no %s and the line %q\nstdout:\n%s\nstderr:\n%s", code, home, line,
					out, errOut)
			}
			code, out, _ = runApp(t, app(), "lease", "-json", "-pool", addr)
			if _, raw = leaseDoc(t, filepath.Join(root, ".last-lease.json")); code != 0 || out != raw ||
				!strings.Contains(out, c.dir) {
				t.Errorf("lease -json: exit %d, want the file's bytes, with %s whole\nstdout:\n%s\nthe file:\n%s", code,
					c.dir, out, raw)
			}
		})
	}
}

// Outside a checkout the file goes to the working directory; an absolute -file goes where it says.
func TestLeaseOutsideACheckout(t *testing.T) {
	addr, _ := leasePool(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if code, out, errOut := runApp(t, &App{}, "lease", "-pool", addr); code != 0 ||
		!strings.Contains(firstLine(out), "; kept in .last-lease.json; ") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, ".last-lease.json")); err != nil {
		t.Errorf("not in the working directory: %v", err)
	}
	abs := filepath.Join(t.TempDir(), "lease.json")
	if code, _, errOut := runApp(t, &App{Root: checkout(t)}, "lease", "-pool", addr, "-file", abs); code != 0 {
		t.Fatalf("-file %s: exit %d\n%s", abs, code, errOut)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("-file %s: %v", abs, err)
	}
}

// lease refuses, exit 2 and one line before the usage when it is a usage mistake, a pool off loopback and a value the
// pool would refuse or misread, with no request sent and no file written. Nothing answering is exit 1. A file that
// cannot be written after the pool leased is exit 1, with the id and the line that gives it back.
func TestLeaseRefusals(t *testing.T) {
	addr, requests := leasePool(t)
	gone := refusedAddr(t)
	for _, c := range []struct {
		args  []string
		code  int
		first string
	}{
		{[]string{"-pool", "0.0.0.0:7700"}, 2, `laug · lease: -pool 0.0.0.0:7700: host "0.0.0.0" is not a loopback ` +
			"address: the API has no auth, so it listens on loopback only"},
		{[]string{"-pool", "http://[2001:db8::1]:7700"}, 2,
			`laug · lease: -pool http://[2001:db8::1]:7700: host "2001:db8::1" is not a loopback address`},
		{[]string{"-pool", "localhost:7700"}, 2,
			`laug · lease: -pool localhost:7700: host "localhost" is not a loopback address`},
		{[]string{"-pool", addr + "/lease"}, 2, "laug · lease: -pool " + addr + "/lease: not host:port or an http:// URL"},
		{[]string{"-ttl", "0s"}, 2, "laug · lease: -ttl 0s: 0 or less"},
		{[]string{"-ttl", "-5m"}, 2, "laug · lease: -ttl -5m0s: 0 or less"},
		{[]string{"-ttl", "1500ms"}, 2, "laug · lease: -ttl 1.5s: not a whole number of seconds"},
		{[]string{"-ttl", "1800"}, 2, "laug · lease: -ttl 1800: a duration, 30m say"},
		{[]string{"-requester", ""}, 2, "laug · lease: -requester is empty"},
		{[]string{"-server-tag", "latest"}, 2, "laug · lease: -server-tag latest: not a release tag, x.y.z"},
		{[]string{"now"}, 2, `laug · lease: takes no arguments, got ["now"]`},
		{[]string{"-dev"}, 2, "laug · lease: no flag -dev"},
		{[]string{"-pool", gone}, 1, "laug · lease: no pool answers on " + gone + " (connection refused); laug pool " +
			"serve -listen " + gone + " starts one, on fake cells by default"},
	} {
		root := checkout(t)
		args := append([]string{"lease", "-pool", addr}, c.args...)
		code, out, errOut := runApp(t, &App{Root: root}, args...)
		if code != c.code || out != "" || !strings.HasPrefix(firstLine(errOut), c.first) ||
			c.code == 1 && strings.Count(errOut, "\n") != 1 {
			t.Errorf("lease %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.args, code, c.code, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(root, ".last-lease.json")); !os.IsNotExist(err) {
			t.Errorf("lease %q wrote .last-lease.json: %v", c.args, err)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d requests reached the pool, want 0", n)
	}

	root := checkout(t)
	code, out, errOut := runApp(t, &App{Root: root}, "lease", "-pool", addr, "-file", "missing/lease.json")
	m := regexp.MustCompile(`^laug · lease: leased ([0-9a-f]{8}) but could not keep it in missing/lease.json: .*; ` +
		`laug release ([0-9a-f]{8}) -pool ` + regexp.QuoteMeta(addr) + ` -file missing/lease.json gives it back\n$`).
		FindStringSubmatch(errOut)
	if code != 1 || out != "" || m == nil || m[1] != m[2] {
		t.Errorf("a file that cannot be written: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".lease.*")); left != nil {
		t.Errorf("temp files left: %q", left)
	}
}

// TestNoPool: nothing answering is said in laug's words, a port that refuses and a wait that ran out in two or three,
// anything else in Go's; a lease, and not a release, names the line that starts a pool, with -listen only off the
// default address.
func TestNoPool(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	for _, c := range []struct {
		err  error
		want string
	}{
		{refused, " (connection refused)"},
		{timeout, " (no answer in time)"},
		{context.DeadlineExceeded, " (no answer in time)"},
		{io.ErrUnexpectedEOF, ": unexpected EOF"},
	} {
		if got := noAnswer(c.err); got != c.want {
			t.Errorf("noAnswer(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	a := &App{Self: "bin/laug"}
	for addr, want := range map[string]string{
		pool.DefaultListen:             "bin/laug pool serve starts one, on fake cells by default",
		"http://" + pool.DefaultListen: "bin/laug pool serve starts one, on fake cells by default",
		"127.0.0.1:7800":               "bin/laug pool serve -listen 127.0.0.1:7800 starts one, on fake cells by default",
	} {
		if got := a.poolServe(addr); got != want {
			t.Errorf("poolServe(%s) = %q, want %q", addr, got, want)
		}
	}
	for _, c := range []struct{ op, id, note, want string }{
		{"lease", "", "", "laug · raun: no pool answers on " + pool.DefaultListen + " (connection refused); bin/laug " +
			"pool serve starts one, on fake cells by default\n"},
		{"release", "0a0b0c0d", "bin/laug release 0a0b0c0d gives it back", "laug · raun: no pool answers on " +
			pool.DefaultListen + " (connection refused); bin/laug release 0a0b0c0d gives it back\n"},
	} {
		var stderr strings.Builder
		a := &App{Self: "bin/laug", Stderr: &stderr}
		if code := poolFailedAs(context.Background(), a, "raun", c.op, pool.DefaultListen, c.id, c.note,
			refused); code != exitFailed || stderr.String() != c.want {
			t.Errorf("%s: exit %d, stderr %q, want %q", c.op, code, stderr.String(), c.want)
		}
	}
}

// A context that ends while the pool builds the cell, as a signal ends it, stops the wait: exit 1, one line, no file.
func TestLeaseStopped(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	t.Cleanup(func() { close(hold); srv.Close() })
	root := checkout(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var stdout, stderr strings.Builder
	code := run(ctx, &App{Root: root, Stdout: &stdout, Stderr: &stderr}, []string{"lease", "-pool", srv.URL})
	if code != 1 || stdout.Len() != 0 || stderr.String() != "laug · lease: stopped before the pool on "+srv.URL+
		" answered; the pool gives back a cell it could not hand over\n" {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".last-lease.json")); !os.IsNotExist(err) {
		t.Errorf("a stopped lease wrote .last-lease.json: %v", err)
	}
}

// slowBackend builds a cell in 300 ms, as a cold cell takes its time.
type slowBackend struct{ tagBackend }

func (b slowBackend) Create(ctx context.Context, id string, block int, tag string) (pool.Cell, error) {
	time.Sleep(300 * time.Millisecond)
	return b.tagBackend.Create(ctx, id, block, tag)
}

// TestLeaseWaits: lease, raun and candidate ask the pool for a cell, and a pool that has not answered in waitSay gets
// one stderr line, before the verb's own lines: that it waits for a cell from that pool, and that Ctrl-C stops the
// wait. A pool that answers at once gets none (TestLease, TestRaun).
func TestLeaseWaits(t *testing.T) {
	saved := waitSay
	waitSay = 50 * time.Millisecond
	t.Cleanup(func() { waitSay = saved })
	cfg := pool.Config{MinWarm: 0, MaxCells: 4, Quota: 4, MaxLease: time.Hour, CreateTimeout: 5 * time.Second,
		HealthPoll: 10 * time.Millisecond, ReapInterval: time.Second}
	srv := httptest.NewServer(pool.Handler(slowBackend{}, cfg, log.New(io.Discard, "", 0)))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	for _, verb := range []string{"lease", "raun", "candidate"} {
		t.Run(verb, func(t *testing.T) {
			a, _ := matrixApp(t, envCase{})
			args := map[string][]string{"lease": {"lease"}, "raun": {"raun", "persistence-latency"},
				"candidate": {"candidate", "benign"}}[verb]
			code, out, errOut := runApp(t, a, append(args, "-pool", addr)...)
			want := "laug · " + verb + ": waiting for a cell from the pool on " + addr + "; Ctrl-C stops the wait\n"
			if code != 0 || strings.Count(errOut, want) != 1 || !strings.HasPrefix(out, "laug · "+verb+": ") {
				t.Errorf("%s from a slow pool: exit %d\nstdout:\n%s\nstderr:\n%s\nwant one line %q", verb, code, out,
					errOut, want)
			}
		})
	}
}
