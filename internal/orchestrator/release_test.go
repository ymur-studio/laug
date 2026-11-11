package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStepOf(t *testing.T) {
	for _, c := range []struct{ name, verb, key, class string }{
		{"open", "open", "", "stop"}, {"preflight", "preflight", "", "stop"}, {"omes", "omes", "", "stop"},
		{"laugd", "laugd", "", "stop"}, {"harness", "harness", "", "stop"}, {"wave-time", "wave-time", "", "stop"},
		{"wave", "wave", "", "stop"}, {"barrier", "barrier", "", "stop"}, {"lease-my-key", "lease", "my-key", "stop"},
		{"quiet-12", "quiet", "12", "stop"},
		{"judge", "combine", "", "skip"}, {"post", "post", "", "skip"}, {"release", "release", "", "skip"},
		{"stopped", "stopped", "", "skip"},
		{"lease-Bad", "", "", "unknown"}, {"quiet-x", "", "", "unknown"}, {"quiet-01", "", "", "unknown"}, {"holder", "", "", "unknown"},
	} {
		verb, key, class := stepOf(c.name)
		if verb != c.verb || key != c.key || class != c.class {
			t.Errorf("stepOf(%q) = %q, %q, %q; want %q, %q, %q", c.name, verb, key, class, c.verb, c.key, c.class)
		}
		if class == "stop" {
			if err := stepKey(verb, key); err != nil || StepName(verb, key) != c.name {
				t.Errorf("%s: stepKey %v, StepName %q", c.name, err, StepName(verb, key))
			}
		}
	}
}

// A judge still running when the release by hand starts (a terminate or a lost db while it ran) is adopted and
// waited for: stopped starts only once judge.done is there, so it never writes the trap verdict beside
// the judge's.
func TestReleaseByHandWaitsForALiveJudge(t *testing.T) {
	a, in := stepGate(t, "STEP_S=2")
	ctx, cancel := context.WithCancelCause(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := a.runStep(ctx, stepIn(in, fakeGID, "combine", ""), "wf-1", "run-1", noBeat)
		got <- err
	}()
	steps := filepath.Join(a.Repo, in.Results, fakeGID, ".steps")
	waitFor(t, "judge.pid", func() bool { return exists(filepath.Join(steps, "judge.pid")) })
	cancel(errors.New("the workflow was terminated"))
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("runStep after its context ended: %v", err)
	}
	if exists(filepath.Join(steps, "judge.done")) {
		t.Fatal("the judge answered before the release by hand: no live judge to wait for")
	}
	quick := withFake(in, "STEP_S=0")
	var out bytes.Buffer
	if _, err := releaseByHand(context.Background(), a, quick, fakeGID, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "judge: adopted, exit 0\n") || strings.Contains(out.String(), "no live step") {
		t.Fatalf("output:\n%s", out.String())
	}
	jd, err1 := os.Stat(filepath.Join(steps, "judge.done"))
	se, err2 := os.Stat(filepath.Join(steps, "stopped.env"))
	if err1 != nil || err2 != nil || se.ModTime().Before(jd.ModTime()) {
		t.Fatalf("judge.done %v, stopped.env %v: stopped must start after the judge answered", err1, err2)
	}
	if b, _ := os.ReadFile(filepath.Join(steps, "judge.started")); strings.Count(string(b), "\n") != 1 {
		t.Fatalf("the judge started %d times, want once", strings.Count(string(b), "\n"))
	}
}

// TestReleaseLine: the line starts with the word laug hands the roles, laug or bin/laug, and names a fake gate's dir
// and a results dir that is not the default.
func TestReleaseLine(t *testing.T) {
	const gid, flags = "g20261016-120000", " -orchestrator 127.0.0.1:7400"
	in := GateRunInput{GateInput: GateInput{Results: "gate/results"}}
	if got := ReleaseLine("laug", gid, in, flags); got != "laug gate stop g20261016-120000 -orchestrator 127.0.0.1:7400" {
		t.Fatalf("laug's defaults: %q", got)
	}
	if got := ReleaseLine("bin/laug", gid, in, ""); got != "bin/laug gate stop g20261016-120000" {
		t.Fatalf("no laug on PATH: %q", got)
	}
	in.Results, in.Fake, in.Extra = "orchestrator/run/record/advance", true, map[string]string{"FAKE_GATE_DIR": "/tmp/a b/it's"}
	if got, want := ReleaseLine("laug", gid, in, flags), `FAKE_GATE_DIR='/tmp/a b/it'\''s' laug gate stop g20261016-120000 `+
		`-results orchestrator/run/record/advance -orchestrator 127.0.0.1:7400`; got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	in.Fake = false
	if got := ReleaseLine("laug", gid, in, ""); got != "laug gate stop g20261016-120000 -results orchestrator/run/record/advance" {
		t.Fatalf("not fake: %q", got)
	}
}
