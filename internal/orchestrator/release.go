package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// laug gate stop: the stop path from a shell, for a gate that no workflow will finish. A terminate, a lost
// db or a stuck workflow task runs no stop path, and leaves the live step, its items, laugd and the lock holder
// running. ReleaseByHand does what Gate's stop path does, through the same Stop and step runner: it stops each live
// step by its record, then runs stopped, post stopped and release stopped. The runner's rule 1 (steps.go, runStep)
// answers a step that already ran from its .done, so a second run changes nothing.

// stepOf reads a step's name back into its verb and key. class is "stop" for a step Stop may stop, "skip" for judge,
// post, release and stopped (the stop path's own, which Stop refuses), and "unknown" for any other name. Names are
// matched whole: a split on the first hyphen would read wave-time as verb wave, key time, which stepKey rejects, and
// leave a gate that ended in wave-time with that step running.
func stepOf(name string) (verb, key, class string) {
	switch name {
	case "open", "preflight", "omes", "laugd", "harness", "barrier", "wave-time", "wave":
		return name, "", "stop"
	case "judge":
		return "combine", "", "skip"
	case "post", "release", "stopped":
		return name, "", "skip"
	}
	if k, ok := strings.CutPrefix(name, "lease-"); ok && leaseRE.MatchString(k) {
		return "lease", k, "stop"
	}
	if k, ok := strings.CutPrefix(name, "quiet-"); ok && quietRE.MatchString(k) {
		return "quiet", k, "stop"
	}
	return "", "", "unknown"
}

// judgeWait bounds the wait for a live judge: its StartToClose in Gate.
const judgeWait = 2 * time.Minute

// ReleaseByHand runs the stop path for gate run gid in in.Results, as workflow gate-<gid> would, and writes what it
// did to out. exit is stopped's, or 2 when post failed or release did not answer done. in must carry the gate's own
// SERVER_TAG, GATE_POOL_LISTEN and GATE_FAKE (gate.json's), so release reaches the gate's own laugd. step.sh's
// environment is built from environ, nil for this process's.
func ReleaseByHand(ctx context.Context, repo string, in GateInput, gid string, out io.Writer, environ []string) (
	exit int, err error) {
	return releaseByHand(ctx, &GateActivities{Repo: repo, Environ: environ}, in, gid, out)
}

func releaseByHand(ctx context.Context, a *GateActivities, in GateInput, gid string, out io.Writer) (exit int, err error) {
	if !gidRE.MatchString(gid) {
		return 2, fmt.Errorf("gate id %q is not g<date>-<time>", gid)
	}
	rel := path.Join(in.Results, gid)
	dir := a.abs(rel)
	wfID := "gate-" + gid
	noBeat := func(StepHeartbeat) {}
	si := func(verb, key string) StepInput {
		return StepInput{GateInput: in, Dir: rel, GID: gid, Name: StepName(verb, key), Verb: verb, Key: key}
	}

	// 1. Each step with a .pid and no .done, by name. A live judge is waited for, judgeWait at most: stopped beside it
	// would write the trap verdict while bin/laug judge writes verdict.json, and the last writer would win. The
	// runner's rule 2 adopts it and answers when judge.done comes; stopped then keeps the judge's verdict.
	pids, _ := filepath.Glob(filepath.Join(dir, ".steps", "*.pid"))
	sort.Strings(pids)
	live := 0
	for _, p := range pids {
		name := strings.TrimSuffix(filepath.Base(p), ".pid")
		if fileExists(filepath.Join(dir, ".steps", name+".done")) {
			continue
		}
		verb, key, class := stepOf(name)
		switch class {
		case "skip":
			if name == "judge" {
				live++
				jctx, cancel := context.WithTimeout(ctx, judgeWait)
				j, err := a.runStep(jctx, si(verb, key), wfID, "", noBeat)
				cancel()
				if err != nil {
					fmt.Fprintf(out, "judge: %s\n", errText(err))
				} else {
					fmt.Fprintf(out, "judge: adopted, exit %d\n", j.Exit)
				}
			}
			continue
		case "unknown":
			fmt.Fprintf(out, "unknown step file %s.pid\n", name)
			continue
		}
		live++
		sr, err := a.stop(ctx, si(verb, key), noBeat, func(s string) { fmt.Fprintln(out, s) })
		if err != nil {
			fmt.Fprintf(out, "stop %s: %s\n", name, errText(err))
			continue
		}
		fmt.Fprintf(out, "stop %s: %s\n", name, stopText(sr))
	}
	if live == 0 {
		fmt.Fprintln(out, "no live step")
	}

	// 2. stopped, post stopped, release stopped, each a gate/step.sh in a session of its own, as Gate runs them.
	exit = 2
	if st, err := a.runStep(ctx, si("stopped", "released by hand: laug gate stop"), wfID, "", noBeat); err != nil {
		fmt.Fprintf(out, "stopped: %s\n", errText(err))
	} else {
		word := verdictOf(dataOf(st))
		if word == "" {
			word = "no verdict"
		}
		fmt.Fprintf(out, "stopped: %s, %s, exit %d, stopped_at %s\n", st.Result, word, st.Exit, verdictStoppedAt(dir))
		exit = st.Exit
	}
	if post, err := a.runStep(ctx, si("post", "stopped"), wfID, "", noBeat); err != nil {
		fmt.Fprintf(out, "post stopped: %s\n", errText(err))
		exit = 2
	} else {
		fmt.Fprintf(out, "post stopped: exit %d\n", post.Exit)
		if post.Exit != 0 {
			exit = 2
		}
	}
	if rs, err := a.runStep(ctx, si("release", "stopped"), wfID, "", noBeat); err != nil {
		fmt.Fprintf(out, "release stopped: %s\n", errText(err))
		exit = 2
	} else {
		var r struct {
			Laugd    string    `json:"laugd"`
			Released int       `json:"released"`
			Left     *[]string `json:"left"`
		}
		_ = json.Unmarshal(rs.Answer, &r)
		left := "unknown (docker compose ls failed)"
		if r.Left != nil {
			left = strings.Join(*r.Left, " ")
			if left == "" {
				left = "none"
			}
		}
		fmt.Fprintf(out, "release stopped: laugd %s, %d released, left %s\n", r.Laugd, r.Released, left)
		if rs.Result != "done" {
			exit = 2
		}
	}
	fmt.Fprintf(out, "laug · released gate %s by hand: exit %d\n", gid, exit)
	return exit, nil
}

// verdictStoppedAt is verdict.json's stopped_at: null for the judge's verdict, the step for the trap verdict. stopped's
// own answer names .state.json's step mark either way, so it is not what the verdict says.
func verdictStoppedAt(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil {
		return "unknown: no verdict.json"
	}
	var v struct {
		StoppedAt json.RawMessage `json:"stopped_at"`
	}
	if json.Unmarshal(b, &v) != nil || len(v.StoppedAt) == 0 {
		return "unknown: verdict.json has none"
	}
	var s string
	if json.Unmarshal(v.StoppedAt, &s) == nil {
		return s
	}
	return string(v.StoppedAt)
}

// ReleaseLine is the laug gate stop line that finds the gate an input started, gid, with flags (LaugFlags) last:
// -results when GATE_RESULTS is not laug's default, and FAKE_GATE_DIR in front for a fake gate (the fake docker's
// compose ls reads it; laug passes it on as a seam). Each value is one shell word.
func ReleaseLine(laug, gid string, in GateRunInput, flags string) string {
	s := ""
	if d := in.Extra["FAKE_GATE_DIR"]; in.Fake && d != "" {
		s = "FAKE_GATE_DIR=" + ShellWord(d) + " "
	}
	s += laug + " gate stop " + gid
	if in.Results != "" && in.Results != "gate/results" {
		s += " -results " + ShellWord(in.Results)
	}
	return s + flags
}

var plainWordRE = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]+$`)

// ShellWord is s as one shell word: as it is when it needs no quoting, else in single quotes.
func ShellWord(s string) string {
	if plainWordRE.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// stopText is a StopResult in one line: "<sent or nothing sent>, <done|no .done>[, exit <n>][: <why>]".
func stopText(sr StopResult) string {
	s := sr.Sent
	if s == "" {
		s = "nothing sent"
	}
	if sr.Done {
		s += ", done"
	} else {
		s += ", no .done"
	}
	if sr.Exit != nil {
		s += fmt.Sprintf(", exit %d", *sr.Exit)
	}
	if sr.Why != "" {
		s += ": " + sr.Why
	}
	return s
}
