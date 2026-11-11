package poolapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/poolapi"
)

// cfg is a small pool: no warm cell to start with, room for 3, 2 leases a requester, 1h at most.
var cfg = pool.Config{MinWarm: 0, MaxCells: 3, Quota: 2, MaxLease: time.Hour, CreateTimeout: time.Second,
	HealthPoll: 10 * time.Millisecond, ReapInterval: time.Second}

// newPool runs internal/pool's API under httptest on b and returns a client of it.
func newPool(t *testing.T, b pool.Backend, c pool.Config) *poolapi.Client {
	t.Helper()
	srv := httptest.NewServer(pool.Handler(b, c, log.New(io.Discard, "", 0)))
	t.Cleanup(srv.Close)
	client, err := poolapi.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestLeaseResetRelease drives the fake backend's pool through one lease's life: a lease for 30 min as laug, a reset
// that keeps id, block and expiry, a release, and then a second release and a reset of the same id, which the pool
// refuses with 404.
func TestLeaseResetRelease(t *testing.T) {
	ctx := context.Background()
	c := newPool(t, pool.Fake(), cfg)
	l, doc, err := c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 1800, Requester: "laug"})
	if err != nil {
		t.Fatal(err)
	}
	if l.ID == "" || l.State != "leased" || l.Requester != "laug" || l.Address == "" ||
		l.ExpiresAt.Sub(l.LeasedAt) != 30*time.Minute || !bytes.HasSuffix(doc, []byte("}\n")) {
		t.Errorf("lease %+v\n%s", l, doc)
	}
	st, _, err := c.Status(ctx)
	if err != nil || len(st.Cells) != 1 || st.Cells[0].ID != l.ID || st.Cells[0].State != "leased" || st.MaxLease != "1h0m0s" {
		t.Errorf("status after the lease: %+v, %v", st, err)
	}

	r, rdoc, err := c.Reset(ctx, l.ID)
	if err != nil || r.ID != l.ID || r.Block != l.Block || !r.ExpiresAt.Equal(l.ExpiresAt) || r.State != "leased" ||
		len(rdoc) == 0 {
		t.Errorf("reset: %+v, %v; lease %+v", r, err, l)
	}

	doc, err = c.Release(ctx, l.ID)
	if err != nil || string(doc) != `{"released":"`+l.ID+`"}`+"\n" {
		t.Errorf("release: %q, %v", doc, err)
	}
	for _, again := range []func() error{
		func() error { _, err := c.Release(ctx, l.ID); return err },
		func() error { _, _, err := c.Reset(ctx, l.ID); return err },
	} {
		var pe *poolapi.Error
		if err := again(); !errors.As(err, &pe) || pe.Code != 404 || pe.Text != "cell not found" {
			t.Errorf("after the release: %v, want 404 cell not found", err)
		}
	}
}

// The pool's refusals come back as *Error with its text: a lease past the quota and a tag that is not x.y.z, both
// before any cell is built.
func TestLeaseRefused(t *testing.T) {
	ctx := context.Background()
	c := newPool(t, pool.Fake(), cfg)
	for range 2 {
		if _, _, err := c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 60, Requester: "laug"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, x := range []struct {
		req  poolapi.LeaseRequest
		want string
	}{
		{poolapi.LeaseRequest{TTLSeconds: 60, Requester: "laug"},
			"POST /lease answered 409 Conflict: quota: laug already holds 2 lease(s)"},
		{poolapi.LeaseRequest{TTLSeconds: 60, Requester: "other", ServerTag: "latest"},
			`POST /lease answered 400 Bad Request: server_tag must be a release tag, x.y.z; got "latest"`},
	} {
		_, doc, err := c.Lease(ctx, x.req)
		var pe *poolapi.Error
		if !errors.As(err, &pe) || err.Error() != x.want || doc != nil {
			t.Errorf("lease %+v: %v, want %s", x.req, err, x.want)
		}
	}
	if !poolapi.TagRE.MatchString("1.32.1") || poolapi.TagRE.MatchString("1.32") || poolapi.TagRE.MatchString("v1.32.1") {
		t.Error("TagRE is not x.y.z")
	}
}

// fullBackend prints a cell with every field the backend contract names, so the pool's answers hold every key.
type fullBackend struct{}

func (fullBackend) Create(_ context.Context, id string, block int, tag string) (pool.Cell, error) {
	return pool.Cell{ID: id, Address: "127.0.0.1:7233", UI: "http://127.0.0.1:8233", Grafana: "http://127.0.0.1:3000",
		Prom: "http://127.0.0.1:9090", Toxiproxy: "http://127.0.0.1:8474", Project: "laug-" + id,
		DynConfig: "cells/run/laug-" + id + "/dynamicconfig.yaml", SSH: "root@fake-" + id, ServerTag: tag,
		Backend: "firecracker", FCVersion: "v1.17.0", CacheType: "Unsafe"}, nil
}
func (fullBackend) Destroy(context.Context, string, int, string) error { return nil }
func (fullBackend) Health(context.Context, string, int, string) error  { return nil }
func (fullBackend) Fault(context.Context, string, int, string, string, string) error {
	return nil
}

// TestShapes holds the client's types to the pool's answers: a lease, a reset and a status, with every key present,
// decode with no unknown field and encode back to the same bytes, keys in the same order.
func TestShapes(t *testing.T) {
	ctx := context.Background()
	c := newPool(t, fullBackend{}, cfg)
	_, doc, err := c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 600, Requester: "laug", ServerTag: "1.32.1"})
	if err != nil {
		t.Fatal(err)
	}
	var l poolapi.Lease
	roundTrip(t, "lease", doc, &l)
	if l.SSH == "" || l.ServerTag != "1.32.1" || l.Backend != "firecracker" || l.FCVersion != "v1.17.0" || l.CacheType != "Unsafe" {
		t.Errorf("lease: not every key came back: %s", doc)
	}
	_, doc, err = c.Reset(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, "reset", doc, &poolapi.Lease{})
	_, doc, err = c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, "status", doc, &poolapi.Status{})
}

// roundTrip decodes doc into v, refusing a key v lacks, and encodes v back to doc's bytes.
func roundTrip(t *testing.T, what string, doc []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v\n%s", what, err, doc)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(b)+"\n" != string(doc) {
		t.Errorf("%s: the client's type encodes\n%s\nthe pool sent\n%s", what, b, doc)
	}
}

// TestRequests holds what the client sends: make lease's body, {"ttl_seconds", "requester"}, with server_tag only
// when one is named, {"id"} for reset and release, and {"id", "kind", "body"} for vmfault, each a POST of
// application/json with the URL's host:port as its Host, as the pool requires.
func TestRequests(t *testing.T) {
	var got, hosts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type")+" "+string(b))
		hosts = append(hosts, r.Host)
		switch r.URL.Path {
		case "/release":
			w.Write([]byte(`{"released":"0a0b0c0d"}` + "\n"))
		case "/vmfault":
			w.Write([]byte(`{"ok":true}` + "\n"))
		default:
			w.Write([]byte(`{"id":"0a0b0c0d","address":"fake-0a0b0c0d:7233","block":0,"state":"leased"}` + "\n"))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := poolapi.New(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 1800, Requester: "laug"})
	c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 60, Requester: `a "b"`, ServerTag: "1.32.1"})
	c.Reset(ctx, "0a0b0c0d")
	c.Release(ctx, "0a0b0c0d")
	c.VMFault(ctx, "0a0b0c0d", "drive", json.RawMessage(`{"drive_id":"rootfs"}`))
	want := []string{
		`POST /lease application/json {"ttl_seconds":1800,"requester":"laug"}`,
		`POST /lease application/json {"ttl_seconds":60,"requester":"a \"b\"","server_tag":"1.32.1"}`,
		`POST /reset application/json {"id":"0a0b0c0d"}`,
		`POST /release application/json {"id":"0a0b0c0d"}`,
		`POST /vmfault application/json {"id":"0a0b0c0d","kind":"drive","body":{"drive_id":"rootfs"}}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	for i, h := range hosts {
		if h != host {
			t.Errorf("request %d went with Host %q, want %q", i+1, h, host)
		}
	}
}

// TestNew takes a loopback IP with a port, bare or as an http:// or https:// URL with no path, and refuses every
// other host, a name included, and anything with a path, a query or a user.
func TestNew(t *testing.T) {
	for addr, base := range map[string]string{
		"127.0.0.1:7700": "http://127.0.0.1:7700", "http://127.0.0.1:7700/": "http://127.0.0.1:7700",
		"https://127.0.0.2:7700": "https://127.0.0.2:7700", "[::1]:7700": "http://[::1]:7700",
		"http://127.0.0.1": "http://127.0.0.1",
	} {
		c, err := poolapi.New(addr)
		if err != nil || c.Base() != base {
			t.Errorf("New(%q): %v, want base %s", addr, err, base)
		}
	}
	for addr, want := range map[string]string{
		"[2001:db8::1]:7700":        `host "2001:db8::1" is not a loopback address: the API has no auth`,
		"example.com:7700":          `host "example.com" is not a loopback address`,
		"http://0.0.0.0:7700":       `host "0.0.0.0" is not a loopback address`,
		"localhost:7700":            `host "localhost" is not a loopback address`,
		"http://[::]:7700":          `host "::" is not a loopback address`,
		":7700":                     `host "" is not a loopback address`,
		"ftp://127.0.0.1:7700":      "not host:port or an http:// URL with no path",
		"127.0.0.1:7700/status":     "not host:port or an http:// URL with no path",
		"http://127.0.0.1:7700?x=1": "not host:port or an http:// URL with no path",
		"http://u@127.0.0.1:7700":   "not host:port or an http:// URL with no path",
		"":                          "not host:port or an http:// URL with no path",
	} {
		if _, err := poolapi.New(addr); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("New(%q): %v, want %q", addr, err, want)
		}
	}
}

// refusedAddr is a loopback address that refuses every connection while the test runs: a socket bound to a port
// and never listening. The kernel answers a connect there with a reset, and binds no other socket to the port while
// this one holds it, so no server a test starts after can answer there. A listener closed at once frees its port,
// and the next httptest server could get it (seen once in cmd/laug: pool status -pool <gone> met a server that is
// not a pool). cmd/laug's tests hold a copy, as a test helper is not shared between packages.
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

// TestNotAPool: nothing listening is a dial error without net/url's wrapping; a server that answers 500 with no
// {"error"} is *Error with no text; a 200 that is not the document asked for is ErrNotPool.
func TestNotAPool(t *testing.T) {
	gone := refusedAddr(t)
	c, _ := poolapi.New(gone)
	if _, _, err := c.Status(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "dial tcp "+gone+": ") {
		t.Errorf("status of a closed port: %v", err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	c, _ = poolapi.New(broken.URL)
	var pe *poolapi.Error
	if _, _, err := c.Status(context.Background()); !errors.As(err, &pe) || pe.Text != "" ||
		err.Error() != "GET /status answered 500 Internal Server Error" {
		t.Errorf("status of a broken server: %v", err)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"cells":[]}`))
	}))
	t.Cleanup(other.Close)
	c, _ = poolapi.New(other.URL)
	ctx := context.Background()
	_, _, serr := c.Status(ctx)
	_, _, lerr := c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 60, Requester: "laug"})
	_, _, rerr := c.Reset(ctx, "0a0b0c0d")
	_, relerr := c.Release(ctx, "0a0b0c0d")
	for _, err := range []error{serr, lerr, rerr, relerr} {
		if !errors.Is(err, poolapi.ErrNotPool) {
			t.Errorf("a server that is not a pool: %v, want ErrNotPool", err)
		}
	}
}

// A context that ends while a lease waits ends the request, and the error says so.
func TestLeaseCancelled(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	t.Cleanup(func() { close(hold); srv.Close() })
	c, _ := poolapi.New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 60, Requester: "laug"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("lease past its context: %v", err)
	}
}

// faultBackend is fullBackend with a Fault that keeps each call's id, kind and body.
type faultBackend struct {
	fullBackend
	calls *[]string
}

func (b faultBackend) Fault(_ context.Context, id string, _ int, _, kind, body string) error {
	*b.calls = append(*b.calls, id+" "+kind+" "+body)
	return nil
}

// TestVMFault holds VMFault to the shape the pool's route reads: the pool hands the backend the lease's id, the kind
// and the body as compact JSON, and answers {"ok":true}; an id it does not hold comes back as *Error, 404.
func TestVMFault(t *testing.T) {
	ctx := context.Background()
	var calls []string
	c := newPool(t, faultBackend{calls: &calls}, cfg)
	l, _, err := c.Lease(ctx, poolapi.LeaseRequest{TTLSeconds: 600, Requester: "laug"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := c.VMFault(ctx, l.ID, "drive", json.RawMessage(`{ "drive_id": "rootfs",
		"rate_limiter": {"ops": {"size": 100, "refill_time": 1000}} }`))
	if err != nil || string(doc) != `{"ok":true}`+"\n" {
		t.Fatalf("vmfault: %q, %v", doc, err)
	}
	want := l.ID + ` drive {"drive_id":"rootfs","rate_limiter":{"ops":{"size":100,"refill_time":1000}}}`
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("backend got %q, want [%s]", calls, want)
	}
	var pe *poolapi.Error
	if _, err := c.VMFault(ctx, "0badcell", "drive", json.RawMessage(`{}`)); !errors.As(err, &pe) || pe.Code != 404 {
		t.Errorf("vmfault on an unknown cell: %v, want 404", err)
	}
}
