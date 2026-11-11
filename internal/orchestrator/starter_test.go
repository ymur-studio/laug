package orchestrator

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ymur-studio/laug/internal/script"
)

// The lock's holder decides: its identity must be HEAD's, or HEAD's -dirty form while the tree has changes. A foreign
// identity, a -dirty one on a clean tree and no identity at all are refused, with both sides printed.
func TestCheckHolder(t *testing.T) {
	const clean, dirty = "laug-worker@abc1234", "laug-worker@abc1234-dirty"
	const restart = ": make build, then restart laug orchestrator worker"
	for _, c := range []struct {
		name, id, dirty, err string
	}{
		{"matching", clean, clean, ""},
		{"matching, the tree has changes", clean, dirty, ""},
		{"dirty, the tree has changes", dirty, dirty, ""},
		{"dirty, the tree clean", dirty, clean, "the worker holding the lock is laug-worker@abc1234-dirty, this checkout's HEAD is abc1234" + restart},
		{"foreign", "laug-worker@0000000", clean, "the worker holding the lock is laug-worker@0000000, this checkout's HEAD is abc1234" + restart},
		{"foreign, the tree has changes", "laug-worker@0000000-dirty", dirty,
			"the worker holding the lock is laug-worker@0000000-dirty, this checkout's HEAD is abc1234 with changes" + restart},
		{"unstamped", "laug-worker@unknown", clean, "the worker holding the lock is laug-worker@unknown, this checkout's HEAD is abc1234" + restart},
		{"empty", "", clean, "the worker holding the lock, pid 4242, wrote no identity into orchestrator/run/worker.lock " +
			"yet, or is a build from before the lock held one, this checkout's HEAD is abc1234" + restart},
	} {
		err := checkHolder(Holder{Pid: 4242, Identity: c.id}, clean, c.dirty, "laug")
		if got := fmt.Sprint(err); (c.err == "" && err != nil) || (c.err != "" && got != c.err) {
			t.Errorf("%s: %v\nwant %q", c.name, err, c.err)
		}
	}
}

func TestCheckPollers(t *testing.T) {
	const queue, clean, dirty = "laug-gate-1a2b3c4d", "laug-worker@abc1234", "laug-worker@abc1234-dirty"
	if _, err := checkPollers(nil, clean, dirty, queue, 4242, "laug"); err == nil ||
		err.Error() != "worker pid 4242 runs, but the server lists no poller on laug-gate-1a2b3c4d yet; after a server restart that lasts about 13 s: try again" {
		t.Fatalf("no poller: %v", err)
	}
	_, err := checkPollers([]string{"laug-worker@0000000"}, clean, dirty, queue, 4242, "laug")
	if err == nil || !strings.Contains(err.Error(), "no poller on laug-gate-1a2b3c4d is laug-worker@abc1234 or laug-worker@abc1234-dirty: pollers laug-worker@0000000;") {
		t.Fatalf("only another commit's worker: %v", err)
	}
	if _, err := checkPollers([]string{"laug-worker@0000000"}, clean, clean, queue, 4242, "laug"); err == nil || strings.Contains(err.Error(), "-dirty") {
		t.Fatalf("a clean tree names one form: %v", err)
	}
	for _, c := range []struct {
		pollers       []string
		dirty, atHead string
	}{
		{[]string{clean}, dirty, clean},
		{[]string{"laug-worker@0000000", dirty}, dirty, dirty},
		{[]string{clean}, clean, clean},
	} {
		got, err := checkPollers(c.pollers, clean, c.dirty, queue, 4242, "laug")
		if err != nil || got != c.atHead {
			t.Errorf("pollers %v: %q, %v; want %q", c.pollers, got, err, c.atHead)
		}
	}
}

// decideStart's four branches, on a run directory as gate/step.sh check leaves it (.state.json and an empty .steps/)
// in a temporary checkout, with RemoveEmptyRun as remove.
func TestDecideStart(t *testing.T) {
	const gid, queue = "g20261016-120000", "laug-gate-1a2b3c4d"
	const id, shown = "gate-" + gid, "results/" + gid
	startErr := errors.New("context deadline exceeded")
	running := func(q string) *workflowpb.WorkflowExecutionInfo {
		return &workflowpb.WorkflowExecutionInfo{Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, TaskQueue: q,
			Execution: &commonpb.WorkflowExecution{WorkflowId: id, RunId: "run-1"}}
	}
	for _, c := range []struct {
		name     string
		err      error
		info     *workflowpb.WorkflowExecutionInfo
		derr     error
		extra    bool // a file beside check's, so the directory is not empty
		describe bool // describe is called
		follow   bool
		removed  bool // the directory is gone after
		line     string
	}{
		{name: "already started", err: serviceerror.NewWorkflowExecutionAlreadyStarted("started", "", ""), removed: true,
			line: "laug · " + id + " belongs to another run (another checkout started a gate in the same second): start again"},
		{name: "already started, not empty", err: serviceerror.NewWorkflowExecutionAlreadyStarted("started", "", ""), extra: true,
			line: "laug · " + id + " belongs to another run (another checkout started a gate in the same second): start again"},
		{name: "runs on this queue", err: startErr, info: running(queue), describe: true, follow: true,
			line: "laug · the start answered context deadline exceeded, but " + id + " runs on " + queue},
		{name: "not found", err: startErr, derr: serviceerror.NewNotFound("no such workflow"), describe: true, removed: true,
			line: "laug · start " + id + " failed: context deadline exceeded; removed " + shown},
		{name: "not found, not empty", err: startErr, derr: serviceerror.NewNotFound("no such workflow"), describe: true, extra: true,
			line: "laug · start " + id + " failed: context deadline exceeded; left " + shown + ": it is not empty"},
		{name: "runs on another queue", err: startErr, info: running("laug-gate-deadbeef"), describe: true,
			line: "laug · start " + id + " failed: context deadline exceeded; the server says " +
				statusText(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING) + " on laug-gate-deadbeef; left " + shown},
		{name: "describe fails", err: startErr, derr: serviceerror.NewUnavailable("connection refused"), describe: true,
			line: "laug · start " + id + " failed: context deadline exceeded; the server says connection refused; left " + shown},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			dir := filepath.Join(repo, "results", gid)
			if err := os.MkdirAll(filepath.Join(dir, ".steps"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".state.json"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.extra {
				if err := os.WriteFile(filepath.Join(dir, "gate.json"), []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			described := false
			describe := func() (*workflowpb.WorkflowExecutionInfo, error) { described = true; return c.info, c.derr }
			remove := func() (bool, error) {
				return RemoveEmptyRun(repo, GateInput{Results: "results"}, gid)
			}
			d := decideStart(id, shown, queue, c.err, describe, remove)
			if d.line != c.line || d.follow != c.follow || (c.follow && d.runID != "run-1") || described != c.describe {
				t.Errorf("got %+v, described %v\nwant line %q, follow %v, described %v", d, described, c.line, c.follow, c.describe)
			}
			if _, err := os.Stat(dir); (err == nil) == c.removed {
				t.Errorf("the run's directory: stat %v, want removed %v", err, c.removed)
			}
		})
	}
}

// The starter prints every step as it began and every item end, each once, in time order, from whatever the queries
// answered: a line once two answers in a row agree on its time, and the rest at the last answer, so steps of under
// 2 s and the judge path's print too. An answer on a stale clock (a worker just restarted) prints nothing: the next
// answer's time differs, and that one prints. A failed query prints once.
func TestProgressPrinter(t *testing.T) {
	at := func(s int) time.Time { return time.Date(2026, 10, 16, 12, 0, s, 0, time.UTC) }
	marks := func(ids ...string) []GateMark {
		var ms []GateMark
		for i, id := range ids {
			ms = append(ms, GateMark{ID: id, At: at(i)})
		}
		return ms
	}
	zero, term := 0, 143
	by := "TERM"
	items := map[string]ItemEndAt{
		"pl":     {ItemEnd: ItemEnd{Key: "pl", Found: true, Exit: &term, StoppedBy: &by}, At: at(9)},
		"steady": {ItemEnd: ItemEnd{Key: "steady", Found: true, Exit: &zero}, At: at(8)},
	}
	stale := append(marks("open", "preflight", "omes", "wave"), GateMark{ID: "judge", At: at(3)})
	judged := append(marks("open", "preflight", "omes", "wave"), GateMark{ID: "judge", At: at(10)})
	var pp progressPrinter
	var b bytes.Buffer
	pp.print(&b, GateProgress{Began: marks("open", "preflight")}, nil, false, false)
	pp.print(&b, GateProgress{}, errors.New("deadline exceeded"), false, false)
	pp.print(&b, GateProgress{}, errors.New("deadline exceeded"), false, false)
	pp.print(&b, GateProgress{Began: marks("open", "preflight", "omes", "wave"), Items: items}, nil, false, false)
	pp.print(&b, GateProgress{}, errors.New("context canceled"), true, false)
	pp.print(&b, GateProgress{Began: stale, Items: items}, nil, false, false)
	pp.print(&b, GateProgress{Began: judged, Items: items}, nil, false, false)
	pp.print(&b, GateProgress{Began: judged, Items: items}, nil, false, true)
	want := `progress: no answer (deadline exceeded)
12:00:00 UTC open
12:00:01 UTC preflight
12:00:02 UTC omes
12:00:03 UTC wave
12:00:08 UTC  item steady: exit 0
12:00:09 UTC  item pl: exit 143 after TERM
12:00:10 UTC judge
`
	if b.String() != want {
		t.Fatalf("printed\n%s\nwant\n%s", b.String(), want)
	}

	// One answer's lines in time order: an item end that stopped gave comes before the steps after stopped, those of
	// the same time included.
	pp, b = progressPrinter{}, bytes.Buffer{}
	ms := []GateMark{{ID: "stop-wave", At: at(1)}, {ID: "stopped", At: at(1)}, {ID: "post-stopped", At: at(2)}, {ID: "release-stopped", At: at(3)}}
	pl := map[string]ItemEndAt{"pl": items["pl"]}
	pl["pl"] = ItemEndAt{ItemEnd: pl["pl"].ItemEnd, At: at(2)}
	pp.print(&b, GateProgress{Began: ms, Items: pl}, nil, false, true)
	want = `12:00:01 UTC stop-wave
12:00:01 UTC stopped
12:00:02 UTC  item pl: exit 143 after TERM
12:00:02 UTC post-stopped
12:00:03 UTC release-stopped
`
	if b.String() != want {
		t.Fatalf("one answer: printed\n%s\nwant\n%s", b.String(), want)
	}

	// No last answer: the lines held print as they were.
	pp, b = progressPrinter{}, bytes.Buffer{}
	pp.print(&b, GateProgress{Began: marks("open")}, nil, false, false)
	pp.print(&b, GateProgress{}, errors.New("deadline exceeded"), false, true)
	if want := "progress: no answer (deadline exceeded)\n12:00:00 UTC open\n"; b.String() != want {
		t.Fatalf("no last answer: printed\n%s\nwant\n%s", b.String(), want)
	}
}

func TestStatusLine(t *testing.T) {
	info := &workflowpb.WorkflowExecutionInfo{Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
		StartTime: timestamppb.New(time.Date(2026, 10, 16, 18, 28, 17, 0, time.UTC)), CloseTime: timestamppb.New(time.Date(2026, 10, 16, 18, 28, 30, 0, time.UTC))}
	if got := statusLine("g20261016-182817", "gate-g20261016-182817", info); got != "laug · gate g20261016-182817: workflow gate-g20261016-182817 Completed, started 18:28:17 UTC, closed 18:28:30 UTC" {
		t.Fatalf("%q", got)
	}
	info.Status, info.CloseTime = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, nil
	if got := statusLine("g20261016-182817", "gate-g20261016-182817", info); got != "laug · gate g20261016-182817: workflow gate-g20261016-182817 Running, started 18:28:17 UTC" {
		t.Fatalf("%q", got)
	}
}

// gateJSONInput: gate.json's tag, pool and fake replace laug's, Extra comes from the environment laug built for a fake
// gate only, its first FAKE_GATE_DIR as getenv reads it, and a bad gate.json, or a fake one with no FAKE_GATE_DIR, is
// refused.
func TestGateJSONInput(t *testing.T) {
	fromMake := GateInput{ServerTag: "1.31.0", PoolListen: "127.0.0.1:7700", Results: "gate/results",
		Extra: map[string]string{"FAKE_GATE_ITEM_S": "9"}}
	dir := t.TempDir()
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "gate.json"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if in, found, err := gateJSONInput(fromMake, dir, "r/g", nil); err != nil || found || in.ServerTag != "1.31.0" || in.PoolListen != "127.0.0.1:7700" || in.Extra["FAKE_GATE_ITEM_S"] != "9" {
		t.Fatalf("no gate.json: %+v, %v, %v", in, found, err)
	}
	environ := []string{"FAKE_GATE_DIR=/tmp/fake", "GATE_BACKEND=fake", "OTHER_KNOB=x", "FAKE_GATE_DIR=/tmp/second"}
	write(`{"server_tag":"1.32.1","pool":"127.0.0.1:17891","fake":true,"plan":"gate/plan.yaml"}`)
	in, found, err := gateJSONInput(fromMake, dir, "r/g", environ)
	if err != nil || !found || in.ServerTag != "1.32.1" || in.PoolListen != "127.0.0.1:17891" || !in.Fake {
		t.Fatalf("a fake gate.json: %+v, %v, %v", in, found, err)
	}
	for k := range in.Extra {
		if k != "GATE_BACKEND" && !strings.HasPrefix(k, "FAKE_GATE_") {
			t.Errorf("Extra holds %s", k)
		}
	}
	if in.Extra["FAKE_GATE_DIR"] != "/tmp/fake" || in.Extra["GATE_BACKEND"] != "fake" || in.Extra["FAKE_GATE_ITEM_S"] != "" {
		t.Fatalf("Extra %v: want this environment's, not laug's input", in.Extra)
	}
	write(`{"server_tag":"1.32.1","pool":"127.0.0.1:17891","fake":false}`)
	if in, _, err := gateJSONInput(fromMake, dir, "r/g", environ); err != nil || in.Fake || in.Extra != nil {
		t.Fatalf("a gate.json that is not fake: %+v, %v", in, err)
	}
	for _, bad := range []string{`{"server_tag":"1.32","pool":"127.0.0.1:17891"}`, `{"server_tag":"1.32.1","pool":"0.0.0.0:7700"}`,
		`{"server_tag":"1.32.1","pool":"127.0.0.1:70000"}`, `not json`} {
		write(bad)
		if _, _, err := gateJSONInput(fromMake, dir, "r/g", environ); err == nil || !strings.Contains(err.Error(), "r/g/gate.json does not name") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	write(`{"server_tag":"1.32.1","pool":"127.0.0.1:17891","fake":true}`)
	if _, _, err := gateJSONInput(fromMake, dir, "r/g", []string{"FAKE_GATE_DIR=", "GATE_BACKEND=fake"}); !errors.Is(err, errNoFakeDir) {
		t.Fatalf("a fake gate with no FAKE_GATE_DIR: %v", err)
	}
}

// S1 when the workflow returned no verdict but verdict.json is there.
func TestReportNoVerdict(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "results", "g20261016-120000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "verdict.json"), []byte(`{"verdict":"BLOCK","stopped_at":"wave"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	(&starter{repo: repo}).report(&b, GateInput{Results: "results"}, "g20261016-120000",
		GateResult{Dir: "results/g20261016-120000", End: "stopped", Exit: 2})
	if !strings.Contains(b.String(), "S1: the workflow carried no verdict; results/g20261016-120000/verdict.json has 39 bytes\n") {
		t.Fatalf("report:\n%s", b.String())
	}
}

// Every laug command the binaries print carries the server they were printed from, and says so when laug cannot reach
// its namespace.
func TestLaugFlags(t *testing.T) {
	if got := LaugFlags("127.0.0.1:7900", "default"); got != " -orchestrator 127.0.0.1:7900" {
		t.Errorf("default namespace: %q", got)
	}
	if got := LaugFlags("127.0.0.1:7400", "other"); got != " -orchestrator 127.0.0.1:7400 (namespace other: laug reaches default only)" {
		t.Errorf("another namespace: %q", got)
	}
}

// CLIFlags prints internal/script's OrchCLI, the Makefile's ORCH_CLI, which the detach line's temporal workflow cancel
// carries, flag for flag.
func TestCLIFlagsAreORCHCLI(t *testing.T) {
	for _, c := range []struct{ addr, ns string }{{"127.0.0.1:7900", "default"}, {"127.0.0.1:7400", "other"}} {
		if got, want := CLIFlags(c.addr, c.ns), fmt.Sprintf(script.OrchCLI, c.addr, c.ns); got != want {
			t.Errorf("CLIFlags %q\nOrchCLI  %q", got, want)
		}
	}
}
