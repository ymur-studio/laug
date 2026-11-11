// Package pool is laug's warm pool manager: lease, release, reset, TTL, quotas, over HTTP on loopback. laug pool
// serve runs it (cmd/laug), a gate's own with -pid-file none, as gate/run.sh and gate/step.sh start it. Standard
// library only. Backends are scripts (see README.md beside this file).
package pool

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cell is what a backend returns for a running cell.
type Cell struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	UI        string `json:"ui,omitempty"`
	Grafana   string `json:"grafana,omitempty"`
	Prom      string `json:"prometheus,omitempty"`
	Toxiproxy string `json:"toxiproxy,omitempty"`
	Project   string `json:"project,omitempty"`             // compose project on the host, laug-<id>; for a microVM cell it names none, so host tools refuse
	DynConfig string `json:"dynconfig,omitempty"`           // this cell's own dynamic config file: on the host, or in the guest when SSH is set
	SSH       string `json:"ssh,omitempty"`                 // a microVM cell's guest, root@<address>; faults reach its Docker over ssh
	ServerTag string `json:"server_tag,omitempty"`          // the temporalio/server tag the cell runs, as the backend says (compose: SERVER_TAG)
	Backend   string `json:"backend"`                       // the backend that made the cell, as it names itself: compose or firecracker (the fake: fake)
	FCVersion string `json:"firecracker_version,omitempty"` // firecracker: the VMM version its snapshot was taken with (snapshot.json)
	CacheType string `json:"cache_type,omitempty"`          // firecracker: the drive's cache_type its snapshot was taken with (snapshot.json)
}

// Backend creates, destroys and health-checks cells. block is the host port block laugd allotted
// the cell; the backend derives its published ports from it and never picks ports itself. tag is
// the server tag the cell's lease named, "" for none; every verb gets it for the cell's whole life.
// Fault acts on a leased cell's VMM: kind names what (drive, for now) and body is the compact JSON the backend sends
// on. The Firecracker backend acts on it, the compose backend answers an error, and the fake records the call and
// succeeds.
type Backend interface {
	Create(ctx context.Context, id string, block int, tag string) (Cell, error)
	Destroy(ctx context.Context, id string, block int, tag string) error
	Health(ctx context.Context, id string, block int, tag string) error
	Fault(ctx context.Context, id string, block int, tag, kind, body string) error
}

// scriptBackend shells out to a script: <path> create|destroy|health <id>, and <path> vmfault <id> <kind> <body>.
// Each verb runs with the pool's environment, env, or this process's when env is nil, LAUG_BLOCK set to the cell's
// block, and SERVER_TAG set to the tag its lease named, when it named one, for the cell's whole life: create, health,
// destroy and reset. create's, destroy's and vmfault's stderr go to stderr, or this process's when it is nil. An
// *os.File is handed to create and destroy as it is; any other writer, and vmfault's always, gets a pipe, which
// os/exec copies until every process holding it has closed it (Cmd.Stderr's documentation), so a verb that
// leaves a process behind would hold the call open.
type scriptBackend struct {
	path   string
	env    []string
	stderr io.Writer
}

// Script is a backend that runs the script at path, as scriptBackend says.
func Script(path string, env []string, stderr io.Writer) Backend {
	return scriptBackend{path: path, env: env, stderr: stderr}
}

func (b scriptBackend) cmd(ctx context.Context, verb, id string, block int, tag string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, b.path, append([]string{verb, id}, args...)...)
	base := b.env
	if base == nil {
		base = os.Environ()
	}
	c.Env = environ(base, block, tag)
	c.Stderr = b.stderr
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	return c
}

// environ is a backend call's environment: laugd's own, with LAUG_BLOCK set to the cell's block and, when the cell
// has a tag of its own, SERVER_TAG set to it, each in place of any value laugd's environment holds, so no key is there
// twice (os/exec would keep the last; this does not rely on it).
func environ(base []string, block int, tag string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k == "LAUG_BLOCK" || (tag != "" && k == "SERVER_TAG") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, fmt.Sprintf("LAUG_BLOCK=%d", block))
	if tag != "" {
		out = append(out, "SERVER_TAG="+tag)
	}
	return out
}

func (b scriptBackend) Create(ctx context.Context, id string, block int, tag string) (Cell, error) {
	out, err := b.cmd(ctx, "create", id, block, tag).Output()
	if err != nil {
		return Cell{}, fmt.Errorf("backend create %s: %w", id, err)
	}
	var c Cell
	if err := json.Unmarshal(out, &c); err != nil {
		return Cell{}, fmt.Errorf("backend create %s: bad JSON: %w", id, err)
	}
	if c.ID == "" {
		c.ID = id
	}
	return c, nil
}

func (b scriptBackend) Destroy(ctx context.Context, id string, block int, tag string) error {
	return b.cmd(ctx, "destroy", id, block, tag).Run()
}

func (b scriptBackend) Health(ctx context.Context, id string, block int, tag string) error {
	c := b.cmd(ctx, "health", id, block, tag)
	c.Stderr = nil
	return c.Run()
}

// Fault runs vmfault <id> <kind> <body>. Its stderr goes where the other verbs' does and, trimmed to its last 1 KiB,
// into the error, so the route can answer with the backend's own line: the compose backend's usage error, say.
func (b scriptBackend) Fault(ctx context.Context, id string, block int, tag, kind, body string) error {
	c := b.cmd(ctx, "vmfault", id, block, tag, kind, body)
	var msg bytes.Buffer
	c.Stderr = io.MultiWriter(c.Stderr, &msg)
	if err := c.Run(); err != nil {
		m := strings.TrimSpace(msg.String())
		if len(m) > 1024 {
			m = m[len(m)-1024:]
		}
		if m == "" {
			return fmt.Errorf("backend vmfault %s %s: %w", id, kind, err)
		}
		return fmt.Errorf("backend vmfault %s %s: %w: %s", id, kind, err, m)
	}
	return nil
}

// fakeBackend answers instantly; useful for exercising the API without Docker. It ignores the tag and prints none.
// Its Fault keeps the last call, for a test to read, and succeeds.
type fakeBackend struct {
	mu        sync.Mutex
	lastFault []string // id, kind, body of the last Fault call
}

// Fake is a backend that answers at once and starts nothing: a cell is a row in the pool's table and nothing more.
func Fake() Backend { return &fakeBackend{} }

func (*fakeBackend) Create(_ context.Context, id string, block int, _ string) (Cell, error) {
	return Cell{ID: id, Address: fmt.Sprintf("fake-%s:%d", id, 7233+10*block), Backend: "fake"}, nil
}
func (*fakeBackend) Destroy(context.Context, string, int, string) error { return nil }
func (*fakeBackend) Health(context.Context, string, int, string) error  { return nil }
func (b *fakeBackend) Fault(_ context.Context, id string, _ int, _, kind, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastFault = []string{id, kind, body}
	return nil
}

// lastFaultCall is the id, kind and body of the last Fault call, nil before the first.
func (b *fakeBackend) lastFaultCall() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lastFault...)
}

type state string

const (
	stateWarm   state = "warm"
	stateLeased state = "leased"
	stateBusy   state = "busy"  // being created, reset or destroyed
	stateStuck  state = "stuck" // a destroy failed or timed out: kept, with its block, until a retried one succeeds
)

type entry struct {
	Cell
	Block     int       `json:"block"`
	State     state     `json:"state"`
	Requester string    `json:"requester,omitempty"`
	LeasedAt  time.Time `json:"leased_at"`
	ExpiresAt time.Time `json:"expires_at"`
	tag       string    // the SERVER_TAG laugd gives the backend for this cell, "" for laugd's own environment
	retrying  bool      // a stuck cell's destroy is being tried again (retryStuck); one try at a time
	retries   int       // a stuck cell's tries that failed, for the log
}

// tagOf is the server tag a cell runs: the one its backend printed, else the one laugd gave it.
func tagOf(e *entry) string {
	if e.ServerTag != "" {
		return e.ServerTag
	}
	return e.tag
}

// tagRE is the server tag a lease may name: a release, x.y.z.
var tagRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

var (
	errNotFound  = errors.New("cell not found")
	errNotLeased = errors.New("not leased")
	errPoolFull  = errors.New("pool is at max cells")
)

// Config is everything a pool needs besides its backend.
type Config struct {
	MinWarm       int
	MaxCells      int
	Quota         int
	MaxLease      time.Duration
	CreateTimeout time.Duration
	HealthPoll    time.Duration
	ReapInterval  time.Duration
}

// Pool holds the cells and the guardrails.
type Pool struct {
	mu           sync.Mutex
	backend      Backend
	cfg          Config
	cells        map[string]*entry
	blocks       []bool // blocks[i] is true while port block i belongs to a cell
	replenishing bool
	log          *log.Logger
}

func newPool(b Backend, cfg Config) *Pool {
	return &Pool{
		backend: b,
		cfg:     cfg,
		cells:   map[string]*entry{},
		blocks:  make([]bool, cfg.MaxCells),
		log:     log.Default(),
	}
}

// allocBlock hands out the lowest free port block. Must be called with p.mu held. There is one block
// per possible cell, so a pool under MaxCells can never run out.
func (p *Pool) allocBlock() (int, error) {
	for i, used := range p.blocks {
		if !used {
			p.blocks[i] = true
			return i, nil
		}
	}
	return 0, errPoolFull
}

// forget drops a cell and returns its block to the free list. Must be called with p.mu held.
func (p *Pool) forget(e *entry) {
	delete(p.cells, e.ID)
	if e.Block >= 0 && e.Block < len(p.blocks) {
		p.blocks[e.Block] = false
	}
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *Pool) waitHealthy(ctx context.Context, id string, block int, tag string) error {
	t := time.NewTicker(p.cfg.HealthPoll)
	defer t.Stop()
	for {
		if err := p.backend.Health(ctx, id, block, tag); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cell %s not healthy before deadline: %w", id, ctx.Err())
		case <-t.C:
		}
	}
}

// held counts the cells requester holds or is being given: leased to it, busy on its behalf (a cold create for its
// lease, a reset of its lease, or a release under way), or stuck after a destroy failed. Must be called with
// p.mu held.
func (p *Pool) held(requester string) int {
	n := 0
	for _, e := range p.cells {
		if e.Requester == requester && (e.State == stateLeased || e.State == stateBusy || e.State == stateStuck) {
			n++
		}
	}
	return n
}

// reserve takes a slot and a port block for a cell about to be built: a busy entry that carries its requester from
// the start ("" for a warm cell), so the quota counts it at once and no other lease can take it, and the tag its
// lease named ("" for none). Must be called with p.mu held.
func (p *Pool) reserve(requester, tag string) (*entry, error) {
	if len(p.cells) >= p.cfg.MaxCells {
		return nil, errPoolFull
	}
	block, err := p.allocBlock()
	if err != nil {
		return nil, err
	}
	id := newID()
	e := &entry{Cell: Cell{ID: id}, Block: block, State: stateBusy, Requester: requester, tag: tag}
	p.cells[id] = e
	return e, nil
}

// build creates the cell reserve made room for and waits for its health. On a failure it destroys what the create
// left, on a context of its own, and gives the slot back; when that destroy fails too, keepStuck keeps the cell, its
// block and its slot. Either way the error is errBackend, so the route answers 502. On success, under the lock that
// stores the cell, it leases the cell to requester, or marks it warm when requester is "": a cell built for a lease is
// never warm on the way. It returns a copy.
func (p *Pool) build(ctx context.Context, e *entry, requester string, ttl time.Duration) (entry, error) {
	id, block, tag := e.ID, e.Block, e.tag
	c, err := p.backend.Create(ctx, id, block, tag)
	if err == nil {
		err = p.waitHealthy(ctx, id, block, tag)
	}
	if err != nil {
		dctx, dcancel := context.WithTimeout(context.Background(), p.cfg.CreateTimeout)
		derr := p.backend.Destroy(dctx, id, block, tag)
		dcancel()
		p.mu.Lock()
		defer p.mu.Unlock()
		if derr != nil {
			return entry{}, keepStuck(e, err, derr)
		}
		p.forget(e)
		return entry{}, errBackend{err}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e.Cell = c
	if requester != "" {
		p.markLeased(e, requester, ttl)
	} else {
		e.State = stateWarm
		e.Requester = ""
	}
	return *e, nil
}

// Lease hands out a warm cell, or builds one if the pool has room. The quota check and the reservation of a cold
// cell's slot are one critical section, and the cell goes from busy to leased in another. A lease that names a tag
// takes only a warm cell of that tag, and otherwise gets a cell built with it. A ttl over MaxLease is cut to it, and
// so is one of 0 or less, which /lease refuses with 400 before it gets here. A quota or a full pool is a plain error;
// a cold cell's failed build is build's errBackend.
func (p *Pool) Lease(ctx context.Context, requester string, ttl time.Duration, tag string) (entry, error) {
	if ttl <= 0 || ttl > p.cfg.MaxLease {
		ttl = p.cfg.MaxLease
	}
	p.mu.Lock()
	if n := p.held(requester); n >= p.cfg.Quota {
		p.mu.Unlock()
		return entry{}, fmt.Errorf("quota: %s already holds %d lease(s)", requester, n)
	}
	for _, e := range p.cells {
		if e.State == stateWarm && (tag == "" || tagOf(e) == tag) {
			p.markLeased(e, requester, ttl)
			snapshot := *e
			p.mu.Unlock()
			go p.replenish()
			return snapshot, nil
		}
	}
	e, err := p.reserve(requester, tag)
	p.mu.Unlock()
	if err != nil {
		return entry{}, err
	}
	snapshot, err := p.build(ctx, e, requester, ttl)
	if err != nil {
		return entry{}, err
	}
	go p.replenish()
	return snapshot, nil
}

// markLeased must be called with p.mu held. A lease's times are in UTC whatever the host's zone; time.Now's are in
// Local.
func (p *Pool) markLeased(e *entry, requester string, ttl time.Duration) {
	e.State = stateLeased
	e.Requester = requester
	e.LeasedAt = time.Now().UTC()
	e.ExpiresAt = e.LeasedAt.Add(ttl)
}

// Release destroys a leased cell and frees its block; replenish builds a fresh warm one. The destroy runs on a context
// the request cannot cancel, bounded by CreateTimeout as create and reset are, so a hung destroy cannot hold the cell
// busy, or the reaper, forever. A destroy that fails or times out may leave containers holding the block's ports.
// Then the cell stays in the table as stuck, with its block, its requester and its place in the quota; Release starts
// no replenish and returns errBackend with the destroy's error. The reaper tries the destroy again each tick
// (retryStuck); once a try succeeds, the cell goes and its block, its slot in -max and its quota place come back. A
// stuck cell still in the table when the pool stops is laug pool down -remove-cells's to remove, with -backend
// firecracker for a microVM cell.
func (p *Pool) Release(ctx context.Context, id string) error {
	p.mu.Lock()
	e, ok := p.cells[id]
	if !ok {
		p.mu.Unlock()
		return errNotFound
	}
	if e.State != stateLeased {
		p.mu.Unlock()
		return fmt.Errorf("cell %s is %s, not leased", id, e.State)
	}
	e.State = stateBusy
	p.mu.Unlock()

	dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.CreateTimeout)
	err := p.backend.Destroy(dctx, id, e.Block, e.tag)
	dcancel()
	p.mu.Lock()
	if err != nil {
		e.State = stateStuck
		p.mu.Unlock()
		err = errBackend{fmt.Errorf("destroy failed; the cell is kept as stuck, with block %d: %w", e.Block, err)}
		p.log.Printf("release %s: %v", id, err)
		return err
	}
	p.forget(e)
	p.mu.Unlock()
	go p.replenish()
	return nil
}

// Reset destroys and recreates the cell behind a lease, keeping the lease, the id and the port block. errNotFound for
// no such cell; an error in Release's form for a cell that is not leased. A failed first destroy is logged, and the
// create goes on. What a failed create left is destroyed before its block goes back; when that destroy fails too, it
// is logged, and keepStuck keeps the cell, its block and its slot. A failed create, or a health check that never
// passed, returns errBackend either way, so the route answers 502.
func (p *Pool) Reset(ctx context.Context, id string) (entry, error) {
	p.mu.Lock()
	e, ok := p.cells[id]
	if !ok {
		p.mu.Unlock()
		return entry{}, errNotFound
	}
	if e.State != stateLeased {
		p.mu.Unlock()
		return entry{}, fmt.Errorf("cell %s is %s, not leased", id, e.State)
	}
	requester, expires, block, tag := e.Requester, e.ExpiresAt, e.Block, e.tag
	e.State = stateBusy
	p.mu.Unlock()

	if derr := p.backend.Destroy(ctx, id, block, tag); derr != nil {
		p.log.Printf("reset %s: destroy before create: %v", id, derr)
	}
	c, err := p.backend.Create(ctx, id, block, tag)
	if err == nil {
		err = p.waitHealthy(ctx, id, block, tag)
	}
	if err != nil {
		// The create may have left containers holding this block's ports. Destroy them on a context the
		// request cannot cancel before the block goes back on the free list.
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.CreateTimeout)
		derr := p.backend.Destroy(dctx, id, block, tag)
		dcancel()
		if derr != nil {
			p.log.Printf("reset %s: destroy after failed create: %v", id, derr)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if derr != nil {
			return entry{}, keepStuck(e, err, derr)
		}
		p.forget(e)
		go p.replenish()
		return entry{}, errBackend{err}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e.Cell = c
	e.State = stateLeased
	e.Requester = requester
	e.LeasedAt = time.Now().UTC()
	e.ExpiresAt = expires
	return *e, nil
}

// keepStuck keeps e as stuck after a failed create or reset (err) whose destroy failed too (derr), as Release keeps a
// cell whose destroy fails. The create may have left containers holding the block's ports, so the cell keeps its
// block, its requester and its place in the quota and in -max until the reaper's retry of its destroy succeeds
// (retryStuck). It returns the errBackend that says so. Must be called with the pool's lock held.
func keepStuck(e *entry, err, derr error) error {
	e.State = stateStuck
	return errBackend{fmt.Errorf("%w; destroy failed too; the cell is kept as stuck, with block %d: %w", err, e.Block,
		derr)}
}

// errBackend wraps a backend's failure, so the route answers 502 with its text: a Fault's error or a release's failed
// destroy. It also wraps a failed create in a cold lease or a reset, and a cell that never passed its health check.
type errBackend struct{ err error }

func (e errBackend) Error() string { return e.err.Error() }
func (e errBackend) Unwrap() error { return e.err }

// Fault hands a leased cell's VMM a change through the backend: kind names what, body is compact JSON. The cell stays
// leased and is not marked busy; the backend checks its block still holds the cell. errNotFound for no such cell, an
// error wrapping errNotLeased for one not leased, errBackend for the backend's failure.
func (p *Pool) Fault(ctx context.Context, id, kind, body string) error {
	p.mu.Lock()
	e, ok := p.cells[id]
	if !ok {
		p.mu.Unlock()
		return errNotFound
	}
	if e.State != stateLeased {
		p.mu.Unlock()
		return fmt.Errorf("cell %s is %s, %w", id, e.State, errNotLeased)
	}
	block, tag := e.Block, e.tag
	p.mu.Unlock()
	fctx, cancel := context.WithTimeout(ctx, p.cfg.CreateTimeout)
	defer cancel()
	if err := p.backend.Fault(fctx, id, block, tag, kind, body); err != nil {
		return errBackend{err}
	}
	return nil
}

// replenish builds warm cells until minWarm is met or maxCells is reached.
// Busy cells count as in-flight so two replenish runs do not overshoot.
func (p *Pool) replenish() {
	p.mu.Lock()
	if p.replenishing {
		p.mu.Unlock()
		return
	}
	p.replenishing = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.replenishing = false
		p.mu.Unlock()
	}()

	for {
		p.mu.Lock()
		ready, total := 0, len(p.cells)
		for _, e := range p.cells {
			if e.State == stateWarm || e.State == stateBusy {
				ready++
			}
		}
		if ready >= p.cfg.MinWarm || total >= p.cfg.MaxCells {
			p.mu.Unlock()
			return
		}
		e, err := p.reserve("", "")
		p.mu.Unlock()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), p.cfg.CreateTimeout)
			_, err = p.build(ctx, e, "", 0)
			cancel()
		}
		if err != nil {
			p.log.Printf("replenish: %v", err)
			return
		}
	}
}

// reaper releases leases past their expiry until ctx is done; each tick it also starts a replenish and tries each
// stuck cell's destroy again (retryStuck). A release or a retry under way when ctx ends runs on, on a context the stop
// does not end, as handleRelease's does: a stop never kills a backend's destroy part way and then forgets a cell whose
// containers may still run. No release or retry starts after ctx ends.
func (p *Pool) reaper(ctx context.Context) {
	t := time.NewTicker(p.cfg.ReapInterval)
	defer t.Stop()
	start := time.Now()
	wallStart := start.Round(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// Round(0) strips the monotonic reading so the comparison is wall-clock: a TTL is a promise about
		// wall time, and in a VM the two clocks can disagree. On the lab host they agreed, 0.9999 and 0 steps (H1);
		// a 600 s lease's 31.2 s past its TTL was release 9.7 s after it plus up to 21.6 s to destroy (P4;
		// cells/results/2026-10-23-boot1.txt). Wall clock removes a stepped clock as a cause.
		now := time.Now().Round(0)
		// If these two drift apart the wall clock stepped; logged so the next late reap has its cause in
		// laugd's own log. laug host clock measures the same drift without laugd.
		if mono, wall := time.Since(start), now.Sub(wallStart); (mono - wall).Abs() > p.cfg.ReapInterval {
			p.log.Printf("reaper: clock step detected: monotonic %s, wall %s since start", mono.Round(time.Second), wall.Round(time.Second))
		}
		go p.replenish() // a failed create gave up; retry on the next tick
		p.retryStuck(ctx)
		var expired []string
		p.mu.Lock()
		for id, e := range p.cells {
			if e.State == stateLeased && now.After(e.ExpiresAt.Round(0)) {
				expired = append(expired, id)
			}
		}
		p.mu.Unlock()
		for _, id := range expired {
			if ctx.Err() != nil {
				return
			}
			p.log.Printf("ttl expired: releasing %s", id)
			if err := p.Release(context.WithoutCancel(ctx), id); err != nil {
				p.log.Printf("release %s: %v", id, err)
			}
		}
	}
}

// retryStuck starts one more try of each stuck cell's destroy, a goroutine a cell, so a destroy that hangs until
// -create-timeout holds up no reap. Under the lock it marks each try in flight and copies what Destroy needs;
// retryDestroy runs it with the lock free. A cell whose try is still in flight gets no second. Nothing starts once ctx
// is done.
func (p *Pool) retryStuck(ctx context.Context) {
	type try struct {
		e       *entry
		id, tag string
		block   int
	}
	var tries []try
	p.mu.Lock()
	if ctx.Err() == nil {
		for _, e := range p.cells {
			if e.State == stateStuck && !e.retrying {
				e.retrying = true
				tries = append(tries, try{e, e.ID, e.tag, e.Block})
			}
		}
	}
	p.mu.Unlock()
	for _, t := range tries {
		go p.retryDestroy(ctx, t.e, t.id, t.block, t.tag)
	}
}

// retryDestroy runs a stuck cell's destroy once more, on a context the stop does not end, bounded by CreateTimeout as
// Release's is. When it succeeds the pool forgets the cell, which frees its block, its slot in -max and its place in
// its requester's quota, and starts a replenish, as a release does. When it fails the cell stays stuck for the next
// tick. The pool logs a failed try only when its count is a power of two: the 1st, 2nd, 4th, 8th and so on. An outage
// of a day at a 10 s tick then gives at most 14 of these lines a cell, not 8640. A script backend's destroy still
// writes its own stderr on every try, and laug pool serve sends that to the same log. The try that succeeds logs its
// count. Only this forgets a stuck cell, and one runs a cell at a time.
func (p *Pool) retryDestroy(ctx context.Context, e *entry, id string, block int, tag string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.CreateTimeout)
	err := p.backend.Destroy(dctx, id, block, tag)
	cancel()
	p.mu.Lock()
	e.retrying = false
	if err != nil {
		e.retries++
		n := e.retries
		p.mu.Unlock()
		if n&(n-1) == 0 {
			p.log.Printf("stuck %s: destroy retry %d failed; block %d kept: %v", id, n, block, err)
		}
		return
	}
	n := e.retries + 1
	p.forget(e)
	p.mu.Unlock()
	p.log.Printf("stuck %s: destroy retry %d succeeded; block %d is free", id, n, block)
	go p.replenish()
}

type statusView struct {
	MinWarm  int     `json:"min_warm"`
	MaxCells int     `json:"max_cells"`
	Quota    int     `json:"quota"`
	MaxLease string  `json:"max_lease"`
	Cells    []entry `json:"cells"`
}

func (p *Pool) Status() statusView {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := statusView{MinWarm: p.cfg.MinWarm, MaxCells: p.cfg.MaxCells, Quota: p.cfg.Quota, MaxLease: p.cfg.MaxLease.String()}
	for _, e := range p.cells {
		v.Cells = append(v.Cells, *e)
	}
	sort.Slice(v.Cells, func(i, j int) bool { return v.Cells[i].ID < v.Cells[j].ID })
	return v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

type idRequest struct {
	ID string `json:"id"`
}

// handleLease reads {"ttl_seconds": 600, "requester": "ci", "server_tag": "x.y.z"} and answers the lease. Before any
// cell is reserved it answers 400 for a body that does not decode, a server_tag that is not x.y.z, or a ttl_seconds
// of 0 or less. A missing ttl_seconds is 0, and the 400 says so. A ttl_seconds over MaxLease in whole seconds gets
// MaxLease. The check comes before the multiplication into a time.Duration, because that multiplication wraps in an
// int64: 1<<62 s is 0 ns, and 1<<55+1 s is 1 s. A missing server_tag is no error: the lease takes a warm cell of any
// tag, or builds one with the pool's own. It answers 409 for the requester's quota or a full pool. It answers 502 with
// the backend's text when a cold cell's create fails or the cell never passes its health check.
func (p *Pool) handleLease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TTLSeconds int    `json:"ttl_seconds"`
		Requester  string `json:"requester"`
		ServerTag  string `json:"server_tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.ServerTag != "" && !tagRE.MatchString(req.ServerTag) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("server_tag must be a release tag, x.y.z; got %q", req.ServerTag))
		return
	}
	if req.TTLSeconds <= 0 {
		msg := fmt.Sprintf("ttl_seconds must be 1 or more; got %d", req.TTLSeconds)
		if req.TTLSeconds == 0 {
			msg += " (a missing ttl_seconds reads as 0)"
		}
		writeError(w, http.StatusBadRequest, errors.New(msg))
		return
	}
	ttl := p.cfg.MaxLease
	if int64(req.TTLSeconds) <= int64(p.cfg.MaxLease/time.Second) {
		ttl = time.Duration(req.TTLSeconds) * time.Second // at most MaxLease, so it cannot wrap
	}
	if req.Requester == "" {
		req.Requester = "anonymous"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), p.cfg.CreateTimeout)
	defer cancel()
	e, err := p.Lease(ctx, req.Requester, ttl, req.ServerTag)
	if err != nil {
		code := http.StatusConflict
		if errors.As(err, new(errBackend)) {
			code = http.StatusBadGateway
		}
		writeError(w, code, err)
		return
	}
	if r.Context().Err() != nil {
		// The client left while a cold cell was built. Nobody holds this id, so give the cell back now
		// instead of holding it, and a quota slot, for the whole TTL.
		p.log.Printf("lease %s: client gone before the reply; releasing", e.ID)
		go func() {
			rctx, rcancel := context.WithTimeout(context.Background(), p.cfg.CreateTimeout)
			defer rcancel()
			if err := p.Release(rctx, e.ID); err != nil {
				p.log.Printf("release %s: %v", e.ID, err)
			}
		}()
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (p *Pool) handleRelease(w http.ResponseWriter, r *http.Request) {
	var req idRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("body must be {\"id\": \"...\"}"))
		return
	}
	if err := p.Release(context.WithoutCancel(r.Context()), req.ID); err != nil {
		code := http.StatusConflict
		switch {
		case errors.Is(err, errNotFound):
			code = http.StatusNotFound
		case errors.As(err, new(errBackend)):
			code = http.StatusBadGateway
		}
		writeError(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"released": req.ID})
}

// handleReset reads {"id": "..."} and answers the lease with its fresh cell. It answers 400 for a bad body, 404 for an
// unknown cell and 409 for one not leased. It answers 502 with the backend's text when the create fails or the cell
// never passes its health check.
func (p *Pool) handleReset(w http.ResponseWriter, r *http.Request) {
	var req idRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("body must be {\"id\": \"...\"}"))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), p.cfg.CreateTimeout)
	defer cancel()
	e, err := p.Reset(ctx, req.ID)
	if err != nil {
		code := http.StatusConflict
		switch {
		case errors.Is(err, errNotFound):
			code = http.StatusNotFound
		case errors.As(err, new(errBackend)):
			code = http.StatusBadGateway
		}
		writeError(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// kindRE is the kind a /vmfault may name: lowercase letters. The backend says which it knows.
var kindRE = regexp.MustCompile(`^[a-z]+$`)

// maxFaultBody bounds a /vmfault request, 64 KiB. A drive limiter's is under 200 bytes.
const maxFaultBody = 64 << 10

// handleVMFault reads {"id": "...", "kind": "drive", "body": {...}} and runs the backend's Fault with the body as
// compact JSON: 404 for an unknown cell, 409 for one not leased, 400 for a bad request, 502 with the backend's text
// when it fails, and {"ok": true} otherwise.
func (p *Pool) handleVMFault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string          `json:"id"`
		Kind string          `json:"kind"`
		Body json.RawMessage `json:"body"`
	}
	bad := errors.New(`body must be {"id": "...", "kind": "drive", "body": {...}}`)
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFaultBody)).Decode(&req); err != nil ||
		req.ID == "" || !kindRE.MatchString(req.Kind) {
		writeError(w, http.StatusBadRequest, bad)
		return
	}
	var obj map[string]json.RawMessage
	var body bytes.Buffer
	if json.Unmarshal(req.Body, &obj) != nil || obj == nil || json.Compact(&body, req.Body) != nil {
		writeError(w, http.StatusBadRequest, errors.New(`the request's "body" must be a JSON object`))
		return
	}
	if err := p.Fault(context.WithoutCancel(r.Context()), req.ID, req.Kind, body.String()); err != nil {
		code := http.StatusConflict
		switch {
		case errors.Is(err, errNotFound):
			code = http.StatusNotFound
		case errors.As(err, new(errBackend)):
			code = http.StatusBadGateway
		}
		writeError(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// newMux wires the HTTP API; split from main so tests can drive it with httptest. Every request passes one check
// before its route, so no route can miss it. Its Host header must be localhost or a loopback IP, with or without a
// port, or the answer is 403. A request with a body must send Content-Type application/json, a parameter such as
// charset allowed, or the answer is 415. Each refusal is one line, {"error": "..."}. The API has no auth, and a page
// open in a browser on this host can send it requests. Such a page may POST text/plain, or a body with no
// Content-Type, without asking first; to send application/json it must ask first, with an OPTIONS, a CORS preflight
// (the Fetch standard's CORS-safelisted request-header; no browser run). The mux answers that OPTIONS 405. A
// page whose DNS name rebinds to 127.0.0.1 can read the answers it gets, but its requests carry its own name as the
// Host (the Fetch standard's forbidden request-header names, Host among them, so a page cannot set it; no
// browser run).
func newMux(p *Pool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /lease", p.handleLease)
	mux.HandleFunc("POST /release", p.handleRelease)
	mux.HandleFunc("POST /reset", p.handleReset)
	mux.HandleFunc("POST /vmfault", p.handleVMFault)
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, p.Status()) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackName(r.Host) {
			writeError(w, http.StatusForbidden,
				fmt.Errorf("Host %q is not localhost or a loopback IP: the API has no auth", r.Host))
			return
		}
		if r.ContentLength != 0 { // -1 is a body of unknown length, a chunked one
			ct := r.Header.Get("Content-Type")
			if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType,
					fmt.Errorf("Content-Type %q is not application/json: a body must be JSON", ct))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// loopbackName is whether host, a request's Host header, is localhost or a loopback IP literal (net.IP's IsLoopback,
// the rule -listen and internal/poolapi keep), with or without a port: 127.0.0.1:7700, localhost, [::1]:7700 and ::1
// are; 0.0.0.0 and a cell's 10.254.0.2 are not.
func loopbackName(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Handler is the HTTP API of a new pool of b's cells under cfg, as Serve answers it, for a client's tests under
// httptest (internal/poolapi, cmd/laug). It runs no reaper, so no lease expires and no stuck cell's destroy is tried
// again, and no first replenish: a lease starts one. logger takes the pool's log lines; nil is the standard logger.
func Handler(b Backend, cfg Config, logger *log.Logger) http.Handler {
	p := newPool(b, cfg)
	if logger != nil {
		p.log = logger
	}
	return newMux(p)
}

// DefaultListen is where a pool listens unless told otherwise. The API has no auth: whoever reaches it can lease,
// release and reset cells, and throttle a leased microVM cell's drive, so it stays on loopback.
const DefaultListen = "127.0.0.1:7700"

// StopGrace bounds how long Serve waits, once its context is done, for the requests in flight before it closes their
// connections, so that a stop takes under 2 s.
const StopGrace = 1500 * time.Millisecond

// Serve runs a pool of b's cells under cfg on ln until ctx is done: the reaper, a first replenish, and the HTTP API.
// logger takes the pool's log lines; nil is the standard logger. Once ctx is done the API stops taking requests, those
// in flight get StopGrace to finish, and then their connections close. A create, reset or destroy under way, a TTL
// reap's or a stuck cell's retry among them, is neither waited for nor cut short, and no cell is destroyed: Serve
// returns how many cells the table held, and nil. It returns the error of a listener that fails before then.
func Serve(ctx context.Context, ln net.Listener, b Backend, cfg Config, logger *log.Logger) (int, error) {
	p := newPool(b, cfg)
	if logger != nil {
		p.log = logger
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p.reaper(rctx)
	go p.replenish()

	srv := &http.Server{Handler: newMux(p), ErrorLog: p.log}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return len(p.Status().Cells), err
	case <-ctx.Done():
	}
	sctx, scancel := context.WithTimeout(context.Background(), StopGrace)
	defer scancel()
	if srv.Shutdown(sctx) != nil {
		_ = srv.Close()
	}
	return len(p.Status().Cells), nil
}
