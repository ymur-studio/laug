package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
)

// The four roles laug runs in its own process refuse before they dial anything, with a line each, exit 2: a flag
// they do not take, no -repo, no TEMPORAL_ADDRESS in the environment laug built, a bad gate id, two modes at once.
// The line names the laug command the role runs for, as every laug error does: "laug · <command>: ". A line to type
// starts with LAUG, the word laug sets in the environment it builds, or laug when it holds none. None of these rows
// reaches a server, so none needs one.
func TestRolesRefuseBeforeTheServer(t *testing.T) {
	repo := t.TempDir()
	if _, err := Queue(filepath.Join(repo, RunDir), true, "laug"); err != nil {
		t.Fatal(err)
	}
	addr := []string{"TEMPORAL_ADDRESS=127.0.0.1:7400"}
	for _, c := range []struct {
		name    string
		main    func(context.Context, string, []string, []string, io.Writer, io.Writer) int
		cmd     string
		args    []string
		environ []string
		code    int
		stderr  string
	}{
		{"worker, a bad flag", WorkerMain, "orchestrator worker", []string{"-nope"}, addr, 2,
			"flag provided but not defined: -nope"},
		{"worker, no -repo", WorkerMain, "orchestrator worker", nil, addr, 2,
			"laug · orchestrator worker: -repo is required: run laug orchestrator worker"},
		{"worker, no address", WorkerMain, "orchestrator worker", []string{"-repo", repo}, nil, 2,
			"laug · orchestrator worker: TEMPORAL_ADDRESS is empty: run laug orchestrator worker, which sets it from " +
				"-orchestrator"},
		{"probe, no -repo", ProbeMain, "orchestrator probe", nil, addr, 2,
			"laug · orchestrator probe: run laug orchestrator probe [-idle 60s] [-unpolled]"},
		{"probe, no address", ProbeMain, "orchestrator probe", []string{"-repo", repo}, nil, 2,
			"laug · orchestrator probe: TEMPORAL_ADDRESS is empty: run laug orchestrator probe, which sets it from " +
				"-orchestrator"},
		{"probe, no worker", ProbeMain, "orchestrator probe", []string{"-repo", repo}, addr, 2,
			"laug · orchestrator probe: no worker runs for this checkout (no process holds " +
				"orchestrator/run/worker.lock): laug orchestrator worker"},
		{"probe, no worker, laug not on PATH", ProbeMain, "orchestrator probe", []string{"-repo", repo},
			append([]string{"LAUG=bin/laug"}, addr...), 2,
			"laug · orchestrator probe: no worker runs for this checkout (no process holds " +
				"orchestrator/run/worker.lock): bin/laug orchestrator worker"},
		{"starter, -status no address, laug not on PATH", StarterMain, "gate status", []string{"-repo", repo,
			"-status", "g20261016-120000"}, []string{"LAUG=bin/laug"}, 2,
			"laug · gate status: TEMPORAL_ADDRESS is empty: run bin/laug gate status, which sets it from -orchestrator"},
		{"record, not a gate id", RecordMain, "orchestrator record", []string{"-repo", repo, "-gate", "g1", "-out",
			"x.json"}, addr, 2,
			`laug · orchestrator record: -gate "g1": a gate id, g<date>-<time>, as laug gate run -workflow printed it`},
		{"record, no address", RecordMain, "orchestrator record", []string{"-repo", repo, "-gate", "g20261016-120000",
			"-out", "x.json"}, nil, 2,
			"laug · orchestrator record: TEMPORAL_ADDRESS is empty: run laug orchestrator record, which sets it from " +
				"-orchestrator"},
		{"starter, no -repo", StarterMain, "gate status", []string{"-status", "g20261016-120000"}, addr, 2,
			"laug · gate status: -repo is required: run laug gate run -workflow"},
		{"starter, two modes", StarterMain, "gate status", []string{"-repo", repo, "-status", "g20261016-120000",
			"-cancel", "g20261016-120000"}, addr, 2,
			"laug · gate status: -status and -cancel are different things: name one"},
		{"starter, -reason alone", StarterMain, "gate terminate", []string{"-repo", repo, "-reason", "x"}, addr, 2,
			"laug · gate terminate: -reason goes with -terminate"},
		{"starter, -terminate without -reason", StarterMain, "gate terminate", []string{"-repo", repo, "-terminate",
			"g20261016-120000"}, addr, 2, "laug · gate terminate: -terminate takes -reason: history records why"},
		{"starter, -cancel not a gate id", StarterMain, "gate cancel", []string{"-repo", repo, "-cancel", "x"}, addr, 2,
			`laug · gate cancel: -cancel "x": a gate id, g<date>-<time>, as laug gate run -workflow printed it`},
		{"starter, -status no address", StarterMain, "gate status", []string{"-repo", repo, "-status",
			"g20261016-120000"}, nil, 2,
			"laug · gate status: TEMPORAL_ADDRESS is empty: run laug gate status, which sets it from -orchestrator"},
		{"starter, no tag", StarterMain, "gate run", []string{"-repo", repo}, addr, 2,
			`laug · gate run: SERVER_TAG must be a release tag, x.y.z; got ""`},
		{"starter, -otlp off loopback", StarterMain, "gate run", []string{"-repo", repo, "-otlp", "localhost:7751"},
			addr, 2, `laug · gate run: -otlp "localhost:7751": 127.0.0.1:<port>, the stack's Collector, ` +
				`127.0.0.1:7751 (laug's -otlp)`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := c.main(context.Background(), c.cmd, c.args, c.environ, &stdout, &stderr)
			if code != c.code || !strings.Contains(stderr.String(), c.stderr+"\n") || stdout.Len() != 0 {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s\nwant a line:\n%s", code, c.code, stdout.String(),
					stderr.String(), c.stderr)
			}
		})
	}
}

// The worker refuses a TEMPORAL_SDK_FLAG_* or TEMPORAL_DEBUG in this process's environment, which the SDK read when
// the binary started, whatever the environment laug built says.
func TestWorkerRefusesTheSDKsFlags(t *testing.T) {
	for _, name := range []string{"TEMPORAL_DEBUG", "TEMPORAL_SDK_FLAG_1"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "1")
			var stdout, stderr bytes.Buffer
			code := WorkerMain(context.Background(), "orchestrator worker", []string{"-repo", t.TempDir()},
				[]string{"TEMPORAL_ADDRESS=x:1"}, &stdout, &stderr)
			if want := "laug · orchestrator worker: " + name + " is set; the SDK read it when laug started: " +
				"unset it, then start laug orchestrator worker again\n"; code != 2 || stderr.String() != want {
				t.Errorf("exit %d\n%s", code, stderr.String())
			}
		})
	}
}

// The starter's input is the environment laug built: its first entry of a name wins, an empty value takes the
// default, and a fake gate carries GATE_BACKEND and FAKE_GATE_* and nothing else.
func TestStarterInput(t *testing.T) {
	s := &starter{environ: []string{"SERVER_TAG=1.32.1", "SERVER_TAG=9.9.9", "PLAN=", "ITEMS=2", "GATE_FAKE=1",
		"FAKE_GATE_DIR=/f", "GATE_BACKEND=fake", "OTHER=x", "XDG_RUNTIME_DIR=/run/u"}}
	in, err := s.input()
	if err != nil {
		t.Fatal(err)
	}
	want := GateInput{ServerTag: "1.32.1", Plan: "gate/plan.yaml", Items: "2", Post: "local", Base: "main",
		OmesDir: ".upstream/omes", Results: "gate/results", Lock: "gate/.lock", HostLock: "/run/u/laug-gate.lock",
		PoolListen: "127.0.0.1:7700", Fake: true}
	extra := in.Extra
	in.Extra = nil
	if !reflect.DeepEqual(in, want) || len(extra) != 2 || extra["FAKE_GATE_DIR"] != "/f" || extra["GATE_BACKEND"] != "fake" {
		t.Errorf("input %+v, extra %v\nwant %+v", in, extra, want)
	}
	for _, bad := range [][]string{{"SERVER_TAG=1.32"}, {"SERVER_TAG=1.32.1", "ITEMS=0"},
		{"SERVER_TAG=1.32.1", "GATE_FAKE=yes"}, {"SERVER_TAG=1.32.1", "GATE_POOL_LISTEN=0.0.0.0:7700"}} {
		if _, err := (&starter{environ: bad}).input(); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// cancelGate asks first: a gate that does not run, or one the server does not know, gets a refusal and no request;
// a running one gets its first line, the request, and a line that says it was sent.
func TestCancelGate(t *testing.T) {
	const gid = "g20261016-120000"
	info := func(s enumspb.WorkflowExecutionStatus) *workflowpb.WorkflowExecutionInfo {
		return &workflowpb.WorkflowExecutionInfo{Status: s}
	}
	for _, c := range []struct {
		name    string
		info    *workflowpb.WorkflowExecutionInfo
		derr    error
		sendErr error
		code    int
		sent    bool
		stdout  string
		stderr  string
	}{
		{name: "running", info: info(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING), sent: true,
			stdout: "laug · gate cancel " + gid + ": before the judge its stop path stops the live step, writes a " +
				"BLOCK that names it, hands it on and releases; from the judge on nothing changes; its status: laug " +
				"gate status " + gid + " -orchestrator 127.0.0.1:7400\ncancel requested for workflow gate-" + gid + "\n"},
		{name: "completed", info: info(enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED), code: 2,
			stderr: "laug · gate cancel: workflow gate-" + gid + " is completed, not running: nothing to cancel; how " +
				"it ended: laug gate status " + gid + " -orchestrator 127.0.0.1:7400\n"},
		{name: "not known", derr: serviceerror.NewNotFound("no such workflow"), code: 2,
			stderr: "laug · gate cancel: the server at 127.0.0.1:7400, namespace default, does not know workflow " +
				"gate-" + gid + "; nothing done\n"},
		{name: "describe fails", derr: errors.New("unavailable"), code: 2,
			stderr: "laug · gate cancel: describe gate-" + gid + ": unavailable, so whether it runs is not known; " +
				"nothing done\n"},
		{name: "the request fails", info: info(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING), sendErr: errors.New("x"),
			code: 1, sent: true, stderr: "laug · gate cancel: the cancel request for gate-" + gid + ": x\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			sent := false
			code := cancelGate(gid, "127.0.0.1:7400", "default", "laug",
				func() (*workflowpb.WorkflowExecutionInfo, error) { return c.info, c.derr },
				func() error { sent = true; return c.sendErr }, &stdout, &stderr)
			if code != c.code || sent != c.sent || stderr.String() != c.stderr ||
				(c.stdout != "" && stdout.String() != c.stdout) {
				t.Errorf("exit %d, sent %t\nstdout:\n%s\nstderr:\n%s", code, sent, stdout.String(), stderr.String())
			}
		})
	}
}
