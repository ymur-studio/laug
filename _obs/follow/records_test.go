package follow

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// planLog writes item.log lines as faults/lib.sh's log() writes them, with Omes's and the raun's other lines between.
func planLog(runID string, lines ...string) string {
	var b strings.Builder
	b.WriteString("go: downloading go.temporal.io/sdk v1.38.0\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "!") {
			b.WriteString(l[1:] + "\n")
			continue
		}
		b.WriteString("[" + runID + "] " + l + "\n")
		b.WriteString(`{"level":"info","ts":"2026-10-17T02:08:00.000Z","msg":"Iteration completed"}` + "\n")
	}
	return b.String()
}

// TestItemLogMarks: before the raun JSON, item.log's four line forms give the marks at +280.2, +290.2, +401.2 and
// +431.2 s from t0, read as the log grows and only from whole lines; a line of another run id and the raun's other
// lines give none; then the raun JSON's stamps replace them. A mark after 00:00 UTC lands on the next day.
func TestItemLogMarks(t *testing.T) {
	dir := gateCopy(t)
	raun := read(t, filepath.Join(dir, "pl", gateID+"-pl.json"))
	os.Remove(filepath.Join(dir, "pl", gateID+"-pl.json"))
	tl := read(t, filepath.Join(dir, "timeline.jsonl"))
	write(t, filepath.Join(dir, "timeline.jsonl"), tl[:strings.Index(tl, `{"step":"ended pl"`)])
	write(t, filepath.Join(dir, "laugd.log"), "")
	root := fakeRoot(t)
	addLaugd(t, root, 7001, "127.0.0.1:7700", filepath.Join(dir, "laugd.log"))
	now := time.Unix(1792203100, 0)
	f := testFollower(t, obsDir(t), root, "127.0.0.1:7700", nil, &now)
	run := gateID + "-pl"
	log := filepath.Join(dir, "pl", "item.log")
	part1 := planLog(run, `02:07:28Z before: {"workflow_task_schedule_to_start_p99_ms":17.35}`, "02:08:58Z injected",
		"!["+gateID+"-steady] 02:09:00Z injected", "02:09:08Z landed after 10 s: signal 9812.406217944144")
	part2 := planLog(run, `02:10:59Z removed 1 s after the hold ended; during: {"persistence_latency_p99_ms":9910.14}`,
		"02:11:29Z recovered 30 s after removal; settling 30s, then 90s for the after window")
	write(t, log, part1+part2[:40])
	marks := func() map[string]float64 {
		out := map[string]float64{}
		for k, v := range parseBody(t, body(f)) {
			if m := regexp.MustCompile(`^laug_fault_mark_seconds\{gate_id="` + gateID + `",item="pl",mark="([a-z]+)"\}$`).FindStringSubmatch(k); m != nil {
				out[m[1]] = v
			}
		}
		return out
	}
	rel := func(m map[string]float64) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[k] = strings.TrimRight(strings.TrimRight(fmtValue(float64(int64((v-1792202657.782)*10+0.5))/10), "0"), ".")
		}
		return out
	}
	f.poll()
	if got := rel(marks()); len(got) != 2 || got["inject"] != "280.2" || got["landed"] != "290.2" {
		t.Errorf("after the first part and a half line: %v, want inject +280.2 and landed +290.2", got)
	}
	write(t, log, part1+part2)
	f.poll()
	if got := rel(marks()); len(got) != 4 || got["inject"] != "280.2" || got["landed"] != "290.2" || got["remove"] != "401.2" || got["recovered"] != "431.2" {
		t.Errorf("the four marks: %v, want +280.2, +290.2, +401.2 and +431.2", got)
	}
	write(t, filepath.Join(dir, "pl", gateID+"-pl.json"), strings.Replace(raun, `"inject_at": "1792202938"`, `"inject_at": "1792202939"`, 1))
	f.poll()
	if got := marks(); got["inject"] != 1792202939 || got["recovered"] != 1792203089 || len(got) != 4 {
		t.Errorf("with the raun JSON: %v, want its stamps", got)
	}

	// Across midnight: t0 at 23:59:30 UTC on 2026-10-16; 23:59:50Z is that day, 00:00:05Z the next.
	t0 := 1792195170.0
	if markTime(t0, 23, 59, 50) != 1792195190 || markTime(t0, 0, 0, 5) != 1792195205 || markTime(1792202657.782, 2, 4, 17) != 1792202657 {
		t.Errorf("markTime: %v %v %v", markTime(t0, 23, 59, 50), markTime(t0, 0, 0, 5), markTime(1792202657.782, 2, 4, 17))
	}
	dir2 := gateCopy(t)
	os.Remove(filepath.Join(dir2, "pl", gateID+"-pl.json"))
	write(t, filepath.Join(dir2, "clock.json"), `{"t0": {"wall": 1792195170.000, "mono": 1.00}}`+"\n")
	write(t, filepath.Join(dir2, "pl", "item.log"), planLog(run, "23:59:50Z injected", "00:00:05Z landed after 15 s: signal 30"))
	g := newGateDir(gateID, dir2)
	g.refresh()
	if m := g.items["pl"].logMarks; m["inject"] != 1792195190 || m["landed"] != 1792195205 {
		t.Errorf("across 00:00 UTC: %v, want 1792195190 and 1792195205", m)
	}
}

// TestItemLogLineForms pins the line form the marks are read from: faults/lib.sh's log() and faults/run.sh's four
// messages, and that each, filled in as run.sh fills it, matches the reader's pattern for its mark.
func TestItemLogLineForms(t *testing.T) {
	lib := strings.Split(read(t, "../../faults/lib.sh"), "\n")
	form := `log() { echo "[$RUN_ID] $(date -u +%H:%M:%SZ) $*" >&2; }`
	at := 0
	for i, l := range lib {
		if strings.HasPrefix(l, "log() ") {
			if l != form {
				t.Errorf("faults/lib.sh:%d is %q, not the form the marks are read in, %q", i+1, l, form)
			}
			at = i + 1
		}
	}
	if at == 0 {
		t.Fatalf("faults/lib.sh defines no log()")
	} else {
		t.Logf("faults/lib.sh:%d holds log()", at)
	}
	runSh := read(t, "../../faults/run.sh")
	for _, c := range []struct{ mark, source, filled string }{
		{"inject", `else log "injected"; fi`, "injected"},
		{"landed", `log "landed after $(( landed_at - inject_at )) s: signal $s"`, "landed after 10 s: signal 9812.4"},
		{"remove", `else log "removed ${remove_wait_s} s after the hold ended; during: `, "removed 1 s after the hold ended; during: {}"},
		{"remove", `else log "removed: $(cat_get remove)"; fi`, "removed: toxic remove latency"},
		{"recovered", `log "recovered $(( recovered_at - remove_at )) s after removal; settling ${SETTLE_S}s, then ${AFTER_S}s for the after window"`,
			"recovered 30 s after removal; settling 30s, then 90s for the after window"},
	} {
		if !strings.Contains(runSh, c.source) {
			t.Errorf("faults/run.sh no longer writes %s", c.source)
		}
		line := "[" + gateID + "-pl] 02:08:58Z " + c.filled
		m := logLine.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%q does not read as a log line", line)
			continue
		}
		hits := 0
		for _, lm := range logMarks {
			if lm.re.MatchString(m[5]) {
				hits++
				if lm.mark != c.mark {
					t.Errorf("%q reads as %s, want %s", c.filled, lm.mark, c.mark)
				}
			}
		}
		if hits != 1 {
			t.Errorf("%q matches %d marks, want 1", c.filled, hits)
		}
	}
	for _, other := range []string{"remove failed: toxic remove exited 1", "inject failed: x", "landed", "recovered in 30s", "the EXIT trap tries the remove again"} {
		for _, lm := range logMarks {
			if lm.re.MatchString(other) {
				t.Errorf("%q reads as %s", other, lm.mark)
			}
		}
	}
}

// TestHalfLineAndHalfFile: a timeline and a poll.jsonl that end in half a line, and a verdict.json cut in half, are
// read up to their last whole line and not at all, the half file counted as skipped once however many polls find it,
// with no error; once whole they are read. The command, run on such a directory, serves and exits 0, and -openmetrics
// on it prints the rest and exits 0.
func TestHalfLineAndHalfFile(t *testing.T) {
	dir := gateCopy(t)
	tl := read(t, filepath.Join(dir, "timeline.jsonl"))
	cut := tl[:strings.Index(tl, `{"step":"combine"`)]
	half := `{"step":"combine","wall":17922032`
	write(t, filepath.Join(dir, "timeline.jsonl"), cut+half)
	pl := read(t, filepath.Join(dir, "poll.jsonl"))
	write(t, filepath.Join(dir, "poll.jsonl"), pl+`{"wall":1792203299.1,"busy":0,"proj`)
	v := read(t, filepath.Join(dir, "verdict.json"))
	write(t, filepath.Join(dir, "verdict.json"), v[:len(v)/2])
	write(t, filepath.Join(dir, "laugd.log"), "")
	root := fakeRoot(t)
	addLaugd(t, root, 8001, "127.0.0.1:7700", filepath.Join(dir, "laugd.log"))
	now := time.Unix(1792203211, 0)
	obs := obsDir(t)
	f := testFollower(t, obs, root, "127.0.0.1:7700", nil, &now)
	f.poll()
	got := parseBody(t, body(f))
	g := `gate_id="` + gateID + `"`
	if _, ok := got[`laug_gate_step_seconds{`+g+`,step="combine"}`]; ok {
		t.Errorf("the half combine line was read")
	}
	if _, ok := got[`laug_gate_step_seconds{`+g+`,step="wave end"}`]; !ok {
		t.Errorf("the whole line before it was not read")
	}
	for k := range got {
		if strings.HasPrefix(k, "laug_gate_verdict") || strings.HasPrefix(k, "laug_gate_rule") {
			t.Errorf("half a verdict.json gave %s", k)
		}
	}
	if got["laug_follow_skipped_files_total"] != 1 {
		t.Errorf("laug_follow_skipped_files_total %v, want 1: the half file, and no line still being written", got["laug_follow_skipped_files_total"])
	}
	f.poll()
	f.poll()
	if v := parseBody(t, body(f))["laug_follow_skipped_files_total"]; v != 1 {
		t.Errorf("2 polls later laug_follow_skipped_files_total is %v, want 1: the same half file is counted once", v)
	}
	var stderr bytes.Buffer
	var out bytes.Buffer
	if code := runIn(t, []string{"-openmetrics", dir, "-dir", obs}, &out, &stderr); code != 0 || !strings.HasSuffix(out.String(), "# EOF\n") ||
		!strings.Contains(stderr.String(), "1 files or lines skipped") {
		t.Errorf("-openmetrics over the half file: exit %d, stderr %q", code, stderr.String())
	}
	// The command over the same directory: it serves, and exits 0 on TERM.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	addr := make(chan net.Addr, 1)
	onListen = func(a net.Addr) { addr <- a }
	defer func() { onListen = func(net.Addr) {} }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-listen", "127.0.0.1:0", "-dir", t.TempDir(), "-root", root, "-every", "20ms"}, io.Discard, io.Discard)
	}()
	var a net.Addr
	select {
	case a = <-addr:
	case code := <-done:
		cancel()
		t.Fatalf("follow exited %d before it listened", code)
	}
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get("http://" + a.String() + "/metrics")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("GET /metrics: %v %v", resp, err)
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Contains(b, []byte(`laug_gate_step_seconds{`+g+`,step="wave end"}`)) {
			t.Errorf("the command's body lacks the gate's lines")
		}
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("follow over half files: exit %d, want 0", code)
	}

	write(t, filepath.Join(dir, "timeline.jsonl"), cut+`{"step":"combine","wall":1792203210.162,"mono":20993.88}`+"\n")
	write(t, filepath.Join(dir, "verdict.json"), v)
	f.poll()
	got = parseBody(t, body(f))
	if got[`laug_gate_step_seconds{`+g+`,step="combine"}`] != 1792203210.162 || got[`laug_gate_rule{`+g+`,rule="R6",name="identity"}`] != 1 {
		t.Errorf("once whole, the combine line and verdict.json were not read")
	}
}

// runIn is run with the test's own context.
func runIn(t *testing.T, args []string, stdout, stderr io.Writer) int {
	t.Helper()
	return run(context.Background(), args, stdout, stderr)
}
