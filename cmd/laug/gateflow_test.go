package main

import (
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGateWorkflow: gate run -workflow and the verbs on a gate that runs as workflow Gate, status, cancel, terminate
// and stop, each the starter's role in laug's own process: the env matrix, the refusals before anything runs, the
// role's exit, -fake's directory, and the bare forms.
func TestGateWorkflow(t *testing.T) {
	const gid = "g20261016-120000"
	runKnobs := func(addr, tag, plan, items, base, results string) []string {
		return append(orchKnobs(addr), "SERVER_TAG="+tag, "PLAN="+plan, "ITEMS="+items, "BASE="+base, "GATE_FAKE=",
			"GATE_POST=local", "GATE_RESULTS="+results, "OMES_DIR=.upstream/omes")
	}
	runStrays := []string{"BASE", "GATE_FAKE", "GATE_POST", "GATE_RESULTS", "ITEMS", "OMES_DIR", "ORCH_ADDRESS",
		"ORCH_OTLP", "PLAN", "SERVER_TAG", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE"}
	gidStrays := []string{"GATE", "ORCH_ADDRESS", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE"}
	cases := []envCase{
		{name: "run -workflow", args: []string{"gate", "run", "1.31.0", "-workflow"}, how: inProcess, script: "gate",
			argv:   []string{"-repo", "$ROOT"},
			knobs:  runKnobs("127.0.0.1:7400", "1.31.0", "gate/plan.yaml", "", "main", "gate/results"),
			strays: runStrays, out: "laug · gate run 1.31.0 -workflow: workflow Gate on this checkout's worker through " +
				"127.0.0.1:7400; Ctrl-C detaches, and the gate runs on: laug gate status <gate id> shows it",
			errOut: "laug · gate run: ORCH_OTLP=127.0.0.1:1 in the environment is not passed on; -otlp sets it"},
		{name: "run -workflow, every flag", args: []string{"gate", "run", "1.32.1", "-workflow", "-plan",
			"gate/plans/noise.yaml", "-items", "2", "-base", "HEAD", "-results", "results-x", "-otlp", "127.0.0.1:7751",
			"-orchestrator", "127.0.0.1:7500"}, how: inProcess, script: "gate",
			argv:   []string{"-repo", "$ROOT", "-otlp", "127.0.0.1:7751"},
			knobs:  runKnobs("127.0.0.1:7500", "1.32.1", "gate/plans/noise.yaml", "2", "HEAD", "results-x"),
			strays: runStrays},
		{name: "run -otlp, no -workflow", args: []string{"gate", "run", "1.31.0", "-otlp", "127.0.0.1:7751"}, code: 2,
			errOut: "laug · gate run: -orchestrator and -otlp go with -workflow, which runs the gate through the " +
				"orchestrator's dev server"},
		{name: "run -workflow -json", args: []string{"gate", "run", "1.31.0", "-workflow", "-json"}, code: 2},
		{name: "run -workflow -dry-run", args: []string{"gate", "run", "1.31.0", "-workflow", "-dry-run"}, code: 2,
			errOut: "laug · gate run: -dry-run prints gate/run.sh's line; with -workflow the starter runs in laug's own " +
				"process, and there is no line to print"},
		{name: "run -workflow, -otlp off loopback", args: []string{"gate", "run", "1.31.0", "-workflow", "-otlp",
			"10.0.0.1:7751"}, code: 2},
		{name: "run -workflow, no tag", args: []string{"gate", "run", "-workflow"}, code: 2},
		{name: "status", args: []string{"gate", "status", gid}, how: inProcess, script: "gate",
			argv: []string{"-repo", "$ROOT", "-status", gid}, knobs: orchKnobs("127.0.0.1:7400"), strays: gidStrays},
		{name: "status -orchestrator", args: []string{"gate", "status", gid, "-orchestrator", "127.0.0.1:7500"},
			how: inProcess, script: "gate", argv: []string{"-repo", "$ROOT", "-status", gid},
			knobs: orchKnobs("127.0.0.1:7500"), strays: gidStrays},
		{name: "status, not a gate id", args: []string{"gate", "status", "x"}, code: 2,
			errOut: "laug · gate status: x: not a gate id, g<UTC date>-<UTC time>, as gate run names one"},
		{name: "status, two", args: []string{"gate", "status", gid, gid}, code: 2},
		{name: "status -dry-run", args: []string{"gate", "status", gid, "-dry-run"}, code: 2,
			errOut: "laug · gate status: no flag -dry-run"},
		{name: "cancel", args: []string{"gate", "cancel", gid}, how: inProcess, script: "gate",
			argv: []string{"-repo", "$ROOT", "-cancel", gid}, knobs: orchKnobs("127.0.0.1:7400"), strays: gidStrays},
		{name: "cancel -orchestrator", args: []string{"gate", "cancel", gid, "-orchestrator", "127.0.0.1:7500"},
			how: inProcess, script: "gate", argv: []string{"-repo", "$ROOT", "-cancel", gid},
			knobs: orchKnobs("127.0.0.1:7500"), strays: gidStrays},
		{name: "cancel, not a gate id", args: []string{"gate", "cancel", "g1"}, code: 2},
		{name: "terminate", args: []string{"gate", "terminate", gid, "-reason", "stuck; laug gate stop follows"},
			how: inProcess, script: "gate", argv: []string{"-repo", "$ROOT", "-terminate", gid, "-reason",
				"stuck; laug gate stop follows"}, knobs: orchKnobs("127.0.0.1:7400"),
			strays: []string{"ORCH_ADDRESS", "REASON", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE", "WF"},
			out: "laug · gate terminate " + gid + ": workflow gate-" + gid + " ends at once, and no stop path runs; " +
				"then laug gate stop " + gid + " runs it by hand"},
		{name: "terminate -orchestrator", args: []string{"gate", "terminate", gid, "-reason", "x", "-orchestrator",
			"127.0.0.1:7500"}, how: inProcess, script: "gate",
			argv:   []string{"-repo", "$ROOT", "-terminate", gid, "-reason", "x"},
			knobs:  orchKnobs("127.0.0.1:7500"),
			strays: []string{"ORCH_ADDRESS", "REASON", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE", "WF"},
			out: "laug · gate terminate " + gid + ": workflow gate-" + gid + " ends at once, and no stop path runs; " +
				"then laug gate stop " + gid + " -orchestrator 127.0.0.1:7500 runs it by hand"},
		{name: "terminate, no reason", args: []string{"gate", "terminate", gid}, code: 2,
			errOut: "laug · gate terminate: names no reason: -reason says why, and history records it as " +
				"'laug: <why>'"},
		{name: "terminate, a quote in the reason", args: []string{"gate", "terminate", gid, "-reason", "it's"},
			code: 2, errOut: "laug · gate terminate: -reason 'it'\\''s': say why in letters, digits, blanks and " +
				",.;:()/_=-, from a letter or digit"},
		{name: "stop", args: []string{"gate", "stop", gid}, how: inProcess, script: "gate",
			argv:  []string{"-repo", "$ROOT", "-release", gid},
			knobs: runKnobs("127.0.0.1:7400", "1.31.0", "gate/plan.yaml", "", "main", "gate/results"),
			strays: []string{"BASE", "GATE", "GATE_FAKE", "GATE_POST", "GATE_RESULTS", "ITEMS", "OMES_DIR",
				"ORCH_ADDRESS", "PLAN", "SERVER_TAG", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE"},
			out: "laug · gate stop " + gid + ": workflow gate-" + gid + "'s stop path by hand: its live step stopped, " +
				"then stopped, post and release, the run in gate/results",
			errOut: "laug · gate stop: SERVER_TAG=9.9.9 in the environment is not passed on; laug sets it"},
		{name: "stop -results", args: []string{"gate", "stop", gid, "-results", "results-x"}, how: inProcess,
			script: "gate", argv: []string{"-repo", "$ROOT", "-release", gid},
			knobs: runKnobs("127.0.0.1:7400", "1.31.0", "gate/plan.yaml", "", "main", "results-x"),
			strays: []string{"BASE", "GATE", "GATE_FAKE", "GATE_POST", "GATE_RESULTS", "ITEMS", "OMES_DIR",
				"ORCH_ADDRESS", "PLAN", "SERVER_TAG", "TEMPORAL_ADDRESS", "TEMPORAL_NAMESPACE"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.more = append(append(slices.Clone(gateStrays), orchStrays...), c.more...)
			a, rec := gateApp(t, c)
			code, out, errOut := runApp(t, a, c.args...)
			checkRow(t, c, rec, code, out, errOut)
		})
	}

	// The starter's exit is the verb's: the gate's 0, 1 or 2, or a refusal's 2.
	t.Run("the role's exit", func(t *testing.T) {
		for _, args := range [][]string{{"gate", "run", "1.31.0", "-workflow"}, {"gate", "status", gid},
			{"gate", "cancel", gid}, {"gate", "terminate", gid, "-reason", "x"}, {"gate", "stop", gid}} {
			for _, want := range []int{0, 1, 2} {
				a, rec := gateApp(t, envCase{})
				rec.role = func(string, []string, []string, io.Writer, io.Writer) int { return want }
				if code, _, errOut := runApp(t, a, args...); code != want || len(rec.calls) != 1 {
					t.Errorf("%q, the role exits %d: exit %d, %d calls\n%s", args, want, code, len(rec.calls), errOut)
				}
			}
		}
	})
	t.Run("outside a checkout", func(t *testing.T) {
		for _, args := range [][]string{{"gate", "run", "1.31.0", "-workflow"}, {"gate", "status", gid},
			{"gate", "cancel", gid}, {"gate", "terminate", gid, "-reason", "x"}, {"gate", "stop", gid}} {
			rec := &recorder{}
			code, _, errOut := runApp(t, &App{Runner: rec, Roles: rec}, args...)
			if code != 2 || len(rec.calls) != 0 || !strings.Contains(errOut, "so it needs a laug checkout") {
				t.Errorf("%q: exit %d, %d calls\n%s", args, code, len(rec.calls), errOut)
			}
		}
	})
	t.Run("run -workflow -fake", func(t *testing.T) {
		a, rec := gateApp(t, envCase{})
		a.Environ = append([]string{"TMPDIR=" + t.TempDir()}, a.Environ...)
		code, out, errOut := runApp(t, a, "gate", "run", "1.31.0", "-fake", "-workflow")
		if code != 0 || len(rec.calls) != 1 || rec.calls[0].Script != "gate" || rec.hows[0] != inProcess {
			t.Fatalf("exit %d, %d calls\n%s%s", code, len(rec.calls), out, errOut)
		}
		if fake := envOf(rec.envs[0], "FAKE_GATE_DIR"); fake == "" || envOf(rec.envs[0], "GATE_FAKE") != "1" {
			t.Errorf("FAKE_GATE_DIR %q, GATE_FAKE %q", fake, envOf(rec.envs[0], "GATE_FAKE"))
		}
		if !strings.HasPrefix(out, "laug · gate run 1.31.0 -fake: ") || !strings.Contains(out, "; workflow Gate on "+
			"this checkout's worker through 127.0.0.1:7400; Ctrl-C detaches") {
			t.Errorf("first line: %s", out)
		}
	})
	// Bare, each names the newest run and the line to type, which for terminate ends with the -reason it requires.
	t.Run("bare", func(t *testing.T) {
		for _, v := range []string{"status", "cancel", "terminate", "stop"} {
			a, rec := gateApp(t, envCase{})
			writeFile(t, filepath.Join(a.Root, "gate", "results", gid, "verdict.json"), `{"verdict": "BLOCK"}`)
			code, out, errOut := runApp(t, a, "gate", v)
			line := "laug gate " + v + " " + gid
			if v == "terminate" {
				line += " -reason <why>"
			}
			if code != 2 || len(rec.calls) != 0 || out != "" || !strings.HasPrefix(errOut, "laug · gate "+v+": names "+
				"no gate run: give it a gate id, as gate run -workflow printed it; workflow gate-<gate id> is the "+
				"gate\nthe newest run in gate/results is "+gid+" (BLOCK):\n  "+line+"\n") {
				t.Errorf("bare %s: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", v, code, len(rec.calls), out, errOut)
			}
		}
	})
}
