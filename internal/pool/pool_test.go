package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingBackend is the fake backend plus a log of destroys and of each call's tag, so a test can see what the pool
// did. Its create prints the tag it was given as the cell's server_tag, or 1.31.0 with none, as
// cells/backend-compose.sh's default does.
type recordingBackend struct {
	fakeBackend
	mu        sync.Mutex
	destroyed []string
	blocks    map[string]int      // id -> block passed on create
	tags      map[string][]string // id -> "<verb> <tag>" of each call, in order
}

// record logs a call's verb and tag. Must be called with b.mu held.
func (b *recordingBackend) record(id, verb, tag string) {
	if b.tags == nil {
		b.tags = map[string][]string{}
	}
	b.tags[id] = append(b.tags[id], verb+" "+tag)
}

func (b *recordingBackend) Create(ctx context.Context, id string, block int, tag string) (Cell, error) {
	b.mu.Lock()
	if b.blocks == nil {
		b.blocks = map[string]int{}
	}
	b.blocks[id] = block
	b.record(id, "create", tag)
	b.mu.Unlock()
	c, err := b.fakeBackend.Create(ctx, id, block, tag)
	c.ServerTag = tag
	if tag == "" {
		c.ServerTag = "1.31.0"
	}
	return c, err
}

func (b *recordingBackend) Health(ctx context.Context, id string, block int, tag string) error {
	b.mu.Lock()
	b.record(id, "health", tag)
	b.mu.Unlock()
	return b.fakeBackend.Health(ctx, id, block, tag)
}

func (b *recordingBackend) Destroy(_ context.Context, id string, _ int, tag string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.destroyed = append(b.destroyed, id)
	b.record(id, "destroy", tag)
	return nil
}

// callsOf is the "<verb> <tag>" log of one cell's backend calls, joined by "; ".
func (b *recordingBackend) callsOf(id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.tags[id], "; ")
}

func (b *recordingBackend) destroyedIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.destroyed...)
}

// failingDestroyBackend is recordingBackend whose destroy fails while fail is set, as a backend script that exits
// non-zero or hits -create-timeout would. Each destroy is still recorded.
type failingDestroyBackend struct {
	recordingBackend
	fail bool
}

func (b *failingDestroyBackend) setFail(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = v
}

func (b *failingDestroyBackend) Destroy(ctx context.Context, id string, block int, tag string) error {
	_ = b.recordingBackend.Destroy(ctx, id, block, tag)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errors.New("exit status 1")
	}
	return nil
}

func testConfig() Config {
	return Config{
		MinWarm: 2, MaxCells: 4, Quota: 1,
		MaxLease: time.Hour, CreateTimeout: 5 * time.Second,
		HealthPoll: 10 * time.Millisecond, ReapInterval: 100 * time.Millisecond,
	}
}

func startPool(t *testing.T, cfg Config) (*Pool, *recordingBackend, *httptest.Server) {
	t.Helper()
	b := &recordingBackend{}
	p := newPool(b, cfg)
	p.replenish()
	srv := httptest.NewServer(newMux(p))
	t.Cleanup(srv.Close)
	return p, b, srv
}

func post(t *testing.T, srv *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func warmCount(p *Pool) int {
	n := 0
	for _, e := range p.Status().Cells {
		if e.State == stateWarm {
			n++
		}
	}
	return n
}

// replenishRunning reports whether a replenish is under way. A replenish marks its last cell warm before it clears
// the flag, so a test that waits for a warm count waits for this too.
func replenishRunning(p *Pool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.replenishing
}

// waitFor polls until cond holds or the deadline passes; replenish runs in goroutines after a lease.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLeaseReturnsWarmCellAndReplenishes(t *testing.T) {
	p, _, srv := startPool(t, testConfig())
	if got := warmCount(p); got != 2 {
		t.Fatalf("warm after start: %d, want 2", got)
	}
	code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if code != http.StatusOK {
		t.Fatalf("lease: %d %v", code, lease)
	}
	if lease["state"] != string(stateLeased) || lease["requester"] != "ci" {
		t.Fatalf("lease body: %v", lease)
	}
	if !strings.HasPrefix(lease["address"].(string), "fake-") {
		t.Fatalf("address not from the backend: %v", lease["address"])
	}
	waitFor(t, "replenish to 2 warm", func() bool { return warmCount(p) == 2 })
	if total := len(p.Status().Cells); total != 3 {
		t.Fatalf("cells after lease+replenish: %d, want 3 (2 warm + 1 leased)", total)
	}
}

func TestQuotaRefusesSecondLease(t *testing.T) {
	_, _, srv := startPool(t, testConfig())
	if code, _ := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"}); code != http.StatusOK {
		t.Fatalf("first lease: %d", code)
	}
	code, body := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if code != http.StatusConflict || !strings.Contains(body["error"].(string), "quota") {
		t.Fatalf("second lease past quota: %d %v, want 409 quota", code, body)
	}
	if code, _ := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "someone-else"}); code !=
		http.StatusOK {
		t.Fatalf("another requester should still lease: %d", code)
	}
}

func TestReleaseDestroysAndFreesBlock(t *testing.T) {
	p, b, srv := startPool(t, testConfig())
	_, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	id := lease["id"].(string)
	block := int(lease["block"].(float64))

	code, _ := post(t, srv, "/release", map[string]any{"id": id})
	if code != http.StatusOK {
		t.Fatalf("release: %d", code)
	}
	if ids := b.destroyedIDs(); len(ids) != 1 || ids[0] != id {
		t.Fatalf("destroyed %v, want [%s]", ids, id)
	}
	for _, e := range p.Status().Cells {
		if e.ID == id {
			t.Fatalf("released cell %s still in the pool", id)
		}
	}
	p.mu.Lock()
	free := !p.blocks[block]
	p.mu.Unlock()
	if !free {
		t.Fatalf("block %d still held after release", block)
	}
	if code, _ := post(t, srv, "/release", map[string]any{"id": id}); code != http.StatusNotFound {
		t.Fatalf("second release: %d, want 404", code)
	}
}

// A release whose destroy fails keeps the cell, stuck, with its block: the route answers 502 with the error, the
// status shows it, it counts toward its requester's quota, no replenish runs for it, and a later lease gets another
// block.
func TestFailedReleaseKeepsCellAndBlock(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm, cfg.Quota = 1, 2
	b := &failingDestroyBackend{}
	p := newPool(b, cfg)
	var logged bytes.Buffer
	p.log = log.New(&logged, "", 0)
	p.replenish()
	srv := httptest.NewServer(newMux(p))
	t.Cleanup(srv.Close)
	_, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	id := lease["id"].(string)
	block := int(lease["block"].(float64))
	// The lease took the one warm cell, so a warm cell here is the lease's replenish's; wait for it to finish too.
	waitFor(t, "the lease's replenish to build 1 warm and finish", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		warm := 0
		for _, e := range p.cells {
			if e.State == stateWarm {
				warm++
			}
		}
		return warm == 1 && !p.replenishing
	})

	b.setFail(true)
	code, out := post(t, srv, "/release", map[string]any{"id": id})
	msg, _ := out["error"].(string)
	want := fmt.Sprintf("destroy failed; the cell is kept as stuck, with block %d: exit status 1", block)
	if code != http.StatusBadGateway || msg != want {
		t.Fatalf("release with a failing destroy: %d %v, want 502 %q", code, out, want)
	}
	if got := logged.String(); got != "release "+id+": "+want+"\n" {
		t.Errorf("log: %q", got)
	}
	b.setFail(false)
	var kept *entry
	for _, e := range getStatus(t, srv).Cells {
		if e.ID == id {
			kept = &e
		}
	}
	if kept == nil || kept.State != stateStuck || kept.Block != block || kept.Requester != "ci" {
		t.Fatalf("status after the failed release: %+v, want %s stuck on block %d for ci", kept, id, block)
	}
	p.mu.Lock()
	held, n, replenishing := p.blocks[block], p.held("ci"), p.replenishing
	p.mu.Unlock()
	if !held || n != 1 || replenishing {
		t.Fatalf("block %d held %v, ci holds %d (want 1), replenishing %v (want false)", block, held, n, replenishing)
	}
	if code, out := post(t, srv, "/release", map[string]any{"id": id}); code != http.StatusConflict ||
		out["error"] != "cell "+id+" is stuck, not leased" {
		t.Errorf("release of the stuck cell: %d %v, want 409", code, out)
	}
	// The other 3 blocks lease, one each, and then the pool is full: the stuck cell holds the fourth. Before each
	// lease, wait for the warm count to settle at 1 and the replenish to end. The third lease, if it came while the
	// replenish after the second lease was still building, would find no warm cell and the pool at max.
	for i := 0; i < cfg.MaxCells-1; i++ {
		waitFor(t, fmt.Sprintf("1 warm before lease %d", i), func() bool {
			return warmCount(p) == 1 && !replenishRunning(p)
		})
		code, l := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": fmt.Sprint("other", i)})
		if code != http.StatusOK || int(l["block"].(float64)) == block {
			t.Fatalf("lease %d after the failed release: %d %v, want a block other than %d", i, code, l, block)
		}
	}
	code, out = post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "x"})
	if code != http.StatusConflict || !strings.Contains(out["error"].(string), "max cells") {
		t.Fatalf("lease with 3 leased and 1 stuck of max 4: %d %v, want 409 pool is at max cells", code, out)
	}
}

// POST /vmfault hands a leased cell's backend the body as compact JSON, and refuses an unknown cell (404), a warm one
// (409) and a bad request (400) without calling it. A backend whose vmfault fails, as the compose backend's has no such
// verb, answers 502 with its own line.
func TestVMFault(t *testing.T) {
	p, b, srv := startPool(t, testConfig())
	_, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	id := lease["id"].(string)
	body := map[string]any{"drive_id": "rootfs", "rate_limiter": map[string]any{"ops": map[string]any{"size": 100, "refill_time": 1000}}}
	code, out := post(t, srv, "/vmfault", map[string]any{"id": id, "kind": "drive", "body": body})
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("vmfault on a leased cell: %d %v", code, out)
	}
	want := []string{id, "drive", `{"drive_id":"rootfs","rate_limiter":{"ops":{"refill_time":1000,"size":100}}}`}
	if got := b.lastFaultCall(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("backend got %q, want %q", got, want)
	}
	var warm string
	for _, e := range p.Status().Cells {
		if e.State == stateWarm {
			warm = e.ID
		}
	}
	if warm == "" {
		t.Fatal("no warm cell to refuse")
	}
	for _, x := range []struct {
		req  any
		code int
	}{
		{map[string]any{"id": warm, "kind": "drive", "body": body}, http.StatusConflict},
		{map[string]any{"id": "0badcell", "kind": "drive", "body": body}, http.StatusNotFound},
		{map[string]any{"id": id, "kind": "drive", "body": []int{1}}, http.StatusBadRequest},
		{map[string]any{"id": id, "kind": "drive", "body": "x"}, http.StatusBadRequest},
		{map[string]any{"id": id, "kind": "drive"}, http.StatusBadRequest},
		{map[string]any{"id": id, "kind": "Drive; rm", "body": body}, http.StatusBadRequest},
		{map[string]any{"kind": "drive", "body": body}, http.StatusBadRequest},
		{"not an object", http.StatusBadRequest},
	} {
		if code, out := post(t, srv, "/vmfault", x.req); code != x.code || out["error"] == nil {
			t.Errorf("vmfault %v: %d %v, want %d and an error", x.req, code, out, x.code)
		}
	}
	if got := b.lastFaultCall(); got[0] != id || len(got) != 3 || got[2] != want[2] {
		t.Errorf("a refused request reached the backend: %q", got)
	}

	script := filepath.Join(t.TempDir(), "backend.sh")
	sh := "#!/bin/sh\n" +
		"[ \"$1\" = create ] && { printf '{\"id\":\"%s\",\"address\":\"127.0.0.1:%s\"}\\n' \"$2\" \"$LAUG_BLOCK\"; exit 0; }\n" +
		"[ \"$1\" = vmfault ] && { echo \"unknown command: $1 ($3 $4)\" >&2; exit 2; }\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	sp := newPool(Script(script, nil, io.Discard), testConfig())
	ssrv := httptest.NewServer(newMux(sp))
	t.Cleanup(ssrv.Close)
	_, lease = post(t, ssrv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	code, out = post(t, ssrv, "/vmfault", map[string]any{"id": lease["id"], "kind": "drive", "body": map[string]any{"drive_id": "rootfs"}})
	if msg, _ := out["error"].(string); code != http.StatusBadGateway ||
		!strings.HasSuffix(msg, `exit status 2: unknown command: vmfault (drive {"drive_id":"rootfs"})`) {
		t.Errorf("vmfault on a backend without it: %d %v, want 502 with the script's line", code, out)
	}
	// The replenish the lease started runs the script too; let it finish before the temp dir goes.
	waitFor(t, "replenish to 2 warm", func() bool { return warmCount(sp) == 2 })
}

func TestResetKeepsLeaseIDAndBlock(t *testing.T) {
	_, b, srv := startPool(t, testConfig())
	_, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	id := lease["id"].(string)

	code, reset := post(t, srv, "/reset", map[string]any{"id": id})
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, reset)
	}
	if reset["id"] != id || reset["block"] != lease["block"] || reset["expires_at"] != lease["expires_at"] {
		t.Fatalf("reset changed the lease: before %v, after %v", lease, reset)
	}
	if reset["state"] != string(stateLeased) || reset["requester"] != "ci" {
		t.Fatalf("reset lease body: %v", reset)
	}
	if ids := b.destroyedIDs(); len(ids) != 1 || ids[0] != id {
		t.Fatalf("reset should destroy exactly the old cell, got %v", ids)
	}
	b.mu.Lock()
	gotBlock := b.blocks[id]
	b.mu.Unlock()
	if gotBlock != int(lease["block"].(float64)) {
		t.Fatalf("backend re-created %s on block %d, lease says %v", id, gotBlock, lease["block"])
	}
}

// A reset logs a failed first destroy and goes on: the create runs under the same id and block, and the lease holds.
func TestResetLogsFirstDestroyError(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm = 1
	b := &failingDestroyBackend{}
	p := newPool(b, cfg)
	var logged bytes.Buffer
	p.log = log.New(&logged, "", 0)
	p.replenish()
	e, err := p.Lease(context.Background(), "ci", time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	b.setFail(true)
	r, err := p.Reset(context.Background(), e.ID)
	b.setFail(false)
	if err != nil || r.ID != e.ID || r.Block != e.Block || r.State != stateLeased || r.Requester != "ci" {
		t.Fatalf("reset after a failed destroy: %+v, %v; lease %+v", r, err, e)
	}
	if got, want := logged.String(), "reset "+e.ID+": destroy before create: exit status 1\n"; got != want {
		t.Errorf("log: %q, want %q", got, want)
	}
	if got, want := b.callsOf(e.ID), "create ; health ; destroy ; create ; health "; got != want {
		t.Errorf("calls: %q, want %q", got, want)
	}
	b.mu.Lock()
	gotBlock := b.blocks[e.ID]
	b.mu.Unlock()
	if gotBlock != e.Block {
		t.Errorf("re-created on block %d, lease says %d", gotBlock, e.Block)
	}
}

// A reset answers 409 for a cell that is there but not leased, with Release's line, and 404 for an unknown id.
func TestResetOfCellNotLeased(t *testing.T) {
	p, b, srv := startPool(t, testConfig())
	var warm string
	for _, e := range p.Status().Cells {
		if e.State == stateWarm {
			warm = e.ID
		}
	}
	if code, out := post(t, srv, "/reset", map[string]any{"id": warm}); code != http.StatusConflict ||
		out["error"] != "cell "+warm+" is warm, not leased" {
		t.Errorf("reset of warm cell %s: %d %v, want 409", warm, code, out)
	}
	if code, out := post(t, srv, "/reset", map[string]any{"id": "0badcell"}); code != http.StatusNotFound ||
		out["error"] != "cell not found" {
		t.Errorf("reset of an unknown id: %d %v, want 404", code, out)
	}
	if ids := b.destroyedIDs(); len(ids) != 0 {
		t.Errorf("a refused reset destroyed %v", ids)
	}
}

func TestTTLIsReaped(t *testing.T) {
	p, b, srv := startPool(t, testConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.reaper(ctx)

	// The reaper compares wall-clock times (Round(0)), so the test does too: on a host whose wall clock is
	// stepped, a monotonic reading sees a correct reap as early (3 failures in 70 runs).
	start := time.Now().Round(0)
	_, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 1, "requester": "ci"})
	id := lease["id"].(string)
	waitFor(t, "reaper to release the 1 s lease", func() bool {
		for _, d := range b.destroyedIDs() {
			if d == id {
				return true
			}
		}
		return false
	})
	if since := time.Now().Round(0).Sub(start); since < time.Second {
		t.Fatalf("reaped after %s, before the 1 s TTL", since)
	}
	waitFor(t, "warm count back to min", func() bool { return warmCount(p) == 2 })
}

func TestBlocksNeverCollideAndPoolFullIsRefused(t *testing.T) {
	cfg := testConfig()
	cfg.Quota = 10
	p, _, srv := startPool(t, cfg)
	seen := map[int]string{}
	for i := 0; i < cfg.MaxCells; i++ {
		// A cell a replenish is building holds a slot of -max but cannot be leased until it is warm.
		// So the last lease, if it comes while that build is in flight, finds no warm cell and the pool at max, and
		// is refused. Wait until the warm count settles and the replenish flag clears. With i cells leased, the count
		// settles at MinWarm, or at the slots left when fewer; then no build can start.
		want := min(cfg.MinWarm, cfg.MaxCells-i)
		waitFor(t, fmt.Sprintf("%d warm before lease %d", want, i), func() bool {
			return warmCount(p) == want && !replenishRunning(p)
		})
		code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
		if code != http.StatusOK {
			t.Fatalf("lease %d: %d %v", i, code, lease)
		}
		block := int(lease["block"].(float64))
		if other, dup := seen[block]; dup {
			t.Fatalf("block %d given to both %s and %s", block, other, lease["id"])
		}
		seen[block] = lease["id"].(string)
	}
	code, body := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if code != http.StatusConflict || !strings.Contains(body["error"].(string), "max cells") {
		t.Fatalf("lease past max: %d %v, want 409 pool is at max cells", code, body)
	}
}

// failingResetBackend fails the second create of one id, as a reset whose create breaks would. It also
// records whether a destroy found the cell's block already back on the free list.
type failingResetBackend struct {
	recordingBackend
	pool       *Pool
	creates    map[string]int
	freedEarly int
}

func (b *failingResetBackend) Create(ctx context.Context, id string, block int, tag string) (Cell, error) {
	b.mu.Lock()
	if b.creates == nil {
		b.creates = map[string]int{}
	}
	b.creates[id]++
	n := b.creates[id]
	b.mu.Unlock()
	if n > 1 {
		return Cell{}, context.DeadlineExceeded
	}
	return b.recordingBackend.Create(ctx, id, block, tag)
}

func (b *failingResetBackend) Destroy(ctx context.Context, id string, block int, tag string) error {
	b.pool.mu.Lock()
	held := b.pool.blocks[block]
	b.pool.mu.Unlock()
	if !held {
		b.mu.Lock()
		b.freedEarly++
		b.mu.Unlock()
	}
	return b.recordingBackend.Destroy(ctx, id, block, tag)
}

func TestFailedResetDestroysBeforeFreeingBlock(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm = 1
	b := &failingResetBackend{}
	p := newPool(b, cfg)
	b.pool = p
	p.replenish()
	e, err := p.Lease(context.Background(), "t", time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Reset(context.Background(), e.ID); err == nil {
		t.Fatal("reset succeeded with a failing create")
	}
	n := 0
	for _, id := range b.destroyedIDs() {
		if id == e.ID {
			n++
		}
	}
	if n != 2 { // once before the re-create, once after it failed
		t.Fatalf("cell %s destroyed %d times, want 2", e.ID, n)
	}
	b.mu.Lock()
	early := b.freedEarly
	b.mu.Unlock()
	if early != 0 {
		t.Fatalf("%d destroy(s) ran after the block was back on the free list", early)
	}
	for _, c := range p.Status().Cells {
		if c.ID == e.ID {
			t.Fatalf("failed reset left cell %s in the pool", e.ID)
		}
	}
}

// brokenBackend is failingDestroyBackend whose create fails too while failCreate is set, as a compose up that exits
// non-zero and then a down that cannot reach Docker would. Each create is still recorded.
type brokenBackend struct {
	failingDestroyBackend
	failCreate bool
}

// setFails sets both: create and destroy fail while v is true.
func (b *brokenBackend) setFails(v bool) {
	b.setFail(v)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failCreate = v
}

func (b *brokenBackend) Create(ctx context.Context, id string, block int, tag string) (Cell, error) {
	c, err := b.recordingBackend.Create(ctx, id, block, tag)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failCreate {
		return Cell{}, errors.New("exit status 1")
	}
	return c, err
}

// cellOnBlock is the status's cell on block, or a failure naming the cells when no cell or more than one holds it.
func cellOnBlock(t *testing.T, cells []entry, block int) entry {
	t.Helper()
	var on []entry
	for _, e := range cells {
		if e.Block == block {
			on = append(on, e)
		}
	}
	if len(on) != 1 {
		t.Fatalf("%d cells on block %d, want 1: %+v", len(on), block, cells)
	}
	return on[0]
}

// A create whose destroy fails too keeps the cell as stuck, as a failed release does, in each place a create runs:
// a replenish's warm build, a cold lease's build and a reset, the last two answered 502. The cell keeps its block, its
// requester and its place in the quota and in -max, not until the pool stops but until the destroy succeeds, and the
// cells after it get other blocks. Only the reaper tries that destroy again (TestStuckCellDestroyIsRetried), and no
// reaper runs here.
func TestFailedCreateAndDestroyKeepCellStuck(t *testing.T) {
	stuckErr := func(block int) string {
		return fmt.Sprintf("exit status 1; destroy failed too; the cell is kept as stuck, with block %d: exit status 1",
			block)
	}

	// A replenish's warm build: the cell is kept on block 0 with no requester, and the next replenish builds on
	// block 1. Both replenish calls run here, not in a goroutine, so nothing else builds.
	cfg := testConfig()
	cfg.MinWarm = 1
	b := &brokenBackend{}
	p := newPool(b, cfg)
	var logged bytes.Buffer
	p.log = log.New(&logged, "", 0)
	b.setFails(true)
	p.replenish()
	b.setFails(false)
	if got, want := logged.String(), "replenish: "+stuckErr(0)+"\n"; got != want {
		t.Errorf("replenish's log: %q, want %q", got, want)
	}
	p.replenish()
	cells := p.Status().Cells
	warmStuck := cellOnBlock(t, cells, 0)
	if len(cells) != 2 || warmStuck.State != stateStuck || warmStuck.Requester != "" ||
		cellOnBlock(t, cells, 1).State != stateWarm {
		t.Fatalf("after a failed warm build and a good one: %+v, want block 0 stuck for no one and block 1 warm", cells)
	}
	if got := b.callsOf(warmStuck.ID); got != "create ; destroy " {
		t.Errorf("the stuck warm cell's calls: %q, want one create and one destroy", got)
	}

	// A cold lease's build, then a reset, each with create and destroy failing. MinWarm 0, so no replenish builds.
	cfg = testConfig()
	cfg.MinWarm, cfg.Quota = 0, 2
	b = &brokenBackend{}
	p = newPool(b, cfg)
	logged.Reset()
	p.log = log.New(&logged, "", 0)
	srv := httptest.NewServer(newMux(p))
	t.Cleanup(srv.Close)

	b.setFails(true)
	code, out := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	b.setFails(false)
	if code != http.StatusBadGateway || out["error"] != stuckErr(0) {
		t.Fatalf("a cold lease whose create and destroy fail: %d %v, want 502 %q", code, out, stuckErr(0))
	}
	cold := cellOnBlock(t, getStatus(t, srv).Cells, 0)
	if cold.State != stateStuck || cold.Requester != "ci" {
		t.Fatalf("the cold lease's cell: %+v, want stuck on block 0 for ci", cold)
	}
	if got := b.callsOf(cold.ID); got != "create ; destroy " {
		t.Errorf("the stuck cold cell's calls: %q, want one create and one destroy", got)
	}

	code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if code != http.StatusOK || lease["block"] != float64(1) {
		t.Fatalf("ci's second lease, with 1 stuck of quota 2: %d %v, want 200 on block 1", code, lease)
	}
	id := lease["id"].(string)
	b.setFails(true)
	code, out = post(t, srv, "/reset", map[string]any{"id": id})
	b.setFails(false)
	if code != http.StatusBadGateway || out["error"] != stuckErr(1) {
		t.Fatalf("a reset whose create and destroy fail: %d %v, want 502 %q", code, out, stuckErr(1))
	}
	if got, want := logged.String(), "reset "+id+": destroy before create: exit status 1\n"+
		"reset "+id+": destroy after failed create: exit status 1\n"; got != want {
		t.Errorf("the reset's log: %q, want %q", got, want)
	}
	reset := cellOnBlock(t, getStatus(t, srv).Cells, 1)
	if reset.ID != id || reset.State != stateStuck || reset.Requester != "ci" {
		t.Fatalf("the reset's cell: %+v, want %s stuck on block 1 for ci", reset, id)
	}
	if got := b.callsOf(id); got != "create ; health ; destroy ; create ; destroy " {
		t.Errorf("the reset cell's calls: %q", got)
	}

	// The two stuck cells hold ci's quota of 2 and 2 of -max's 4, and neither releases nor resets.
	if code, out := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"}); code !=
		http.StatusConflict || out["error"] != "quota: ci already holds 2 lease(s)" {
		t.Errorf("ci's lease with 2 stuck of quota 2: %d %v, want 409 quota", code, out)
	}
	for _, sid := range []string{cold.ID, id} {
		for _, path := range []string{"/release", "/reset"} {
			if code, out := post(t, srv, path, map[string]any{"id": sid}); code != http.StatusConflict ||
				out["error"] != "cell "+sid+" is stuck, not leased" {
				t.Errorf("%s of stuck cell %s: %d %v, want 409", path, sid, code, out)
			}
		}
	}
	for i, want := range []float64{2, 3} {
		code, l := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": fmt.Sprint("other", i)})
		if code != http.StatusOK || l["block"] != want {
			t.Fatalf("lease %d beside the 2 stuck cells: %d %v, want 200 on block %v", i, code, l, want)
		}
	}
	if code, out := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "x"}); code !=
		http.StatusConflict || out["error"] != "pool is at max cells" {
		t.Errorf("a lease with 2 leased and 2 stuck of max 4: %d %v, want 409 pool is at max cells", code, out)
	}
	p.mu.Lock()
	held, blocks := p.held("ci"), append([]bool(nil), p.blocks...)
	p.mu.Unlock()
	if held != 2 || !blocks[0] || !blocks[1] {
		t.Errorf("ci holds %d (want 2); blocks %v (want 0 and 1 held)", held, blocks)
	}
	n := map[string]int{}
	for _, d := range b.destroyedIDs() {
		n[d]++
	}
	if n[cold.ID] != 1 || n[id] != 2 {
		t.Errorf("destroys: %v, want 1 of %s and 2 of %s: a stuck cell stays until the destroy succeeds, and only the "+
			"reaper, which does not run here, tries it again", n, cold.ID, id)
	}
}

// lockedLog is a pool's log that a test reads while the pool's goroutines write it.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(b)
}

// lines is the log's lines, sorted, so lines that goroutines wrote in either order compare the same.
func (l *lockedLog) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := strings.Split(strings.TrimSuffix(l.buf.String(), "\n"), "\n")
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	slices.Sort(out)
	return out
}

func (l *lockedLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
}

// holdingBackend is brokenBackend whose destroy, while hold is set, sends its id on entered as it starts and then
// waits until hold is closed, so a test sees a destroy in flight.
type holdingBackend struct {
	brokenBackend
	hold    chan struct{}
	entered chan string
}

func (b *holdingBackend) Destroy(ctx context.Context, id string, block int, tag string) error {
	b.mu.Lock()
	hold := b.hold
	b.mu.Unlock()
	if hold != nil {
		b.entered <- id
		<-hold
	}
	return b.brokenBackend.Destroy(ctx, id, block, tag)
}

// retriesInFlight counts the stuck cells whose destroy is being tried again.
func retriesInFlight(p *Pool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.cells {
		if e.retrying {
			n++
		}
	}
	return n
}

// A stuck cell's destroy is tried again on each reaper tick until a try succeeds, whichever path left it stuck: a
// failed release, a reset or a cold lease's build whose destroy failed too, or a replenish's warm build. Through k
// failed tries the cell stays stuck with its block, its slot in -max and its requester's quota place, and a tick that
// finds a try in flight starts no second. Then a try succeeds: the cell goes, and a replenish builds a warm cell on its
// block. The log names the 1st, 2nd and 4th failed try and the one that succeeded. Here retryStuck stands in for the
// reaper's tick, so each try starts when the test says. Last, an outage under a running reaper: a pool whose creates
// and destroys fail fills -max with stuck cells, and once the backend is back the reaper alone brings it to -min warm.
func TestStuckCellDestroyIsRetried(t *testing.T) {
	const k = 5 // failed tries before the one that succeeds
	cfg := testConfig()
	cfg.MinWarm = 1 // MaxCells 4, Quota 1
	settle := func(t *testing.T, p *Pool) {
		t.Helper()
		waitFor(t, "1 warm cell and no replenish", func() bool { return warmCount(p) == 1 && !replenishRunning(p) })
	}
	lease := func(t *testing.T, srv *httptest.Server, requester string) (int, map[string]any) {
		t.Helper()
		return post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": requester})
	}
	for _, c := range []struct {
		name string
		// stick leaves one cell stuck on block 0 and one warm cell on block 1, and returns the stuck cell's requester.
		stick func(t *testing.T, p *Pool, b *holdingBackend, srv *httptest.Server) string
	}{
		{"a release", func(t *testing.T, p *Pool, b *holdingBackend, srv *httptest.Server) string {
			p.replenish()
			_, l := lease(t, srv, "ci")
			settle(t, p)
			b.setFail(true)
			if code, out := post(t, srv, "/release", map[string]any{"id": l["id"]}); code != http.StatusBadGateway {
				t.Fatalf("a release whose destroy fails: %d %v, want 502", code, out)
			}
			b.setFail(false)
			return "ci"
		}},
		{"a reset", func(t *testing.T, p *Pool, b *holdingBackend, srv *httptest.Server) string {
			p.replenish()
			_, l := lease(t, srv, "ci")
			settle(t, p)
			b.setFails(true)
			if code, out := post(t, srv, "/reset", map[string]any{"id": l["id"]}); code != http.StatusBadGateway {
				t.Fatalf("a reset whose create and destroy fail: %d %v, want 502", code, out)
			}
			b.setFails(false)
			return "ci"
		}},
		{"a cold lease", func(t *testing.T, p *Pool, b *holdingBackend, srv *httptest.Server) string {
			b.setFails(true)
			if code, out := lease(t, srv, "ci"); code != http.StatusBadGateway {
				t.Fatalf("a cold lease whose create and destroy fail: %d %v, want 502", code, out)
			}
			b.setFails(false)
			p.replenish()
			return "ci"
		}},
		{"a warm build", func(t *testing.T, p *Pool, b *holdingBackend, srv *httptest.Server) string {
			b.setFails(true)
			p.replenish()
			b.setFails(false)
			p.replenish()
			return ""
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &holdingBackend{entered: make(chan string, 4)}
			p := newPool(b, cfg)
			var logged lockedLog
			p.log = log.New(&logged, "", 0)
			srv := httptest.NewServer(newMux(p))
			t.Cleanup(srv.Close)
			requester := c.stick(t, p, b, srv)
			stuck := cellOnBlock(t, p.Status().Cells, 0)
			id := stuck.ID
			if stuck.State != stateStuck || stuck.Requester != requester ||
				cellOnBlock(t, p.Status().Cells, 1).State != stateWarm {
				t.Fatalf("after %s: %+v, want block 0 stuck for %q and block 1 warm", c.name, p.Status().Cells,
					requester)
			}
			// Three other requesters lease blocks 1 to 3, so block 0 is the only one a destroy can free.
			for i := 1; i < cfg.MaxCells; i++ {
				settle(t, p)
				if code, l := lease(t, srv, fmt.Sprint("other", i)); code != http.StatusOK || l["block"] != float64(i) {
					t.Fatalf("lease %d beside the stuck cell: %d %v, want 200 on block %d", i, code, l, i)
				}
			}
			waitFor(t, "the last lease's replenish to end", func() bool { return !replenishRunning(p) })
			destroys := func() int {
				n := 0
				for _, d := range b.destroyedIDs() {
					if d == id {
						n++
					}
				}
				return n
			}
			base := destroys()
			holds := 0 // ci's leases: the stuck cell is ci's unless a replenish built it
			if requester == "ci" {
				holds = 1
			}
			logged.reset()

			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			p.retryStuck(canceled)
			if n := retriesInFlight(p); n != 0 {
				t.Fatalf("a tick after the reaper's context ended started %d tries, want none", n)
			}
			b.setFail(true)
			for try := 1; try <= k; try++ {
				if try == 1 {
					b.mu.Lock()
					b.hold = make(chan struct{})
					b.mu.Unlock()
				}
				p.retryStuck(context.Background())
				if try == 1 {
					select {
					case got := <-b.entered:
						if got != id {
							t.Fatalf("the first try destroyed %s, want %s", got, id)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("no try of the stuck cell's destroy began within 2 s")
					}
					p.retryStuck(context.Background())
					p.retryStuck(context.Background())
					select {
					case got := <-b.entered:
						t.Fatalf("two more ticks started a second try of %s while the first was in flight", got)
					case <-time.After(50 * time.Millisecond):
					}
					if e := cellOnBlock(t, p.Status().Cells, 0); e.ID != id || e.State != stateStuck {
						t.Fatalf("while its first try runs: %+v, want %s stuck", e, id)
					}
					b.mu.Lock()
					close(b.hold)
					b.hold = nil
					b.mu.Unlock()
				}
				waitFor(t, fmt.Sprintf("try %d to end", try), func() bool { return retriesInFlight(p) == 0 })
				e := cellOnBlock(t, p.Status().Cells, 0)
				p.mu.Lock()
				held, quota := p.blocks[0], p.held("ci")
				p.mu.Unlock()
				if e.ID != id || e.State != stateStuck || e.Requester != requester || !held || quota != holds {
					t.Fatalf("after failed try %d: %+v, block 0 held %v, ci holds %d; want %s stuck on block 0 for %q",
						try, e, held, quota, id, requester)
				}
				if n := destroys(); n != base+try {
					t.Fatalf("after try %d: %d destroys of %s, want %d, one a try", try, n, id, base+try)
				}
			}
			if code, out := lease(t, srv, "x"); code != http.StatusConflict || out["error"] != "pool is at max cells" {
				t.Errorf("a lease with 3 leased and 1 stuck of max 4: %d %v, want 409 pool is at max cells", code, out)
			}
			if requester == "ci" {
				if code, out := lease(t, srv, "ci"); code != http.StatusConflict ||
					out["error"] != "quota: ci already holds 1 lease(s)" {
					t.Errorf("ci's lease while its cell is stuck: %d %v, want 409 quota", code, out)
				}
			}

			b.setFail(false)
			p.retryStuck(context.Background())
			waitFor(t, "the try that succeeds, then a replenish's warm cell on block 0", func() bool {
				for _, e := range p.Status().Cells {
					if e.Block == 0 && e.State == stateWarm {
						return !replenishRunning(p)
					}
				}
				return false
			})
			cells := p.Status().Cells
			if warm := cellOnBlock(t, cells, 0); warm.ID == id || len(cells) != cfg.MaxCells ||
				b.callsOf(warm.ID) != "create ; health " {
				t.Fatalf("after the try that succeeded: %+v, want a new warm cell on block 0 beside 3 leases", cells)
			}
			if n := destroys(); n != base+k+1 {
				t.Errorf("%d destroys of %s, want %d", n, id, base+k+1)
			}
			if code, l := lease(t, srv, "ci"); code != http.StatusOK || l["block"] != float64(0) {
				t.Errorf("ci's lease once the stuck cell went: %d %v, want 200 on block 0", code, l)
			}
			want := []string{
				fmt.Sprintf("stuck %s: destroy retry 1 failed; block 0 kept: exit status 1", id),
				fmt.Sprintf("stuck %s: destroy retry 2 failed; block 0 kept: exit status 1", id),
				fmt.Sprintf("stuck %s: destroy retry 4 failed; block 0 kept: exit status 1", id),
				fmt.Sprintf("stuck %s: destroy retry %d succeeded; block 0 is free", id, k+1),
			}
			waitFor(t, "the tries' log lines", func() bool { return len(logged.lines()) >= len(want) })
			if got := logged.lines(); !slices.Equal(got, want) {
				t.Errorf("log, sorted:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}

	t.Run("an outage, then the backend back", func(t *testing.T) {
		cfg := testConfig() // MinWarm 2, MaxCells 4
		cfg.ReapInterval = 10 * time.Millisecond
		b := &brokenBackend{}
		b.setFails(true)
		p := newPool(b, cfg)
		p.log = log.New(io.Discard, "", 0)
		ctx, stop := context.WithCancel(context.Background())
		t.Cleanup(stop)
		go p.reaper(ctx)
		count := func(s state) int {
			n := 0
			for _, e := range p.Status().Cells {
				if e.State == s {
					n++
				}
			}
			return n
		}
		waitFor(t, "the outage to fill -max with stuck cells", func() bool { return count(stateStuck) == cfg.MaxCells })
		if _, err := p.Lease(context.Background(), "ci", time.Minute, ""); !errors.Is(err, errPoolFull) {
			t.Fatalf("a lease with -max full of stuck cells: %v, want %v", err, errPoolFull)
		}
		b.setFails(false)
		waitFor(t, "the reaper to bring the pool back to 2 warm cells and none stuck", func() bool {
			return count(stateWarm) == cfg.MinWarm && len(p.Status().Cells) == cfg.MinWarm && !replenishRunning(p) &&
				retriesInFlight(p) == 0
		})
	})
}

// A script backend gets laugd's environment, and what its create prints reaches the lease: SERVER_TAG set where laugd
// runs comes back as the lease's server_tag, as with laug pool serve -server-tag x.y.z and cells/backend-compose.sh.
func TestScriptBackendServerTagReachesLease(t *testing.T) {
	script := filepath.Join(t.TempDir(), "backend.sh")
	body := "#!/bin/sh\n" +
		"[ \"$1\" = create ] && printf '{\"id\":\"%s\",\"address\":\"127.0.0.1:%s\",\"server_tag\":\"%s\"}\\n' \"$2\" \"$LAUG_BLOCK\" \"$SERVER_TAG\"\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVER_TAG", "1.32.1")
	p := newPool(scriptBackend{path: script}, testConfig())
	p.replenish()
	srv := httptest.NewServer(newMux(p))
	t.Cleanup(srv.Close)
	code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if code != http.StatusOK {
		t.Fatalf("lease: %d %v", code, lease)
	}
	if lease["server_tag"] != "1.32.1" {
		t.Fatalf("lease server_tag: %v, want 1.32.1 from the backend's create", lease["server_tag"])
	}
	// The replenish the lease started runs the script too; let it finish before the temp dir goes.
	waitFor(t, "replenish to 2 warm", func() bool { return warmCount(p) == 2 })
}

// gatedBackend is recordingBackend with a Create that first runs wait: a receive on a channel the test closes, or a
// sleep, so a test can look at the pool while a cell is being built.
type gatedBackend struct {
	recordingBackend
	wait func()
}

func (b *gatedBackend) Create(ctx context.Context, id string, block int, tag string) (Cell, error) {
	b.wait()
	return b.recordingBackend.Create(ctx, id, block, tag)
}

// startGated is a pool with no warm cell whose every Create waits until open is called. The test's cleanup calls open
// too, before the server closes, so a failing test leaves no create, and no request, waiting.
func startGated(t *testing.T, cfg Config) (*Pool, *httptest.Server, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	p := newPool(&gatedBackend{wait: func() { <-gate }}, cfg)
	srv := httptest.NewServer(newMux(p))
	t.Cleanup(srv.Close)
	t.Cleanup(open) // cleanups run last first: the gate opens, then the server closes
	return p, srv, open
}

// getStatus reads GET /status.
func getStatus(t *testing.T, srv *httptest.Server) statusView {
	t.Helper()
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	var v statusView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	return v
}

type leaseResult struct {
	e   entry
	err error
}

// leaseAsync runs a lease for requester in a goroutine; its answer comes on the channel.
func leaseAsync(p *Pool, requester string) <-chan leaseResult {
	ch := make(chan leaseResult, 1)
	go func() {
		e, err := p.Lease(context.Background(), requester, time.Minute, "")
		ch <- leaseResult{e, err}
	}()
	return ch
}

// awaitLease is the lease leaseAsync started, or a failure when it erred or took over 2 s.
func awaitLease(t *testing.T, ch <-chan leaseResult) entry {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("lease: %v", r.err)
		}
		return r.e
	case <-time.After(2 * time.Second):
		t.Fatal("lease did not return within 2 s")
	}
	return entry{}
}

// A cold lease's slot carries its requester from the moment it is reserved, a busy entry is never handed out,
// and the lease comes back with the id reserved for it. That build marks the cell leased in the critical section
// that stores it, so it is never warm on the way, is read in the code; TestColdLeaseNeverWarm is its smoke test.
func TestColdLeaseReservesForRequester(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm, cfg.Quota = 0, 2
	p, srv, open := startGated(t, cfg)
	got := leaseAsync(p, "ci")
	var slot entry
	waitFor(t, "the cold lease's slot in /status", func() bool {
		cells := getStatus(t, srv).Cells
		if len(cells) == 1 {
			slot = cells[0]
			return true
		}
		return false
	})
	if slot.State != stateBusy || slot.Requester != "ci" {
		t.Fatalf("the slot being built: state %s, requester %q; want busy, ci", slot.State, slot.Requester)
	}
	open()
	e := awaitLease(t, got)
	if e.State != stateLeased || e.Requester != "ci" || e.ID != slot.ID {
		t.Fatalf("lease: id %s, state %s, requester %q; want %s, leased, ci", e.ID, e.State, e.Requester, slot.ID)
	}
}

// A cell being built for a requester counts toward its quota, so its second lease is refused at once, while
// another requester reserves a slot of its own.
func TestQuotaCountsCellsBeingBuilt(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm, cfg.Quota = 0, 1
	p, srv, open := startGated(t, cfg)
	ci := leaseAsync(p, "ci")
	waitFor(t, "one busy cell in /status", func() bool {
		c := getStatus(t, srv).Cells
		return len(c) == 1 && c[0].State == stateBusy
	})
	client := &http.Client{Timeout: 2 * time.Second}
	buf, _ := json.Marshal(map[string]any{"ttl_seconds": 600, "requester": "ci"})
	resp, err := client.Post(srv.URL+"/lease", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("a second lease for ci did not answer within 2 s (%v): it passed the quota and waits on a create", err)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if msg, _ := body["error"].(string); resp.StatusCode != http.StatusConflict || !strings.Contains(msg, "quota") {
		t.Fatalf("a second lease for ci while its first cell is built: %d %v, want 409 quota", resp.StatusCode, body)
	}
	if c := getStatus(t, srv).Cells; len(c) != 1 || c[0].Requester != "ci" {
		t.Fatalf("after the refusal: %+v, want one busy cell for ci", c)
	}
	other := leaseAsync(p, "other")
	waitFor(t, "two busy cells, for ci and other, in /status", func() bool {
		c := getStatus(t, srv).Cells
		if len(c) != 2 || c[0].State != stateBusy || c[1].State != stateBusy {
			return false
		}
		return (c[0].Requester == "ci" && c[1].Requester == "other") || (c[0].Requester == "other" && c[1].Requester == "ci")
	})
	open()
	for want, ch := range map[string]<-chan leaseResult{"ci": ci, "other": other} {
		if e := awaitLease(t, ch); e.State != stateLeased || e.Requester != want {
			t.Fatalf("lease for %s: state %s, requester %q", want, e.State, e.Requester)
		}
	}
}

// A smoke test: no cell shows warm while a cold lease builds it. The gap TestColdLeaseReservesForRequester names,
// between create's unlock and Lease's next lock, was a few instructions wide, so this passed before the fix too.
func TestColdLeaseNeverWarm(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm = 0
	p := newPool(&gatedBackend{wait: func() { time.Sleep(50 * time.Millisecond) }}, cfg)
	stop, seen := make(chan struct{}), make(chan int, 1)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				seen <- n
				return
			default:
			}
			for _, c := range p.Status().Cells {
				if c.State == stateWarm {
					n++
				}
			}
		}
	}()
	e, err := p.Lease(context.Background(), "ci", time.Minute, "")
	close(stop)
	warm := <-seen
	if err != nil {
		t.Fatal(err)
	}
	if warm != 0 {
		t.Fatalf("a cell built for a lease read warm %d time(s) on the way", warm)
	}
	if e.State != stateLeased || e.Requester != "ci" {
		t.Fatalf("lease: state %s, requester %q; want leased, ci", e.State, e.Requester)
	}
}

// A smoke test: two cold leases of one requester at once, within its quota, get a cell and a block each.
func TestConcurrentColdLeasesGetTwoCells(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm, cfg.Quota = 0, 2
	p := newPool(&gatedBackend{wait: func() { time.Sleep(50 * time.Millisecond) }}, cfg)
	a, b := leaseAsync(p, "ci"), leaseAsync(p, "ci")
	x, y := awaitLease(t, a), awaitLease(t, b)
	if x.ID == y.ID || x.Block == y.Block {
		t.Fatalf("two leases share a cell or a block: %s on %d, %s on %d", x.ID, x.Block, y.ID, y.Block)
	}
	for _, e := range []entry{x, y} {
		if e.State != stateLeased || e.Requester != "ci" {
			t.Fatalf("lease %s: state %s, requester %q; want leased, ci", e.ID, e.State, e.Requester)
		}
	}
}

// A lease's server tag reaches every backend call for its cell: its create and health, and its release's destroy.
func TestLeaseTagReachesBackend(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm = 0
	_, b, srv := startPool(t, cfg)
	code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci", "server_tag": "1.32.1"})
	if code != http.StatusOK || lease["server_tag"] != "1.32.1" {
		t.Fatalf("lease naming 1.32.1: %d %v", code, lease)
	}
	id := lease["id"].(string)
	if code, body := post(t, srv, "/release", map[string]any{"id": id}); code != http.StatusOK {
		t.Fatalf("release: %d %v", code, body)
	}
	if got, want := b.callsOf(id), "create 1.32.1; health 1.32.1; destroy 1.32.1"; got != want {
		t.Fatalf("backend calls for %s: %q, want %q", id, got, want)
	}
}

// A warm cell, built with no tag, goes to a lease that names its tag; a lease of another tag gets a cell built with
// that tag and never takes the warm one.
func TestLeaseTakesWarmCellOfItsTag(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm, cfg.Quota = 1, 2
	p, b, srv := startPool(t, cfg)
	cells := p.Status().Cells
	if len(cells) != 1 || cells[0].State != stateWarm || cells[0].ServerTag != "1.31.0" {
		t.Fatalf("after start: %+v, want one warm cell of 1.31.0", cells)
	}
	code, same := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci", "server_tag": "1.31.0"})
	if code != http.StatusOK || same["id"] != cells[0].ID {
		t.Fatalf("lease naming 1.31.0: %d %v, want the warm cell %s", code, same, cells[0].ID)
	}
	waitFor(t, "replenish to 1 warm", func() bool { return warmCount(p) == 1 })
	var spare string
	for _, c := range p.Status().Cells {
		if c.State == stateWarm {
			spare = c.ID
		}
	}
	code, other := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci", "server_tag": "1.32.1"})
	if code != http.StatusOK || other["server_tag"] != "1.32.1" || other["id"] == spare {
		t.Fatalf("lease naming 1.32.1: %d %v, want a new cell of 1.32.1, not the warm %s", code, other, spare)
	}
	if got, want := b.callsOf(other["id"].(string)), "create 1.32.1; health 1.32.1"; got != want {
		t.Fatalf("backend calls for the 1.32.1 cell: %q, want %q", got, want)
	}
	for _, c := range p.Status().Cells {
		if c.ID == spare && c.State != stateWarm {
			t.Fatalf("the warm cell of 1.31.0 is %s after a lease of 1.32.1", c.State)
		}
	}
}

// A server tag that is not a release, x.y.z, is refused before any cell is reserved.
func TestLeaseBadTagIs400(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm = 0
	p, b, srv := startPool(t, cfg)
	code, body := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci", "server_tag": "latest"})
	if want := `server_tag must be a release tag, x.y.z; got "latest"`; code != http.StatusBadRequest || body["error"] != want {
		t.Fatalf("lease naming latest: %d %v, want 400 %q", code, body, want)
	}
	b.mu.Lock()
	creates := len(b.blocks)
	b.mu.Unlock()
	if n := len(p.Status().Cells); n != 0 || creates != 0 {
		t.Fatalf("after a refused tag: %d cell(s), %d create(s); want none", n, creates)
	}
}

// A lease whose ttl_seconds is 0 or less, or missing, is refused with 400 before any cell is reserved: the 2 warm cells
// stay warm and no create runs. The 400 for 0 says a missing ttl_seconds reads as 0. The pool once gave such a lease
// its max lease. A ttl_seconds of 1 then leases a cell for 1 s.
func TestLeaseTTLOfZeroOrLessIs400(t *testing.T) {
	p, b, srv := startPool(t, testConfig()) // MinWarm 2: startPool's replenish builds 2 warm cells
	creates := func() int {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.blocks)
	}
	before := creates()
	for _, c := range []struct {
		name string
		body map[string]any
		got  string
	}{
		{"0", map[string]any{"ttl_seconds": 0, "requester": "ci"}, "0 (a missing ttl_seconds reads as 0)"},
		{"-5", map[string]any{"ttl_seconds": -5, "requester": "ci"}, "-5"},
		{"missing", map[string]any{"requester": "ci"}, "0 (a missing ttl_seconds reads as 0)"},
	} {
		code, body := post(t, srv, "/lease", c.body)
		if want := "ttl_seconds must be 1 or more; got " + c.got; code != http.StatusBadRequest ||
			body["error"] != want {
			t.Errorf("a lease with ttl_seconds %s: %d %v, want 400 %q", c.name, code, body, want)
		}
	}
	if n, w, c := len(p.Status().Cells), warmCount(p), creates(); n != 2 || w != 2 || c != before {
		t.Fatalf("after the refused leases: %d cell(s), %d warm, %d create(s), %d before; want the 2 warm cells and "+
			"no create", n, w, c, before)
	}
	code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 1, "requester": "ci"})
	leased, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(lease["leased_at"]))
	expires, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(lease["expires_at"]))
	if code != http.StatusOK || expires.Sub(leased) != time.Second {
		t.Fatalf("a lease with ttl_seconds 1: %d %v, want 200 and 1s from leased_at to expires_at", code, lease)
	}
}

// A ttl_seconds over MaxLease in whole seconds leases for MaxLease, the 1 h of testConfig. /lease cuts it before it
// becomes a time.Duration. Multiplied first, 1<<62 s wraps to 0 ns and math.MaxInt64 s to -1 s, which Lease's own cut
// hid; 1<<55+1 s wraps to 1 s, a lease that expired at once. 3600 s is MaxLease itself and 3601 one second over.
func TestLeaseTTLOverMaxLeaseGetsMaxLease(t *testing.T) {
	cfg := testConfig()
	_, _, srv := startPool(t, cfg)
	for _, ttl := range []int64{3600, 3601, 1<<55 + 1, 1 << 62, math.MaxInt64} {
		code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": ttl, "requester": "ci"})
		leased, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(lease["leased_at"]))
		expires, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(lease["expires_at"]))
		if code != http.StatusOK || expires.Sub(leased) != cfg.MaxLease {
			t.Fatalf("a lease with ttl_seconds %d: %d %v, want 200 and %s from leased_at to expires_at", ttl, code,
				lease, cfg.MaxLease)
		}
		if code, out := post(t, srv, "/release", map[string]any{"id": lease["id"]}); code != http.StatusOK {
			t.Fatalf("release after ttl_seconds %d: %d %v", ttl, code, out)
		}
	}
}

// buildFailBackend is recordingBackend whose create fails while failCreate is set, as a compose up that exits non-zero
// would. Its health check fails while failHealth is set, as a cell that never comes up would. Its destroy works.
type buildFailBackend struct {
	recordingBackend
	failCreate, failHealth bool
}

func (b *buildFailBackend) set(create, health bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failCreate, b.failHealth = create, health
}

func (b *buildFailBackend) Create(ctx context.Context, id string, block int, tag string) (Cell, error) {
	c, err := b.recordingBackend.Create(ctx, id, block, tag)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failCreate {
		return Cell{}, errors.New("exit status 1")
	}
	return c, err
}

func (b *buildFailBackend) Health(ctx context.Context, id string, block int, tag string) error {
	err := b.recordingBackend.Health(ctx, id, block, tag)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failHealth {
		return errors.New("exit status 1")
	}
	return err
}

// A backend failure on /lease and /reset answers 502 with the failure's text, as /release's failed destroy does. The
// failures are a cold lease's create that fails or whose cell never passes its health check, and the same in a reset.
// Each time the pool destroys what the create left and frees the block, and a reset's lease ends. A quota or a full
// pool stays 409 (TestQuotaRefusesSecondLease, TestBlocksNeverCollideAndPoolFullIsRefused); a create whose destroy
// fails too is TestFailedCreateAndDestroyKeepCellStuck's.
func TestBackendFailureIs502(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm, cfg.CreateTimeout = 0, 200*time.Millisecond
	b := &buildFailBackend{}
	p := newPool(b, cfg)
	srv := httptest.NewServer(newMux(p))
	t.Cleanup(srv.Close)
	const unhealthy = " not healthy before deadline: context deadline exceeded"
	lease := func(what string) string {
		t.Helper()
		b.set(false, false)
		code, l := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
		if code != http.StatusOK || l["block"] != float64(0) {
			t.Fatalf("a lease %s: %d %v, want 200 on block 0", what, code, l)
		}
		return l["id"].(string)
	}
	none := func(what string) {
		t.Helper()
		if c := getStatus(t, srv).Cells; len(c) != 0 {
			t.Fatalf("after %s: %+v, want no cell", what, c)
		}
	}

	b.set(true, false)
	code, out := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if code != http.StatusBadGateway || out["error"] != "exit status 1" {
		t.Errorf("a cold lease whose create fails: %d %v, want 502 %q", code, out, "exit status 1")
	}
	none("a cold lease whose create failed")

	b.set(false, true)
	code, out = post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if msg := fmt.Sprint(out["error"]); code != http.StatusBadGateway || !strings.HasPrefix(msg, "cell ") ||
		!strings.HasSuffix(msg, unhealthy) {
		t.Errorf("a cold lease whose cell never passes its health check: %d %v, want 502 %q", code, out,
			"cell <id>"+unhealthy)
	}
	none("a cold lease whose cell never passed its health check")

	id := lease("after the two failed builds")
	b.set(true, false)
	if code, out := post(t, srv, "/reset", map[string]any{"id": id}); code != http.StatusBadGateway ||
		out["error"] != "exit status 1" {
		t.Errorf("a reset whose create fails: %d %v, want 502 %q", code, out, "exit status 1")
	}
	none("a reset whose create failed")

	id2 := lease("after the failed reset")
	b.set(false, true)
	if code, out := post(t, srv, "/reset", map[string]any{"id": id2}); code != http.StatusBadGateway ||
		out["error"] != "cell "+id2+unhealthy {
		t.Errorf("a reset whose cell never passes its health check: %d %v, want 502 %q", code, out,
			"cell "+id2+unhealthy)
	}
	none("a reset whose cell never passed its health check")

	n := map[string]int{}
	for _, d := range b.destroyedIDs() {
		n[d]++
	}
	ok := len(n) == 4
	for d, k := range n {
		ok = ok && (k == 2) == (d == id || d == id2) && (k == 1 || k == 2)
	}
	if !ok {
		t.Errorf("destroys: %v, want 1 of each failed lease's cell and 2 of each reset's, %s and %s", n, id, id2)
	}
}

// TestLeaseAsTextPlainIs415 posts a lease as a page in a browser on this host could without a CORS preflight: a
// well-formed body sent as text/plain. The pool answers 415 with one line and leases nothing, though two cells are
// warm. The same body as application/json, with a charset, then leases one.
func TestLeaseAsTextPlainIs415(t *testing.T) {
	p, b, srv := startPool(t, testConfig())
	const lease = `{"ttl_seconds": 600, "requester": "page"}`
	resp, err := http.Post(srv.URL+"/lease", "text/plain;charset=UTF-8", strings.NewReader(lease))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want := `{"error":"Content-Type \"text/plain;charset=UTF-8\" is not application/json: a body must be JSON"}` + "\n"
	if resp.StatusCode != http.StatusUnsupportedMediaType || string(got) != want {
		t.Fatalf("lease as text/plain: %d %q; want 415 %q", resp.StatusCode, got, want)
	}
	b.mu.Lock()
	creates := len(b.blocks)
	b.mu.Unlock()
	if n := warmCount(p); n != 2 || len(p.Status().Cells) != 2 || creates != 2 {
		t.Fatalf("after a lease as text/plain: %d warm of %d cell(s), %d create(s); want the 2 warm cells alone",
			n, len(p.Status().Cells), creates)
	}
	resp, err = http.Post(srv.URL+"/lease", "application/json; charset=utf-8", strings.NewReader(lease))
	if err != nil {
		t.Fatal(err)
	}
	var e entry
	err = json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || e.State != stateLeased || e.Requester != "page" {
		t.Fatalf("the same lease as application/json: %d %+v %v; want 200 and a lease to page", resp.StatusCode, e,
			err)
	}
}

// TestContentTypes sends each route's body under each Content-Type. application/json passes, with a parameter or in
// capitals; anything else, none included, is 415 before the route runs. /release of an unknown id shows a pass as 404.
func TestContentTypes(t *testing.T) {
	_, _, srv := startPool(t, testConfig())
	for _, c := range []struct {
		ct   string
		want int
	}{
		{"application/json", http.StatusNotFound},
		{"application/json; charset=utf-8", http.StatusNotFound},
		{"Application/JSON", http.StatusNotFound},
		{"", http.StatusUnsupportedMediaType},
		{"text/plain", http.StatusUnsupportedMediaType},
		{"application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"multipart/form-data; boundary=x", http.StatusUnsupportedMediaType},
		{"application/json, text/plain", http.StatusUnsupportedMediaType},
		{"application/jsonx", http.StatusUnsupportedMediaType},
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/release", strings.NewReader(`{"id": "0badcell"}`))
		if c.ct != "" {
			req.Header.Set("Content-Type", c.ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("POST /release as %q: %d, want %d", c.ct, resp.StatusCode, c.want)
		}
	}
	// A body of unknown length is still a body: a reader other than *strings.Reader goes chunked, with no length.
	unknown := io.MultiReader(strings.NewReader(`{"id": "0badcell"}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/release", unknown)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("POST /release chunked as text/plain: %d, want 415", resp.StatusCode)
	}
	// A request with no body needs no Content-Type; the preflight a browser sends before application/json is 405.
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/status", http.StatusOK},
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodOptions, "/lease", http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s with no body: %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
}

// TestForeignHostIs403 sends each route a request whose Host header is evil.example, as a page whose DNS name rebinds
// to 127.0.0.1 would. Each is 403 with one line before its route runs, so the lease leases nothing.
func TestForeignHostIs403(t *testing.T) {
	p, _, srv := startPool(t, testConfig())
	want := `{"error":"Host \"evil.example\" is not localhost or a loopback IP: the API has no auth"}` + "\n"
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/lease", `{"ttl_seconds": 600, "requester": "page"}`},
		{http.MethodPost, "/release", `{"id": "0badcell"}`},
		{http.MethodPost, "/reset", `{"id": "0badcell"}`},
		{http.MethodPost, "/vmfault", `{"id": "0badcell", "kind": "drive", "body": {}}`},
		{http.MethodGet, "/status", ""},
		{http.MethodGet, "/healthz", ""},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader(c.body))
		req.Host = "evil.example"
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || string(got) != want {
			t.Errorf("%s %s with Host evil.example: %d %q; want 403 %q", c.method, c.path, resp.StatusCode, got, want)
		}
	}
	if n := warmCount(p); n != 2 || len(p.Status().Cells) != 2 {
		t.Errorf("after the requests with Host evil.example: %d warm of %d cell(s); want the 2 warm cells alone", n,
			len(p.Status().Cells))
	}
}

// TestLoopbackNames holds the Host headers newMux answers: localhost and loopback IPs, with or without a port, and
// nothing else. Each is a GET /healthz through newMux, Host set as a client could send it.
func TestLoopbackNames(t *testing.T) {
	h := newMux(newPool(&recordingBackend{}, testConfig()))
	// other is a loopback IP other than 127.0.0.1: -listen takes any loopback IP, and a client sends the one it
	// dialled.
	other := net.JoinHostPort(net.IPv4(127, 0, 0, 2).String(), "7700")
	for host, want := range map[string]int{
		"127.0.0.1": http.StatusOK, "127.0.0.1:7700": http.StatusOK, other: http.StatusOK,
		"localhost": http.StatusOK, "localhost:7700": http.StatusOK, "LOCALHOST:7700": http.StatusOK,
		"[::1]:7700": http.StatusOK, "[::1]": http.StatusOK, "::1": http.StatusOK,
		"": http.StatusForbidden, "evil.example": http.StatusForbidden, "evil.example:7700": http.StatusForbidden,
		"127.0.0.1.evil.example": http.StatusForbidden, "localhost.evil.example:7700": http.StatusForbidden,
		"0.0.0.0:7700": http.StatusForbidden, "[::]:7700": http.StatusForbidden,
		"10.254.0.2:7700": http.StatusForbidden,
	} {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("GET /healthz with Host %q: %d %q, want %d", host, w.Code, w.Body.String(), want)
		}
	}
}

// A reset builds the cell again with the tag its lease named.
func TestResetKeepsTag(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm = 0
	_, b, srv := startPool(t, cfg)
	_, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci", "server_tag": "1.32.1"})
	id := lease["id"].(string)
	code, reset := post(t, srv, "/reset", map[string]any{"id": id})
	if code != http.StatusOK || reset["server_tag"] != "1.32.1" {
		t.Fatalf("reset: %d %v", code, reset)
	}
	if got, want := b.callsOf(id), "create 1.32.1; health 1.32.1; destroy 1.32.1; create 1.32.1; health 1.32.1"; got != want {
		t.Fatalf("backend calls for %s: %q, want %q", id, got, want)
	}
}

// A lease's leased_at and expires_at are UTC whatever the host's zone: on the wire they end in Z, after a lease and
// after a reset, and in the table their location is UTC. time.Now's is Local even on a host that runs UTC, so the
// location check fails a regression on every host; it reads the location rather than swapping time.Local, which
// goroutines other tests leave behind read too.
func TestLeaseTimesAreUTC(t *testing.T) {
	cfg := testConfig()
	cfg.MinWarm = 0
	p, _, srv := startPool(t, cfg)
	code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci"})
	if code != http.StatusOK {
		t.Fatalf("lease: %d %v", code, lease)
	}
	code, reset := post(t, srv, "/reset", map[string]any{"id": lease["id"]})
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, reset)
	}
	for _, answer := range []struct {
		what string
		body map[string]any
	}{{"lease", lease}, {"reset", reset}} {
		for _, k := range []string{"leased_at", "expires_at"} {
			if s, _ := answer.body[k].(string); !strings.HasSuffix(s, "Z") {
				t.Errorf("the %s's %s is %q, want a UTC time ending in Z", answer.what, k, s)
			}
		}
	}
	zone := func(t time.Time) string {
		if t.Location() == time.Local {
			return "Local (the host's zone)"
		}
		return t.Location().String()
	}
	for _, c := range p.Status().Cells {
		if c.LeasedAt.Location() != time.UTC || c.ExpiresAt.Location() != time.UTC {
			t.Errorf("cell %s: leased_at in %s, expires_at in %s; want UTC", c.ID, zone(c.LeasedAt), zone(c.ExpiresAt))
		}
	}
}

// environ sets LAUG_BLOCK, and SERVER_TAG when the cell has a tag, in place of laugd's own: each key once.
func TestEnvironReplacesKeys(t *testing.T) {
	base := []string{"FOO=b", "SERVER_TAG=a", "LAUG_BLOCK=9"}
	if got, want := strings.Join(environ(base, 2, "1.32.1"), " "), "FOO=b LAUG_BLOCK=2 SERVER_TAG=1.32.1"; got != want {
		t.Fatalf("environ with a tag: %q, want %q", got, want)
	}
	if got, want := strings.Join(environ(base, 2, ""), " "), "FOO=b SERVER_TAG=a LAUG_BLOCK=2"; got != want {
		t.Fatalf("environ with no tag: %q, want %q", got, want)
	}
	if got := strings.Join(base, " "); got != "FOO=b SERVER_TAG=a LAUG_BLOCK=9" {
		t.Fatalf("environ changed its base: %q", got)
	}
}

// A lease's tag reaches a script backend over laugd's own SERVER_TAG: the two warm cells run laugd's 1.31.0, and a
// lease naming 1.32.1 gets a cell of 1.32.1. It cannot tell replacing the key from appending it, since os/exec keeps
// the last of a duplicate key (Cmd.Env's documentation, dedupEnv in the Go release go.mod names);
// TestEnvironReplacesKeys checks environ itself.
func TestScriptBackendLeaseTagWinsOverEnv(t *testing.T) {
	script := filepath.Join(t.TempDir(), "backend.sh")
	body := "#!/bin/sh\n" +
		"[ \"$1\" = create ] && printf '{\"id\":\"%s\",\"address\":\"127.0.0.1:%s\",\"server_tag\":\"%s\"}\\n' \"$2\" \"$LAUG_BLOCK\" \"$SERVER_TAG\"\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVER_TAG", "1.31.0")
	p := newPool(scriptBackend{path: script}, testConfig())
	p.replenish()
	for _, c := range p.Status().Cells {
		if c.State != stateWarm || c.ServerTag != "1.31.0" {
			t.Fatalf("warm cell %s: state %s, server_tag %q; want warm, 1.31.0", c.ID, c.State, c.ServerTag)
		}
	}
	srv := httptest.NewServer(newMux(p))
	t.Cleanup(srv.Close)
	code, lease := post(t, srv, "/lease", map[string]any{"ttl_seconds": 600, "requester": "ci", "server_tag": "1.32.1"})
	if code != http.StatusOK || lease["server_tag"] != "1.32.1" {
		t.Fatalf("lease naming 1.32.1 under SERVER_TAG=1.31.0: %d %v, want server_tag 1.32.1", code, lease)
	}
	// The replenish the lease started finds 2 warm and builds nothing; let it end before the temp dir goes.
	waitFor(t, "two warm cells of 1.31.0 left", func() bool { return warmCount(p) == 2 })
}

// Serve answers on its listener, and once its context is done it stops within StopGrace, under 2 s, though a cold
// lease is still waiting on its create: the lease's connection is closed, Serve returns nil and the count of cells in
// its table, and the port no longer answers.
func TestServeStopsWithinGrace(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	cfg := testConfig()
	cfg.MinWarm, cfg.Quota = 0, 2
	var logged bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		cells int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		n, err := Serve(ctx, ln, &gatedBackend{wait: func() { <-gate }}, cfg, log.New(&logged, "", 0))
		done <- result{n, err}
	}()
	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	leased := make(chan error, 1)
	go func() {
		resp, err := http.Post(base+"/lease", "application/json", strings.NewReader(`{"requester":"ci","ttl_seconds":60}`))
		if err == nil {
			resp.Body.Close()
		}
		leased <- err
	}()
	waitFor(t, "the cold lease's cell to be busy", func() bool {
		resp, err := http.Get(base + "/status")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var v statusView
		return json.NewDecoder(resp.Body).Decode(&v) == nil && len(v.Cells) == 1 && v.Cells[0].State == stateBusy
	})
	start := time.Now()
	cancel()
	select {
	case r := <-done:
		if took := time.Since(start); r.err != nil || r.cells != 1 || took >= 2*time.Second {
			t.Fatalf("Serve returned %d, %v after %s; want 1, nil, under 2 s", r.cells, r.err, took)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within 3 s of its context's end")
	}
	select {
	case err := <-leased:
		if err == nil {
			t.Error("the lease in flight got an answer; its connection should have closed")
		}
	case <-time.After(2 * time.Second):
		t.Error("the lease in flight was still open 2 s after Serve returned")
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Error("the listener still accepts after Serve returned")
	}
}

// slowDestroyBackend is recordingBackend with a Destroy that waits until the test calls finish, or until its context
// ends. started closes when the first destroy begins; ended gets the context's error, or nil when finish came first.
type slowDestroyBackend struct {
	recordingBackend
	started, done chan struct{}
	once          sync.Once
	ended         chan error
}

func (b *slowDestroyBackend) Destroy(ctx context.Context, _ string, _ int, _ string) error {
	b.once.Do(func() { close(b.started) })
	select {
	case <-ctx.Done():
		b.ended <- ctx.Err()
		return ctx.Err()
	case <-b.done:
		b.ended <- nil
		return nil
	}
}

// A stop leaves a TTL reap's destroy under way to run on, as it leaves a release's: the stop does not end the
// destroy's context, which would kill the backend's script part way, and the cell stays in the table Serve counts.
func TestServeLeavesAReapDestroy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.MinWarm = 0
	b := &slowDestroyBackend{started: make(chan struct{}), done: make(chan struct{}), ended: make(chan error, 1)}
	finish := sync.OnceFunc(func() { close(b.done) })
	t.Cleanup(finish)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		cells int
		err   error
	}
	served := make(chan result, 1)
	go func() {
		n, err := Serve(ctx, ln, b, cfg, log.New(io.Discard, "", 0))
		served <- result{n, err}
	}()
	resp, err := http.Post("http://"+ln.Addr().String()+"/lease", "application/json",
		strings.NewReader(`{"requester":"ci","ttl_seconds":1}`))
	if err != nil {
		t.Fatalf("POST /lease: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /lease: %d, want 200", resp.StatusCode)
	}
	select {
	case <-b.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the reaper did not start the 1 s lease's destroy within 3 s")
	}
	cancel()
	var r result
	select {
	case r = <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within 3 s of its context's end")
	}
	select {
	case err := <-b.ended:
		t.Fatalf("the stop ended the reap's destroy: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if r.err != nil || r.cells != 1 {
		t.Fatalf("Serve returned %d, %v; want 1, nil: the cell whose destroy is under way", r.cells, r.err)
	}
	finish()
	select {
	case err := <-b.ended:
		if err != nil {
			t.Fatalf("the reap's destroy ended with %v after the test let it finish", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reap's destroy did not end within 2 s of the test letting it finish")
	}
}

// A listener that fails ends Serve at once with its error.
func TestServeListenerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Serve(context.Background(), ln, Fake(), testConfig(), log.New(io.Discard, "", 0))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve on a closed listener: %v, want the listener's error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve on a closed listener did not return within 2 s")
	}
}

// Script hands the verbs the environment and stderr it was given, not this process's.
func TestScriptEnvAndStderr(t *testing.T) {
	script := filepath.Join(t.TempDir(), "backend.sh")
	body := "#!/bin/sh\n" +
		"echo \"$1 $FROM_POOL\" >&2\n" +
		"[ \"$1\" = create ] && printf '{\"id\":\"%s\",\"address\":\"127.0.0.1:%s\"}\\n' \"$2\" \"$LAUG_BLOCK\"\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FROM_POOL", "the process's")
	var stderr bytes.Buffer
	b := Script(script, []string{"PATH=" + os.Getenv("PATH"), "FROM_POOL=given"}, &stderr)
	c, err := b.Create(context.Background(), "0a0b0c0d", 3, "")
	if err != nil || c.Address != "127.0.0.1:3" {
		t.Fatalf("create: %+v, %v", c, err)
	}
	if err := b.Destroy(context.Background(), "0a0b0c0d", 3, ""); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if got, want := stderr.String(), "create given\ndestroy given\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
}
