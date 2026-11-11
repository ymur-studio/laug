package script

import (
	"slices"
	"testing"
)

// parent is a caller's environment with a stray copy of four knobs, a seam, a tool's, two names no script gets and a
// second PATH, which loses to the first.
var parent = []string{
	"PATH=/usr/bin:/bin", "HOME=/h", "SERVER_TAG=1.32.1", "RUN_ID=stray", "DURATION=10m", "PROJECT=laug",
	"FAKE_GATE_DIR=/x", "GOFLAGS=-mod=mod", "USER=someone", "TEMPORAL_ADDRESS=127.0.0.1:7233", "PATH=/second",
	"NOT_A_PAIR",
}

// TestEnv: a script gets the base names, the tools' and the seams as the caller has them, then the knobs its verb
// sets, and no other name: a stray SERVER_TAG or RUN_ID never reaches it. A stray copy of a knob the verb maps is
// reported unless it equals what the verb sets.
func TestEnv(t *testing.T) {
	for _, c := range []struct {
		name   string
		cmd    Command
		env    []string
		strays []Stray
	}{
		{"maps nothing", Command{Script: "load/sweep.sh"},
			[]string{"PATH=/usr/bin:/bin", "HOME=/h", "FAKE_GATE_DIR=/x", "GOFLAGS=-mod=mod"}, nil},
		{"clears SERVER_TAG", Command{Script: "load/run-omes.sh", Clear: []string{"SERVER_TAG", "DURATION"},
			Set: []Var{Bare("RUN_ID", "20261022-120000"), Bare("PROJECT", "laug")}},
			[]string{"PATH=/usr/bin:/bin", "HOME=/h", "FAKE_GATE_DIR=/x", "GOFLAGS=-mod=mod",
				"RUN_ID=20261022-120000", "PROJECT=laug"},
			[]Stray{{"DURATION", "10m", ""}, {"RUN_ID", "stray", ""}, {"SERVER_TAG", "1.32.1", ""}}},
		{"sets SERVER_TAG", Command{Script: "load/run-omes.sh", Set: []Var{Bare("SERVER_TAG", "1.31.0")},
			Pass: []Var{Bare("DURATION", "10m")}},
			[]string{"PATH=/usr/bin:/bin", "HOME=/h", "FAKE_GATE_DIR=/x", "GOFLAGS=-mod=mod",
				"DURATION=10m", "SERVER_TAG=1.31.0"},
			[]Stray{{"SERVER_TAG", "1.32.1", ""}}},
		{"SERVER_TAG from an argument", Command{Script: "gate/run.sh", Set: []Var{Quoted("SERVER_TAG", "1.31.0")},
			By: map[string]string{"SERVER_TAG": "<tag>"}},
			[]string{"PATH=/usr/bin:/bin", "HOME=/h", "FAKE_GATE_DIR=/x", "GOFLAGS=-mod=mod", "SERVER_TAG=1.31.0"},
			[]Stray{{"SERVER_TAG", "1.32.1", "<tag>"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, strays := Env(parent, c.cmd)
			if !slices.Equal(env, c.env) {
				t.Errorf("env:\n got %q\nwant %q", env, c.env)
			}
			if !slices.Equal(strays, c.strays) {
				t.Errorf("strays:\n got %q\nwant %q", strays, c.strays)
			}
		})
	}
	if env, _ := Env(nil, Command{Script: "x.sh"}); env == nil {
		t.Error("an empty environment is nil, which os/exec reads as the process's own")
	}
}

func TestStrayString(t *testing.T) {
	for _, c := range []struct {
		s    Stray
		want string
	}{
		{Stray{"SERVER_TAG", "1.32.1", ""}, "SERVER_TAG=1.32.1 in the environment is not passed on; -server-tag sets it"},
		{Stray{"SERVER_TAG", "1.32.1", "<tag>"}, "SERVER_TAG=1.32.1 in the environment is not passed on; <tag> sets it"},
		{Stray{"OMES_DIR", "/x", ""}, "OMES_DIR=/x in the environment is not passed on; laug sets it"},
		{Stray{"FAULT", "x", ""}, "FAULT=x in the environment is not passed on; fault <fault> sets it"},
		{Stray{"RUN_ID", "a\nb", ""}, `RUN_ID="a\nb" in the environment is not passed on; -run-id sets it`},
	} {
		if got := c.s.String(); got != c.want {
			t.Errorf("got %q\nwant %q", got, c.want)
		}
	}
}
