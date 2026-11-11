package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// TestGateToleranceSet holds how gate tolerance resolves the set, with a runner that records and a yq that copies:
// load/thresholds.sh "" gets the set's environment: LC_ALL=C and no THRESHOLDS_RELEASES. It gets no knob: not
// THRESHOLDS, which would name another set, and none of GATE_RESULTS, PAIR and EVIDENCE, which it never reads. Its
// stdout goes to yq, even when it fails, as in a pipe; and a failure of either is tolerance.NoBase's line, exit 2,
// nothing computed.
func TestGateToleranceSet(t *testing.T) {
	set, err := os.ReadFile(filepath.Join("..", "..", "internal", "tolerance", "testdata", "set.json"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "g20261017-174403"
	setup := func(t *testing.T, yq string, code int) (*App, *recorder, *bytes.Buffer, *bytes.Buffer) {
		t.Helper()
		root := checkout(t)
		shellCopy(t, filepath.Join("..", "..", runPath(id)), filepath.Join(root, "gate", "results", id))
		bin := t.TempDir()
		writeExec(t, filepath.Join(bin, "yq"), yq)
		rec := &recorder{run: func(c script.Command, env []string, stdout, stderr io.Writer) int {
			if code == 0 {
				stdout.Write(set)
			} else { // as a script that printed part of its set, then failed
				io.WriteString(stdout, "window: [")
				io.WriteString(stderr, "thresholds: no threshold set at load/steady-state.yaml\n")
			}
			return code
		}}
		var stdout, stderr bytes.Buffer
		a := &App{Root: root, Stdout: &stdout, Stderr: &stderr, Runner: rec, Environ: []string{"PATH=" + bin +
			":/usr/bin:/bin", "THRESHOLDS=stray.yaml", "THRESHOLDS_RELEASES=stray", "LC_ALL=en_US.UTF-8", "LANG=C.UTF-8"}}
		return a, rec, &stdout, &stderr
	}
	t.Run("resolved", func(t *testing.T) {
		a, rec, stdout, stderr := setup(t, "#!/bin/sh\nexec cat\n", 0)
		code := run(context.Background(), a, []string{"gate", "tolerance", id})
		if code != 0 || !strings.HasPrefix(stdout.String(), "laug · gate tolerance: 1 gate(s), A/A gates of 1.31.0 on "+
			"m8i.8xlarge, the incumbent arm\n") || stderr.Len() > 0 {
			t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
		}
		if len(rec.calls) != 1 || rec.calls[0].Script != "load/thresholds.sh" || len(rec.calls[0].Args) != 1 ||
			rec.calls[0].Args[0].Text != "" || rec.hows[0] != script.Run {
			t.Fatalf("handed over %+v", rec.calls)
		}
		env := rec.envs[0]
		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			if _, knob := script.Lookup(name); knob || name == "THRESHOLDS_RELEASES" ||
				name == "LC_ALL" && kv != "LC_ALL=C" {
				t.Errorf("load/thresholds.sh's environment holds %s", kv)
			}
		}
		for _, want := range []string{"LC_ALL=C", "LANG=C.UTF-8"} {
			if !slices.Contains(env, want) {
				t.Errorf("load/thresholds.sh's environment lacks %s: %q", want, env)
			}
		}
	})
	t.Run("load/thresholds.sh fails", func(t *testing.T) {
		// yq still reads what it printed, as in the script's pipe, and says what it read.
		a, _, stdout, stderr := setup(t, "#!/bin/sh\nin=$(cat)\necho \"yq: read $in\" >&2\nexit 1\n", 2)
		code := run(context.Background(), a, []string{"gate", "tolerance", id})
		want := "thresholds: no threshold set at load/steady-state.yaml\nyq: read window: [\n" +
			"laug · gate tolerance: load/thresholds.sh does not resolve the base\n"
		if code != 2 || stdout.Len() > 0 || stderr.String() != want {
			t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
		}
	})
	t.Run("yq fails", func(t *testing.T) {
		a, _, stdout, stderr := setup(t, "#!/bin/sh\necho 'yq: bad' >&2\nexit 1\n", 0)
		code := run(context.Background(), a, []string{"gate", "tolerance", id})
		want := "yq: bad\nlaug · gate tolerance: load/thresholds.sh does not resolve the base\n"
		if code != 2 || stdout.Len() > 0 || stderr.String() != want {
			t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
		}
	})
}

// shellCopy copies the directory src to dst with cp -R.
func shellCopy(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-R", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v\n%s", err, out)
	}
}
