package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// fakeProc writes the two /proc files HostStamp reads into a temp dir.
func fakeProc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sys/kernel/random"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("594312.40 11203344.90\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sys/kernel/random/boot_id"), []byte("00000000-1111-2222-3333-444444444444\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A wall time whose milliseconds end in 0.
var wall120 = time.Unix(1760496001, 120_000_000)

func fixedActivities(t *testing.T) *ProbeActivities {
	return &ProbeActivities{Now: func() time.Time { return wall120 }, Proc: fakeProc(t), Every: 100 * time.Millisecond}
}

// Why the wall time is a RawMessage: a float64 drops the trailing 0.
func TestFloatDropsTrailingZero(t *testing.T) {
	b, _ := json.Marshal(1760496001.120)
	if string(b) != "1760496001.12" {
		t.Fatalf("float64 marshals as %s; the reason for WallJSON is gone", b)
	}
	if got := string(WallJSON(wall120)); got != "1760496001.120" {
		t.Fatalf("WallJSON = %s, want 1760496001.120", got)
	}
	if got := string(WallJSON(time.Unix(1760496001, 7_000_000))); got != "1760496001.007" {
		t.Fatalf("WallJSON = %s, want 1760496001.007", got)
	}
}

func TestHostStampThroughTheDataConverter(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(fixedActivities(t))
	val, err := env.ExecuteActivity("HostStamp")
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := val.Get(&raw); err != nil {
		t.Fatal(err)
	}
	want := `{"wall":1760496001.120,"uptime_s":594312.40,"boot_id":"00000000-1111-2222-3333-444444444444"}`
	if string(raw) != want {
		t.Fatalf("payload\n got %s\nwant %s", raw, want)
	}
}

func TestHostStampRefusesABadUptime(t *testing.T) {
	a := fixedActivities(t)
	if err := os.WriteFile(filepath.Join(a.Proc, "uptime"), []byte("up 3 days\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.HostStamp(t.Context()); err == nil {
		t.Fatal("HostStamp took a non-number for uptime")
	}
}

func TestProbeWorkflow(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(Probe)
	env.RegisterActivity(fixedActivities(t))
	env.ExecuteWorkflow(Probe, ProbeInput{Seconds: 1})
	if !env.IsWorkflowCompleted() {
		t.Fatal("Probe did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := env.GetWorkflowResult(&raw); err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(raw, []byte(`"wall":1760496001.120,`)); n != 2 {
		t.Fatalf("the result holds the 3-decimal wall %d times, want 2 (start and end): %s", n, raw)
	}
	var res ProbeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Idle == nil || res.Idle.Seconds != 1 || res.Idle.Heartbeats < 5 || res.Idle.Heartbeats > 11 {
		t.Fatalf("idle %+v: want 1 s and about 10 heartbeat calls at 100 ms", res.Idle)
	}
	if res.Unpolled != nil {
		t.Fatalf("unpolled ran without being asked: %+v", res.Unpolled)
	}
	if res.Start.Before.IsZero() || res.Start.After.Before(res.Start.Before) || res.End.Before.Before(res.Start.After) {
		t.Fatalf("workflow.Now out of order: %+v %+v", res.Start, res.End)
	}
	if res.Start.Before.Location() != time.UTC {
		t.Fatalf("workflow.Now is not in UTC: %s", res.Start.Before)
	}
}

func TestProbeSkipsIdleAtZero(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	Register(env, t.TempDir(), "") // the real activities: this host's clock and /proc
	env.ExecuteWorkflow(Probe, ProbeInput{})
	var res ProbeResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}
	if res.Idle != nil {
		t.Fatalf("Idle ran at 0 s: %+v", res.Idle)
	}
}

func TestIdleHeartbeatsAndStopsOnCancel(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(&ProbeActivities{Every: 250 * time.Millisecond})
	val, err := env.ExecuteActivity("Idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	var r IdleResult
	if err := val.Get(&r); err != nil {
		t.Fatal(err)
	}
	if r.Seconds != 1 || r.Heartbeats < 3 || r.Heartbeats > 4 {
		t.Fatalf("Idle(1) at 250 ms = %+v, want 3 or 4 heartbeat calls", r)
	}

	a := &ProbeActivities{Every: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if _, err := a.Idle(ctx, 60); !errors.Is(err, context.Canceled) {
		t.Fatalf("Idle after cancel returned %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Idle took %s to see the cancel", d)
	}
}

func TestDescribeUnpolled(t *testing.T) {
	var u Unpolled
	describeUnpolled(fmt.Errorf("activity error: %w", temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START, nil)), &u)
	if u.TimeoutType != enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START.String() || u.Error == "" {
		t.Fatalf("describeUnpolled = %+v", u)
	}
	var none Unpolled
	describeUnpolled(nil, &none)
	if none != (Unpolled{}) {
		t.Fatalf("describeUnpolled(nil) wrote %+v", none)
	}
}

func TestQueueIsMadeOnceAndKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if _, err := Queue(dir, false, "bin/laug"); err == nil || !strings.Contains(err.Error(), ": bin/laug orchestrator worker makes it") {
		t.Fatalf("Queue without create on a fresh checkout: %v", err)
	}
	q1, err := Queue(dir, true, "laug")
	if err != nil {
		t.Fatal(err)
	}
	if !queueRE.MatchString(q1) {
		t.Fatalf("queue %q is not laug-gate-<8 hex>", q1)
	}
	q2, err := Queue(dir, true, "laug")
	if err != nil || q2 != q1 {
		t.Fatalf("second call gave %q, %v; want %q", q2, err, q1)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("run dir holds %d entries, want only queue", len(entries))
	}
	if err := os.WriteFile(filepath.Join(dir, "queue"), []byte("laug-gate-1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Queue(dir, true, "laug"); err == nil {
		t.Fatal("Queue took a file that is not laug-gate-<8 hex>")
	}
}

func TestIdentityFromTheVCSStamp(t *testing.T) {
	rev := debug.BuildSetting{Key: "vcs.revision", Value: "82a189e0123456789abcdef0123456789abcdef0"}
	for _, c := range []struct {
		settings []debug.BuildSetting
		want     string
	}{
		{[]debug.BuildSetting{rev, {Key: "vcs.modified", Value: "false"}}, "laug-worker@82a189e"},
		{[]debug.BuildSetting{rev, {Key: "vcs.modified", Value: "true"}}, "laug-worker@82a189e-dirty"},
		{nil, "laug-worker@unknown"},
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "not-a-commit"}}, "laug-worker@unknown"},
	} {
		if got := identityFrom(c.settings); got != c.want {
			t.Errorf("identityFrom(%v) = %q, want %q", c.settings, got, c.want)
		}
	}
	if got := WorkerIdentity("318afd0aaaabbbbccccddddeeeeffff000011112", "true"); got != "laug-worker@318afd0-dirty" {
		t.Errorf("WorkerIdentity from -ldflags = %q", got)
	}
	if got := WorkerIdentity("", ""); got != "laug-worker@unknown" {
		t.Errorf("WorkerIdentity in a test binary, which go test does not stamp = %q", got)
	}
}
