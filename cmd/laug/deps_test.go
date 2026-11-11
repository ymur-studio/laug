package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// depsStubs are the tools deps asks, as stubs that answer as the dev host's did, each steered by a STUB_ variable in
// the App's environment, so a test also sees that every probe gets that environment. go answers only from /. git
// answers HEAD with STUB_HEAD, else headSHA, and status with STUB_STATUS, as gitStub does.
var depsStubs = map[string]string{
	"go": `case "$*" in
"env GOVERSION GOTOOLCHAIN") [ "$(pwd)" = / ] || { echo "go: asked from $(pwd)" >&2; exit 3; }
  printf 'go%s\n%s\n' "${STUB_GO-1.26.5}" "${STUB_TC-local}" ;;
*) exit 2 ;;
esac`,
	"git": `case "$*" in
--version) echo "git version 2.43.0" ;;
"rev-parse HEAD") echo "${STUB_HEAD-` + headSHA + `}" ;;
"status --porcelain"|"status --porcelain -- . :(exclude)gate/results") [ -z "$STUB_STATUS" ] || echo "$STUB_STATUS" ;;
*) exit 2 ;;
esac`,
	"docker": `case "$*" in
"compose version --short")
  [ -z "$STUB_NO_COMPOSE" ] || { echo "docker: 'compose' is not a docker command." >&2; exit 1; }
  echo 2.29.1 ;;
"version --format {{.Server.Version}}") [ -z "$STUB_NO_DAEMON" ] || { echo "Cannot connect to the daemon" >&2; exit 1; }
  echo 27.3.1 ;;
*) exit 2 ;;
esac`,
	"temporal": `case "$*" in
--version) echo "temporal version 1.4.1 (Server 1.28.0, UI 2.38.3)" ;;
"server start-dev --help") echo "  --http-port int"; [ -n "$STUB_OLD_TEMPORAL" ] || echo "  --metrics-port int" ;;
*) exit 2 ;;
esac`,
	"jq":     `[ "$*" = --version ] && echo "jq-${STUB_JQ-1.7.1}"`,
	"yq":     `[ "$*" = --version ] && echo "${STUB_YQ-yq (https://github.com/mikefarah/yq/) version v4.54.1}"`,
	"bc":     `[ "$*" = --version ] && echo "bc 1.07.1"`,
	"curl":   `[ "$*" = --version ] && echo "${STUB_CURL_LINE-curl ${STUB_CURL-8.5.0} (x86_64-pc-linux-gnu) libcurl/8}"`,
	"ss":     `[ "$*" = --version ] && echo "ss utility, iproute2-6.1.0"`,
	"setsid": `[ "$*" = --version ] && echo "setsid from util-linux 2.39.3"`,
	"flock":  `[ "$*" = --version ] && echo "flock from util-linux 2.39.3"`,
	"env": `case "$*" in
--version) echo "env (GNU coreutils) 9.4" ;;
"--default-signal=INT,QUIT --version") [ -z "$STUB_OLD_ENV" ] || { echo "env: unrecognized option" >&2; exit 125; }
  echo "env (GNU coreutils) 9.4" ;;
*) exit 2 ;;
esac`,
	"cmp":  `[ "$*" = --version ] && echo "cmp (GNU diffutils) 3.10"`,
	"diff": `[ "$*" = --version ] && echo "diff (GNU diffutils) 3.10"`,
}

// depsWorld is a PATH of depsStubs but those named in without, and the STUB_ settings given, as an environment.
func depsWorld(t *testing.T, without []string, settings ...string) []string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range depsStubs {
		if !slices.Contains(without, name) {
			writeExec(t, filepath.Join(dir, name), "#!/bin/sh\n"+body+"\n")
		}
	}
	return append([]string{"PATH=" + dir}, settings...)
}

// depsHost stands in for the host deps -lab measures and the /dev/kvm it looks for: vcpu, MemAvailable in kB, and
// whether the device is there.
func depsHost(t *testing.T, vcpu int, availKB int64, kvm bool) {
	t.Helper()
	savedCPU, savedMem, savedKVM := numCPU, meminfoPath, kvmPath
	t.Cleanup(func() { numCPU, meminfoPath, kvmPath = savedCPU, savedMem, savedKVM })
	numCPU = func() int { return vcpu }
	dir := t.TempDir()
	meminfoPath = filepath.Join(dir, "meminfo")
	writeFile(t, meminfoPath, fmt.Sprintf("MemTotal:       49283072 kB\nMemFree:         1000000 kB\n"+
		"MemAvailable:   %8d kB\n", availKB))
	kvmPath = filepath.Join(dir, "kvm")
	if kvm {
		writeFile(t, kvmPath, "")
	}
}

// withOmes gives root a clone at .upstream/omes whose HEAD holds head, and a gate/host-type.sh, hostTypeStub.
func withOmes(t *testing.T, root, head string) {
	t.Helper()
	if head != "" {
		writeFile(t, filepath.Join(root, ".upstream", "omes", ".git", "HEAD"), head+"\n")
	}
	writeExec(t, filepath.Join(root, "gate", "host-type.sh"), hostTypeStub)
}

// hostTypeStub stands in for gate/host-type.sh under JSON=1, as laug host type -json runs it: the type declared is
// STUB_DECLARED, m8i.8xlarge unless set, none when empty; a gate refuses the host with STUB_REFUSE as the reason when
// it is set; STUB_HOST_FAIL makes it fail as a script with no yq would. It answers only from /, as deps asks, and only
// with LAUG set, as laug sets it for every script, since the reason names a line to type.
const hostTypeStub = `#!/bin/sh
[ "$JSON" = 1 ] && [ "$LAUG" = laug ] || { echo "host-type.sh: JSON=$JSON LAUG=$LAUG" >&2; exit 3; }
[ "$(pwd)" = / ] || { echo "host-type.sh: asked from $(pwd)" >&2; exit 3; }
[ -z "$STUB_HOST_FAIL" ] || { echo "load/thresholds.sh: yq: not found" >&2; exit 2; }
d="\"${STUB_DECLARED-m8i.8xlarge}\""; [ "$d" != '""' ] || d=null
p=true r=null; [ -z "$STUB_REFUSE" ] || { p=false r="\"$STUB_REFUSE\""; }
echo "{\"declared\":$d,\"vcpu\":32,\"mem_gib\":123,\"set\":{\"type\":\"m8i.8xlarge\",\"vcpu\":32,\"mem_gib\":123},\"passes\":$p,\"reason\":$r}"
`

// depsRows is deps's text output after its first line, one row a line, but the -offline closing line: status, name,
// version.
func depsRows(out string) [][3]string {
	var rows [][3]string
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	row := regexp.MustCompile(`^  (ok|MISSING|note) +(.+?) {2,}(\S+) {2}`)
	for _, l := range lines[1:] {
		if m := row.FindStringSubmatch(l); m != nil {
			rows = append(rows, [3]string{m[1], m[2], m[3]})
		}
	}
	return rows
}

func TestDeps(t *testing.T) {
	depsHost(t, 24, 31_000_000, true)
	stamp(t, headSHA, nil) // bin/laug built from the stub's HEAD
	defaultNames := []string{"go", "git", "bin/laug", "docker compose", "temporal", "jq", "yq", "bc", "curl", "ss",
		"setsid", "flock", "env", "cmp, diff", "/dev/kvm"}
	offlineNames := []string{"go", "git", "bin/laug", "jq", "yq", "bc", "curl", "ss", "setsid", "flock", "env",
		"cmp, diff"}
	labNames := append(slices.Clone(defaultNames), "docker engine", "omes", "this host", "host type")
	cellNames := []string{"go", "git", "bin/laug", "docker compose", "temporal", "jq", "yq", "bc", "curl", "ss", "setsid",
		"cmp, diff", "docker engine", "omes"}

	t.Run("every set, every tool there", func(t *testing.T) {
		for _, c := range []struct {
			args  []string
			first string
			names []string
		}{
			{nil, "laug · deps: 0 of 15 missing, 0 of the offline path's 12: the tools laug's scripts call", defaultNames},
			{[]string{"-offline"}, "laug · deps -offline: 0 of 12 missing: what make build, thresholds, gate run -fake, " +
				"gate rejudge and make harness-test call", offlineNames},
			{[]string{"-lab"}, "laug · deps -lab: 0 of 19 missing: the tools laug's scripts call, the docker daemon, " +
				"Omes and this host", labNames},
			{[]string{"-cell"}, "laug · deps -cell: 0 of 14 missing: what a lease, a load and a raun on compose cells " +
				"call, the docker daemon and Omes among them", cellNames},
		} {
			root := checkout(t)
			withOmes(t, root, omesRef)
			env := depsWorld(t, nil)
			code, out, errOut := runApp(t, &App{Root: root, Environ: env}, append([]string{"deps"}, c.args...)...)
			if code != 0 || errOut != "" || firstLine(out) != c.first {
				t.Errorf("deps %q: exit %d\n%s%s", c.args, code, out, errOut)
			}
			var names []string
			for _, r := range depsRows(out) {
				names = append(names, r[1])
				if r[0] != "ok" {
					t.Errorf("deps %q: %s is %s", c.args, r[1], r[0])
				}
			}
			if !slices.Equal(names, c.names) {
				t.Errorf("deps %q checks %q, want %q", c.args, names, c.names)
			}
			if bare := strings.HasSuffix(out, "\n  exit 1 only for the offline path's tools, -offline's list; -cell "+
				"and -lab exit 1 on any of theirs\n"); bare != (len(c.args) == 0) {
				t.Errorf("deps %q: the exit line is there: %t\n%s", c.args, bare, out)
			}
			left := strings.HasSuffix(out, "\n  left out: "+offlineLeftOut+"\n")
			if left != slices.Equal(c.args, []string{"-offline"}) {
				t.Errorf("deps %q: the left-out line is there: %t\n%s", c.args, left, out)
			}
			if dir := strings.TrimPrefix(env[0], "PATH="); strings.Contains(out, dir) || strings.Contains(out, root) {
				t.Errorf("deps %q printed a local path:\n%s", c.args, out)
			}
		}
	})

	t.Run("versions", func(t *testing.T) {
		code, out, _ := runApp(t, &App{Environ: depsWorld(t, nil)}, "deps")
		want := map[string]string{"go": "1.26.5", "git": "2.43.0", "docker compose": "2.29.1", "temporal": "1.4.1",
			"jq": "1.7.1", "yq": "4.54.1", "bc": "1.07.1", "curl": "8.5.0", "ss": "6.1.0", "setsid": "2.39.3",
			"flock": "2.39.3", "env": "9.4", "cmp, diff": "3.10", "/dev/kvm": "-", "bin/laug": "-"}
		for _, r := range depsRows(out) {
			if want[r[1]] != r[2] {
				t.Errorf("%s: version %q, want %q", r[1], r[2], want[r[1]])
			}
		}
		if code != 0 {
			t.Errorf("exit %d:\n%s", code, out)
		}
	})

	// Each tool gone in turn: the default set misses it, and it and -offline exit 1 only for a tool the offline path
	// calls (exit 1 is a tool missing: of the offline set, or any with -lab), so a CI runner with no temporal CLI or
	// Docker passes bare deps. /dev/kvm gone is a note.
	t.Run("one tool gone", func(t *testing.T) {
		for _, c := range []struct {
			name    string
			stubs   []string
			offline bool
		}{
			{"go", []string{"go"}, true}, {"git", []string{"git"}, true}, {"docker compose", []string{"docker"}, false},
			{"temporal", []string{"temporal"}, false}, {"jq", []string{"jq"}, true}, {"yq", []string{"yq"}, true},
			{"bc", []string{"bc"}, true}, {"curl", []string{"curl"}, true}, {"ss", []string{"ss"}, true},
			{"setsid", []string{"setsid"}, true}, {"flock", []string{"flock"}, true}, {"env", []string{"env"}, true},
			{"cmp, diff", []string{"cmp"}, true}, {"cmp, diff", []string{"diff"}, true},
		} {
			env := depsWorld(t, c.stubs)
			want := map[bool]int{true: 1, false: 0}[c.offline]
			code, out, _ := runApp(t, &App{Environ: env}, "deps")
			first := fmt.Sprintf("laug · deps: 1 of 15 missing, %d of the offline path's 12: ", want)
			if code != want || !slices.Contains(depsRows(out), [3]string{"MISSING", c.name, "-"}) ||
				!strings.HasPrefix(out, first) {
				t.Errorf("deps without %s: exit %d, want %d\n%s", c.stubs, code, want, out)
			}
			code, out, _ = runApp(t, &App{Environ: env}, "deps", "-offline")
			if code != want {
				t.Errorf("deps -offline without %s: exit %d, want %d\n%s", c.stubs, code, want, out)
			}
			code, out, _ = runApp(t, &App{Environ: env}, "deps", "-lab")
			if code != 1 {
				t.Errorf("deps -lab without %s: exit %d, want 1\n%s", c.stubs, code, out)
			}
		}
		depsHost(t, 24, 31_000_000, false)
		code, out, _ := runApp(t, &App{Environ: depsWorld(t, nil)}, "deps")
		if code != 0 || !slices.Contains(depsRows(out), [3]string{"note", "/dev/kvm", "-"}) {
			t.Errorf("deps with no /dev/kvm: exit %d\n%s", code, out)
		}
	})

	t.Run("versions laug does not take", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			settings []string
			args     []string
			status   string // the row's
			code     int
			why      string // in the row
		}{
			{"go", []string{"STUB_GO=1.24.1", "STUB_TC=auto"}, nil, "note", 0,
				"GOTOOLCHAIN=auto downloads it on the first build"},
			{"go", []string{"STUB_GO=1.24.1", "STUB_TC=go1.24.1+auto"}, nil, "note", 0, "downloads it"},
			{"go", []string{"STUB_GO=1.24.1", "STUB_TC=local"}, nil, "MISSING", 1, "GOTOOLCHAIN=local downloads none"},
			// go.mod's Go builds bin/laug, and only Omes needs 1.26.5, which the offline path never builds: bare deps
			// says MISSING and exits 0, -lab exits 1.
			{"go", []string{"STUB_GO=1.26.0"}, nil, "MISSING", 0, "Omes 1.26.5"},
			{"go", []string{"STUB_GO=1.26.0"}, []string{"-lab"}, "MISSING", 1, "Omes 1.26.5"},
			{"go", []string{"STUB_GO=1.26.0"}, []string{"-offline"}, "ok", 0, "go.mod's 1.26.0"},
			{"go", []string{"STUB_GO=1.27rc1"}, nil, "ok", 0, ""},
			{"curl", []string{"STUB_CURL=7.68.0"}, nil, "MISSING", 1, "--fail-with-body, new in 7.76.0"},
			{"curl", []string{"STUB_CURL_LINE=curl, a build of no number"}, nil, "MISSING", 1, "its --version gives no version"},
			{"curl", []string{"STUB_CURL=7.76.0"}, nil, "ok", 0, "--fail-with-body"},
			// jq 1.7 or later passes, 1.7.x and a later one alike, and the row says 1.7 or later.
			{"jq", []string{"STUB_JQ=1.6"}, nil, "MISSING", 1, "every gate script and load/thresholds.sh call it"},
			{"jq", []string{"STUB_JQ=1.6"}, []string{"-offline"}, "MISSING", 1, ""},
			{"jq", []string{"STUB_JQ=1.8.1"}, []string{"-offline"}, "ok", 0, "1.7 or later: every gate script"},
			{"jq", []string{"STUB_JQ=1.8.1"}, nil, "ok", 0, "1.7 or later: every gate script"},
			{"jq", []string{"STUB_JQ=1.7"}, nil, "ok", 0, "1.7 or later: every gate script"},
			{"yq", []string{"STUB_YQ=yq 3.2.3"}, nil, "MISSING", 1, `the yq on PATH says "yq 3.2.3"`},
			{"yq", []string{"STUB_YQ=yq (https://github.com/mikefarah/yq/) version v3.4.1"}, nil, "MISSING", 1,
				"mikefarah/yq v4"},
			{"temporal", []string{"STUB_OLD_TEMPORAL=1"}, nil, "MISSING", 0,
				"server start-dev takes --metrics-port, for laug orchestrator server"},
			{"docker compose", []string{"STUB_NO_COMPOSE=1"}, nil, "MISSING", 0, "not docker-compose"},
			{"docker compose", []string{"STUB_NO_COMPOSE=1"}, []string{"-lab"}, "MISSING", 1, "not docker-compose"},
			{"env", []string{"STUB_OLD_ENV=1"}, nil, "MISSING", 1, "takes no --default-signal: gate/lib.sh"},
		} {
			args := append([]string{"deps"}, c.args...)
			code, out, _ := runApp(t, &App{Environ: depsWorld(t, nil, c.settings...)}, args...)
			var line string
			for _, l := range strings.Split(out, "\n") {
				if r := depsRows("\n" + l); len(r) == 1 && r[0][1] == c.name {
					line = l
				}
			}
			if code != c.code || !strings.HasPrefix(line, "  "+c.status+" ") || !strings.Contains(line, c.why) {
				t.Errorf("%s with %q: exit %d, want %d; row %q, want %s and %q", strings.Join(args, " "), c.settings,
					code, c.code, line, c.status, c.why)
			}
		}
	})

	t.Run("-lab", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			head     string // .upstream/omes's HEAD; "" no clone
			root     bool   // in a checkout
			vcpu     int
			availKB  int64
			settings []string
			missing  map[string]string // the rows MISSING and a part of each one's text
		}{
			{"a lab host", omesRef, true, 32, 120_000_000, nil, nil},
			{"no Omes", "", true, 32, 120_000_000, nil, map[string]string{"omes": "laug upstream clones it"}},
			{"Omes at another commit", strings.Repeat("ab", 20), true, 32, 120_000_000, nil,
				map[string]string{"omes": ".upstream/omes is at abababa, not f0de206: laug upstream checks f0de206 out"}},
			{"Omes on a branch", "ref: refs/heads/nowhere", true, 32, 120_000_000, nil,
				map[string]string{"omes": "HEAD names refs/heads/nowhere"}},
			{"outside a checkout", "", false, 32, 120_000_000, nil, map[string]string{"omes": "not in a laug checkout",
				"host type": "not in a laug checkout, so no gate/host-type.sh to ask"}},
			{"8 vCPU", omesRef, true, 8, 120_000_000, nil,
				map[string]string{"this host": "8 vCPU and 114.4 GiB available; a gate refuses under 16 vCPU or 8 GiB"}},
			{"7.9 GiB available", omesRef, true, 16, 8_336_180, nil,
				map[string]string{"this host": "16 vCPU and 7.9 GiB available"}},
			{"no daemon", omesRef, true, 32, 120_000_000, []string{"STUB_NO_DAEMON=1"},
				map[string]string{"docker engine": "no daemon answered docker version: exit status 1: Cannot connect"}},
			// The host type is what laug host type says: a gate on cells passes it, or refuses it with the reason.
			{"a host type declared that a gate passes", omesRef, true, 32, 120_000_000, nil, nil},
			{"no host type declared", omesRef, true, 32, 120_000_000, []string{"STUB_DECLARED=",
				"STUB_REFUSE=this host declares no host type"},
				map[string]string{"host type": "  MISSING  host type       -         this host declares no host type"}},
			{"another host type declared", omesRef, true, 32, 120_000_000, []string{"STUB_DECLARED=m7i.8xlarge",
				"STUB_REFUSE=this host's declared type is not m8i.8xlarge"},
				map[string]string{"host type": "m7i.8xlarge  this host's declared type is not m8i.8xlarge"}},
			{"no host-type.sh", omesRef, true, 32, 120_000_000, nil,
				map[string]string{"host type": "no gate/host-type.sh in this checkout to ask"}},
			{"host-type.sh fails", omesRef, true, 32, 120_000_000, []string{"STUB_HOST_FAIL=1"},
				map[string]string{"host type": "gate/host-type.sh did not answer: exit status 2: load/thresholds.sh: " +
					"yq: not found; laug host type says more"}},
		} {
			t.Run(c.name, func(t *testing.T) {
				depsHost(t, c.vcpu, c.availKB, true)
				a := &App{Environ: depsWorld(t, nil, c.settings...)}
				if c.root {
					a.Root = checkout(t)
					withOmes(t, a.Root, c.head)
					if c.name == "no host-type.sh" {
						os.Remove(filepath.Join(a.Root, "gate", "host-type.sh"))
					}
				}
				code, out, _ := runApp(t, a, "deps", "-lab")
				want := map[bool]int{true: 1, false: 0}[len(c.missing) > 0]
				if code != want || !strings.HasPrefix(out, fmt.Sprintf("laug · deps -lab: %d of 19 missing", len(c.missing))) {
					t.Errorf("exit %d, want %d\n%s", code, want, out)
				}
				for _, l := range strings.Split(out, "\n") {
					r := depsRows("\n" + l)
					if len(r) != 1 {
						continue
					}
					part, isMissing := c.missing[r[0][1]]
					if (r[0][0] == "MISSING") != isMissing || !strings.Contains(l, part) {
						t.Errorf("row %q; want MISSING %t with %q", l, isMissing, part)
					}
				}
			})
		}
	})

	// -cell exits 1 on any of its checks missing, the daemon and Omes among them, Go held to Omes's line; what only a
	// gate or the Firecracker backend needs, and the host's size and type, it does not check.
	t.Run("-cell", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			head     string // .upstream/omes's HEAD; "" no clone
			vcpu     int
			stubs    []string // tools gone
			settings []string
			missing  map[string]string // the rows MISSING and a part of each one's text
		}{
			{"a cell host, 8 vCPU", omesRef, 8, nil, nil, nil},
			{"no Omes", "", 24, nil, nil, map[string]string{"omes": "laug upstream clones it"}},
			{"no daemon", omesRef, 24, nil, []string{"STUB_NO_DAEMON=1"},
				map[string]string{"docker engine": "no daemon answered docker version"}},
			{"no temporal", omesRef, 24, []string{"temporal"}, nil, map[string]string{"temporal": "not on PATH"}},
			{"go.mod's Go, not Omes's", omesRef, 24, nil, []string{"STUB_GO=1.26.0"},
				map[string]string{"go": "Omes 1.26.5"}},
			{"no flock, an env with no --default-signal", omesRef, 24, []string{"flock"}, []string{"STUB_OLD_ENV=1"},
				nil},
			{"no host type declared", omesRef, 24, nil, []string{"STUB_DECLARED=", "STUB_REFUSE=no type"}, nil},
		} {
			t.Run(c.name, func(t *testing.T) {
				depsHost(t, c.vcpu, 120_000_000, false)
				a := &App{Root: checkout(t), Environ: depsWorld(t, c.stubs, c.settings...)}
				withOmes(t, a.Root, c.head)
				code, out, _ := runApp(t, a, "deps", "-cell")
				want := map[bool]int{true: 1, false: 0}[len(c.missing) > 0]
				if code != want || !strings.HasPrefix(out, fmt.Sprintf("laug · deps -cell: %d of 14 missing",
					len(c.missing))) {
					t.Errorf("exit %d, want %d\n%s", code, want, out)
				}
				var names []string
				for _, l := range strings.Split(out, "\n") {
					r := depsRows("\n" + l)
					if len(r) != 1 {
						continue
					}
					names = append(names, r[0][1])
					part, isMissing := c.missing[r[0][1]]
					if (r[0][0] == "MISSING") != isMissing || !strings.Contains(l, part) {
						t.Errorf("row %q; want MISSING %t with %q", l, isMissing, part)
					}
				}
				if !slices.Equal(names, cellNames) {
					t.Errorf("-cell checks %q, want %q", names, cellNames)
				}
			})
		}
	})

	// bin/laug is held to HEAD as gate run holds it (checkStamp): built from another commit, or not there, it is
	// MISSING, and bare, -offline and -lab exit 1, since gate run -fake refuses it and gate rejudge judges nothing.
	// Built from HEAD with changes, in a tree that has some, it is a note; outside a checkout, a note too.
	t.Run("bin/laug", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			stamp    string
			err      error
			root     bool
			settings []string
			status   string // the row's, and its version, need and a part of its why in -json
			version  any
			need     any
			why      string
		}{
			{"built from HEAD", headSHA, nil, true, nil, "ok", "2222222", "2222222", "built from HEAD"},
			{"built from another commit", otherSHA, nil, true, nil, "missing", "1111111", "2222222",
				"bin/laug was built from 1111111, and HEAD is 2222222: make build builds it from HEAD; until then " +
					"gate run -fake refuses it, and gate rejudge judges nothing"},
			{"not built", "", os.ErrNotExist, true, nil, "missing", nil, "2222222",
				"there is no bin/laug: make build builds it"},
			{"no HEAD", headSHA, nil, true, []string{"STUB_HEAD="}, "missing", "2222222", nil, "git rev-parse HEAD"},
			{"built from HEAD with changes, the tree has some", headSHA + "-dirty", nil, true,
				[]string{"STUB_STATUS= M cmd/laug/deps.go"}, "note", "2222222-dirty", "2222222",
				"bin/laug was built from HEAD with changes not committed, and the tree has some"},
			{"outside a checkout", headSHA, nil, false, nil, "note", nil, nil, "not in a laug checkout"},
		} {
			t.Run(c.name, func(t *testing.T) {
				stamp(t, c.stamp, c.err)
				a := &App{Environ: depsWorld(t, nil, c.settings...)}
				if c.root {
					a.Root = checkout(t)
					withOmes(t, a.Root, omesRef)
				}
				want := map[string]int{"ok": 0, "note": 0, "missing": 1}[c.status]
				sets := [][]string{nil, {"-offline"}, {"-lab"}}
				if !c.root {
					sets = sets[:2] // -lab misses Omes outside a checkout
				}
				for _, args := range sets {
					code, out, _ := runApp(t, a, append(append([]string{"deps"}, args...), "-json")...)
					var r depsReport
					if err := json.Unmarshal([]byte(out), &r); err != nil || code != want {
						t.Errorf("deps %q -json: exit %d, want %d, %v\n%s", args, code, want, err, out)
						continue
					}
					var got map[string]any
					for _, row := range r.Checks {
						if row.Name == "bin/laug" {
							b, _ := json.Marshal(row)
							json.Unmarshal(b, &got)
						}
					}
					if got["status"] != c.status || got["version"] != c.version || got["need"] != c.need ||
						!strings.Contains(fmt.Sprint(got["why"]), c.why) {
						t.Errorf("deps %q: the bin/laug row %v, want %s, version %v, need %v, why %q", args, got,
							c.status, c.version, c.need, c.why)
					}
				}
			})
		}
	})

	// The text table keeps its columns: the version column is as wide as its longest value, so a stamp with -dirty,
	// 13 characters, puts no row out of line.
	t.Run("a -dirty stamp in the table", func(t *testing.T) {
		stamp(t, headSHA+"-dirty", nil)
		a := &App{Environ: depsWorld(t, nil, "STUB_STATUS= M cmd/laug/deps.go"), Root: checkout(t)}
		withOmes(t, a.Root, omesRef)
		_, js, _ := runApp(t, a, "deps", "-json")
		var r depsReport
		if err := json.Unmarshal([]byte(js), &r); err != nil {
			t.Fatal(err)
		}
		_, out, _ := runApp(t, a, "deps")
		rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")[1 : len(r.Checks)+1]
		at, dirty := -1, false
		for i, c := range r.Checks {
			col := strings.Index(rows[i], "  "+c.Why)
			if c.Name == "bin/laug" {
				dirty = c.Version != nil && *c.Version == "2222222-dirty"
			}
			if at < 0 {
				at = col
			}
			if col < 0 || col != at {
				t.Errorf("row %q: its why at column %d, the first row's at %d\n%s", c.Name, col, at, out)
			}
		}
		if !dirty {
			t.Errorf("no bin/laug row stamped 2222222-dirty:\n%s", js)
		}
	})

	t.Run("-json", func(t *testing.T) {
		code, out, errOut := runApp(t, &App{Environ: depsWorld(t, []string{"bc"}, "STUB_GO=1.24.1", "STUB_TC=auto")},
			"deps", "-json")
		var r struct {
			Set            string
			Missing        int
			MissingOffline int `json:"missing_offline"`
			Checks         []map[string]any
		}
		dec := json.NewDecoder(strings.NewReader(out))
		dec.DisallowUnknownFields()
		// bc is a tool of the offline path's, make harness-test's, so deps exits 1 with it missing.
		if err := dec.Decode(&r); err != nil || dec.More() || code != 1 || errOut != "" || strings.Count(out, "\n") != 1 {
			t.Fatalf("deps -json: exit %d, %v\n%s%s", code, err, out, errOut)
		}
		if r.Set != "default" || r.Missing != 1 || r.MissingOffline != 1 || len(r.Checks) != 15 {
			t.Errorf("set %q, missing %d, missing_offline %d, %d checks", r.Set, r.Missing, r.MissingOffline,
				len(r.Checks))
		}
		for _, c := range r.Checks {
			if len(c) != 5 {
				t.Errorf("a check has %d fields, want name, status, version, need and why: %v", len(c), c)
			}
			switch c["name"] {
			case "go":
				if c["status"] != "note" || c["version"] != "1.24.1" || c["need"] != "1.26.5" {
					t.Errorf("go: %v", c)
				}
			case "bc":
				if c["status"] != "missing" || c["version"] != nil || c["need"] != nil {
					t.Errorf("bc: %v", c)
				}
			case "curl":
				if c["status"] != "ok" || c["version"] != "8.5.0" || c["need"] != "7.76.0" {
					t.Errorf("curl: %v", c)
				}
			}
		}
		code, out, _ = runApp(t, &App{Environ: depsWorld(t, nil)}, "deps", "-offline", "-json")
		if code != 0 || !strings.HasPrefix(out, `{"set":"offline","missing":0,"missing_offline":0,"checks":[{"name":"go",`+
			`"status":"ok"`) {
			t.Errorf("deps -offline -json: exit %d\n%s", code, out)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		for _, c := range []struct {
			args []string
			line string
		}{
			{[]string{"-offline", "-lab"}, "laug · deps: -offline, -cell and -lab: one set at a time"},
			{[]string{"-cell", "-lab"}, "laug · deps: -offline, -cell and -lab: one set at a time"},
			{[]string{"-offline", "-cell"}, "laug · deps: -offline, -cell and -lab: one set at a time"},
			{[]string{"go"}, `laug · deps: takes no arguments, got ["go"]`},
			{[]string{"-all"}, "laug · deps: no flag -all"},
		} {
			code, out, errOut := runApp(t, &App{Environ: depsWorld(t, nil)}, append([]string{"deps"}, c.args...)...)
			if code != 2 || out != "" || firstLine(errOut) != c.line || !strings.Contains(errOut, "usage: laug deps") {
				t.Errorf("deps %q: exit %d\n%s%s", c.args, code, out, errOut)
			}
		}
	})

	// The recipe's tools are all checked: REQUIRED_TOOLS as the Makefile laug replaced held it, docker by its
	// compose plugin.
	t.Run("the recipe's tools", func(t *testing.T) {
		for _, tool := range strings.Fields("go git docker temporal jq yq bc curl ss setsid flock") {
			if tool == "docker" {
				tool = "docker compose"
			}
			if !slices.ContainsFunc(depsChecks, func(c depsCheck) bool { return c.name == tool }) {
				t.Errorf("REQUIRED_TOOLS names %s, which deps does not check", tool)
			}
		}
	})
}

func TestAtLeast(t *testing.T) {
	for _, c := range []struct {
		have, need string
		want       bool
	}{
		{"1.26.5", "1.26.5", true}, {"1.26.10", "1.26.5", true}, {"1.26.0", "1.26.5", false},
		{"1.27", "1.26.5", true}, {"1.26", "1.26.0", true}, {"8.5.0", "7.76.0", true}, {"7.68.0", "7.76.0", false},
		{"2", "2", true}, {"1.9", "2", false},
	} {
		if got := atLeast(c.have, c.need); got != c.want {
			t.Errorf("atLeast(%q, %q) = %t, want %t", c.have, c.need, got, c.want)
		}
	}
}
