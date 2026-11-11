package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// stampless stands in for a build with no make build stamp and the build info settings given, key then value.
func stampless(t *testing.T, settings ...string) {
	t.Helper()
	savedCommit, savedRead := commit, readBuildInfo
	t.Cleanup(func() { commit, readBuildInfo = savedCommit, savedRead })
	commit = ""
	info := &debug.BuildInfo{}
	for i := 0; i+1 < len(settings); i += 2 {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
	}
	readBuildInfo = func() (*debug.BuildInfo, bool) { return info, true }
}

func TestVersion(t *testing.T) {
	stampless(t)
	code, out, errOut := runArgs(t, "version")
	want := "laug · version dev " + runtime.Version() + " server 1.31.0 omes f0de206\n"
	if code != 0 || out != want || errOut != "" {
		t.Errorf("version: exit %d\nstdout %q\nwant   %q\nstderr %q", code, out, want, errOut)
	}

	for _, flag := range []string{"--version", "-version"} {
		if code, out, errOut := runArgs(t, flag); code != 0 || out != want || errOut != "" {
			t.Errorf("%s: exit %d\nstdout %q\nwant   %q, version's\nstderr %q", flag, code, out, want, errOut)
		}
	}

	code, out, errOut = runArgs(t, "version", "-json")
	if code != 0 || errOut != "" {
		t.Fatalf("version -json: exit %d, stderr %q", code, errOut)
	}
	var got map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&got); err != nil || dec.More() {
		t.Fatalf("version -json is not one object: %v\n%s", err, out)
	}
	wantJSON := map[string]any{"commit": "dev", "go": runtime.Version(), "server_tag": "1.31.0",
		"omes_ref": "f0de20635c8204b0c9b8277ce00493204a3c335a", "omes_commit": nil}
	if len(got) != len(wantJSON) {
		t.Errorf("version -json has %d fields, want %d: %v", len(got), len(wantJSON), got)
	}
	for k, v := range wantJSON {
		if w, ok := got[k]; !ok || w != v {
			t.Errorf("version -json %s = %v, want %v", k, got[k], v)
		}
	}

	// The flag after nothing and before nothing: -json parses wherever it sits.
	if code, _, _ := runArgs(t, "version", "-json", "-json=false"); code != 0 {
		t.Errorf("version -json -json=false: exit %d", code)
	}
}

// TestVersionOmes: in a checkout that holds Omes's clone, version names the commit .upstream/omes is at, and says when
// it is not the one every result used; -json gives it in full as omes_commit, null with no clone or a HEAD it cannot
// read.
func TestVersionOmes(t *testing.T) {
	stampless(t)
	other := strings.Repeat("ab", 20)
	for _, c := range []struct {
		name, head string // .upstream/omes/.git/HEAD; "" no clone
		text       string // what the line adds
		json       any
	}{
		{"no clone", "", "", nil},
		{"at the ref", omesRef, ", .upstream/omes at f0de206", omesRef},
		{"at another commit", other, ", .upstream/omes at abababa, not f0de206", other},
		{"on a branch in refs/", "ref: refs/heads/main", ", .upstream/omes at f0de206", omesRef},
		{"a HEAD it cannot read", "ref: refs/heads/gone", ", .upstream/omes at a commit laug cannot read", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := checkout(t)
			if c.head != "" {
				withOmes(t, root, c.head)
				writeFile(t, filepath.Join(root, ".upstream", "omes", ".git", "refs", "heads", "main"), omesRef+"\n")
			}
			code, out, errOut := runApp(t, &App{Root: root}, "version")
			want := "laug · version dev " + runtime.Version() + " server 1.31.0 omes f0de206" + c.text + "\n"
			if code != 0 || out != want || errOut != "" {
				t.Errorf("exit %d\nstdout %q\nwant   %q\nstderr %q", code, out, want, errOut)
			}
			_, out, _ = runApp(t, &App{Root: root}, "version", "-json")
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil || got["omes_commit"] != c.json {
				t.Errorf("version -json: omes_commit %v, want %v (%v)\n%s", got["omes_commit"], c.json, err, out)
			}
		})
	}
}

// TestVersionStamp: what make build passes through -ldflags reaches the line, cut to 7 hex digits, and -json in full.
func TestVersionStamp(t *testing.T) {
	saved := commit
	t.Cleanup(func() { commit = saved })
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct{ stamp, text string }{
		{sha, "0123456"},
		{sha + "-dirty", "0123456-dirty"},
		{"dev", "dev"},
		{"not-a-commit", "not-a-commit"},
	} {
		commit = c.stamp
		if _, out, _ := runArgs(t, "version"); !strings.HasPrefix(out, "laug · version "+c.text+" go") {
			t.Errorf("stamp %q: %q", c.stamp, out)
		}
		if _, out, _ := runArgs(t, "version", "-json"); !strings.Contains(out, `"commit":"`+c.stamp+`"`) {
			t.Errorf("stamp %q, -json: %q", c.stamp, out)
		}
	}
}

// TestBuildCommit: with no make build stamp, version reads the VCS stamp of a plain go build or go install, with
// -dirty for a modified tree, and says dev when there is none or no build info at all. A stamp wins over both.
func TestBuildCommit(t *testing.T) {
	sha := "89abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		name, stamp string
		settings    []string
		noInfo      bool
		want        string
	}{
		{"clean", "", []string{"vcs", "git", "vcs.revision", sha, "vcs.modified", "false"}, false, sha},
		{"modified", "", []string{"vcs.modified", "true", "vcs.revision", sha}, false, sha + "-dirty"},
		{"no revision", "", []string{"vcs.modified", "true"}, false, "dev"},
		{"no build info", "", nil, true, "dev"},
		{"the stamp wins", "0123456789abcdef0123456789abcdef01234567", []string{"vcs.revision", sha}, false,
			"0123456789abcdef0123456789abcdef01234567"},
	} {
		t.Run(c.name, func(t *testing.T) {
			stampless(t, c.settings...)
			commit = c.stamp
			if c.noInfo {
				readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
			}
			if got := buildCommit(); got != c.want {
				t.Errorf("buildCommit() = %q, want %q", got, c.want)
			}
		})
	}
	stampless(t, "vcs.revision", sha, "vcs.modified", "true")
	if _, out, _ := runArgs(t, "version"); !strings.HasPrefix(out, "laug · version 89abcde-dirty go") {
		t.Errorf("version with a modified VCS stamp: %q", out)
	}
	if _, out, _ := runArgs(t, "version", "-json"); !strings.Contains(out, `"commit":"`+sha+`-dirty"`) {
		t.Errorf("version -json with a modified VCS stamp: %q", out)
	}
}

// TestConstantsMatchKnobs: constants.go mirrors the defaults of the Makefile laug replaced, as internal/script's knob
// table keeps them, the Makefile's SERVER_TAG_DEFAULT, which its Firecracker targets still read, and go.mod's go line;
// this fails when they part.
func TestConstantsMatchKnobs(t *testing.T) {
	for name, want := range map[string]string{"SERVER_TAG": serverTagDefault, "OMES_REF": omesRef,
		"OMES_URL": omesURL, "PROJECT": projectDefault, "NAMESPACE": namespaceDefault, "SCENARIO": scenarioDefault,
		"OMES_DIR": omesDir, "CELL_ADDRESS": cellAddressDefault, "PROM_URL": promDefault, "PLAN": planDefault,
		"BASE": baseDefault, "GATE_POST": gatePostDefault, "GATE_RESULTS": gateResultsDefault,
		"SECONDS": fmt.Sprint(clockSecondsDefault), "TIMER": timerDefault} {
		if k, ok := script.Lookup(name); !ok || k.Default != want {
			t.Errorf("knob %s: default %q, constants.go %q", name, k.Default, want)
		}
	}
	mk, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^SERVER_TAG_DEFAULT := (\S+)$`).FindSubmatch(mk); m == nil ||
		string(m[1]) != serverTagDefault {
		t.Errorf("Makefile SERVER_TAG_DEFAULT: %q, constants.go %q", m, serverTagDefault)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^go (\S+)$`).FindSubmatch(mod); m == nil || string(m[1]) != goModGo {
		t.Errorf("go.mod's go line: %q, constants.go %q", m, goModGo)
	}
}
