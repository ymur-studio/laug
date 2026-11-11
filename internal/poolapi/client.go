// Package poolapi is the client side of the pool's HTTP API (internal/pool): lease, release, reset, status and
// vmfault, and the last-lease file, .last-lease.json, that laug lease keeps and the scripts read. It talks to a pool on
// loopback only, since the API has no auth. Standard library only.
package poolapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Lease is a leased cell as the pool answers POST /lease and POST /reset, and as each cell of GET /status reads: the
// cell the backend printed, then the pool's own fields. The JSON names and their order are the pool's (internal/pool,
// Cell and entry); TestShapes holds them equal, byte for byte.
type Lease struct {
	ID         string    `json:"id"`
	Address    string    `json:"address"`
	UI         string    `json:"ui,omitempty"`
	Grafana    string    `json:"grafana,omitempty"`
	Prometheus string    `json:"prometheus,omitempty"`
	Toxiproxy  string    `json:"toxiproxy,omitempty"`
	Project    string    `json:"project,omitempty"`
	DynConfig  string    `json:"dynconfig,omitempty"`
	SSH        string    `json:"ssh,omitempty"`
	ServerTag  string    `json:"server_tag,omitempty"`
	Backend    string    `json:"backend"`
	FCVersion  string    `json:"firecracker_version,omitempty"`
	CacheType  string    `json:"cache_type,omitempty"`
	Block      int       `json:"block"`
	State      string    `json:"state"`
	Requester  string    `json:"requester,omitempty"`
	LeasedAt   time.Time `json:"leased_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Status is the pool's GET /status: its settings and every cell, sorted by id.
type Status struct {
	MinWarm  int     `json:"min_warm"`
	MaxCells int     `json:"max_cells"`
	Quota    int     `json:"quota"`
	MaxLease string  `json:"max_lease"`
	Cells    []Lease `json:"cells"`
}

// LeaseRequest is the body of POST /lease. The pool clamps a TTL over its max lease to it; laug lease sends whole
// seconds. An empty ServerTag is left out, so the pool hands out any warm cell.
type LeaseRequest struct {
	TTLSeconds int    `json:"ttl_seconds"`
	Requester  string `json:"requester"`
	ServerTag  string `json:"server_tag,omitempty"`
}

// TagRE is the server tag a lease may name, a release, x.y.z: the pool's tagRE, which answers 400 to anything else.
var TagRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// ErrNotPool is what a client returns when what answers is not a pool: a 200 whose body is not the document asked for.
var ErrNotPool = errors.New("not a pool")

// Error is a pool's refusal: an HTTP status other than 200, with the pool's {"error": "..."} text when it sent one.
type Error struct {
	Op     string // "POST /lease"
	Code   int    // 409
	Status string // "409 Conflict"
	Text   string // "quota: laug already holds 2 lease(s)"; empty when the body held no {"error"}
}

func (e *Error) Error() string {
	if e.Text == "" {
		return fmt.Sprintf("%s answered %s", e.Op, e.Status)
	}
	return fmt.Sprintf("%s answered %s: %s", e.Op, e.Status, e.Text)
}

// statusTimeout bounds GET /status. Lease, reset and release wait as long as their context: a cold lease or a reset
// takes the pool's -create-timeout at most (5m by default), and a release a destroy.
const statusTimeout = 5 * time.Second

// maxBody is the most of an answer a client reads, 1 MiB. A /status of 6 cells is under 3 KiB.
const maxBody = 1 << 20

// Client talks to one pool.
type Client struct {
	base string
	http *http.Client
}

// New is a client of the pool at addr: host:port, or an http:// or https:// URL with no path. The host must be a
// loopback IP, as the pool's own -listen must: the API has no auth, so a pool listens on loopback only, and a name
// may resolve anywhere.
func New(addr string) (*Client, error) {
	raw := addr
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("not host:port or an http:// URL with no path")
	}
	if err := LoopbackHost(u.Hostname()); err != nil {
		return nil, err
	}
	return &Client{base: u.Scheme + "://" + u.Host, http: &http.Client{}}, nil
}

// LoopbackHost refuses a host that is not a loopback IP literal, 127.0.0.0/8 or ::1.
func LoopbackHost(host string) error {
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("host %q is not a loopback address: the API has no auth, so it listens on loopback only", host)
	}
	return nil
}

// Base is the pool's base URL, http://127.0.0.1:7700 for the default.
func (c *Client) Base() string { return c.base }

// Lease asks the pool for a cell. It returns the lease and the pool's answer as it came, which is what laug lease
// keeps in the last-lease file.
func (c *Client) Lease(ctx context.Context, r LeaseRequest) (Lease, []byte, error) {
	body, err := c.do(ctx, http.MethodPost, "/lease", r)
	if err != nil {
		return Lease{}, nil, err
	}
	l, err := decodeLease(body)
	return l, body, err
}

// Reset asks the pool to destroy the cell behind lease id and build it again, under the same lease, id and port
// block. It returns the lease and the pool's answer as it came.
func (c *Client) Reset(ctx context.Context, id string) (Lease, []byte, error) {
	body, err := c.do(ctx, http.MethodPost, "/reset", idRequest{id})
	if err != nil {
		return Lease{}, nil, err
	}
	l, err := decodeLease(body)
	return l, body, err
}

// Release gives the cell behind lease id back; the pool destroys it. It returns the pool's answer as it came,
// {"released": "<id>"}.
func (c *Client) Release(ctx context.Context, id string) ([]byte, error) {
	body, err := c.do(ctx, http.MethodPost, "/release", idRequest{id})
	if err != nil {
		return nil, err
	}
	var r struct {
		Released string `json:"released"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Released != id {
		return body, fmt.Errorf("%w: its answer to POST /release names no release of %s", ErrNotPool, id)
	}
	return body, nil
}

// VMFault asks the pool to hand a leased microVM cell's VMM a change: kind names what (drive) and body is the JSON
// object the backend sends on. It returns the pool's answer as it came, {"ok":true}. Only the Firecracker backend
// answers it; the compose backend's refusal comes back as *Error, 502.
func (c *Client) VMFault(ctx context.Context, id, kind string, body json.RawMessage) ([]byte, error) {
	out, err := c.do(ctx, http.MethodPost, "/vmfault", vmFaultRequest{ID: id, Kind: kind, Body: body})
	if err != nil {
		return nil, err
	}
	var r struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(out, &r); err != nil || !r.OK {
		return out, fmt.Errorf("%w: its answer to POST /vmfault is not {\"ok\":true}", ErrNotPool)
	}
	return out, nil
}

// Status reads GET /status, 5 s at most. It returns the status and the pool's answer as it came.
func (c *Client) Status(ctx context.Context) (Status, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	body, err := c.do(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		return Status{}, nil, err
	}
	var st Status
	if err := json.Unmarshal(body, &st); err != nil || st.MaxLease == "" {
		return Status{}, body, fmt.Errorf("%w: its /status is no pool status", ErrNotPool)
	}
	return st, body, nil
}

type idRequest struct {
	ID string `json:"id"`
}

// vmFaultRequest is the body of POST /vmfault.
type vmFaultRequest struct {
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body"`
}

// decodeLease reads a lease from the pool's answer and refuses one with no id.
func decodeLease(body []byte) (Lease, error) {
	var l Lease
	if err := json.Unmarshal(body, &l); err != nil || l.ID == "" {
		return Lease{}, fmt.Errorf("%w: its answer holds no lease id", ErrNotPool)
	}
	return l, nil
}

// do sends one request, with in as its JSON body when it is not nil, and returns the answer's body when the status is
// 200. A transport error comes back without net/url's "Post <url>:" wrapping; an answer of another status is *Error.
// The pool answers 403 to a Host header that is not localhost or a loopback IP, and 415 to a body not sent as
// Content-Type application/json (internal/pool's newMux). Go's client sends the URL's host:port as the Host, a
// loopback IP by New's check, and do sets that Content-Type on every body, so all four POSTs pass both; TestRequests
// holds it.
func (c *Client) do(ctx context.Context, method, path string, in any) ([]byte, error) {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		e := &Error{Op: method + " " + path, Code: resp.StatusCode, Status: resp.Status}
		var pe struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &pe) == nil {
			e.Text = pe.Error
		}
		return nil, e
	}
	return body, nil
}
