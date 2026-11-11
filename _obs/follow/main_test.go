package follow

// The tests of follow (docs/DESIGN.md section 9), on testdata: a copy of
// gate/results/g20261017-020417 byte for byte, the target files it gives, and two laugd /status answers. A test
// that needs a process (a gate's laugd, a lock holder) has it in a fake proc tree in a temp dir, or, for the locks,
// takes a real flock on a file in a temp dir: no test touches a host lock path, laugd or Docker.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const gateID = "g20261017-020417"

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// copyTree copies src into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// gateCopy copies testdata's gate into <tmp>/results/<gate id> and returns that directory.
func gateCopy(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "results", gateID)
	copyTree(t, filepath.Join("testdata", gateID), dir)
	return dir
}

// obsDir is a temp OBS_DIR whose conf holds the repository's A/A lists and the catalog's budget for the faults the
// gate ran, as bin/obs up copies them (catalog.json from faults/catalog.yaml: budget_s 60 for persistence-latency).
func obsDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	lists, _ := filepath.Glob("../aa/*.txt")
	if len(lists) == 0 {
		t.Fatal("no _obs/aa/*.txt in the checkout")
	}
	for _, l := range lists {
		write(t, filepath.Join(d, "conf", "aa", filepath.Base(l)), read(t, l))
	}
	write(t, filepath.Join(d, "conf", "catalog.json"), `{"faults":{"persistence-latency":{"signal":"persistence_latency_p99_ms","budget_s":60}}}`+"\n")
	return d
}

// fakeRoot is a proc tree with an empty locks file and no process.
func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "proc", "locks"), "")
	return root
}

// addLaugd puts a process in the fake tree that runs laugd on addr with its stdout on out, as gate/run.sh starts it.
func addLaugd(t *testing.T, root string, pid int, addr, out string) {
	t.Helper()
	d := filepath.Join(root, "proc", strconv.Itoa(pid))
	write(t, filepath.Join(d, "cmdline"), strings.Join([]string{"bin/laug", "pool", "serve", "-pid-file", "none", "-listen", addr,
		"-min", "0", "-max", "2", "-quota", "2", "-max-lease", "20m0s", "-server-tag", "1.31.0", "-backend", "compose"},
		"\x00")+"\x00")
	if err := os.MkdirAll(filepath.Join(d, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(d, "fd", "1")); err != nil {
		t.Fatal(err)
	}
}

// countingLaugd is a laugd stand-in that counts every request and answers /status with the file *status names.
func countingLaugd(t *testing.T) (addr string, requests *atomic.Int64, status *atomic.Value) {
	t.Helper()
	requests, status = &atomic.Int64{}, &atomic.Value{}
	status.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		name := status.Load().(string)
		if r.URL.Path != "/status" || name == "" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(read(t, name)))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), requests, status
}

// follower for a test: OBS_DIR obs, proc tree root, laugd on pool, lock files locks, the clock at *now.
func testFollower(t *testing.T, obs, root, pool string, locks []string, now *time.Time) *follower {
	t.Helper()
	f, err := newFollower(obs, root, pool, locks, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	f.now = func() time.Time { return *now }
	return f
}

// parseBody reads a text-format body into series → value (a trailing timestamp dropped), and fails on a series twice,
// a label name twice in one series, or a sample with no TYPE.
func parseBody(t *testing.T, body string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	typed := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if f, ok := strings.CutPrefix(line, "# TYPE "); ok {
			typed[strings.Fields(f)[0]] = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		end := strings.LastIndexByte(line, '}')
		if end < 0 {
			end = strings.IndexByte(line, ' ') - 1
		}
		series, rest := line[:end+1], strings.Fields(line[end+1:])
		name := series
		if i := strings.IndexByte(series, '{'); i >= 0 {
			name = series[:i]
		}
		if !typed[name] {
			t.Errorf("%s has no TYPE", name)
		}
		if n := repeatedLabel(series[len(name):]); n != "" {
			t.Errorf("%s has the label %s twice", series, n)
		}
		v, err := strconv.ParseFloat(rest[0], 64)
		if err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if _, dup := out[series]; dup {
			t.Errorf("%s twice", series)
		}
		out[series] = v
	}
	return out
}

// repeatedLabel gives the first label name a {name="value",...} set holds twice, or "": a value is read to its closing
// quote, a backslash escaping the next byte.
func repeatedLabel(set string) string {
	if set == "" {
		return ""
	}
	seen := map[string]bool{}
	s := strings.TrimSuffix(strings.TrimPrefix(set, "{"), "}")
	for s != "" {
		eq := strings.IndexByte(s, '=')
		if eq < 0 || eq+1 >= len(s) || s[eq+1] != '"' {
			return "(a set that does not parse: " + set + ")"
		}
		name := s[:eq]
		if seen[name] {
			return name
		}
		seen[name] = true
		i := eq + 2
		for i < len(s) && s[i] != '"' {
			if s[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(s) {
			return "(a set that does not parse: " + set + ")"
		}
		s = strings.TrimPrefix(s[i+1:], ",")
	}
	return ""
}

func body(f *follower) string { return string(*f.body.Load()) }

// listDir is a directory's files, dot files included, each with its bytes.
func listDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		out[e.Name()] = read(t, filepath.Join(dir, e.Name()))
	}
	return out
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// replay plays the copied gate back into an empty directory as the gate wrote it: each timeline line is appended
// after the files written before it (lease.json at its leased line, harness.json and the first poll at the wave
// line, an item's exit, run and verdict JSON at its ended line, verdict.json and the full clock.json at combine,
// cleanup.json at laugd stopped), the polls as their walls pass. The gate's laugd is in the fake tree from its laugd
// line to its laugd stopped line. at is called after each line, with the clock half a second past it.
func replay(t *testing.T, dir, root, pool string, at func(s step, now time.Time)) {
	t.Helper()
	src := filepath.Join("testdata", gateID)
	for _, rel := range []string{"timeline.jsonl", "poll.jsonl", "harness.json", "verdict.json", "verdict.md", "cleanup.json",
		"steady", "pl"} {
		if err := os.RemoveAll(filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	var clock map[string]any
	json.Unmarshal([]byte(read(t, filepath.Join(src, "clock.json"))), &clock)
	write(t, filepath.Join(dir, "clock.json"), `{"t0": {"wall": 1792202657.782, "mono": 20441.50}}`+"\n")
	tl := wholeLines([]byte(read(t, filepath.Join(src, "timeline.jsonl"))))
	polls := wholeLines([]byte(read(t, filepath.Join(src, "poll.jsonl"))))
	copyItem := func(key string, names ...string) {
		for _, n := range names {
			write(t, filepath.Join(dir, key, n), read(t, filepath.Join(src, key, n)))
		}
	}
	np := 0
	var tline, pline bytes.Buffer
	for _, raw := range tl {
		var l struct {
			Step string  `json:"step"`
			Wall float64 `json:"wall"`
		}
		json.Unmarshal(raw, &l)
		for np < len(polls) {
			var p struct{ Wall float64 }
			json.Unmarshal(polls[np], &p)
			if p.Wall > l.Wall {
				break
			}
			pline.Write(append(polls[np], '\n'))
			write(t, filepath.Join(dir, "poll.jsonl"), pline.String())
			np++
		}
		switch {
		case l.Step == "laugd":
			write(t, filepath.Join(dir, "laugd.log"), "")
			addLaugd(t, root, 4321, pool, filepath.Join(dir, "laugd.log"))
		case l.Step == "laugd stopped":
			os.RemoveAll(filepath.Join(root, "proc", "4321"))
			write(t, filepath.Join(dir, "cleanup.json"), read(t, filepath.Join(src, "cleanup.json")))
		case strings.HasPrefix(l.Step, "leased "):
			copyItem(strings.TrimPrefix(l.Step, "leased "), "lease.json")
		case l.Step == "wave":
			write(t, filepath.Join(dir, "harness.json"), read(t, filepath.Join(src, "harness.json")))
		case l.Step == "ended steady":
			copyItem("steady", "exit.json", "load.exit", gateID+"-steady.json", gateID+"-steady.verdict.json")
		case l.Step == "ended pl":
			copyItem("pl", "exit.json", gateID+"-pl.json")
		case l.Step == "combine":
			write(t, filepath.Join(dir, "clock.json"), read(t, filepath.Join(src, "clock.json")))
			write(t, filepath.Join(dir, "verdict.json"), read(t, filepath.Join(src, "verdict.json")))
			write(t, filepath.Join(dir, "verdict.md"), read(t, filepath.Join(src, "verdict.md")))
		}
		tline.Write(append(raw, '\n'))
		write(t, filepath.Join(dir, "timeline.jsonl"), tline.String())
		s := step{name: l.Step, wall: l.Wall}
		if m := itemStep.FindStringSubmatch(l.Step); m != nil {
			s.name, s.item = m[1], m[2]
		}
		at(s, time.Unix(0, int64((l.Wall+0.5)*1e9)))
	}
	for name, n := range map[string]int{"poll.jsonl": len(polls), "timeline.jsonl": len(tl)} {
		if got := len(wholeLines([]byte(read(t, filepath.Join(dir, name))))); got != n {
			t.Fatalf("replay wrote %d lines of %s, the gate %d", got, name, n)
		}
	}
	for _, rel := range []string{"gate.json", "plan.json", "thresholds.json", "clock.json", "harness.json", "poll.jsonl",
		"timeline.jsonl", "verdict.json", "verdict.md", "cleanup.json", "steady/lease.json", "steady/exit.json", "steady/load.exit",
		"steady/" + gateID + "-steady.json", "steady/" + gateID + "-steady.verdict.json", "pl/lease.json", "pl/exit.json", "pl/" + gateID + "-pl.json"} {
		if read(t, filepath.Join(dir, rel)) != read(t, filepath.Join(src, rel)) {
			t.Errorf("after the replay %s is not the copy's", rel)
		}
	}
}

// TestReplayGivesTargetsAndRecords: the copy of g20261017-020417, played back as the gate wrote it, gives the target
// files of testdata/targets byte for byte from each leased line to the release line, Omes targets from the wave line
// to each ended line, a .expires.json of each lease's expires_at, and, 1 minute after done, records equal to the
// gate's JSON: the six steady readings to the float, pl's four windows, the four fault marks, the six rules and the
// 21 timeline lines. 601 s after done the gate is dropped. The gate's laugd, a stand-in that counts requests, is asked
// nothing.
func TestReplayGivesTargetsAndRecords(t *testing.T) {
	dir := gateCopy(t)
	root := fakeRoot(t)
	obs := obsDir(t)
	pool, requests, _ := countingLaugd(t)
	now := time.Unix(1792202600, 0)
	f := testFollower(t, obs, root, pool, nil, &now)
	targets := filepath.Join(obs, "targets")
	cellS, cellP := read(t, "testdata/targets/cell-13880449.json"), read(t, "testdata/targets/cell-7ee6b0fa.json")
	omes := func(key, w, c, cell string) string {
		return `[{"targets": ["127.0.0.1:` + w + `"],` + "\n" + `  "labels": {"gate_id": "` + gateID + `", "item": "` + key + `", "role": "worker", "cell": "` + cell + `"}},` + "\n" +
			` {"targets": ["127.0.0.1:` + c + `"],` + "\n" + `  "labels": {"gate_id": "` + gateID + `", "item": "` + key + `", "role": "client", "cell": "` + cell + `"}}]` + "\n"
	}
	omesS, omesP := omes("steady", "19100", "19101", "13880449"), omes("pl", "19102", "19103", "7ee6b0fa")
	expS, expP := `"cell-13880449.json":1792203876`, `"cell-7ee6b0fa.json":1792203892`
	oS, oP := `"omes-`+gateID+`-steady.json":1792203876`, `"omes-`+gateID+`-pl.json":1792203892`
	steps := 0
	replay(t, dir, root, pool, func(s step, at time.Time) {
		now = at
		f.poll()
		steps++
		got := listDir(t, targets)
		leasedS := s.wall >= 1792202676.197 && s.wall < 1792203210.474
		leasedP := s.wall >= 1792202692.768 && s.wall < 1792203210.474
		waveS := s.wall >= 1792202848.301 && s.wall < 1792203150.599
		waveP := s.wall >= 1792202848.301 && s.wall < 1792203210.028
		w := map[string]string{}
		var exp []string
		if leasedS {
			w["cell-13880449.json"] = cellS
			exp = append(exp, expS)
		}
		if leasedP {
			w["cell-7ee6b0fa.json"] = cellP
			exp = append(exp, expP)
		}
		if waveP {
			w["omes-"+gateID+"-pl.json"] = omesP
			exp = append(exp, oP)
		}
		if waveS {
			w["omes-"+gateID+"-steady.json"] = omesS
			exp = append(exp, oS)
		}
		sort.Strings(exp)
		w[".expires.json"] = "{" + strings.Join(exp, ",") + "}\n"
		for name, b := range w {
			if got[name] != b {
				t.Errorf("after %s %s: %s is\n%s\nwant\n%s", s.name, s.item, name, got[name], b)
			}
		}
		for name := range got {
			if _, ok := w[name]; !ok {
				t.Errorf("after %s %s: %s, which should not be there", s.name, s.item, name)
			}
		}
	})
	if steps != 21 {
		t.Fatalf("the replay gave %d lines, want 21", steps)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("the gate's laugd got %d requests from follow, want 0", n)
	}

	now = time.Unix(1792203254+60, 0)
	f.poll()
	got := parseBody(t, body(f))
	var verdict, raun map[string]any
	json.Unmarshal([]byte(read(t, filepath.Join(dir, "steady", gateID+"-steady.verdict.json"))), &verdict)
	json.Unmarshal([]byte(read(t, filepath.Join(dir, "pl", gateID+"-pl.json"))), &raun)
	g := `gate_id="` + gateID + `"`
	for _, r := range []string{"workflow_task_schedule_to_start_p99_ms", "activity_schedule_to_start_p99_ms", "workflow_end_to_end_p95_ms",
		"persistence_latency_p99_ms", "service_error_rate", "frontend_request_p99_ms"} {
		k := `laug_item_reading{` + g + `,item="steady",window="whole",reading="` + r + `"}`
		if v, ok := got[k]; !ok || v != verdict["readings"].(map[string]any)[r].(float64) {
			t.Errorf("%s = %v (%v), want %v", k, v, ok, verdict["readings"].(map[string]any)[r])
		}
	}
	nWin := 0
	for _, w := range []string{"before", "during", "drain", "after"} {
		for r, v := range raun[w].(map[string]any) {
			k := `laug_item_reading{` + g + `,item="pl",window="` + w + `",reading="` + r + `"}`
			got, ok := got[k]
			switch {
			case v == nil && ok:
				t.Errorf("%s = %v; the raun JSON holds null, so no sample", k, got)
			case v != nil && (!ok || got != v.(float64)):
				t.Errorf("%s = %v (%v), want %v", k, got, ok, v)
			case v != nil:
				nWin++
			}
		}
	}
	if nWin != 33 {
		t.Errorf("pl's four windows gave %d readings, want 33 (36 less the 3 nulls of during)", nWin)
	}
	for mark, want := range map[string]float64{"inject": 280.2, "landed": 290.2, "remove": 401.2, "recovered": 431.2} {
		k := `laug_fault_mark_seconds{` + g + `,item="pl",mark="` + mark + `"}`
		v, ok := got[k]
		stamp, _ := strconv.ParseFloat(raun[mark+"_at"].(string), 64)
		if !ok || v != stamp || math.Round((v-1792202657.782)*10)/10 != want {
			t.Errorf("%s = %v (%v), want the raun JSON's %v, +%v s from t0", k, v, ok, stamp, want)
		}
	}
	for i, r := range []string{"harness", "coverage", "contract", "outcome", "live control", "identity"} {
		k := `laug_gate_rule{` + g + `,rule="R` + strconv.Itoa(i+1) + `",name="` + r + `"}`
		if v, ok := got[k]; !ok || v != 1 {
			t.Errorf("%s = %v (%v), want 1", k, v, ok)
		}
	}
	lines, durations := 0, 0
	for _, raw := range wholeLines([]byte(read(t, filepath.Join(dir, "timeline.jsonl")))) {
		var l struct {
			Step string  `json:"step"`
			Wall float64 `json:"wall"`
		}
		json.Unmarshal(raw, &l)
		name, item := l.Step, ""
		if m := itemStep.FindStringSubmatch(l.Step); m != nil {
			name, item = m[1], `,item="`+m[2]+`"`
		}
		k := `{` + g + `,step="` + name + `"` + item + `}`
		if v, ok := got["laug_gate_step_seconds"+k]; ok && v == l.Wall {
			lines++
		} else {
			t.Errorf("laug_gate_step_seconds%s = %v (%v), want %v", k, v, ok, l.Wall)
		}
		if v := got["laug_gate_step_active"+k]; (l.Step == "done") != (v == 1) {
			t.Errorf("laug_gate_step_active%s = %v", k, v)
		}
		if _, ok := got["laug_gate_step_duration_seconds"+k]; ok {
			durations++
		}
	}
	if lines != 21 || durations != 20 {
		t.Errorf("%d timeline lines as laug_gate_step_seconds, %d durations; want 21 and 20", lines, durations)
	}
	for k, want := range map[string]float64{
		`laug_gate_verdict{` + g + `,verdict="ADVANCE",rerun="false",host_quiet="true"}`:                    1,
		`laug_aa{` + g + `,list="plan-2item-m8i.8xlarge"}`:                                                  1,
		`laug_fault_signal{` + g + `,item="pl",field="budget"}`:                                             60,
		`laug_fault_signal{` + g + `,item="pl",field="max"}`:                                                23,
		`laug_gate_harness{` + g + `,field="passed"}`:                                                       965,
		`laug_item_window_seconds{` + g + `,item="steady",window="whole",edge="start"}`:                     1792202848,
		`laug_item_window_seconds{` + g + `,item="pl",window="after",edge="end"}`:                           1792203209,
		`laug_item_failed_iterations{` + g + `,item="pl",window="during"}`:                                  542,
		`laug_gate_poll{` + g + `,field="projects_leased"}`:                                                 2,
		`laug_follow_gate_dir_found{` + g + `}`:                                                             1,
		`laug_threshold{` + g + `,reading="persistence_latency_p99_ms",release="1.31",host_type="vcpu-32"}`: 23,
	} {
		if v, ok := got[k]; !ok || v != want {
			t.Errorf("%s = %v (%v), want %v", k, v, ok, want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(obs, "follow", gateID+".json")); err != nil || !bytes.Contains(b, []byte(dir)) {
		t.Errorf("OBS_DIR/follow/%s.json: %q, %v; want the gate dir", gateID, b, err)
	}

	now = time.Unix(1792203254+601, 0)
	f.poll()
	if b := body(f); strings.Contains(b, gateID) {
		t.Errorf("601 s after done the records are still served")
	}
	if _, err := os.Stat(filepath.Join(obs, "follow", gateID+".json")); err == nil {
		t.Errorf("601 s after done OBS_DIR/follow still names the gate")
	}
}

// Run, as bin/obs follow runs it: ready is called once with the bound address, after OBS_DIR/targets and
// OBS_DIR/follow are made and before the first poll, so GET /metrics answers 503 there; a cancel then ends it with 0.
// A refused flag returns 2 and never calls ready.
func TestRunReady(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	obs := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readies, seen := 0, ""
	ready := func(a net.Addr) {
		readies++
		_, terr := os.Stat(filepath.Join(obs, "targets"))
		_, ferr := os.Stat(filepath.Join(obs, "follow"))
		code := 0
		if resp, err := http.Get("http://" + a.String() + "/metrics"); err == nil {
			code = resp.StatusCode
			resp.Body.Close()
		}
		seen = fmt.Sprintf("targets %v, follow %v, GET /metrics %d", terr, ferr, code)
		cancel()
	}
	args := []string{"-listen", "127.0.0.1:0", "-dir", obs, "-root", t.TempDir(), "-every", "1h"}
	if code := Run(ctx, args, io.Discard, io.Discard, ready); code != 0 || readies != 1 {
		t.Fatalf("exit %d, ready %d times; want 0 and once", code, readies)
	}
	if want := "targets <nil>, follow <nil>, GET /metrics 503"; seen != want {
		t.Errorf("at ready: %s; want %s", seen, want)
	}
	readies = 0
	if code := Run(context.Background(), []string{"-listen", "10.0.0.1:7720", "-dir", obs}, io.Discard, io.Discard,
		func(net.Addr) { readies++ }); code != 2 || readies != 0 {
		t.Errorf("-listen off loopback: exit %d, ready %d times; want 2 and never", code, readies)
	}
}
