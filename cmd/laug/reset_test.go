package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/poolapi"
)

// leaseIn leases a cell from the pool at addr into root's .last-lease.json and returns its id, its expiry as laug
// prints it, in UTC, and the file's bytes.
func leaseIn(t *testing.T, root, addr string) (id, until, raw string) {
	t.Helper()
	if code, out, errOut := runApp(t, &App{Root: root}, "lease", "-pool", addr); code != 0 {
		t.Fatalf("lease: exit %d\n%s%s", code, out, errOut)
	}
	doc, raw := leaseDoc(t, filepath.Join(root, ".last-lease.json"))
	expires, err := time.Parse(time.RFC3339Nano, doc["expires_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return doc["id"].(string), expires.UTC().Format("2006-01-02 15:04:05") + " UTC", raw
}

// TestReset rebuilds the last lease's cell, by -last and by id: the first line says so, with the same expiry in UTC
// and the line that gives it back; the lease follows, indented; -json prints the pool's answer alone, with the keys
// of a lease; the last-lease file stays byte for byte. Bare, it prints the last lease's id and the line to type, or
// that there is none, on stderr with exit 2. A lease the pool does not hold is exit 1.
func TestReset(t *testing.T) {
	addr, _ := leasePool(t)
	root := checkout(t)
	path := filepath.Join(root, ".last-lease.json")

	code, out, errOut := runApp(t, &App{Root: root}, "reset")
	if code != 2 || out != "" || !strings.HasPrefix(errOut, "laug · reset: names no lease: give it a lease id, or -last\n"+
		".last-lease.json holds no lease; laug lease keeps one there\nusage: laug reset <id> | -last ") {
		t.Errorf("bare, no lease: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-last")
	if code != 2 || out != "" || errOut != "laug · reset: -last: .last-lease.json holds no lease; laug lease keeps one there\n" {
		t.Errorf("-last, no lease: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	id, until, raw := leaseIn(t, root, addr)
	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-pool", addr)
	if code != 2 || out != "" || !strings.HasPrefix(errOut, "laug · reset: names no lease: give it a lease id, or -last\n"+
		"the last lease, in .last-lease.json, is "+id+", for laug until "+until+"; to reset it:\n"+
		"  laug reset "+id+" -pool "+addr+"\nusage: laug reset ") {
		t.Errorf("bare: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-last", "-pool", addr)
	first, rest, _ := strings.Cut(out, "\n")
	if code != 0 || errOut != "" || first != "laug · reset: "+id+" rebuilt under the same lease, for laug until "+until+
		"; laug release "+id+" -pool "+addr+" gives it back" || !strings.HasPrefix(rest, "{\n  \"id\": \""+id+"\",\n") ||
		!strings.Contains(rest, "\n  \"state\": \"leased\",\n") || !strings.HasSuffix(rest, "\"\n}\n") {
		t.Errorf("reset -last: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	code, out, errOut = runApp(t, &App{Root: root}, "reset", id, "-json", "-pool", addr)
	var doc, was map[string]any
	json.Unmarshal([]byte(raw), &was)
	if code != 0 || errOut != "" || strings.Count(out, "\n") != 1 || json.Unmarshal([]byte(out), &doc) != nil ||
		doc["id"] != id || doc["block"] != was["block"] || doc["expires_at"] != was["expires_at"] ||
		!reflect.DeepEqual(keysOf(doc), leaseKeys) {
		t.Errorf("reset <id> -json: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(path); string(b) != raw {
		t.Errorf("reset changed .last-lease.json:\n%s\nwas\n%s", b, raw)
	}

	for _, c := range []struct {
		args  []string
		code  int
		first string
	}{
		{[]string{"ffffffff"}, 1, "laug · reset: the pool on " + addr + " refused ffffffff: cell not found (404 Not Found)\n"},
		{[]string{id, "-last"}, 2, "laug · reset: takes a lease id or -last, not both\nusage: "},
		{[]string{id, id}, 2, `laug · reset: takes one lease id, got ["` + id + `" "` + id + `"]` + "\nusage: "},
		{[]string{""}, 2, "laug · reset: the lease id is empty\nusage: "},
		{[]string{id, "-pool", "0.0.0.0:7700"}, 2, `laug · reset: -pool 0.0.0.0:7700: host "0.0.0.0" is not a loopback`},
	} {
		code, out, errOut := runApp(t, &App{Root: root}, append([]string{"reset", "-pool", addr}, c.args...)...)
		if code != c.code || out != "" || !strings.HasPrefix(errOut, c.first) {
			t.Errorf("reset %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", c.args, code, c.code, out, errOut)
		}
	}
	os.WriteFile(path, []byte(`{"address":"x"}`), 0o600)
	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-last", "-pool", addr)
	if code != 2 || out != "" || errOut != "laug · reset: -last: .last-lease.json holds no lease: not a lease: no id\n" {
		t.Errorf("-last, a file with no id: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// failingBackend is tagBackend with three switches. Its create fails while fail is set, and its destroy while stuck is
// set. While hold holds a channel, its create sends on held and waits for that channel to close.
type failingBackend struct {
	tagBackend
	fail, stuck atomic.Bool
	hold        atomic.Pointer[chan struct{}]
	held        chan struct{}
}

func (b *failingBackend) Create(ctx context.Context, id string, block int, tag string) (pool.Cell, error) {
	if h := b.hold.Load(); h != nil {
		b.held <- struct{}{}
		select {
		case <-*h:
		case <-ctx.Done():
			return pool.Cell{}, ctx.Err()
		}
	}
	if b.fail.Load() {
		return pool.Cell{}, errors.New("create failed")
	}
	return b.tagBackend.Create(ctx, id, block, tag)
}

func (b *failingBackend) Destroy(context.Context, string, int, string) error {
	if b.stuck.Load() {
		return errors.New("destroy failed")
	}
	return nil
}

// TestResetFailed holds a failed reset to what it does to the last-lease file. A create that fails ends the lease:
// the pool forgets it, or keeps its cell as stuck when the destroy fails too. Then the file that names it goes, and the
// line says so. A cell busy with another reset, a 404 and no answer leave the file, and the line says it still names
// the lease. Each is exit 1.
func TestResetFailed(t *testing.T) {
	b := &failingBackend{held: make(chan struct{})}
	cfg := pool.Config{MinWarm: 0, MaxCells: 4, Quota: 2, MaxLease: time.Hour, CreateTimeout: 10 * time.Second,
		HealthPoll: 10 * time.Millisecond, ReapInterval: time.Second}
	srv := httptest.NewServer(pool.Handler(b, cfg, log.New(io.Discard, "", 0)))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	root := checkout(t)
	path := filepath.Join(root, ".last-lease.json")
	kept := func(what, raw string) {
		t.Helper()
		if b, err := os.ReadFile(path); err != nil || string(b) != raw {
			t.Errorf("%s changed .last-lease.json: %v\n%s", what, err, b)
		}
	}
	gone := func(what string) {
		t.Helper()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s left .last-lease.json: %v", what, err)
		}
	}
	state := func(id string) string {
		t.Helper()
		c, err := poolapi.New(addr)
		if err != nil {
			t.Fatal(err)
		}
		st, _, err := c.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, cell := range st.Cells {
			if cell.ID == id {
				return cell.State
			}
		}
		return "gone"
	}

	// A cell busy with another reset: refused, 409, and the file stays, since the pool still has the lease.
	id, _, raw := leaseIn(t, root, addr)
	release := make(chan struct{})
	b.hold.Store(&release)
	first := make(chan int)
	go func() {
		code, _, _ := runApp(t, &App{Root: root}, "reset", id, "-pool", addr)
		first <- code
	}()
	<-b.held
	code, out, errOut := runApp(t, &App{Root: root}, "reset", "-last", "-pool", addr)
	if code != 1 || out != "" || errOut != "laug · reset: the pool on "+addr+" refused "+id+": cell "+id+" is busy, "+
		"not leased (409 Conflict); .last-lease.json still names it\n" {
		t.Errorf("a reset of a busy cell: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	b.hold.Store(nil)
	close(release)
	if code := <-first; code != 0 {
		t.Errorf("the reset that held the cell: exit %d", code)
	}
	kept("a reset of a busy cell", raw)

	// A failed create: the pool forgets the lease, and the file that names it goes.
	b.fail.Store(true)
	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-last", "-pool", addr)
	if code != 1 || out != "" || errOut != "laug · reset: the pool on "+addr+" refused "+id+": create failed "+
		"(502 Bad Gateway); .last-lease.json removed, since the pool no longer has it leased\n" {
		t.Errorf("a failed create: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	gone("a failed create")
	if s := state(id); s != "gone" {
		t.Errorf("after a failed create the pool holds %s as %s", id, s)
	}

	// A 404 for that lease, now gone, as a pool other than the lease's answers: the file stays.
	os.WriteFile(path, []byte(raw), 0o600)
	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-last", "-pool", addr)
	if code != 1 || out != "" || errOut != "laug · reset: the pool on "+addr+" refused "+id+": cell not found "+
		"(404 Not Found); .last-lease.json still names it\n" {
		t.Errorf("a 404: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	kept("a 404", raw)

	// No answer: the file stays.
	dead := refusedAddr(t)
	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-last", "-pool", dead)
	if code != 1 || out != "" || errOut != "laug · reset: no pool answers on "+dead+" (connection refused); "+
		".last-lease.json still names it\n" {
		t.Errorf("no answer: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	kept("no answer", raw)

	// A failed create whose destroy fails too: the cell is kept as stuck, and the file that names it goes.
	b.fail.Store(false)
	id, _, _ = leaseIn(t, root, addr)
	b.fail.Store(true)
	b.stuck.Store(true)
	code, out, errOut = runApp(t, &App{Root: root}, "reset", "-last", "-pool", addr)
	if code != 1 || out != "" || !strings.HasPrefix(errOut, "laug · reset: the pool on "+addr+" refused "+id+
		": create failed; destroy failed too; the cell is kept as stuck, with block ") || !strings.HasSuffix(errOut,
		": destroy failed (502 Bad Gateway); .last-lease.json removed, since the pool no longer has it leased\n") {
		t.Errorf("a failed create and destroy: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	gone("a failed create and destroy")
	if s := state(id); s != "stuck" {
		t.Errorf("after a failed create and destroy the pool holds %s as %s", id, s)
	}
}
