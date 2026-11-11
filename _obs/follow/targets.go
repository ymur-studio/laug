package follow

// Where follow's targets come from, and how they are written (docs/DESIGN.md section 9). A gate is found by its laugd
// (internal/gateopen's Scanner): the process that runs laugd on -pool has its stdout on <gate dir>/laugd.log
// (gate/run.sh and gate/step.sh start it so), so a readlink of its fd 1 names the gate's directory wherever the
// checkout is. While that laugd is a gate's, follow asks it nothing: targets come from the gate's own files.
// Outside a gate it reads laugd's /status. Each target group is one file in OBS_DIR/targets, written to a dot name
// and renamed, which Prometheus reads by file_sd; .expires.json beside them holds each file's lease expiry, the
// stale-target guard obs-status reads.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// statusCell is the part of laugd's /status a hand target takes (internal/pool/pool.go, entry). requester is not
// read at all: outside a gate it is $(USER), and no user name goes on a series.
type statusCell struct {
	ID        string    `json:"id"`
	Prom      string    `json:"prometheus"`
	ServerTag string    `json:"server_tag"`
	Block     int       `json:"block"`
	State     string    `json:"state"`
	LeasedAt  time.Time `json:"leased_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// readStatus asks laugd for its cells. An answer that is not a 200 with JSON is an error.
func readStatus(c *http.Client, pool string) ([]statusCell, error) {
	resp, err := c.Get("http://" + pool + "/status")
	if err != nil {
		return nil, errors.New("status: no answer")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: HTTP %d", resp.StatusCode)
	}
	var v struct {
		Cells []statusCell `json:"cells"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&v); err != nil {
		return nil, errors.New("status: not JSON")
	}
	return v.Cells, nil
}

// promTarget turns a cell's Prometheus URL into a scrape target and its backend: 10.254.<b>.2 is a microVM's
// host-only address (SECURITY.md), anything else on 127.0.0.1 a compose cell. Other URLs give none.
func promTarget(raw string) (addr, backend string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Port() == "" || (u.Path != "" && u.Path != "/") {
		return "", "", false
	}
	h := u.Hostname()
	switch {
	case h == "127.0.0.1":
		return u.Host, "compose", true
	case microVM.MatchString(h):
		return u.Host, "firecracker", true
	}
	return "", "", false
}

var (
	microVM = regexp.MustCompile(`^10\.254\.[0-9]{1,3}\.2$`)
	cellID  = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

// group is one target group of a file_sd file; lines are its labels, a line of the file each.
type group struct {
	addr  string
	lines [][][2]string
}

// target is one file of OBS_DIR/targets.
type target struct {
	file    string
	groups  []group
	expires int64       // the lease's expires_at, Unix seconds
	lease   [][2]string // a cell target's labels, for laug_lease_info
}

// cellTarget is the federation target of one leased cell, 5.1's labels: cell, life (leased_at in Unix seconds),
// block, backend and server_tag, and inside a gate gate_id, item and arm.
func cellTarget(id, prom string, block int, tag string, leased, expires time.Time, gateID, item, arm string) (target, bool) {
	addr, backend, ok := promTarget(prom)
	if !ok || !cellID.MatchString(id) {
		return target{}, false
	}
	first := [][2]string{{"cell", id}, {"life", strconv.FormatInt(leased.Unix(), 10)}, {"block", strconv.Itoa(block)}, {"backend", backend}}
	second := nonEmpty([][2]string{{"server_tag", tag}, {"gate_id", gateID}, {"item", item}, {"arm", arm}})
	lines := [][][2]string{first}
	if len(second) > 0 {
		lines = append(lines, second)
	}
	return target{file: "cell-" + id + ".json", groups: []group{{addr, lines}}, expires: expires.Unix(),
		lease: append(append([][2]string{}, first...), nonEmpty([][2]string{{"server_tag", tag}, {"gate_id", gateID}, {"item", item}})...)}, true
}

func nonEmpty(kv [][2]string) [][2]string {
	var out [][2]string
	for _, p := range kv {
		if p[1] != "" {
			out = append(out, p)
		}
	}
	return out
}

// fileBytes is a target file in the shape follow writes: each group's targets on its first
// line, its labels after, a line of the file per line of labels.
func (t target) fileBytes() []byte {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var b bytes.Buffer
	b.WriteString("[")
	for i, g := range t.groups {
		if i > 0 {
			b.WriteString(",\n ")
		}
		b.WriteString(`{"targets": [` + q(g.addr) + "],\n  \"labels\": {")
		for j, line := range g.lines {
			if j > 0 {
				b.WriteString(",\n             ")
			}
			for k, kv := range line {
				if k > 0 {
					b.WriteString(", ")
				}
				b.WriteString(q(kv[0]) + ": " + q(kv[1]))
			}
		}
		b.WriteString("}}")
	}
	b.WriteString("]\n")
	return b.Bytes()
}

var targetFile = regexp.MustCompile(`^(cell|omes)-[A-Za-z0-9_.-]+\.json$`)

// writeTargets makes OBS_DIR/targets hold exactly ts: a file whose bytes changed is written to a dot name and renamed;
// a target file not in ts is removed; .expires.json, each file's lease expiry, goes last. It returns the files past
// their expiry, which is 0 unless a lease ran out between follow's read and now.
func writeTargets(dir string, ts []target, now time.Time) (stale int, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	want := map[string]bool{}
	exp := map[string]int64{}
	for _, t := range ts {
		want[t.file] = true
		exp[t.file] = t.expires
		if t.expires < now.Unix() {
			stale++
		}
		if err := writeIfChanged(filepath.Join(dir, t.file), t.fileBytes()); err != nil {
			return stale, err
		}
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if targetFile.MatchString(e.Name()) && !want[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return stale, err
			}
		}
	}
	b, _ := json.Marshal(exp)
	return stale, writeIfChanged(filepath.Join(dir, ".expires.json"), append(b, '\n'))
}

// writeIfChanged writes b to path by a rename from a dot name, unless path holds b already.
func writeIfChanged(path string, b []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sortTargets orders targets by file name, so a pass writes them and renders laug_lease_info in one order.
func sortTargets(ts []target) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].file < ts[j].file })
}
