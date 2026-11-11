package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// faultStrays are the knobs fault and raun map that strayEnviron holds with another value.
var faultStrays = []string{"MAX_CONCURRENT", "POOL_ADDR", "POOL_QUIET_WAIT_S", "RATE", "RUN_ID", "SWEEP_MARGIN_S",
	"THRESHOLDS"}

// TestFault: the env matrix. fault execs faults/run.sh on the leased cell with the scenario and the fault as its
// arguments, RUN_ID and POOL_ADDR set, and the knobs make passed on unnamed only when their flags are given. It needs a
// fault the catalog faults/run.sh reads names, CATALOG's when the environment sets it, and a lease, but under
// -dry-run, which leases nothing.
func TestFault(t *testing.T) {
	scripted := func(name string, args []string, knobs ...string) envCase {
		return envCase{name: name, args: append([]string{"fault"}, args...), lease: true, how: script.Exec,
			script: "faults/run.sh", argv: []string{scenarioDefault, "persistence-latency"},
			knobs:  append([]string{"RUN_ID=20261022-120000", "POOL_ADDR=http://" + poolDefault}, knobs...),
			strays: faultStrays}
	}
	every := scripted("every flag", []string{"kill-history", "-scenario", "throughput_stress", "-run-id", "r1", "-pool",
		"127.0.0.1:17700", "-quiet-wait", "0s", "-thresholds", "load/steady-state.yaml", "-rate", "30",
		"-max-concurrent", "10", "-sweep-margin", "1m"})
	every.argv = []string{"throughput_stress", "kill-history"}
	every.knobs = []string{"RUN_ID=r1", "POOL_ADDR=http://127.0.0.1:17700", "POOL_QUIET_WAIT_S=0",
		"THRESHOLDS=load/steady-state.yaml", "RATE=30", "MAX_CONCURRENT=10", "SWEEP_MARGIN_S=60"}
	first := scripted("a fault on the leased cell", []string{"persistence-latency"})
	first.out = "laug · fault: persistence-latency on lease c0ffee01 (127.0.0.1:7243), run 20261022-120000; Ctrl-C " +
		"removes the fault and stops it"
	seam := scripted("a fault of the catalog CATALOG names", []string{"test-act"})
	seam.argv = []string{scenarioDefault, "test-act"}
	seam.files = map[string]string{"fixture.yaml": "faults:\n  test-act:\n    signal: x\n"}
	seam.more = []string{"CATALOG=fixture.yaml"}
	runMatrix(t, []envCase{
		first, every, seam,
		{name: "bare", args: []string{"fault"}, code: 2},
		{name: "no lease", args: []string{"fault", "persistence-latency"}, code: 2,
			errOut: "laug · fault: runs on the leased cell, and .last-lease.json holds no lease: laug lease leases one, " +
				"or laug raun persistence-latency leases, runs the fault and gives the cell back"},
		{name: "-dry-run, no lease", args: []string{"fault", "persistence-latency", "-dry-run"}, strays: faultStrays,
			out: "RUN_ID=20261022-120000 POOL_ADDR='http://" + poolDefault + "' faults/run.sh " + scenarioDefault +
				" persistence-latency"},
		{name: "a fault the catalog lacks", args: []string{"fault", "nope"}, lease: true, code: 2},
		{name: "a pool off loopback", args: []string{"fault", "persistence-latency", "-pool", "10.0.0.1:7700"},
			lease: true, code: 2},
		{name: "-quiet-wait not whole", args: []string{"fault", "persistence-latency", "-quiet-wait", "1.5s"},
			lease: true, code: 2, errOut: "laug · fault: -quiet-wait 1.5s: whole seconds, 0 or more"},
		{name: "-rate 0", args: []string{"fault", "persistence-latency", "-rate", "0"}, lease: true, code: 2,
			errOut: "laug · fault: -rate 0: a whole number over 0"},
		{name: "two faults", args: []string{"fault", "a", "b"}, code: 2,
			errOut: `laug · fault: takes one fault, got ["a" "b"]`},
	})

	// Bare, and with a name the catalog lacks and none near it, it names the catalog's faults, all of them in its
	// order, down and across (TestMistakeFits holds them to one screen); bare, then its synopsis, and no flag. A name
	// 1 edit from one gets that one alone.
	a, _ := matrixApp(t, envCase{})
	_, names, err := faultChoice.names(a)
	if err != nil || len(names) < 10 {
		t.Fatalf("the catalog: %d faults, %v", len(names), err)
	}
	head := fmt.Sprintf("faults/catalog.yaml holds %d:", len(names))
	for _, c := range []struct {
		args       []string
		want, then string
	}{
		{[]string{"fault"}, "laug · fault: names no fault: " + head, "usage: laug fault <fault> [-"},
		{[]string{"fault", "nope"}, "laug · fault: no fault nope in " + strings.Replace(head, " holds", ", which holds",
			1), ""},
		{[]string{"raun"}, "laug · raun: names no fault: " + head, "usage: laug raun <fault> [-"},
		{[]string{"raun", "persistnce-latency"}, "laug · raun: no fault persistnce-latency in faults/catalog.yaml; did " +
			"you mean persistence-latency?", ""},
	} {
		a, rec := matrixApp(t, envCase{lease: true})
		code, _, errOut := runApp(t, a, c.args...)
		first, _, _ := strings.Cut(errOut, "\n")
		listed, rest := downAcross(errOut)
		if strings.Contains(c.want, " holds ") && !slices.Equal(listed, names) {
			t.Errorf("%q lists %q, want the catalog's %q", c.args, listed, names)
		}
		if code != 2 || first != c.want || !strings.HasPrefix(rest, c.then) || c.then == "" && rest != "" ||
			strings.Contains(errOut, "\n  -") || len(rec.calls) != 0 {
			t.Errorf("%q: exit %d\nstderr:\n%s\nwant:\n%s\n...%s...", c.args, code, errOut, c.want, c.then)
		}
	}
}
