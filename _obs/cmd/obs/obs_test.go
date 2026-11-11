package main

// The stack's verbs over _obs/obs.sh and _obs/follow. The env matrix runs each script verb against a recorder, with
// the bin/obs make build leaves as a stand-in in the checkout; purge reads an OBS_DIR made in a temp dir; follow runs
// in process on a loopback port, and answers -gate-running, -openmetrics and -decode; status and find run for real
// through the built binary in this checkout, with an OBS_DIR in a temp dir and a curl that never answers, so no test
// reads a stack, a store or a gate lock of this host; and _obs/obs.sh's part functions start and match the stack's
// cellstat and follow from a copy of the built binary, on ports of the kernel's choosing.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// obsStrays is a stray copy of each knob the obs verbs map beyond strayEnviron's FORCE.
var obsStrays = []string{"ONLY=stray", "OBS_CELLSTAT=1", "OBS_DIR=/stray", "GATE=g20260101-000000",
	"GATE_RESULTS=stray", "RUN=stray", "PACK=stray.tar.gz", "JSON=1", "OBS_FOLLOW_BUILD=stray"}

// obsCase is a row of the obs matrix: unbuilt leaves bin/obs out of the checkout.
type obsCase struct {
	envCase
	unbuilt bool
}

// obsApp is a row's App, as runMatrix's, with obsStrays, XDG_STATE_HOME /var/state, so OBS_DIR's default is
// /var/state/laug-obs, which a line prints as $XDG_STATE_HOME/laug-obs, and an executable at bin/obs, as make build
// leaves it, unless the row says not.
func obsApp(t *testing.T, c obsCase) (*App, *recorder) {
	t.Helper()
	c.more = append(append(slices.Clone(obsStrays), "XDG_STATE_HOME=/var/state"), c.more...)
	a, rec := matrixApp(t, c.envCase)
	if !c.unbuilt {
		writeExec(t, filepath.Join(a.Root, obsBin), "#!/bin/sh\n")
	}
	return a, rec
}

func TestObs(t *testing.T) {
	const dir = "/srv/obs"
	all := []string{"OBS_CELLSTAT", "OBS_DIR", "ONLY"}
	two := func(names ...string) []string { return names }
	verdict := `{"verdict":"ADVANCE"}`
	for _, c := range []obsCase{
		{envCase: envCase{name: "up", args: []string{"up", "-dir", dir}, how: script.Exec, script: obsScript,
			argv: []string{"up"}, knobs: []string{"OBS_DIR=" + dir}, strays: all,
			out: "laug · obs up: the stack's six parts on 127.0.0.1, ports 7710 to 7769, state in /srv/obs; the store " +
				"settles first, 20 min at most; bin/obs down stops it",
			errOut: "laug · obs up: ONLY=stray in the environment is not passed on; -only sets it"}},
		{envCase: envCase{name: "up -only -no-cellstat", args: []string{"up", "-only", "grafana", "-no-cellstat"},
			how: script.Exec, script: obsScript, argv: []string{"up"}, knobs: []string{"ONLY=grafana", "OBS_CELLSTAT=0"},
			strays: all, out: "laug · obs up: grafana alone, no cellstat on 127.0.0.1, ports 7710 to 7769, state in " +
				"$XDG_STATE_HOME/laug-obs; the store settles first, 20 min at most; bin/obs down stops it",
			errOut: "laug · obs up: OBS_CELLSTAT=1 in the environment is not passed on; -no-cellstat sets it"}},
		{envCase: envCase{name: "up -only a part it lacks", args: []string{"up", "-only", "store"}, code: 2,
			errOut: "laug · obs up: -only store: one of prometheus, jaeger, collector, cellstat, follow, grafana"}},
		{envCase: envCase{name: "up -only cellstat -no-cellstat", args: []string{"up", "-only", "cellstat",
			"-no-cellstat"}, code: 2, errOut: "laug · obs up: -only cellstat with -no-cellstat starts nothing"}},
		{envCase: envCase{name: "up -dir relative", args: []string{"up", "-dir", "state/obs"}, code: 2,
			errOut: "laug · obs up: -dir state/obs: an absolute path, outside every checkout"}},
		{envCase: envCase{name: "up, an argument", args: []string{"up", "grafana"}, code: 2,
			errOut: `laug · obs up: takes no arguments, got ["grafana"]; -only names a part`}},
		{envCase: envCase{name: "up, no bin/obs", args: []string{"up"}, code: 2,
			errOut: "laug · obs up: bin/obs, which _obs/obs.sh runs, is not built: make build builds it"},
			unbuilt: true},
		{envCase: envCase{name: "up -dry-run, no bin/obs", args: []string{"up", "-dry-run"}, strays: all,
			out: "_obs/obs.sh up"}, unbuilt: true},
		{envCase: envCase{name: "down", args: []string{"down", "-dir", dir}, how: script.Exec, script: obsScript,
			argv: []string{"down"}, knobs: []string{"OBS_DIR=" + dir, "OBS_FOLLOW_BUILD=ok"},
			strays: two("FORCE", "OBS_DIR", "OBS_FOLLOW_BUILD", "ONLY"),
			out: "laug · obs down: the stack's parts stop by their records, TERM then KILL after 30 s; the data stays " +
				"in /srv/obs; bin/obs up starts it again",
			errOut: "laug · obs down: FORCE=1 in the environment is not passed on; -while-gate-runs sets it"}},
		{envCase: envCase{name: "down -while-gate-runs -only", args: []string{"down", "-while-gate-runs",
			"-only", "follow"}, how: script.Exec, script: obsScript, argv: []string{"down"},
			knobs:  []string{"FORCE=1", "ONLY=follow", "OBS_FOLLOW_BUILD=ok"},
			strays: two("OBS_DIR", "OBS_FOLLOW_BUILD", "ONLY"),
			out: "laug · obs down: follow stops by its record, TERM then KILL after 30 s, even while a gate " +
				"runs; the data stays in $XDG_STATE_HOME/laug-obs; bin/obs up starts it again"}},
		{envCase: envCase{name: "down, no bin/obs", args: []string{"down"}, how: script.Exec,
			script: obsScript, argv: []string{"down"}, knobs: []string{"OBS_FOLLOW_BUILD=failed"},
			strays: two("FORCE", "OBS_DIR", "OBS_FOLLOW_BUILD", "ONLY"),
			errOut: "laug · obs down: bin/obs is not built, so _obs/obs.sh cannot ask whether a gate runs, and " +
				"counts one as running; -while-gate-runs stops the stack anyway, and make build builds it"},
			unbuilt: true},
		{envCase: envCase{name: "status", args: []string{"status"}, how: script.Exec, script: obsScript,
			argv: []string{"status"}, strays: two("JSON", "OBS_DIR"),
			errOut: "laug · obs status: JSON=1 in the environment is not passed on; -json sets it"}, unbuilt: true},
		{envCase: envCase{name: "status -dir", args: []string{"status", "-dir", dir}, how: script.Exec,
			script: obsScript, argv: []string{"status"}, knobs: []string{"OBS_DIR=" + dir},
			strays: two("JSON", "OBS_DIR")}},
		{envCase: envCase{name: "find <gate id>", args: []string{"find", "g20261017-171604"}, how: script.Exec,
			script: obsScript, argv: []string{"find"}, knobs: []string{"GATE=g20261017-171604"},
			strays: two("GATE", "GATE_RESULTS", "OBS_DIR", "RUN"),
			errOut: "laug · obs find: GATE=g20260101-000000 in the environment is not passed on; <gate id> sets it"},
			unbuilt: true},
		{envCase: envCase{name: "find -run-id", args: []string{"find", "-run-id", "base1-100551"},
			how: script.Exec, script: obsScript, argv: []string{"find"}, knobs: []string{"RUN=base1-100551"},
			strays: two("GATE", "GATE_RESULTS", "OBS_DIR", "RUN")}},
		{envCase: envCase{name: "find, the newest gate run", args: []string{"find", "-results", "r"},
			files: map[string]string{"r/g20261017-171604/verdict.json": verdict,
				"r/g20261018-092540/verdict.json": verdict, "r/g20261018-092540-2/x": "", "r/notes/x": ""},
			how: script.Exec, script: obsScript, argv: []string{"find"},
			knobs: []string{"GATE=g20261018-092540-2", "GATE_RESULTS=r"}, strays: two("GATE", "GATE_RESULTS", "OBS_DIR",
				"RUN"), errOut: "laug · obs find: names no gate or run, so the newest gate run in r, g20261018-092540-2"}},
		{envCase: envCase{name: "find, no gate run", args: []string{"find"}, code: 1,
			errOut: "laug · obs find: names no gate or run, and gate/results holds no gate run: bin/obs find " +
				"<gate id>, or -run-id <run id>"}},
		{envCase: envCase{name: "find, not a gate id", args: []string{"find", "g2026"}, code: 2,
			errOut: "laug · obs find: g2026: not a gate id, g<UTC date>-<UTC time>, as gate run names one"}},
		{envCase: envCase{name: "find, a gate id and -run-id", args: []string{"find", "g20261017-171604",
			"-run-id", "r1"}, code: 2, errOut: "laug · obs find: takes a gate id or -run-id, not both"}},
		{envCase: envCase{name: "find -run-id -results", args: []string{"find", "-run-id", "r1", "-results",
			"r"}, code: 2, errOut: "laug · obs find: -results is where a gate run is; -run-id reads load/results"}},
		{envCase: envCase{name: "find -run-id, two words", args: []string{"find", "-run-id", "a b"}, code: 2,
			errOut: "laug · obs find: -run-id 'a b': not one word of letters, digits, ., _ and -, from a letter or " +
				"digit"}},
		{envCase: envCase{name: "backfill", args: []string{"backfill", "-dir", dir}, how: script.Exec,
			script: obsScript, argv: []string{"backfill"}, knobs: []string{"OBS_DIR=" + dir},
			strays: two("GATE", "GATE_RESULTS", "OBS_DIR"),
			out: "laug · obs backfill: each gate run in gate/results over 3 h old that the store lacks, rendered, " +
				"checked by promtool, moved into the store in /srv/obs, then read back against its files; Ctrl-C " +
				"stops it"}},
		{envCase: envCase{name: "backfill <gate id> -results", args: []string{"backfill", "g20261017-171604",
			"-results", "r"}, how: script.Exec, script: obsScript, argv: []string{"backfill"},
			knobs: []string{"GATE=g20261017-171604", "GATE_RESULTS=r"}, strays: two("GATE", "GATE_RESULTS", "OBS_DIR"),
			out: "laug · obs backfill: gate g20261017-171604 of r, rendered, checked by promtool, moved into the " +
				"store in $XDG_STATE_HOME/laug-obs, then read back against its files; Ctrl-C stops it"}},
		{envCase: envCase{name: "backfill, no bin/obs", args: []string{"backfill"}, code: 2,
			errOut: "laug · obs backfill: bin/obs, which _obs/obs.sh runs, is not built: make build builds it"},
			unbuilt: true},
		{envCase: envCase{name: "pack -pack", args: []string{"pack", "-pack", "/srv/packs/a.tar.gz"},
			how: script.Exec, script: obsScript, argv: []string{"pack"}, knobs: []string{"PACK=/srv/packs/a.tar.gz"},
			strays: two("OBS_DIR", "PACK"), out: "laug · obs pack: the stopped stack's store and traces in " +
				"$XDG_STATE_HOME/laug-obs into one tarball in /srv/packs/a.tar.gz, every file checked first against " +
				"check/deny-shapes.txt; Ctrl-C stops it"}},
		{envCase: envCase{name: "pack", args: []string{"pack", "-dir", dir}, how: script.Exec,
			script: obsScript, argv: []string{"pack"}, knobs: []string{"OBS_DIR=" + dir}, strays: two("OBS_DIR", "PACK"),
			out: "laug · obs pack: the stopped stack's store and traces in /srv/obs into one tarball in " +
				"/srv/obs/packs/, every file checked first against check/deny-shapes.txt; Ctrl-C stops it"}},
		{envCase: envCase{name: "view <pack>", args: []string{"view", "out/x.tar.gz"},
			files: map[string]string{"out/x.tar.gz": "x"}, how: script.Exec, script: obsScript, argv: []string{"view"},
			knobs: []string{"PACK=out/x.tar.gz"}, strays: two("OBS_DIR", "PACK"),
			out: "laug · obs view: out/x.tar.gz read by the store and Grafana alone on 127.0.0.1:7730 and 7740, no " +
				"scrape job, so it measures nothing; bin/obs down stops it",
			errOut: "laug · obs view: PACK=stray.tar.gz in the environment is not passed on; <pack> sets it"}},
		{envCase: envCase{name: "view, no such pack", args: []string{"view", "out/x.tar.gz"}, code: 2,
			errOut: "laug · obs view: out/x.tar.gz: no such file; bin/obs pack writes one"}},
		{envCase: envCase{name: "view, two packs", args: []string{"view", "a", "b"}, code: 2,
			errOut: `laug · obs view: takes one pack, got ["a" "b"]`}},
		{envCase: envCase{name: "purge -remove-packs alone", args: []string{"purge", "-remove-packs"}, code: 2,
			errOut: "laug · obs purge: -remove-packs takes -remove-state: the packs go with OBS_DIR"}},
		{envCase: envCase{name: "purge -remove-state -dry-run", args: []string{"purge", "-remove-state",
			"-remove-packs", "-dry-run"}, strays: two("OBS_DIR"), out: "FORCE=1 _obs/obs.sh purge"}, unbuilt: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := obsApp(t, c)
			code, out, errOut := runApp(t, a, c.args...)
			checkRow(t, c.envCase, rec, code, out, errOut)
		})
	}

	// -json keeps the script a child with JSON=1 and prints its object on one line, exit 1 too when a part is down.
	t.Run("status -json", func(t *testing.T) {
		for _, c := range []struct {
			name, out string
			exit      int
			wantOut   string
			wantCode  int
		}{
			{"every part up", "{\n  \"parts\": []\n}\n", 0, `{"parts":[]}` + "\n", 0},
			{"a part down", "{\n  \"obs_dir\": false\n}\n", 1, `{"obs_dir":false}` + "\n", 1},
			{"refused", "", 2, "", 2},
			{"a tool's code", "", 7, "", 2},
			{"no object", "laug · obs status, 12:00:00 UTC\n", 0, "", 1},
		} {
			t.Run(c.name, func(t *testing.T) {
				a, rec := obsApp(t, obsCase{})
				rec.run = func(_ script.Command, _ []string, stdout, _ io.Writer) int {
					fmt.Fprint(stdout, c.out)
					return c.exit
				}
				code, out, errOut := runApp(t, a, "status", "-json")
				if code != c.wantCode || out != c.wantOut || len(rec.calls) != 1 || rec.hows[0] != script.Run {
					t.Fatalf("exit %d, want %d; %d calls\nstdout:\n%s\nstderr:\n%s", code, c.wantCode, len(rec.calls),
						out, errOut)
				}
				if !slices.Contains(rec.envs[0], "JSON=1") {
					t.Errorf("the script's environment lacks JSON=1: %q", rec.envs[0])
				}
			})
		}
	})

	// LAUG_DENYLIST reaches the pack as the caller has it, so its check greps that deny list too (_obs/obs.sh, WORDS).
	t.Run("pack, LAUG_DENYLIST", func(t *testing.T) {
		a, rec := obsApp(t, obsCase{envCase: envCase{more: []string{"LAUG_DENYLIST=lists/words.txt"}}})
		code, _, errOut := runApp(t, a, "pack", "-dir", dir)
		if code != 0 || len(rec.envs) != 1 || !slices.Contains(rec.envs[0], "LAUG_DENYLIST=lists/words.txt") {
			t.Fatalf("exit %d, %d calls, env %q\nstderr:\n%s", code, len(rec.envs), rec.envs, errOut)
		}
	})

	t.Run("view, bare", func(t *testing.T) {
		state := t.TempDir()
		for _, p := range []string{"b.tar.gz", "a.tar.gz", "notes.txt"} {
			writeFile(t, filepath.Join(state, "packs", p), "x")
		}
		a, rec := obsApp(t, obsCase{})
		code, out, errOut := runApp(t, a, "view", "-dir", state)
		want := "laug · obs view: names no pack: give it a tarball bin/obs pack wrote\nin " + state + "/packs:\n" +
			"  bin/obs view " + state + "/packs/a.tar.gz   (0.0 MiB)\n  bin/obs view " + state +
			"/packs/b.tar.gz   (0.0 MiB)\nusage: bin/obs view"
		if code != 2 || out != "" || len(rec.calls) != 0 || !strings.HasPrefix(errOut, want) {
			t.Errorf("exit %d, %d calls\nstdout:\n%s\nstderr:\n%s\nwant it to start:\n%s", code, len(rec.calls), out,
				errOut, want)
		}
		code, _, errOut = runApp(t, a, "view", "-dir", filepath.Join(state, "none"))
		if code != 2 || !strings.Contains(errOut, "\n"+state+"/none/packs holds no pack; bin/obs pack makes one "+
			"there\n") {
			t.Errorf("no packs: exit %d\n%s", code, errOut)
		}
		// The default OBS_DIR, and a -dir under $HOME, name no home path: $XDG_STATE_HOME as such, else ~/, and a
		// pack's line to type still reads as a shell word.
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".local", "state", "laug-obs", "packs", "a.tar.gz"), "x")
		writeFile(t, filepath.Join(home, "my obs", "packs", "it's.tar.gz"), "x")
		for _, c := range []struct {
			name string
			env  []string
			args []string
			want string
		}{
			{"XDG_STATE_HOME", []string{"XDG_STATE_HOME=" + filepath.Join(home, "none"), "HOME=" + home}, nil,
				"\n$XDG_STATE_HOME/laug-obs/packs holds no pack; bin/obs pack makes one there\n"},
			{"HOME", []string{"XDG_STATE_HOME=", "HOME=" + home}, nil, "\nin ~/.local/state/laug-obs/packs:\n" +
				"  bin/obs view ~/.local/state/laug-obs/packs/a.tar.gz   (0.0 MiB)\n"},
			{"-dir under HOME", []string{"HOME=" + home + "/"}, []string{"-dir", filepath.Join(home, "my obs")},
				"\nin ~/'my obs'/packs:\n  bin/obs view ~/'my obs'/packs/'it'\\''s.tar.gz'   (0.0 MiB)\n"},
		} {
			a, _ := obsApp(t, obsCase{})
			a.Environ = c.env
			code, _, errOut := runApp(t, a, append([]string{"view"}, c.args...)...)
			if code != 2 || !strings.Contains(errOut, c.want) || strings.Contains(errOut, home) {
				t.Errorf("%s: exit %d\n%s\nwant it to hold:\n%s", c.name, code, errOut, c.want)
			}
		}
	})

	t.Run("purge", func(t *testing.T) { obsPurge(t) })

	t.Run("outside a checkout", func(t *testing.T) {
		for _, v := range [][]string{{"up"}, {"down"}, {"status"}, {"find", "g20261017-171604"}, {"backfill"},
			{"pack"}, {"view", "x.tar.gz"}, {"purge", "-remove-state"}} {
			rec := &recorder{}
			code, out, errOut := runApp(t, &App{Runner: rec}, v...)
			if code != 2 || len(rec.calls) != 0 || out != "" || !strings.HasPrefix(errOut, "laug · obs "+v[0]+": runs _obs/") {
				t.Errorf("obs %s: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", v[0], code, len(rec.calls), out, errOut)
			}
		}
	})

	t.Run("follow", func(t *testing.T) { obsFollow(t) })

	obsScripts(t)
}

// obsPurge: bare, purge counts what -remove-state would remove, by area, and runs nothing; -remove-state refuses an
// OBS_DIR with no marker or with packs, unless -remove-packs, and otherwise runs _obs/obs.sh purge with its counts
// first.
func obsPurge(t *testing.T) {
	state := filepath.Join(t.TempDir(), "laug-obs")
	for p, body := range map[string]string{
		".laug-obs": "created\n", "prometheus/wal/00000000": strings.Repeat("w", 5000), "prometheus/01J/meta.json": "{}",
		"jaeger/keys/000001.sst": "k", "dist/prometheus.tar.gz": "d", "bin/prometheus-2.53.0/prometheus": "b",
		"grafana/grafana.db": "g", "conf/COMMIT": "c", "run/follow.pid": "{}", "packs/b.tar.gz": "p",
		"packs/a.tar.gz": "p", "packs/notes.txt": "n",
	} {
		writeFile(t, filepath.Join(state, p), body)
	}
	st := readObsState(state)
	got := map[string]int{}
	for _, ar := range st.areas {
		got[ar.name] = ar.files
	}
	want := map[string]int{"the store": 2, "the traces": 1, "the releases": 2, "Grafana": 1, "a view's copy": 0,
		"the packs": 3, "the rest": 3}
	if !st.exists || !st.marker || st.files != 12 || fmt.Sprint(got) != fmt.Sprint(want) ||
		!slices.Equal(st.packs, []string{"a.tar.gz", "b.tar.gz"}) || st.bytes < 5000 {
		t.Errorf("readObsState: %+v\nfiles by area %v, want %v", st, got, want)
	}

	a, rec := obsApp(t, obsCase{})
	code, out, errOut := runApp(t, a, "purge", "-dir", state)
	lines := strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")
	if code != 2 || out != "" || len(rec.calls) != 0 || len(lines) != 9 {
		t.Fatalf("bare: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", code, len(rec.calls), out, errOut)
	}
	first := "laug · obs purge: would stop the stack's parts, then remove " + state + ", OBS_DIR: 12 files, "
	last := "bin/obs purge -remove-state -remove-packs -dir " + state + " removes it, the 2 packs with it; copy a " +
		"pack out first to keep it"
	if !strings.HasPrefix(lines[0], first) || !strings.HasSuffix(lines[0], " MiB on disk") || lines[8] != last ||
		!regexp.MustCompile(`^  the store +prometheus/ +2 files +[0-9.]+ MiB$`).MatchString(lines[1]) ||
		!regexp.MustCompile(`^  the packs +packs/, pack.new/ +3 files +[0-9.]+ MiB +2 packs: a.tar.gz, b.tar.gz$`).
			MatchString(lines[6]) {
		t.Errorf("bare:\n%s", errOut)
	}

	for _, c := range []struct {
		name   string
		args   []string
		code   int
		errOut string // stderr's last line
	}{
		{"-remove-state, packs there", []string{"-remove-state"}, 2, "laug · obs purge: " + state + "/packs holds " +
			"2 packs, named above, which go with OBS_DIR: copy each out first, or -remove-packs removes them too; " +
			"nothing stopped or removed"},
		{"-remove-state -remove-packs", []string{"-remove-state", "-remove-packs"}, 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := obsApp(t, obsCase{})
			code, out, errOut := runApp(t, a, append([]string{"purge", "-dir", state}, c.args...)...)
			if code != c.code || !strings.HasSuffix(errOut, c.errOut+"\n") && c.errOut != "" {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
			}
			if c.code != 0 {
				if len(rec.calls) != 0 || !strings.HasPrefix(errOut, "laug · obs purge: a pack: "+state+
					"/packs/a.tar.gz\n") {
					t.Errorf("%d calls\nstderr:\n%s", len(rec.calls), errOut)
				}
				return
			}
			first := "laug · obs purge: the stack's parts stop, then " + state + " goes: 12 files, "
			if len(rec.calls) != 1 || rec.hows[0] != script.Exec || !strings.HasPrefix(out, first) ||
				!strings.Contains(firstLine(out), " MiB on disk, 2 packs among them; then it counts what is left") ||
				!slices.Contains(rec.envs[0], "FORCE=1") || !slices.Contains(rec.envs[0], "OBS_DIR="+state) {
				t.Errorf("%d calls\nstdout:\n%s\nenv %q", len(rec.calls), out, rec.envs)
			}
		})
	}

	// No marker: bare says purge refuses it, and -remove-state refuses before anything runs.
	foreign := t.TempDir()
	writeFile(t, filepath.Join(foreign, "keep.txt"), "x")
	a, rec = obsApp(t, obsCase{})
	_, _, errOut = runApp(t, a, "purge", "-dir", foreign)
	code, _, errOut2 := runApp(t, a, "purge", "-remove-state", "-dir", foreign)
	if errOut != "laug · obs purge: "+foreign+" holds 1 file and no .laug-obs marker, so it is not the stack's, and "+
		"purge refuses it\n" || code != 2 || len(rec.calls) != 0 || errOut2 != "laug · obs purge: "+foreign+" holds "+
		"no .laug-obs marker, so it is not the stack's; nothing stopped or removed\n" {
		t.Errorf("no marker: exit %d, %d calls\nbare:\n%s\n-remove-state:\n%s", code, len(rec.calls), errOut, errOut2)
	}
	// No OBS_DIR: bare has nothing to say but that; -remove-state runs the script, which counts what is left.
	none := filepath.Join(t.TempDir(), "none")
	a, rec = obsApp(t, obsCase{})
	_, _, errOut = runApp(t, a, "purge", "-dir", none)
	code, out, _ = runApp(t, a, "purge", "-remove-state", "-dir", none)
	if errOut != "laug · obs purge: no OBS_DIR at "+none+": nothing to remove\n" || code != 0 || len(rec.calls) != 1 ||
		firstLine(out) != "laug · obs purge: no OBS_DIR at "+none+", nothing to remove; it stops any part a record "+
			"names and counts what is left" {
		t.Errorf("no OBS_DIR: exit %d, %d calls\nbare:\n%s\n-remove-state:\n%s", code, len(rec.calls), errOut, out)
	}
	// The default OBS_DIR names no home path: under $HOME as ~/, and $XDG_STATE_HOME as such.
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".local", "state", "laug-obs", ".laug-obs"), "created\n")
	for _, c := range []struct {
		env  []string
		want string
	}{
		{[]string{"HOME=" + home}, "laug · obs purge: would stop the stack's parts, then remove " +
			"~/.local/state/laug-obs, OBS_DIR: 1 file, "},
		{[]string{"XDG_STATE_HOME=" + filepath.Join(home, "x"), "HOME=" + home}, "laug · obs purge: no OBS_DIR at " +
			"$XDG_STATE_HOME/laug-obs: nothing to remove"},
	} {
		a, _ = obsApp(t, obsCase{})
		a.Environ = c.env
		_, _, errOut = runApp(t, a, "purge")
		if !strings.HasPrefix(errOut, c.want) || strings.Contains(errOut, home) {
			t.Errorf("bare, %q:\n%s\nwant it to start %q", c.env, errOut, c.want)
		}
	}
}

// obsFollow runs follow in process on a loopback port of the kernel's choosing: its first line once it listens, GET
// /metrics there, and exit 0 when the context ends; each refusal before it starts; and the three answers _obs/obs.sh
// asks bin/obs for: -gate-running by a lock this test holds, -openmetrics of a committed gate run, -decode of an empty
// store.
func obsFollow(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	state := t.TempDir()
	writeFile(t, filepath.Join(state, obsMarker), "created\n")
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	a := &App{Stdout: stdout, Stderr: stderr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, a, []string{"follow", "-listen", "127.0.0.1:0", "-pool", "127.0.0.1:1", "-every",
			"50ms", "-dir", state})
	}()
	re := regexp.MustCompile(`^laug · obs follow: each gate's records on http://(127\.0\.0\.1:[0-9]+)/metrics, each ` +
		`leased cell's target in ` + regexp.QuoteMeta(state) + `/targets, a poll of the pool on 127\.0\.0\.1:1 every ` +
		`50ms; Ctrl-C or SIGTERM stops it\n`)
	var m []string
	for deadline := time.Now().Add(10 * time.Second); m == nil; time.Sleep(10 * time.Millisecond) {
		if m = re.FindStringSubmatch(stdout.String()); m == nil && time.Now().After(deadline) {
			cancel()
			t.Fatalf("no first line in 10 s\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	}
	var status int
	for deadline := time.Now().Add(5 * time.Second); status != http.StatusOK && time.Now().Before(deadline); {
		if resp, err := http.Get("http://" + m[1] + "/metrics"); err == nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(state, "targets")); status != http.StatusOK || err != nil {
		t.Errorf("GET /metrics %d, OBS_DIR/targets %v", status, err)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit %d after the context ended\nstderr:\n%s", code, stderr.String())
	}

	foreign := t.TempDir()
	for _, c := range []struct {
		name   string
		args   []string
		errOut string
	}{
		{"-listen off loopback", []string{"-listen", "0.0.0.0:7720"},
			"laug · obs follow: -listen 0.0.0.0:7720: 127.0.0.1:<port>; the listener has no auth"},
		{"-pool off loopback", []string{"-pool", "localhost:7700"},
			"laug · obs follow: -pool localhost:7700: 127.0.0.1:<port>; a pool listens on loopback only"},
		{"-every 0", []string{"-every", "0s"}, "laug · obs follow: -every 0s: over 0"},
		{"-dir relative", []string{"-dir", "obs"}, "laug · obs follow: OBS_DIR obs: an absolute path"},
		{"-dir not the stack's", []string{"-dir", foreign}, "laug · obs follow: " + foreign + " holds no .laug-obs " +
			"marker, so it is not the stack's, and follow writes its targets and records there: bin/obs up makes it"},
		{"an argument", []string{"x"}, `laug · obs follow: takes no arguments, got ["x"]`},
		{"two answers", []string{"-gate-running", "-decode", "-out", foreign},
			"laug · obs follow: -gate-running, -openmetrics and -decode: one at a time"},
		{"-listen with an answer", []string{"-gate-running", "-listen", "127.0.0.1:0"},
			"laug · obs follow: -listen serves until stopped; -gate-running, -openmetrics and -decode answer once"},
		{"-openmetrics empty", []string{"-openmetrics", ""}, "laug · obs follow: -openmetrics: name a gate directory"},
		{"-out alone", []string{"-out", foreign}, "laug · obs follow: -out goes with -decode"},
		{"-decode alone", []string{"-decode"},
			"laug · obs follow: -decode needs -out, an empty dir for the decoded records"},
		{"-decode, not the stack's", []string{"-decode", "-out", t.TempDir(), "-dir", foreign}, "laug · obs follow: " +
			foreign + " holds no .laug-obs marker, so it is not the stack's, and follow writes its targets and records " +
			"there: bin/obs up makes it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"follow", "-dir", state}, c.args...)
			code, out, errOut := runApp(t, &App{}, args...)
			if code != 2 || out != "" || firstLine(errOut) != c.errOut {
				t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
		})
	}
	if entries, _ := os.ReadDir(foreign); len(entries) != 0 {
		t.Errorf("a refused follow wrote %d entries into its -dir", len(entries))
	}

	t.Run("-gate-running", func(t *testing.T) {
		xdg := os.Getenv("XDG_RUNTIME_DIR")
		code, out, errOut := runApp(t, &App{}, "follow", "-gate-running", "-pool", "127.0.0.1:1")
		switch {
		case code == 1 && out == "laug · obs follow: no gate runs on this host, by its locks and the pool on "+
			"127.0.0.1:1\n" && errOut == "":
		case code == 0 && strings.HasPrefix(out, "laug · obs follow: a gate runs: ") && !strings.Contains(out, xdg):
			t.Logf("a gate of this host's is open, so the closed answer is not checked: %s", out)
		default:
			t.Errorf("no lock held here: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		lock, err := os.OpenFile(filepath.Join(xdg, "laug-gate.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		code, out, errOut = runApp(t, &App{}, "follow", "-gate-running", "-pool", "127.0.0.1:1")
		if code != 0 || !strings.HasPrefix(out, "laug · obs follow: a gate runs: ") || strings.Count(out, "\n") != 1 ||
			!strings.Contains(out, "laug-gate.lock") || errOut != "" {
			t.Errorf("a lock held in XDG_RUNTIME_DIR: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})

	t.Run("-openmetrics", func(t *testing.T) {
		// _obs/follow's copy of the committed run, byte for byte (_obs/follow/main_test.go).
		gate, err := filepath.Abs(filepath.Join("..", "..", "follow", "testdata", "g20261017-020417"))
		if err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runApp(t, &App{}, "follow", "-openmetrics", gate, "-dir", state)
		if code != 0 || strings.HasPrefix(out, "laug · ") || !strings.HasSuffix(out, "# EOF\n") ||
			!strings.Contains(out, `gate_id="g20261017-020417"`) {
			t.Errorf("a committed gate run: exit %d, stdout of %d bytes, its first line %q\nstderr:\n%s", code,
				len(out), firstLine(out), errOut)
		}
		code, out, errOut = runApp(t, &App{}, "follow", "-openmetrics", t.TempDir(), "-dir", state)
		if code != 1 || out != "" || !strings.Contains(errOut, "has no timeline line") {
			t.Errorf("a directory with no timeline: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})

	t.Run("-decode", func(t *testing.T) {
		into := t.TempDir()
		code, out, errOut := runApp(t, &App{}, "follow", "-decode", "-out", into, "-dir", state)
		var sum map[string]any
		if code != 0 || strings.Count(out, "\n") != 1 || json.Unmarshal([]byte(out), &sum) != nil {
			t.Errorf("an empty store: exit %d, want 0 and one JSON object\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		writeFile(t, filepath.Join(into, "x"), "")
		code, out, errOut = runApp(t, &App{}, "follow", "-decode", "-out", into, "-dir", state)
		if code != 2 || out != "" || errOut != "follow: -decode: -out is not empty\n" {
			t.Errorf("-out not empty: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})
}

// obsScripts runs status and find through the built binary in this checkout, so each execs its script for real:
// an OBS_DIR in a temp dir, HOME another, a curl first on PATH that never answers, so find reads no store, and a stray
// OBS_DIR and JSON that never reach the script.
func obsScripts(t *testing.T) {
	for _, tool := range []string{"yq", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Logf("%s is not on PATH: the scripts' run is skipped", tool)
			return
		}
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	obsCmd := filepath.Join(obsBinary(t), "obs")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "curl"), "#!/bin/sh\nexit 7\n")
	empty, none := t.TempDir(), filepath.Join(t.TempDir(), "none")
	obs := func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, obsCmd, args...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "OBS_DIR=" + empty,
			"JSON=1"}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		return code, stdout.String(), stderr.String()
	}
	for _, c := range []struct {
		name  string
		args  []string
		code  int
		lines []string // lines stdout holds; the first is its first line
		stray bool     // stderr names the stray OBS_DIR, which differs from the one the verb sets
	}{
		{"status, no OBS_DIR", []string{"status"}, 1, []string{"laug · obs status: no OBS_DIR: the stack was never " +
			"up here, or was purged"}, true},
		{"status -json, no OBS_DIR", []string{"status", "-json", "-dir", none}, 1, []string{`{"obs_dir":false}`}, true},
		{"status, every part down", []string{"status", "-dir", empty}, 1, []string{"", "  prometheus  down: no record",
			"  grafana     down: no record", "  the store: down"}, false},
		{"find <gate id>", []string{"find", "g20261017-171604", "-results", "internal/rejudge/testdata/lab", "-dir",
			empty}, 0, []string{"laug · gate g20261017-171604: BLOCK (verdict.json), 3 items, t0 17:16:04 UTC, " +
			"done 17:16:05 UTC"}, false},
		{"find -run-id", []string{"find", "-run-id", "lab1-v1310-1"}, 0, []string{"laug · run lab1-v1310-1: " +
			"ADVANCE (lab1-v1310-1.verdict.json), cell unknown (its project is laug), 2026-10-23 06:54:04 to " +
			"06:59:05 UTC"}, true},
	} {
		t.Run("the script: "+c.name, func(t *testing.T) {
			code, out, errOut := obs(t, c.args...)
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if code != c.code {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.code, out, errOut)
			}
			if !strings.HasPrefix(out, "laug · obs "+c.args[0]) && !strings.HasPrefix(out, "laug · gate ") &&
				!strings.HasPrefix(out, "laug · run ") && !strings.HasPrefix(out, "{") {
				t.Errorf("stdout's first line is not laug's:\n%s", out)
			}
			for i, l := range c.lines {
				if i == 0 && l != "" && lines[0] != l || i > 0 && !slices.Contains(lines, l) {
					t.Errorf("stdout lacks %q:\n%s", l, out)
				}
			}
			stray := "laug · obs " + c.args[0] + ": OBS_DIR=" + empty + " in the environment is not passed on; -dir " +
				"sets it\n"
			if strings.Contains(errOut, stray) != c.stray {
				t.Errorf("the stray line for OBS_DIR: %t, want %t\n%s", !c.stray, c.stray, errOut)
			}
			if strings.Contains(out+errOut, "laug · make") {
				t.Errorf("a line starts laug · make:\n%s%s", out, errOut)
			}
		})
	}
}

// TestObsMatchesScript holds the stack's verbs to _obs/obs.sh: the verbs it takes are this binary's script verbs and
// test, which make obs-test runs; its parts are -only's, its follow listens where follow does, OBS_DIR resolves as
// this binary resolves it, and every line it prints starts "laug · obs <verb>: ", or "laug · make obs-test: " for test.
func TestObsMatchesScript(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", obsScript))
	if err != nil {
		t.Fatal(err)
	}
	sh := string(b)
	var verbs []string
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z]+)\) [a-z_]+ ;;$`).FindAllStringSubmatch(sh, -1) {
		verbs = append(verbs, m[1])
	}
	var laugs []string
	for _, c := range commands {
		if c.group == groupStack && c.name != "follow" {
			laugs = append(laugs, c.name)
		}
	}
	// make obs-test runs _obs/obs.sh test, and it prints under that target's name.
	verbs = slices.DeleteFunc(verbs, func(v string) bool { return v == "test" })
	slices.Sort(verbs)
	slices.Sort(laugs)
	if !slices.Equal(verbs, laugs) {
		t.Errorf("_obs/obs.sh takes %q; bin/obs runs it as %q", verbs, laugs)
	}
	if m := regexp.MustCompile(`(?m)^PARTS=\(([a-z ]+)\)$`).FindStringSubmatch(sh); m == nil ||
		!slices.Equal(strings.Fields(m[1]), obsParts) {
		t.Errorf("_obs/obs.sh's PARTS %q, laug's %q", m, obsParts)
	}
	for _, want := range []string{"FOLLOW_LISTEN=" + obsFollowListen + " ", "POOL=" + obsPoolAddr + " ",
		`CMD="${1:-}"; VERB="obs $CMD"; [[ "$CMD" != test ]] || VERB="make obs-test"` + "\n",
		`say() { echo "laug · $VERB: $*"; }`,
		`die() { echo "laug · $VERB: $*" >&2; exit 2; }`, `"$OBS_DIR/` + obsMarker + `"`} {
		if !strings.Contains(sh, want) {
			t.Errorf("_obs/obs.sh lacks %q", want)
		}
	}
	// _obs/obs.sh strips _obs/lint.sh's prefix, "laug · make lint: ", from the lint's lines; it prints none under make's
	// name but test's, through $VERB.
	if n := strings.Count(sh, "laug · make") - strings.Count(sh, "laug · make lint: "); n != 0 {
		t.Errorf("_obs/obs.sh prints %d lines under laug · make", n)
	}
	line := regexp.MustCompile(`(?m)^OBS_DIR="\$\{OBS_DIR:-.*$`).FindString(sh)
	if line == "" {
		t.Fatal("_obs/obs.sh: no OBS_DIR default line")
	}
	for _, env := range [][]string{{"XDG_STATE_HOME=/x/state/", "HOME=/h"}, {"HOME=/h"}, {"XDG_STATE_HOME=", "HOME=/h/"},
		{}} {
		out, err := exec.Command("env", append(append([]string{"-i"}, env...), "bash", "-c",
			line+`; printf %s "$OBS_DIR"`)...).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := (&App{Environ: env}).obsDir(""); got != string(out) {
			t.Errorf("%q: laug's OBS_DIR %q, _obs/obs.sh's %q", env, got, out)
		}
	}
	if got := (&App{}).obsDir("/srv/obs/"); got != "/srv/obs" {
		t.Errorf("-dir /srv/obs/: %q", got)
	}
}

// obsPartsScript drives _obs/obs.sh's part functions, sourced without its verb dispatch, from a checkout stand-in
// whose bin/obs is the built binary: $1 the repository, $2 to $4 free ports for follow, the stack's cellstat and a
// batch's. Each check prints "ok   <name>" or "FAIL <name>: ...", and each process it starts "pid <n>".
const obsPartsScript = `set -uo pipefail
repo="$1" fport="$2" cport="$3" bport="$4"
VERB="obs parts"
source <(sed '/^CMD="\${1:-}"; VERB=/,$d' "$repo/_obs/obs.sh") || { echo "FAIL _obs/obs.sh does not source"; exit 1; }
check() { if [[ "$2" == "$3" ]]; then echo "ok   $1"; else echo "FAIL $1: got [$2], want [$3]"; fi; }
nodocker="$(mktemp -d)" sid="$(command -v setsid)"
mkdir -p "$OBS_DIR/conf/obs" "$OBS_DIR/conf/aa" "$OBS_DIR/run" "$OBS_DIR/log"
: >"$OBS_DIR/.laug-obs"; echo '{}' >"$OBS_DIR/conf/catalog.json"
sum="$(sha256sum bin/obs | cut -d' ' -f1)"; echo "$sum" >"$OBS_DIR/conf/obs/obs.sha256"
copy="$OBS_DIR/bin/obs-${sum:0:12}/obs"
check "own_bin, twice: one copy of bin/obs, named by its sha256" "$(own_bin)|$(own_bin)" "$copy|$copy"
check "OBS_DIR/bin holds that copy alone" "$(cd "$OBS_DIR/bin" && find . -type f | sort | tr '\n' ' ')" "./obs-${sum:0:12}/obs "

FOLLOW_LISTEN="127.0.0.1:$fport" POOL=127.0.0.1:1; ANSWER[follow]="http://$FOLLOW_LISTEN/metrics"
start_part follow; echo "pid $(jq -r .pid "$OBS_DIR/run/follow.pid")"
answer follow 20; check "start_part follow: the copy answers on its listener" "$?" 0
f="$(rec_check follow)"; check "rec_check follow: its record holds" "$?" 0
check "follow's command line" "$(tr '\0' ' ' <"/proc/$f/cmdline")" "$copy follow -listen $FOLLOW_LISTEN -pool $POOL -dir $OBS_DIR "

# The stack's cellstat as start_part starts it, from the same copy, but with no docker on PATH.
( exec env -i PATH="$nodocker" "$sid" "$copy" usage record -out none -listen "127.0.0.1:$cport" -interval 1h -for 0 \
    -pids "$OBS_DIR/run" >"$OBS_DIR/log/cellstat.log" 2>&1 </dev/null ) &
c=$!; echo "pid $c"
for _ in $(seq 1 100); do grep -q '^laug · usage record: no file' "$OBS_DIR/log/cellstat.log" && break; sleep 0.1; done
check "the stack's cellstat: its first line" "$(grep '^laug · usage record: no file' "$OBS_DIR/log/cellstat.log")" \
  "laug · usage record: no file (-out none): the last sample on http://127.0.0.1:$cport/metrics, one every 1h0m0s until stopped, pid $c; also the processes recorded in $OBS_DIR/run; Ctrl-C or SIGTERM stops it"
check "the stack's cellstat serves the last sample" "$(curl_local "http://127.0.0.1:$cport/metrics" >/dev/null; echo $?)" 0
rec_write cellstat "$f"
check "rec_check cellstat: a record that names follow's process" "$(rec_check cellstat; echo "exit $?")" \
  "pid $f runs obs follow -listen, not obs usage record, cellstat's verb
exit 1"
rec_write cellstat "$c"
check "rec_check cellstat: the copy running obs usage record" "$(rec_check cellstat)" "$c"
check "the stack's cellstat is named cellstat, and is no batch's" "$(comm_of "$c") $(batch_cellstat "$c" && echo batch || echo stack)" "cellstat stack"

# A batch's cellstat: bin/obs usage record -listen from the checkout's bin/obs.
( exec env -i PATH="$nodocker" "$sid" bin/obs usage record -out none -listen "127.0.0.1:$bport" -interval 1h -for 0 \
    >"$nodocker/batch.log" 2>&1 </dev/null ) &
b=$!; echo "pid $b"
for _ in $(seq 1 100); do grep -q '^laug · usage record: no file' "$nodocker/batch.log" && break; sleep 0.1; done
check "a batch's usage record from the checkout is batch_cellstat's" "$(batch_cellstat "$b" && echo batch || echo stack)" batch
kill -TERM "$b"; wait "$b" 2>/dev/null

check "stop_part follow" "$(stop_part follow | sed -E 's/[0-9.]+ s after/N s after/')" "laug · obs parts: follow: pid $f stopped, N s after TERM"
check "stop_part cellstat" "$(stop_part cellstat | sed -E 's/[0-9.]+ s after/N s after/')" "laug · obs parts: cellstat: pid $c stopped, N s after TERM"
check "their records gone" "$(ls "$OBS_DIR/run")" ""
check "the stack's cellstat's last line" "$(tail -1 "$OBS_DIR/log/cellstat.log")" "laug · usage record: stopped; -out none, so no file holds a line"
check "-out none wrote no file" "$(find "$OBS_DIR" . -name '*.jsonl' -o -name '*.jsonl.pid' | wc -l)" 0

# gate_open asks bin/obs follow -gate-running: none open by this shell's locks, then one it holds, then no bin/obs.
export XDG_RUNTIME_DIR="$nodocker"
out="$(gate_open)"; rc=$?
if [[ $rc == 1 ]]; then check "gate_open, no lock held: exit 1, nothing said" "$rc|$out" "1|"
else echo "ok   gate_open: a gate of this host's is open, so the closed answer is not checked: $out"; fi
exec 7>"$XDG_RUNTIME_DIR/laug-gate.lock"; flock -n 7
out="$(gate_open)"; check "gate_open, a lock held: exit 0 and follow's why, laug's prefix cut" "$?|$out" \
  "0|laug-gate.lock in XDG_RUNTIME_DIR is held"
exec 7>&-
out="$(OBS_FOLLOW_BUILD=failed gate_open)"; check "gate_open, bin/obs not built: a gate counts as running" "$?|$out" \
  "0|bin/obs is not built, so it cannot tell, and a gate counts as running"
rm -rf "$nodocker"
`

// TestObsPartsRunObs holds _obs/obs.sh's two parts of this binary to one copy of bin/obs: own_bin copies the
// checkout's bin/obs once into OBS_DIR/bin/obs-<sha256, 12 hex>/obs; start_part starts follow from it as bin/obs
// follow, which answers; the stack's cellstat runs from it as bin/obs usage record -out none, serving the last sample
// and writing no file; rec_check takes a part's record only for the part's own verb; batch_cellstat tells a batch's
// usage record from the checkout from the stack's; stop_part stops both; and gate_open takes bin/obs follow
// -gate-running's answer. The ports are the kernel's choosing, the pool on 127.0.0.1:1 answers nothing, and cellstat
// finds no docker on its PATH.
func TestObsPartsRunObs(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq", "sha256sum", "setsid", "nice", "find", "flock"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	if err := kernelTree(); err != nil {
		t.Skipf("bin/obs usage record reads this host: %v", err)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(obsBinary(t), "obs"), filepath.Join(work, obsBin)); err != nil {
		t.Fatal(err)
	}
	args := []string{"-c", obsPartsScript, "obs-parts", repo}
	var lns []net.Listener
	for range 3 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		lns = append(lns, ln)
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		args = append(args, port)
	}
	for _, ln := range lns {
		ln.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "OBS_DIR=" + t.TempDir()}
	b, err := cmd.CombinedOutput()
	out := string(b)
	// A process the script started that is still up, a check having failed before it stopped it, is killed here.
	for _, m := range regexp.MustCompile(`(?m)^pid ([0-9]+)$`).FindAllStringSubmatch(out, -1) {
		cl, _ := os.ReadFile("/proc/" + m[1] + "/cmdline")
		if bytes.Contains(cl, []byte("usage\x00record\x00-out\x00none")) || bytes.Contains(cl, []byte("follow\x00-listen")) {
			if pid, err := strconv.Atoi(m[1]); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	oks := 0
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "ok   "):
			oks++
		case strings.HasPrefix(l, "FAIL "):
			t.Error(l)
		}
	}
	if err != nil || oks != 19 {
		t.Errorf("bash: %v, %d checks passed, want 19\n%s", err, oks, out)
	}
}

// TestKnobReadersHere is internal/script's TestKnobReaders for the readers under _obs/, which the main module never
// reads: each is in the tree and names the knob. At least one knob names one, _obs/obs.sh.
func TestKnobReadersHere(t *testing.T) {
	n := 0
	for _, k := range script.Knobs {
		word := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + k.Name + `([^A-Za-z0-9_]|$)`)
		for _, r := range k.Readers {
			if !strings.HasPrefix(r, "_obs/") {
				continue
			}
			n++
			b, err := os.ReadFile(filepath.Join("..", "..", "..", r))
			if err != nil {
				t.Errorf("%s: reader %s: %v", k.Name, r, err)
				continue
			}
			if !word.Match(b) {
				t.Errorf("%s: reader %s never names it", k.Name, r)
			}
		}
	}
	if n == 0 {
		t.Error("no knob names a reader under _obs/; the script table lost _obs/obs.sh")
	}
}
