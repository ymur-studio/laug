package main

// laug deps: the tools laug's scripts call, each with its version or MISSING. Bare, it exits 1 only when a tool the
// offline path calls is missing, so a host with no Docker or temporal CLI, a CI runner among them, passes. -offline
// checks what the offline path calls and no more; -lab adds what a gate on a lab host needs besides, and exits 1 on any
// check.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The sets deps checks: the scripts' tools; the offline path's alone; what a lease, a load and a raun on compose
// cells call; the scripts' tools and the lab's checks.
const (
	setDefault = "default"
	setOffline = "offline"
	setCell    = "cell"
	setLab     = "lab"
)

// depsCheck is one line of deps. offline marks what the offline path needs, so -offline runs it; cell what a walk on
// compose cells calls, so -cell runs it; lab marks a check that neither bare deps nor -offline runs.
type depsCheck struct {
	name               string
	offline, cell, lab bool
	run                func(ctx context.Context, a *App, set string) depsResult
}

// depsResult is one checked line, and one element of -json's checks.
type depsResult struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"`  // ok, missing or note; a note never fails deps
	Version *string `json:"version"` // what the tool says it is; null when absent or unread
	Need    *string `json:"need"`    // the least version laug takes; null when any will do
	Why     string  `json:"why"`     // which scripts call it, or what is wrong
}

// depsReport is what -json prints. MissingOffline counts the misses among the checks -offline runs, each held to the
// offline path's need (go to go.mod's line, not Omes's): bare and -offline exit 1 on those alone, -lab on any.
type depsReport struct {
	Set            string       `json:"set"`
	Missing        int          `json:"missing"`
	MissingOffline int          `json:"missing_offline"`
	Checks         []depsResult `json:"checks"`
}

// depsChecks are the checks, in order: go and git, which build bin/laug and stamp it, then bin/laug; docker compose,
// temporal, jq, yq, bc, curl, ss, setsid and flock; env; cmp and diff; /dev/kvm; then the lab's. bc and ss are the
// offline path's for make harness-test, part of runbook 1's make test, which stops without either
// (load/testdata/harness-test.sh). It converts durations with bc through load/lost-work.sh and reads the ports its
// pools take with ss; gate run -fake itself runs on the fake ss. -cell leaves out what only a gate or the Firecracker
// backend calls, flock and env's --default-signal, /dev/kvm, and the host's size and type, which only a gate holds a
// host to: faults/lib.sh waits on ss, faults/run.sh starts its load under setsid, and cell fi-template-check compares
// with cmp and diff.
var depsChecks = []depsCheck{
	{"go", true, true, false, checkGo},
	{"git", true, true, false, checkGit},
	{"bin/laug", true, true, false, checkBinary},
	{"docker compose", false, true, false, checkCompose},
	{"temporal", false, true, false, checkTemporal},
	{"jq", true, true, false, checkJQ},
	{"yq", true, true, false, checkYQ},
	{"bc", true, true, false, presence("bc", "", "load/lost-work.sh counts lost work with it, for a load's and a "+
		"fault's verdict, and make harness-test stops without it")},
	{"curl", true, true, false, presence("curl", "7.76.0", "gate/run.sh and gate/lib.sh lease and release a gate's "+
		"cells with --fail-with-body, new in 7.76.0")},
	{"ss", true, true, false, presence("ss", "", "make harness-test reads the ports its pools take with it, and "+
		"gate/run.sh's preflight the ports a gate takes")},
	{"setsid", true, true, false, presence("setsid", "", "gate/run.sh starts the gate's pool, and gate/lib.sh each "+
		"item, in a session of its own")},
	{"flock", true, false, false, presence("flock", "", "gate/run.sh holds the gate's two locks with it")},
	{"env", true, false, false, checkEnv},
	{"cmp, diff", true, true, false, checkDiffutils},
	{"/dev/kvm", false, false, false, checkKVM},
	{"docker engine", false, true, true, checkEngine},
	{"omes", false, true, true, checkOmes},
	{"this host", false, false, true, checkHostSize},
	{"host type", false, false, true, checkHostType},
}

// offlineLeftOut says why -offline checks no more: what gate run -fake fakes, and what nothing offline calls.
const offlineLeftOut = "docker, temporal and /dev/kvm: gate run -fake puts stand-ins for docker, ss, nproc, free and " +
	"Omes's go build first on PATH (load/testdata/gate/fakebin), and nothing offline calls temporal or /dev/kvm"

func runDeps(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("deps", "[-offline | -cell | -lab] [-json]")
	offline := f.Bool("offline", false, "check only what the offline path calls: make build, thresholds, gate run "+
		"-fake, gate rejudge and make harness-test")
	cell := f.Bool("cell", false, "check what a lease, a load and a raun on compose cells call, the docker daemon "+
		"and Omes among them. Not the host's size or type. Exit 1 on any check missing")
	lab := f.Bool("lab", false, "check the docker daemon, Omes in .upstream/omes, and this host's size and declared "+
		"type besides, as a gate on a lab host needs them. Exit 1 on any check missing")
	jsonOut := f.Bool("json", false, "print one JSON object: set, missing, missing_offline and checks, each with "+
		"name, status, version, need and why")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	set, sets := setDefault, 0
	for _, s := range []struct {
		on   bool
		name string
	}{{*offline, setOffline}, {*cell, setCell}, {*lab, setLab}} {
		if s.on {
			set, sets = s.name, sets+1
		}
	}
	if sets > 1 {
		return a.usageError(f, "-offline, -cell and -lab: one set at a time")
	}
	r := depsReport{Set: set, Checks: []depsResult{}}
	nOffline := 0
	for _, c := range depsChecks {
		if set == setOffline && !c.offline || set == setCell && !c.cell || c.lab && set != setLab && set != setCell {
			continue
		}
		res := c.run(ctx, a, set)
		res.Name = c.name
		if c.offline {
			nOffline++
		}
		if res.Status == "missing" {
			r.Missing++
			// A miss counts for the offline path when -offline would miss it too: go held to go.mod's line.
			if c.offline && (set == setOffline || c.run(ctx, a, setOffline).Status == "missing") {
				r.MissingOffline++
			}
		}
		r.Checks = append(r.Checks, res)
	}
	exit := exitDone
	if r.MissingOffline > 0 || (set == setCell || set == setLab) && r.Missing > 0 {
		exit = exitFailed
	}
	if *jsonOut {
		if err := writeJSON(a.Stdout, r); err != nil {
			fmt.Fprintf(a.Stderr, "laug · deps: writing JSON: %v\n", a.errShown(err))
			return exitFailed
		}
		return exit
	}
	switch set {
	case setDefault:
		fmt.Fprintf(a.Stdout, "laug · deps: %d of %d missing, %d of the offline path's %d: the tools laug's scripts "+
			"call\n", r.Missing, len(r.Checks), r.MissingOffline, nOffline)
	case setOffline:
		fmt.Fprintf(a.Stdout, "laug · deps -offline: %d of %d missing: what make build, thresholds, gate run -fake, "+
			"gate rejudge and make harness-test call\n", r.Missing, len(r.Checks))
	case setCell:
		fmt.Fprintf(a.Stdout, "laug · deps -cell: %d of %d missing: what a lease, a load and a raun on compose cells "+
			"call, the docker daemon and Omes among them\n", r.Missing, len(r.Checks))
	case setLab:
		fmt.Fprintf(a.Stdout, "laug · deps -lab: %d of %d missing: the tools laug's scripts call, the docker daemon, "+
			"Omes and this host\n", r.Missing, len(r.Checks))
	}
	// The version column is as wide as its longest value, 8 at least: a stamp with -dirty is 13.
	versions, width := make([]string, len(r.Checks)), 8
	for i, c := range r.Checks {
		versions[i] = "-"
		if c.Version != nil {
			versions[i] = *c.Version
		}
		width = max(width, utf8.RuneCountInString(versions[i]))
	}
	for i, c := range r.Checks {
		status := c.Status
		if status == "missing" {
			status = "MISSING"
		}
		fmt.Fprintf(a.Stdout, "  %-7s  %-14s  %-*s  %s\n", status, c.Name, width, versions[i], c.Why)
	}
	switch set {
	case setDefault:
		fmt.Fprintln(a.Stdout, "  exit 1 only for the offline path's tools, -offline's list; -cell and -lab exit 1 on "+
			"any of theirs")
	case setOffline:
		fmt.Fprintln(a.Stdout, "  left out: "+offlineLeftOut)
	}
	return exit
}

// probeTimeout bounds each tool deps asks its version.
const probeTimeout = 20 * time.Second

// probe runs path with args, with the App's environment, from / so that GOTOOLCHAIN switches nothing for go env. It
// returns stdout, and an error that carries the first line of stderr.
func (a *App) probe(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = "/", firstWins(a.Environ), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if line := headLine(strings.TrimSpace(stderr.String())); line != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, line)
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

// headLine is s up to its first newline.
func headLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// versionRE finds a version in a tool's --version line: the first run of dotted numbers.
var versionRE = regexp.MustCompile(`[0-9]+(\.[0-9]+)+`)

// versionOf is the first version in s's first line, nil when there is none.
func versionOf(s string) *string {
	if v := versionRE.FindString(headLine(s)); v != "" {
		return &v
	}
	return nil
}

// atLeast reports whether version have is need or later, as sort -V orders the two: dotted numbers compared in
// turn, a missing one read as 0.
func atLeast(have, need string) bool {
	h, n := strings.Split(have, "."), strings.Split(need, ".")
	for i := 0; i < len(h) || i < len(n); i++ {
		var x, y int
		if i < len(h) {
			x, _ = strconv.Atoi(h[i])
		}
		if i < len(n) {
			y, _ = strconv.Atoi(n[i])
		}
		if x != y {
			return x > y
		}
	}
	return true
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func depsOK(version *string, need, why string) depsResult {
	return depsResult{Status: "ok", Version: version, Need: optional(need), Why: why}
}

func depsMissing(version *string, need, why string) depsResult {
	return depsResult{Status: "missing", Version: version, Need: optional(need), Why: why}
}

// presence is a check that tool is on PATH, and at need or later when need is not empty, with the version its
// --version prints.
func presence(tool, need, why string) func(context.Context, *App, string) depsResult {
	return func(ctx context.Context, a *App, _ string) depsResult {
		path := a.lookPath(tool)
		if path == "" {
			return depsMissing(nil, need, "not on PATH; "+why)
		}
		out, _ := a.probe(ctx, path, "--version")
		v := versionOf(out)
		switch {
		case need != "" && v == nil:
			return depsMissing(v, need, "its --version gives no version; "+why)
		case need != "" && !atLeast(*v, need):
			return depsMissing(v, need, why)
		}
		return depsOK(v, need, why)
	}
}

// checkGit is git's row: make build, the gate's scripts and upstream run it.
func checkGit(ctx context.Context, a *App, set string) depsResult {
	return presence("git", "", "make build stamps bin/laug with HEAD; gate/lib.sh and "+a.self()+" gate rejudge read "+
		"HEAD; "+a.self()+" upstream clones Omes")(ctx, a, set)
}

// checkGo holds the installed Go to what builds laug and Omes: go env GOVERSION and GOTOOLCHAIN, read from /. An older
// Go under GOTOOLCHAIN=auto downloads the newer one on the first build, a note. The offline path builds no Omes, so
// -offline holds Go to go.mod's line alone.
func checkGo(ctx context.Context, a *App, set string) depsResult {
	need, why := goModGo, "make build builds bin/laug with it, go.mod's "+goModGo
	if set != setOffline {
		need = omesGo
		if atLeast(goModGo, omesGo) {
			need = goModGo
		}
		why = "bin/laug needs " + goModGo + " (go.mod) and Omes " + omesGo + " (its go.mod at " + short(omesRef) + ")"
	}
	path := a.lookPath("go")
	if path == "" {
		return depsMissing(nil, need, "not on PATH; "+why)
	}
	out, err := a.probe(ctx, path, "env", "GOVERSION", "GOTOOLCHAIN")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	v := versionOf(lines[0])
	if err != nil || v == nil || len(lines) < 2 {
		return depsMissing(v, need, fmt.Sprintf("go env GOVERSION GOTOOLCHAIN did not answer (%v); %s", err, why))
	}
	tc := strings.TrimSpace(lines[1])
	switch {
	case atLeast(*v, need):
		return depsOK(v, need, why)
	case tc == "auto" || strings.HasSuffix(tc, "+auto"):
		return depsResult{Status: "note", Version: v, Need: optional(need), Why: why + "; GOTOOLCHAIN=" + tc +
			" downloads it on the first build"}
	}
	return depsMissing(v, need, why+"; GOTOOLCHAIN="+tc+" downloads none")
}

// checkBinary holds the checkout's bin/laug to HEAD as gate run does (checkStamp): a bin/laug built from another
// commit is what a reader who pulled and did not build has, and then gate run -fake refuses and gate rejudge judges
// nothing. The version is its stamp, the need HEAD's commit. Outside a checkout there is no bin/laug to hold, a note.
func checkBinary(ctx context.Context, a *App, _ string) depsResult {
	if a.Root == "" {
		return depsResult{Status: "note", Why: "not in a laug checkout, so no bin/laug to hold to HEAD"}
	}
	refuse, warn := a.checkStamp(ctx)
	var version, need *string
	if stamp, err := binaryStamp(filepath.Join(a.Root, "bin", "laug")); err == nil && stamp != "" {
		version = optional(short(stamp))
	}
	if head, err := a.git(ctx, "rev-parse", "HEAD"); err == nil && head != "" {
		need = optional(short(head))
	}
	r := depsResult{Status: "ok", Version: version, Need: need, Why: "built from HEAD, as gate run -fake and gate " +
		"rejudge need it"}
	switch {
	case refuse != "":
		r.Status, r.Why = "missing", refuse+"; until then gate run -fake refuses it, and gate rejudge judges nothing"
	case warn != "":
		r.Status, r.Why = "note", warn
	}
	return r
}

func checkCompose(ctx context.Context, a *App, _ string) depsResult {
	why := "cells/backend-compose.sh and gate/run.sh run every cell under docker compose, v2 or later"
	docker := a.lookPath("docker")
	if docker == "" {
		return depsMissing(nil, "2", "no docker on PATH; "+why)
	}
	out, err := a.probe(ctx, docker, "compose", "version", "--short")
	if err != nil {
		return depsMissing(nil, "2", "docker without the compose plugin, v2 or later: the scripts call docker compose, "+
			"not docker-compose")
	}
	return depsOK(versionOf(out), "2", why)
}

func checkTemporal(ctx context.Context, a *App, _ string) depsResult {
	why := "load/sweep.sh, load/run-omes.sh and cells/backend-compose.sh run it, and " + a.self() +
		" orchestrator server its server start-dev"
	path := a.lookPath("temporal")
	if path == "" {
		return depsMissing(nil, "", "not on PATH; "+why)
	}
	out, err := a.probe(ctx, path, "--version")
	v := versionOf(out)
	help, herr := a.probe(ctx, path, "server", "start-dev", "--help")
	if err != nil || herr != nil || !strings.Contains(help, "--metrics-port") {
		return depsMissing(v, "", "a temporal CLI whose server start-dev takes --metrics-port, for "+a.self()+
			" orchestrator server")
	}
	return depsOK(v, "", why+"; start-dev takes the ports it pins")
}

// checkJQ: every gate script and load/thresholds.sh call jq, 1.7 or later.
func checkJQ(ctx context.Context, a *App, _ string) depsResult {
	why := "every gate script and load/thresholds.sh call it"
	path := a.lookPath("jq")
	if path == "" {
		return depsMissing(nil, "1.7", "not on PATH; 1.7 or later: "+why)
	}
	out, _ := a.probe(ctx, path, "--version")
	v := versionOf(out)
	switch {
	case v == nil:
		return depsMissing(v, "1.7", "its --version gives no version; "+why)
	case !atLeast(*v, "1.7"):
		return depsMissing(v, "1.7", "1.7 or later: "+why)
	}
	return depsOK(v, "1.7", "1.7 or later: "+why)
}

// yqV4 is what mikefarah/yq v4 says to --version.
var yqV4 = regexp.MustCompile(`mikefarah.* v4\.`)

func checkYQ(ctx context.Context, a *App, _ string) depsResult {
	why := "load/thresholds.sh and gate/lib.sh read the threshold sets and the plan with it"
	path := a.lookPath("yq")
	if path == "" {
		return depsMissing(nil, "4", "not on PATH; mikefarah/yq v4: "+why)
	}
	out, _ := a.probe(ctx, path, "--version")
	if !yqV4.MatchString(out) {
		return depsMissing(versionOf(out), "4", fmt.Sprintf("mikefarah/yq v4, and the yq on PATH says %q: %s",
			headLine(strings.TrimSpace(out)), why))
	}
	return depsOK(versionOf(out), "4", "mikefarah/yq v4: "+why)
}

// checkEnv: gate/lib.sh starts a gate's harness and items under env --default-signal (sigdefault), GNU coreutils
// 8.31 or later; the version is coreutils'. An env that takes the option prints its version after it; one that does
// not refuses it first.
func checkEnv(ctx context.Context, a *App, _ string) depsResult {
	why := "gate/lib.sh starts a gate's harness and items under env --default-signal, new in GNU coreutils 8.31"
	path := a.lookPath("env")
	if path == "" {
		return depsMissing(nil, "8.31", "not on PATH; "+why)
	}
	out, err := a.probe(ctx, path, "--default-signal=INT,QUIT", "--version")
	if err != nil {
		out, _ = a.probe(ctx, path, "--version")
		return depsMissing(versionOf(out), "8.31", "takes no --default-signal: "+why)
	}
	return depsOK(versionOf(out), "8.31", why)
}

// checkDiffutils: make harness-test compares files with cmp, byte for byte, and shows a difference with diff, and so
// does cell fi-template-check (cells/fi-template-check.sh).
func checkDiffutils(ctx context.Context, a *App, _ string) depsResult {
	why := "GNU diffutils: make harness-test and " + a.self() + " cell fi-template-check compare files with cmp and " +
		"show a difference with diff"
	cmp, diff := a.lookPath("cmp"), a.lookPath("diff")
	switch {
	case cmp == "" && diff == "":
		return depsMissing(nil, "", "neither on PATH; "+why)
	case cmp == "":
		return depsMissing(nil, "", "no cmp on PATH; "+why)
	case diff == "":
		return depsMissing(nil, "", "no diff on PATH; "+why)
	}
	out, _ := a.probe(ctx, cmp, "--version")
	return depsOK(versionOf(out), "", why)
}

// kvmPath is the device the Firecracker backend boots microVMs with. Tests replace it.
var kvmPath = "/dev/kvm"

// checkKVM is a note: compose cells need no /dev/kvm.
func checkKVM(_ context.Context, _ *App, _ string) depsResult {
	if _, err := os.Stat(kvmPath); err != nil {
		return depsResult{Status: "note", Why: "absent: no Firecracker cells on this host; compose cells need none"}
	}
	return depsOK(nil, "", "the Firecracker backend boots its microVMs with it")
}

// checkEngine: a gate's preflight lists compose projects and images, which needs the daemon, not only the CLI.
func checkEngine(ctx context.Context, a *App, _ string) depsResult {
	why := "the daemon answers: a gate's preflight lists compose projects and images, and the cells run on it"
	docker := a.lookPath("docker")
	if docker == "" {
		return depsMissing(nil, "", "no docker on PATH, so no daemon to ask")
	}
	out, err := a.probe(ctx, docker, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return depsMissing(nil, "", fmt.Sprintf("no daemon answered docker version: %v", err))
	}
	return depsOK(versionOf(out), "", why)
}

// checkOmes: load, fault, raun, candidate and gate run build Omes from .upstream/omes (load/run-omes.sh's OMES_DIR,
// gate/run.sh's preflight), at the commit every committed result used.
func checkOmes(_ context.Context, a *App, _ string) depsResult {
	need := short(omesRef)
	if a.Root == "" {
		return depsMissing(nil, need, "not in a laug checkout, so no "+omesDir+" to read")
	}
	sha, present, err := omesCommit(a.Root)
	switch {
	case !present:
		return depsMissing(nil, need, "no clone in "+omesDir+": "+a.self()+" upstream clones it")
	case err != nil:
		return depsMissing(nil, need, fmt.Sprintf("%s: HEAD not read: %v", omesDir, err))
	case sha != omesRef:
		v := short(sha)
		return depsMissing(&v, need, fmt.Sprintf("%s is at %s, not %s: %s upstream checks %s out", omesDir, v, need,
			a.self(), need))
	}
	v := short(sha)
	return depsOK(&v, need, "load, fault, raun, candidate and gate run build Omes from "+omesDir)
}

// The host a gate takes (gate/run.sh's preflight): 16 vCPU at least, and 8 GiB of memory available. numCPU is nproc's
// count and meminfoPath where free reads MemAvailable; tests replace both.
var (
	numCPU      = runtime.NumCPU
	meminfoPath = "/proc/meminfo"
)

const (
	gateVCPU     = 16
	gateAvailGiB = 8
)

func checkHostSize(_ context.Context, a *App, _ string) depsResult {
	need := fmt.Sprintf("%d vCPU, %d GiB available", gateVCPU, gateAvailGiB)
	avail, err := memAvailable()
	if err != nil {
		return depsMissing(nil, need, fmt.Sprintf("%s: %v", meminfoPath, err))
	}
	vcpu := numCPU()
	gib := math.Floor(float64(avail)/(1<<30)*10) / 10
	have := fmt.Sprintf("%d vCPU and %.1f GiB available; a gate refuses under %d vCPU or %d GiB (gate/run.sh)",
		vcpu, gib, gateVCPU, gateAvailGiB)
	if vcpu < gateVCPU || avail < gateAvailGiB<<30 {
		return depsMissing(nil, need, have)
	}
	return depsOK(nil, need, have)
}

// hostTypeScript prints what a gate checks of this host's type and size, laug host type's script.
const hostTypeScript = "gate/host-type.sh"

// checkHostType asks gate/host-type.sh what laug host type -json prints: the type this host declares, and whether a
// gate on cells passes the host as the one its threshold set names (gate/lib.sh, host_check). The version is the type
// declared, the need the set's. The script writes nothing when it is given no type.
func checkHostType(ctx context.Context, a *App, _ string) depsResult {
	if a.Root == "" {
		return depsMissing(nil, "", "not in a laug checkout, so no gate/host-type.sh to ask")
	}
	if !a.executable(hostTypeScript) {
		return depsMissing(nil, "", "no "+hostTypeScript+" in this checkout to ask")
	}
	b := *a
	b.Environ = append([]string{"JSON=1", selfVar + "=" + a.self()}, a.Environ...) // its reason names a line to type
	out, err := b.probe(ctx, filepath.Join(a.Root, hostTypeScript))
	var r struct {
		Declared *string `json:"declared"`
		Set      struct {
			Type string `json:"type"`
		} `json:"set"`
		Passes bool    `json:"passes"`
		Reason *string `json:"reason"`
	}
	if err == nil {
		err = json.Unmarshal([]byte(out), &r)
	}
	if err != nil {
		return depsMissing(nil, "", fmt.Sprintf("%s did not answer: %v; %s host type says more", hostTypeScript, err,
			a.self()))
	}
	need := optional(r.Set.Type)
	if !r.Passes {
		why := "a gate on cells refuses this host; " + a.self() + " host type says why"
		if r.Reason != nil {
			why = *r.Reason
		}
		return depsResult{Status: "missing", Version: r.Declared, Need: need, Why: why}
	}
	return depsResult{Status: "ok", Version: r.Declared, Need: need, Why: "a gate on cells passes this host as " +
		"load/steady-state.yaml's host, by the type it declares and its size"}
}

// memAvailable is MemAvailable in bytes, the available column free -b prints.
func memAvailable() (int64, error) {
	f, err := os.Open(meminfoPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, found := strings.CutPrefix(sc.Text(), "MemAvailable:"); found {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				return 0, err
			}
			return kb * 1024, nil
		}
	}
	return 0, errors.New("no MemAvailable line")
}
