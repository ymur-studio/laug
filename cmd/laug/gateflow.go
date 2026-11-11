package main

// gate status, cancel, terminate and stop: the verbs on a gate that runs as workflow Gate, gate-<gate id>, on the dev
// server laug orchestrator server runs. Each runs the starter's role, internal/orchestrator's StarterMain, in laug's
// own process, over the SDK's client: -status, -cancel, -terminate and -release. gate run -workflow starts such a gate
// with the same role (gateWorkflow).

import (
	"context"
	"fmt"
	"regexp"

	"github.com/ymur-studio/laug/internal/script"
)

// workflowGate parses the arguments of status, cancel, terminate and stop: one gate id, as gate run -workflow printed
// it. Bare, it names the newest run in results, read once the flags are, with the line to type, tail at its end,
// exit 2. With ok false the verb returns code.
func (a *App) workflowGate(f *flags, args []string, results *string, tail string) (gid string, code int, ok bool) {
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return "", code, false
	}
	switch {
	case len(pos) == 0:
		return "", a.noRun(f, *results, "give it a gate id, as gate run -workflow printed it; workflow gate-<gate id> "+
			"is the gate", true, tail), false
	case len(pos) > 1:
		return "", a.usageError(f, "takes one gate id, got %q", pos), false
	}
	if err := checkGateID(pos[0]); err != nil {
		return "", a.refused(f.cmd, err), false
	}
	return pos[0], exitDone, true
}

// ptr is a pointer to s, for a verb with no -results to hand workflowGate the default.
func ptr(s string) *string { return &s }

// starter is the starter's role for the gate at addr: mode and its arguments after -repo, TEMPORAL_ADDRESS and
// TEMPORAL_NAMESPACE set, ORCH_ADDRESS and the rest cleared.
func (a *App) starter(addr string, clear []string, mode ...string) role {
	return role{name: "gate", args: append(a.repo(), mode...), set: orchEnv(addr), clear: clear}
}

func runGateStatus(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("gate status", "<gate id> [-orchestrator "+orchAddressDefault+"]")
	addr := orchFlag(f)
	gid, code, ok := a.workflowGate(f, args, ptr(gateResultsDefault), "")
	if !ok {
		return code
	}
	if err := checkOrch(*addr); err != nil {
		return a.refused("gate status", err)
	}
	if !a.inCheckout("gate status", "the starter in this checkout") {
		return exitUsage
	}
	// The starter's first line is a laug · line of its own: the workflow's status, started and closed in UTC.
	return a.runRole(ctx, "gate status", a.starter(*addr, []string{"ORCH_ADDRESS", "GATE"}, "-status", gid),
		a.Environ, "")
}

func runGateCancel(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("gate cancel", "<gate id> [-orchestrator "+orchAddressDefault+"]")
	addr := orchFlag(f)
	gid, code, ok := a.workflowGate(f, args, ptr(gateResultsDefault), "")
	if !ok {
		return code
	}
	if err := checkOrch(*addr); err != nil {
		return a.refused("gate cancel", err)
	}
	if !a.inCheckout("gate cancel", "the starter in this checkout") {
		return exitUsage
	}
	// The starter asks for the workflow's status first, since a cancel request on an ended workflow changes nothing,
	// and prints the first line itself once it runs.
	return a.runRole(ctx, "gate cancel", a.starter(*addr, []string{"ORCH_ADDRESS", "GATE"}, "-cancel", gid),
		a.Environ, "")
}

// reasonRE is -reason's shape: letters, digits, blanks and ,.;:()/_=-, from a letter or digit. History records it.
var reasonRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ,.;:()/_=-]*$`)

func runGateTerminate(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("gate terminate", "<gate id> -reason <why> [-orchestrator "+orchAddressDefault+"]")
	reason := f.String("reason", "", "`why`, in letters, digits, blanks and ,.;:()/_=-; history records it as "+
		"'laug: <why>'")
	addr := orchFlag(f)
	gid, code, ok := a.workflowGate(f, args, ptr(gateResultsDefault), " -reason <why>")
	if !ok {
		return code
	}
	if *reason == "" {
		// The CLI's own reason named the user (CLI 1.9.1), so laug asks for one.
		return a.usageError(f, "names no reason: -reason says why, and history records it as 'laug: <why>'")
	}
	err := checkOrch(*addr)
	if err == nil && !reasonRE.MatchString(*reason) {
		err = fmt.Errorf("-reason %s: say why in letters, digits, blanks and ,.;:()/_=-, from a letter or digit",
			shellWord(*reason))
	}
	if err != nil {
		return a.refused("gate terminate", err)
	}
	if !a.inCheckout("gate terminate", "the starter in this checkout") {
		return exitUsage
	}
	first := fmt.Sprintf("laug · gate terminate %s: workflow gate-%s ends at once, and no stop path runs; then %s "+
		"runs it by hand", gid, gid, orchLine(a.self()+" gate stop "+gid, *addr))
	return a.runRole(ctx, "gate terminate", a.starter(*addr, []string{"ORCH_ADDRESS", "WF", "REASON"}, "-terminate",
		gid, "-reason", *reason), a.Environ, first)
}

func runGateStop(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("gate stop", "<gate id> [-results gate/results] [-orchestrator "+orchAddressDefault+"]")
	results := resultsFlag(f)
	addr := orchFlag(f)
	gid, code, ok := a.workflowGate(f, args, results, "")
	if !ok {
		return code
	}
	if err := checkOrch(*addr); err != nil {
		return a.refused("gate stop", err)
	}
	if !a.inCheckout("gate stop", "gate/step.sh") {
		return exitUsage
	}
	// The defaults for the rest of gate run's environment: the starter takes the gate's own server tag, pool and
	// fake from its gate.json, and FAKE_GATE_DIR from the environment, a seam.
	r := a.starter(*addr, []string{"ORCH_ADDRESS", "GATE"}, "-release", gid)
	r.set = append(r.set, gateEnv(serverTagDefault, planDefault, "", baseDefault, "", gatePostDefault, *results,
		omesDir)...)
	r.by = setByLaug("SERVER_TAG", "PLAN", "ITEMS", "BASE", "GATE_FAKE", "GATE_POST", "OMES_DIR")
	first := fmt.Sprintf("laug · gate stop %s: workflow gate-%s's stop path by hand: its live step stopped, then "+
		"stopped, post and release, the run in %s", gid, gid, *results)
	return a.runRole(ctx, "gate stop", r, a.Environ, first)
}

// gateWorkflow runs gate run -workflow's gate with the starter's role: orchEnv, then gate run's environment, set, which
// the starter carries as the workflow's input, its strays named by by; environ is the caller's, FAKE_GATE_DIR in front
// under -fake; otlp, when not empty, traces the start.
func (a *App) gateWorkflow(ctx context.Context, addr, otlp string, set []script.Var, by map[string]string,
	environ []string, first string) int {
	r := a.starter(addr, []string{"ORCH_ADDRESS", "ORCH_OTLP"})
	if otlp != "" {
		r.args = append(r.args, "-otlp", otlp)
	}
	r.set, r.by = append(r.set, set...), by
	return a.runRole(ctx, "gate run", r, environ, first)
}
