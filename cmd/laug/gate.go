package main

// laug gate: the release gate. run is gate/run.sh with its environment (gateEnv), or with -workflow the orchestrator's
// starter; post hands a run's verdict on (gate/post.sh); rejudge, compare and signature are the same-verdict checks, in
// Go (internal/rejudge); tolerance is the incumbent arm's, in Go (internal/tolerance), from the set load/thresholds.sh
// resolves; open names a run's directory and its verdict. status, cancel, terminate and stop, on a gate that runs as
// workflow Gate, are in gateflow.go.

import (
	"bufio"
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/rejudge"
	"github.com/ymur-studio/laug/internal/script"
	"github.com/ymur-studio/laug/internal/tolerance"
)

// gateIDRE is a gate run's id as gate/run.sh makes it: g<UTC date>-<UTC time>, and -<n> for a second run in that
// second.
var gateIDRE = regexp.MustCompile(`^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)

// checkGateID refuses a gate id that is not one.
func checkGateID(id string) error {
	if !gateIDRE.MatchString(id) {
		return fmt.Errorf("%s: not a gate id, g<UTC date>-<UTC time>, as gate run names one", shellWord(id))
	}
	return nil
}

// runRef is a gate run as a verb takes it: an id, one word, read in -results; or, holding a slash, the path to a run's
// directory (runDir).
type runRef struct {
	arg  string // as given
	path string // the run's directory: -results/<id>, or the path given
}

// gateRun reads a gate run argument, or an error that names it.
func gateRun(results, arg string) (runRef, error) {
	if strings.Contains(arg, "/") {
		return runRef{arg, arg}, nil
	}
	if !wordRE.MatchString(arg) {
		return runRef{}, fmt.Errorf("%s: neither a gate id in %s, one word of letters, digits, ., _ and -, nor the "+
			"path to a run's directory", shellWord(arg), results)
	}
	return runRef{arg, filepath.Join(results, arg)}, nil
}

// abs is path as laug opens it: under the checkout's root when relative, as the scripts, run from there, read it;
// outside a checkout, as given.
func (a *App) abs(path string) string {
	if filepath.IsAbs(path) || a.Root == "" {
		return path
	}
	return filepath.Join(a.Root, path)
}

// pathShown is an absolute path as laug prints it, naming no home path: relative to the checkout's root when below
// it, ~/ and the rest when below $HOME, else as it is. A relative path is left as given.
func (a *App) pathShown(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	path = filepath.Clean(path)
	if a.Root != "" {
		if rel, ok := strings.CutPrefix(path, filepath.Clean(a.Root)+string(filepath.Separator)); ok {
			return rel
		}
	}
	if home := a.getenv("HOME"); filepath.IsAbs(home) && filepath.Clean(home) != "/" {
		if rel, ok := strings.CutPrefix(path, filepath.Clean(home)+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return path
}

// wordShown is pathShown as one shell word: quoted when it needs it, with a leading ~/ left out of the quotes, so a
// shell still reads it as $HOME.
func (a *App) wordShown(path string) string {
	s := a.pathShown(path)
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		return "~/" + shellWord(rest)
	}
	return shellWord(s)
}

// textShown is s, a line laug prints, with each absolute path in it written as pathShown writes one: relative to the
// checkout's root when below it, ~/ and the rest when below $HOME. A path counts where it starts s or follows a space, a
// quote, =, :, ( or [, as an error's text names its paths; one in single quotes gets its ~/ outside them, as wordShown
// writes one, so the word still pastes.
func (a *App) textShown(s string) string {
	if a.Root != "" && filepath.IsAbs(a.Root) {
		s = prefixShown(s, filepath.Clean(a.Root)+string(filepath.Separator), "", "'")
	}
	if home := a.getenv("HOME"); filepath.IsAbs(home) && filepath.Clean(home) != "/" {
		s = prefixShown(s, filepath.Clean(home)+string(filepath.Separator), "~/", "~/'")
	}
	return s
}

// prefixShown is s with each prefix that starts a path written as with, or as quoted after a single quote.
func prefixShown(s, prefix, with, quoted string) string {
	re := regexp.MustCompile(`(^|[\s"'=:(\[,])` + regexp.QuoteMeta(prefix))
	return re.ReplaceAllStringFunc(s, func(m string) string {
		lead := m[:len(m)-len(prefix)]
		if lead == "'" {
			return quoted
		}
		return lead + with
	})
}

// errShown is err's text as textShown writes it, for a line that prints an error: an error from the file system or
// from exec names its path whole.
func (a *App) errShown(err error) string {
	return a.textShown(err.Error())
}

// gateVerdict is what laug reads of a run's verdict.json.
type gateVerdict struct {
	GateID    string  `json:"gate_id"`
	Verdict   string  `json:"verdict"`
	Class     *string `json:"class"`
	ServerTag string  `json:"server_tag"`
	StoppedAt *string `json:"stopped_at"`
	Fake      bool    `json:"fake"`
}

// readVerdict reads dir's verdict.json.
func readVerdict(dir string) (gateVerdict, error) {
	var v gateVerdict
	b, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	return v, err
}

// latestRun is the newest gate run in results, by id, and what its verdict.json says; "" when there is none.
func (a *App) latestRun(results string) (string, string) {
	ents, err := os.ReadDir(a.abs(results))
	if err != nil {
		return "", ""
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() && gateIDRE.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return "", ""
	}
	slices.SortFunc(ids, compareGateIDs)
	id := ids[len(ids)-1]
	v, err := readVerdict(filepath.Join(a.abs(results), id))
	if err != nil || v.Verdict == "" {
		return id, "no verdict yet"
	}
	return id, v.Verdict
}

// compareGateIDs orders gate ids by time, then a second run in the same second after the first.
func compareGateIDs(x, y string) int {
	xs, xn, _ := strings.Cut(x[1:], "-")
	ys, yn, _ := strings.Cut(y[1:], "-")
	xt, xi, _ := strings.Cut(xn, "-")
	yt, yi, _ := strings.Cut(yn, "-")
	if c := strings.Compare(xs+xt, ys+yt); c != 0 {
		return c
	}
	n := func(s string) int { i, _ := strconv.Atoi(s); return i }
	return n(xi) - n(yi)
}

// noRun prints that a gate verb names no gate run and what it takes, want, a path in it written as laug writes one
// (textShown); then the newest run in results, with the
// line that names it when line is set, tail after it: the flags the verb requires; then the synopsis; and returns 2.
func (a *App) noRun(f *flags, results, want string, line bool, tail string) int {
	fmt.Fprintf(a.Stderr, "laug · %s: names no gate run: %s\n", f.cmd, a.textShown(want))
	if id, verdict := a.latestRun(results); id != "" {
		fmt.Fprintf(a.Stderr, "the newest run in %s is %s (%s)", a.pathShown(results), id, verdict)
		if line {
			typed := a.self() + " " + f.cmd + " " + id
			if results != gateResultsDefault {
				typed += " -results " + shellWord(results)
			}
			fmt.Fprintf(a.Stderr, ":\n  %s%s", typed, tail)
		}
		fmt.Fprintln(a.Stderr)
	} else {
		fmt.Fprintf(a.Stderr, "%s holds no gate run; %s gate run <tag> makes one\n", a.pathShown(results), a.self())
	}
	f.mistake(a.Stderr)
	return exitUsage
}

// oneRun is what a verb that takes one gate run wants, for noRun.
func oneRun(results string) string {
	return "give it a gate id in " + results + ", or the path to a run's directory"
}

// resultsFlag adds -results to a gate verb's flags.
func resultsFlag(f *flags) *string {
	return f.String("results", gateResultsDefault, "the `dir` that holds the gate runs; a relative path starts at the "+
		"checkout's root")
}

// gateRunOpts is gate run's flags.
type gateRunOpts struct {
	plan, post, results, base string
	items                     int
	fake, allowStale, jsonOut bool
	workflow                  bool
	orchestrator, otlp        string
}

func runGateRun(ctx context.Context, a *App, args []string) int {
	var o gateRunOpts
	f := a.newFlags("gate run", "<tag> [-plan gate/plan.yaml] [-items <n>] [-fake [-allow-stale]] [-post local] "+
		"[-results <dir>] [-base main] [-json | -workflow [-orchestrator "+orchAddressDefault+"] [-otlp "+
		"127.0.0.1:<port>]] [-dry-run]")
	f.StringVar(&o.plan, "plan", planDefault, "the plan `file`, whose items run at once; a relative path starts at the "+
		"checkout's root")
	f.IntVar(&o.items, "items", 0, "run the plan's first `n` items only, from 1; R2 BLOCKs a cut plan (unset, all)")
	f.BoolVar(&o.fake, "fake", false, "a gate on fakes, no Docker, cell or Omes: the run, the fakes' files and an "+
		"empty Omes go in a new dir under TMPDIR")
	f.StringVar(&o.post, "post", gatePostDefault, "the `sink` the verdict is handed to; local, the only one built, "+
		"prints it")
	f.StringVar(&o.results, "results", gateResultsDefault, "the `dir` the run's directory goes in, relative to the "+
		"checkout's root; with -fake, unset, the new dir under TMPDIR")
	f.StringVar(&o.base, "base", baseDefault, "the `commit` the bar's files are compared with: changed_vs_base")
	f.BoolVar(&o.allowStale, "allow-stale", false, "with -fake, run although bin/laug was not built from HEAD (on "+
		"cells, gate/run.sh's preflight refuses it all the same)")
	f.BoolVar(&o.jsonOut, "json", false, "print the run's verdict.json on one line; the gate's text goes to stderr")
	f.BoolVar(&o.workflow, "workflow", false, "run the gate as workflow Gate on this checkout's worker, through "+
		"the orchestrator's dev server, the starter in this process")
	f.StringVar(&o.orchestrator, "orchestrator", orchAddressDefault, "with -workflow, the orchestrator's dev "+
		"server, `host:port`")
	otlp := otlpFlag(f, "-workflow")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	o.otlp = *otlp
	switch {
	case len(pos) == 0:
		return a.usageError(f, "names no server release: give it the tag it judges, x.y.z (%s gate run %s "+
			"-fake runs a gate on fakes)", a.self(), serverTagDefault)
	case len(pos) > 1:
		return a.usageError(f, "takes one server release, got %q", pos)
	case o.allowStale && !o.fake:
		return a.usageError(f, "-allow-stale takes -fake: gate/run.sh's preflight refuses a gate on cells whose "+
			"bin/laug was not built from HEAD all the same, as a refused run in -results")
	case !o.workflow && (given(f, "orchestrator") || given(f, "otlp")):
		return a.usageError(f, "-orchestrator and -otlp go with -workflow, which runs the gate through the "+
			"orchestrator's dev server")
	case o.workflow && o.jsonOut:
		return a.usageError(f, "-json reads gate/run.sh's run; with -workflow the starter prints the gate's report, "+
			"and verdict.json is in the run's directory")
	case o.workflow && *dry:
		return a.usageError(f, "-dry-run prints gate/run.sh's line; with -workflow the starter runs in laug's own "+
			"process, and there is no line to print")
	}
	tag := pos[0]
	var err error
	switch {
	case !poolapi.TagRE.MatchString(tag):
		err = fmt.Errorf("%s: not a release tag, x.y.z", shellWord(tag))
	case given(f, "items") && o.items < 1:
		err = fmt.Errorf("-items %d: how many of the plan's items to run, from 1", o.items)
	case o.workflow:
		if err = checkOrch(o.orchestrator); err == nil {
			err = checkOTLP(o.otlp)
		}
	}
	if err != nil {
		return a.refused("gate run", err)
	}
	if !a.inCheckout("gate run", "gate/run.sh") {
		return exitUsage
	}
	if !*dry {
		if why := a.gatePrereqs(); why != "" {
			return a.refused("gate run", errors.New(why))
		}
		if !o.allowStale {
			refuse, warn := a.checkStamp(ctx)
			if refuse != "" {
				if o.fake { // on cells, gate/run.sh's preflight refuses the same bin/laug
					refuse += "; -allow-stale runs the gate anyway"
				}
				return a.refused("gate run", errors.New(refuse))
			}
			if warn != "" {
				fmt.Fprintf(a.Stderr, "laug · gate run: %s\n", warn)
			}
		}
		// gate/run.sh's preflight would refuse a host that declares no type and keep the refusal as a BLOCK run in
		// -results. That is a workstation's first try, so it is refused here, with nothing written. A host that
		// declares another type is the lab's case, and its refusal stays a record, as two committed runs are.
		if !o.fake && !a.declaresHostType() {
			return a.refused("gate run", fmt.Errorf("this host declares no host type, which a gate on cells refuses: "+
				"%s host type -declare <type> declares one; %s gate run %s -fake runs a gate on fakes", a.self(),
				a.self(), tag))
		}
	}
	items, fake := "", ""
	if given(f, "items") {
		items = strconv.Itoa(o.items)
	}
	results, omes, environ, first, fakeDir, fakeTop := o.results, omesDir, a.Environ, "", "", ""
	if o.fake {
		fake = "1"
		var (
			dir string
			err error
		)
		if dir, fakeDir, err = a.fakeGateDir(*dry, !given(f, "results")); err != nil {
			fmt.Fprintf(a.Stderr, "laug · gate run: -fake: %v\n", a.errShown(err))
			return exitFailed
		}
		omes = filepath.Join(dir, "omes")
		if !given(f, "results") {
			results = filepath.Join(dir, "results")
		}
		where := "the run in " + a.pathShown(results)
		if fakeDir != "" {
			environ = append([]string{"FAKE_GATE_DIR=" + fakeDir}, a.Environ...)
			where += ", the fakes' files in " + a.pathShown(fakeDir)
		} else {
			where += ", the fakes' files in FAKE_GATE_DIR, " + a.pathShown(a.getenv("FAKE_GATE_DIR"))
		}
		first = fmt.Sprintf("laug · gate run %s -fake: fakes stand in for Docker, the cells and Omes; %s", tag, where)
		fakeTop = dir
	}
	set := gateEnv(tag, o.plan, items, o.base, fake, o.post, results, omes)
	if o.workflow {
		// The starter's environment is orchEnv, then gate run's environment, which the starter carries as the
		// workflow's input.
		how := fmt.Sprintf("workflow Gate on this checkout's worker through %s; Ctrl-C detaches, and the gate runs "+
			"on: %s gate status <gate id> shows it", o.orchestrator, a.self())
		if first == "" {
			first = fmt.Sprintf("laug · gate run %s -workflow: %s", tag, how)
		} else {
			first += "; " + how
		}
		return a.gateWorkflow(ctx, o.orchestrator, o.otlp, set, map[string]string{"SERVER_TAG": "<tag>"}, environ,
			first)
	}
	if fakeTop != "" {
		// gate/run.sh execs from laug, so laug cannot print after it: the script's last line names the dir laug made.
		environ = append([]string{"FAKE_GATE_TMP=" + fakeTop}, environ...)
	}
	c := script.Command{Script: "gate/run.sh", Set: set, By: map[string]string{"SERVER_TAG": "<tag>"}}
	b := *a
	b.Environ = environ
	c, env, code, ok := b.prepare("gate run", c, *dry)
	if !ok {
		return code
	}
	if *dry {
		line := script.DryRun(c)
		if fakeDir != "" {
			// FAKE_GATE_DIR is a seam, not a knob, so DryRun does not write it; laug made it, so the line does.
			line = "FAKE_GATE_DIR=" + a.wordShown(fakeDir) + " " + line
		}
		if fakeTop != "" {
			line = "FAKE_GATE_TMP=" + a.wordShown(fakeTop) + " " + line
		}
		fmt.Fprintln(a.Stdout, line)
		return exitDone
	}
	if !o.jsonOut {
		return a.start("gate run", c, env, script.Exec, first, a.Stdout)
	}
	// -json keeps gate/run.sh a child: its text goes to stderr, and its first line names the run, whose verdict.json
	// is the document.
	if first != "" {
		fmt.Fprintln(a.Stderr, first)
	}
	w := &gateIDWriter{w: a.Stderr}
	code = a.start("gate run", c, env, script.Run, "", w)
	if w.id == "" {
		return code
	}
	doc, err := os.ReadFile(filepath.Join(a.abs(results), w.id, "verdict.json"))
	if errors.Is(err, os.ErrNotExist) {
		return code
	}
	if err == nil {
		err = writeCompact(a.Stdout, doc)
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate run: %s's verdict.json: %v\n", w.id, a.errShown(err))
		return exitFailed
	}
	return code
}

// gateEnv is gate/run.sh's environment, and the starter's, which it carries as workflow Gate's input.
func gateEnv(tag, plan, items, base, fake, post, results, omes string) []script.Var {
	return []script.Var{script.Quoted("SERVER_TAG", tag), script.Quoted("PLAN", plan), script.Quoted("ITEMS", items),
		script.Quoted("BASE", base), script.Quoted("GATE_FAKE", fake), script.Quoted("GATE_POST", post),
		script.Quoted("GATE_RESULTS", results), script.Quoted("OMES_DIR", omes)}
}

// gatePrereqs says what a gate needs that this checkout lacks and laug does not build, "" when nothing: bin/laug, whose
// pool serve is the gate's own pool, and whose health check a compose cell's health verb runs. checkStamp refuses a
// bin/laug that is not there too, but -allow-stale skips it.
func (a *App) gatePrereqs() string {
	if !a.executable(laugBin) {
		return "bin/laug, whose pool serve is the gate's own pool, is not built: make build builds it"
	}
	return ""
}

// hostTypeFile is where laug host type -declare writes this host's type, as gate/host-type.sh and gate/lib.sh's
// host_reasons read it: $XDG_CONFIG_HOME/laug/host-type, with $HOME/.config when XDG_CONFIG_HOME is unset or empty.
func (a *App) hostTypeFile() string {
	dir := a.getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = a.getenv("HOME") + "/.config"
	}
	return filepath.Join(dir, "laug", "host-type")
}

// declaresHostType reports whether hostTypeFile's first line holds anything, as host_reasons reads a declaration. A
// file that is not there, or cannot be read, declares nothing, as head -1 on it gives nothing.
func (a *App) declaresHostType() bool {
	b, err := os.ReadFile(a.hostTypeFile())
	line, _, _ := strings.Cut(string(b), "\n")
	return err == nil && line != ""
}

// fakeGateDir makes the dir a gate on fakes keeps its files in, a new one under TMPDIR: results/ for the run when
// results is set, as it is unless -results says where; omes/cmd/omes, empty, which is all gate/run.sh's preflight
// looks for and all the fake go needs (load/testdata/gate/fakebin/go builds nothing); and fake/ for FAKE_GATE_DIR, the
// fakes' files, unless the environment names one. fakeDir is "" when the environment's FAKE_GATE_DIR stands. Under
// -dry-run it makes nothing and names the dir with a *.
func (a *App) fakeGateDir(dry, results bool) (dir, fakeDir string, err error) {
	if dry {
		dir = filepath.Join(a.tmpDir(), "laug-fake-gate-*")
	} else if dir, err = os.MkdirTemp(a.tmpDir(), "laug-fake-gate-"); err != nil {
		return "", "", err
	}
	dirs := []string{filepath.Join("omes", "cmd", "omes")}
	if results {
		dirs = append(dirs, "results")
	}
	if a.getenv("FAKE_GATE_DIR") == "" {
		fakeDir = filepath.Join(dir, "fake")
		dirs = append(dirs, "fake")
	}
	if dry {
		return dir, fakeDir, nil
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return "", "", err
		}
	}
	return dir, fakeDir, nil
}

// gateIDWriter passes gate/run.sh's stdout on and reads the run's id from its first line, "laug · gate <id>: ...".
type gateIDWriter struct {
	w    io.Writer
	line []byte
	read bool
	id   string
}

var gateLineRE = regexp.MustCompile(`^laug · gate (g[0-9]{8}-[0-9]{6}(-[0-9]+)?): `)

func (g *gateIDWriter) Write(p []byte) (int, error) {
	if !g.read {
		g.line = append(g.line, p...)
		if line, _, ok := bytes.Cut(g.line, []byte("\n")); ok {
			g.read = true
			if m := gateLineRE.FindSubmatch(line); m != nil {
				g.id = string(m[1])
			}
			g.line = nil
		}
	}
	return g.w.Write(p)
}

// binaryStamp reads the commit make build stamped into the binary at path. Tests replace it.
var binaryStamp = stampOf

// stampOf reads the build info of the Go binary at path (debug/buildinfo) and returns its stamp, as stampFrom does.
func stampOf(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	return stampFrom(info), nil
}

var ldflagsCommit = regexp.MustCompile(`(?:^|\s)-X[= ]main\.commit=([^\s'"]+)`)

// stampFrom is the commit a build info carries: make build's -X main.commit=<sha>, with -dirty for a tree git status
// listed anything in; else the VCS stamp a plain go build writes in a checkout, with -dirty for a modified tree; else
// "".
func stampFrom(info *debug.BuildInfo) string {
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "-ldflags":
			if m := ldflagsCommit.FindStringSubmatch(s.Value); m != nil {
				return m[1]
			}
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev != "" && modified == "true" {
		rev += "-dirty"
	}
	return rev
}

// checkStamp holds the checkout's bin/laug, whose pool serve every gate pool runs, to HEAD: refuse says
// why the gate refuses it, "" when bin/laug was built from HEAD's commit. warn, when not empty, is the one thing a
// stamp cannot tell: the tree has changes git has not committed outside gate/results, and whether bin/laug holds them.
func (a *App) checkStamp(ctx context.Context) (refuse, warn string) {
	stamp, err := binaryStamp(filepath.Join(a.Root, "bin", "laug"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "there is no bin/laug: make build builds it", ""
	case err != nil:
		return fmt.Sprintf("bin/laug's build info: %v: make build builds it again", err), ""
	case stamp == "" || stamp == "dev":
		return "bin/laug carries no commit: make build stamps it with HEAD's", ""
	}
	if a.lookPath("git") == "" {
		return "no git on PATH to read HEAD with, which bin/laug's commit is held to", ""
	}
	out := func(args ...string) (string, error) { return a.git(ctx, args...) }
	head, err := out("rev-parse", "HEAD")
	if err != nil || head == "" {
		return fmt.Sprintf("git rev-parse HEAD: %v, so bin/laug's commit cannot be held to HEAD", err), ""
	}
	sha, built := strings.CutSuffix(stamp, "-dirty")
	if sha != head {
		return fmt.Sprintf("bin/laug was built from %s, and HEAD is %s: make build builds it from HEAD", short(stamp),
			short(head)), ""
	}
	// make build's -dirty reads git status over the whole tree (the Makefile's LAUG_BUILD); gate.json's tree_dirty
	// reads it by treePathspec, without the earlier runs' directories. The refusal holds the stamp to the first, and
	// the warnings say what the second records: a tree whose only changes are runs under gate/results gets neither,
	// with bin/laug built before those runs or after them. A -dirty build whose changes went while runs stay there
	// passes as one built for the runs alone.
	all, err := out("status", "--porcelain")
	tree := ""
	if err == nil {
		tree, err = out(append([]string{"status", "--porcelain"}, treePathspec...)...)
	}
	switch {
	case err != nil:
		return "", fmt.Sprintf("git status: %v, so whether the tree has changed since bin/laug was built is not known",
			err)
	case built && all == "":
		return fmt.Sprintf("bin/laug was built from %s with changes the tree no longer holds: make build builds it "+
			"from HEAD", short(stamp)), ""
	case built && tree != "":
		return "", "bin/laug was built from HEAD with changes not committed, and the tree has some; laug cannot " +
			"tell whether they are the same (gate.json records tree_dirty)"
	case !built && tree != "":
		return "", "the tree has changes not committed, and bin/laug was built from HEAD without them; make build " +
			"builds them in (gate.json records tree_dirty)"
	}
	return "", ""
}

// treePathspec is the pathspec gate.json's tree_dirty reads git status by, in gate/run.sh and in gate/step.sh's
// verb_open: the checkout less gate/results, where each run leaves its directory untracked.
var treePathspec = []string{"--", ".", ":(exclude)gate/results"}

// errNoGit is git's error when the App's PATH holds no git.
var errNoGit = errors.New("no git on PATH")

// git runs git with args in the checkout, by the git on the App's PATH, 30 s at most, and returns its stdout less the
// blanks around it.
func (a *App) git(ctx context.Context, args ...string) (string, error) {
	git := a.lookPath("git")
	if git == "" {
		return "", errNoGit
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Dir, cmd.Env = a.Root, firstWins(a.Environ)
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}

// gitHead is HEAD's commit in the checkout, or why it cannot be read.
func (a *App) gitHead(ctx context.Context) (string, error) {
	head, err := a.git(ctx, "rev-parse", "HEAD")
	switch {
	case errors.Is(err, errNoGit):
		return "", errors.New("no git on PATH to read HEAD with")
	case err != nil || head == "":
		return "", fmt.Errorf("git rev-parse HEAD: %v", err)
	}
	return head, nil
}

// firstWins is environ with each name once, its first entry kept, as getenv reads it; os/exec would keep the last.
func firstWins(environ []string) []string {
	env, seen := []string{}, map[string]bool{}
	for _, kv := range environ {
		if k, _, ok := strings.Cut(kv, "="); ok && !seen[k] {
			seen[k] = true
			env = append(env, kv)
		}
	}
	return env
}

func runGatePost(_ context.Context, a *App, args []string) int {
	f := a.newFlags("gate post", "<gate id> [-post local] [-results gate/results] [-dry-run]")
	post := f.String("post", gatePostDefault, "the `sink` the verdict is handed to; local, the only one built, "+
		"prints it")
	results := resultsFlag(f)
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.noRun(f, *results, oneRun(*results), true, "")
	case len(pos) > 1:
		return a.usageError(f, "takes one gate run, got %q", pos)
	}
	r, err := gateRun(*results, pos[0])
	if err != nil {
		return a.refused("gate post", err)
	}
	c := script.Command{Script: "gate/post.sh", Args: []script.Value{{Text: r.path, Quote: true}},
		Set: []script.Var{script.Quoted("GATE_POST", *post)}, Clear: []string{"GATE", "GATE_RESULTS"}}
	return a.runScript("gate post", c, *dry, script.Exec, "")
}

func runGateRejudge(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("gate rejudge", "<gate id> | -all [-brief] [-results gate/results]")
	all := f.Bool("all", false, "judge every run in -results again, a line each, then the counts")
	brief := f.Bool("brief", false, "print the run's one line: identical, differs, re-rendered or not judged, and why")
	results := resultsFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0 && !*all:
		return a.noRun(f, *results, oneRun(*results)+"; or -all, every run there", true, "")
	case len(pos) > 0 && *all:
		return a.usageError(f, "takes a gate run or -all, not both")
	case len(pos) > 1:
		return a.usageError(f, "takes one gate run, got %q; -all judges every one", pos)
	case *all && *brief:
		return a.usageError(f, "-brief is one run's line; -all prints a line a run already")
	}
	dir := ""
	if !*all {
		r, err := gateRun(*results, pos[0])
		if err == nil {
			dir, err = a.runDir(*results, r)
		}
		if err != nil {
			return a.refused("gate rejudge", err)
		}
	}
	c := rejudge.Checkout{Root: a.Root, NotHeld: judgeNotHeld(ctx, a)}
	if *all {
		return a.rejudgeAll(c, *results)
	}
	ans := rejudge.Rejudge(dir, c)
	w := bufio.NewWriter(a.Stdout)
	if *brief {
		fmt.Fprintln(w, ans.Line())
	} else {
		writeRejudge(w, ans, c, dir)
	}
	code = ans.Exit()
	if err := w.Flush(); err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate rejudge: writing: %v\n", a.errShown(err))
		return exitFailed
	}
	return code
}

// writeRejudge writes the long form of one run's rejudge: a first line, each file identical or differing at its
// first line, then the judge and why it differs. A run not judged says why second, and no file: a comparison the
// judge was not held for counts for nothing, and a stale bin/laug, the likeliest reason, would print identical twice
// before the line that takes it back.
func writeRejudge(w io.Writer, ans rejudge.Answer, c rejudge.Checkout, dir string) {
	fmt.Fprintf(w, "laug · gate rejudge %s: the run in %s, judged again in Go; nothing is written\n", ans.ID,
		c.Shown(dir))
	if ans.Outcome == rejudge.NotJudged {
		fmt.Fprintf(w, "%-14s%s\n", "not judged", ans.Why)
	}
	if ans.Rejudged.JSON != nil && ans.Outcome != rejudge.NotJudged {
		for _, f := range []struct {
			name, pad           string
			committed, rejudged []byte
		}{
			{"verdict.json", "  ", ans.Committed.JSON, ans.Rejudged.JSON},
			{"verdict.md", "    ", ans.Committed.MD, ans.Rejudged.MD}} {
			n, want, got := rejudge.FirstDiff(f.committed, f.rejudged)
			if n == 0 {
				fmt.Fprintf(w, "%s%sidentical\n", f.name, f.pad)
				continue
			}
			fmt.Fprintf(w, "%s%sdiffers, first at line %d\n  the run's  %s\n  rejudge    %s\n", f.name, f.pad, n,
				want, got)
		}
	}
	if ans.Version.Name != "" {
		r := ans.Recorded
		_, named := r.Version()
		rec := ", the current one: gate.json records no gate/combine.sh sha256 and no internal/judge version"
		switch {
		case r.SHA256 != "" && named:
			rec = ": gate.json records gate/combine.sh " + short12(r.SHA256)
		case r.SHA256 != "":
			rec = ", the current one: gate.json records gate/combine.sh " + short12(r.SHA256) + ", which no " +
				"version names"
		case r.Entry != "" && named:
			// A run judged in Go names the sources that judged it; judged again by other sources, it still judges,
			// and both are shown.
			src := "none"
			if r.Sources != "" {
				src = short12(r.Sources)
			}
			rec = ": gate.json records internal/judge " + r.Entry + ", sources " + src
		case r.Entry != "":
			rec = ", the current one: gate.json records internal/judge " + r.Entry + ", which no version names"
		}
		fmt.Fprintf(w, "%-14s%s%s; laug's judge sources %s\n", "judge", ans.Version.Name, rec,
			short12(judge.Self().SHA256))
	}
	switch ans.Outcome {
	case rejudge.Known:
		fmt.Fprintf(w, "%-14s%s\n", "why", ans.Why)
	case rejudge.Rerendered:
		fmt.Fprintf(w, "%-14s%s: verdict.json is the gate's trap verdict, not a judge's, and that form re-renders "+
			"both files byte for byte\n", "re-rendered", ans.Why)
	case rejudge.Unexplained:
		fmt.Fprintf(w, "%-14s%s\n", "why", "unexplained")
	}
}

// short12 is a sha256's first 12 hex digits, the form a line shows one in.
func short12(sha string) string { return sha[:min(12, len(sha))] }

// rejudgeAll is gate rejudge -all: every run in results judged again, a line each under a first line, then the
// counts.
func (a *App) rejudgeAll(c rejudge.Checkout, results string) int {
	fmt.Fprintln(a.Stdout, "laug · gate rejudge -all: every run in "+results+", judged again in Go under the judge "+
		"version its gate.json records, a line each, then the counts")
	answers, err := rejudge.RejudgeAll(a.abs(results), c)
	if err != nil {
		hint := ""
		if errors.Is(err, rejudge.ErrNoResults) {
			hint = "; -results names none"
		}
		fmt.Fprintf(a.Stderr, "laug · gate rejudge: %v%s\n", a.errShown(err), hint)
		return exitUsage
	}
	w := bufio.NewWriter(a.Stdout)
	for _, ans := range answers {
		fmt.Fprintln(w, ans.Line())
	}
	counts := rejudge.Count(answers)
	fmt.Fprintf(w, "laug · rejudge, %s\n", counts)
	code := counts.Exit()
	if err := w.Flush(); err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate rejudge: writing: %v\n", a.errShown(err))
		return exitFailed
	}
	return code
}

// runDir is the directory a gate run names (runRef): a gate id in
// results, or a path, a relative one from the checkout's root; then made absolute, its symlinks resolved. An error
// when that is not a directory.
func (a *App) runDir(results string, r runRef) (string, error) {
	d := rejudge.Resolve(a.abs(r.path))
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("no results directory at %s", rejudge.Checkout{Root: a.Root}.Shown(d))
	}
	return d, nil
}

func runGateCompare(_ context.Context, a *App, args []string) int {
	f := a.newFlags("gate compare", "<a> <b> [-results gate/results]")
	results := resultsFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.noRun(f, *results, "give it two, A and B, each a gate id in "+*results+" or the path to a run's "+
			"directory", false, "")
	case len(pos) != 2:
		return a.usageError(f, "takes two gate runs, A and B, got %q", pos)
	}
	var (
		dirs [2]string
		sigs [2]jsonx.Value
	)
	for i, p := range pos {
		r, err := gateRun(*results, p)
		if err == nil {
			dirs[i], err = a.runDir(*results, r)
		}
		if err != nil {
			return a.refused("gate compare", err)
		}
	}
	for i, d := range dirs {
		var err error
		if sigs[i], err = rejudge.Signature(d, a.Root); err != nil {
			fmt.Fprintf(a.Stderr, "laug · gate compare: %v\n", a.errShown(err))
			return exitUsage
		}
	}
	diffs := rejudge.Compare(sigs[0], sigs[1])
	o, _ := sigs[0].Obj()
	w := bufio.NewWriter(a.Stdout)
	fmt.Fprintln(w, rejudge.CompareLine(filepath.Base(dirs[0]), filepath.Base(dirs[1]), diffs, o.Len()))
	for _, d := range diffs {
		fmt.Fprintln(w, d)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate compare: writing: %v\n", a.errShown(err))
		return exitFailed
	}
	if len(diffs) > 0 {
		return exitFailed
	}
	return exitDone
}

func runGateSignature(_ context.Context, a *App, args []string) int {
	f := a.newFlags("gate signature", "<gate id> [-results gate/results] [-json]")
	results := resultsFlag(f)
	jsonOut := f.Bool("json", false, "print the signature as one JSON object on one line")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.noRun(f, *results, oneRun(*results), true, "")
	case len(pos) > 1:
		return a.usageError(f, "takes one gate run, got %q", pos)
	}
	r, err := gateRun(*results, pos[0])
	var dir string
	if err == nil {
		dir, err = a.runDir(*results, r)
	}
	if err != nil {
		return a.refused("gate signature", err)
	}
	sig, err := rejudge.Signature(dir, a.Root)
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate signature: %v\n", a.errShown(err))
		return exitUsage
	}
	var out []byte
	if *jsonOut {
		out = jsonx.MarshalCompact(sig)
	} else {
		out = append([]byte("laug · gate signature "+r.arg+": the fields S2 compares, from verdict.json and "+
			"timeline.jsonl\n"), jsonx.Marshal(sig)...)
	}
	if _, err := a.Stdout.Write(out); err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate signature: writing: %v\n", a.errShown(err))
		return exitFailed
	}
	return exitDone
}

func runGateTolerance(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("gate tolerance", "<gate id>... [-pair <a>:<b>] [-evidence cells/results/<file>] "+
		"[-results gate/results | -tables]")
	pair := f.String("pair", "", "two plain item `keys`, <a>:<b>, read in each gate instead of the arm's pair: a look, "+
		"not a tolerance")
	evidence := f.String("evidence", "", "the `file` the compare: block cites: a results file in cells/results/, or "+
		tolerance.TablesEvidence+", which -tables cites when this is not given")
	results := resultsFlag(f)
	fromTables := f.Bool("tables", false, "read the gates from the summary tables, "+tolerance.TablesDir+
		", in place of their directories in -results")
	ids, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(ids) == 0 {
		return a.noRun(f, *results, "give it the A/A gates' ids in "+*results, false, "")
	}
	for _, id := range ids {
		if !gateIDRE.MatchString(id) {
			return a.refused("gate tolerance", fmt.Errorf("%s is not a gate id, g<date>-<time>", shellWord(id)))
		}
	}
	r := tolerance.Request{IDs: ids, Results: *results, Pair: *pair, Evidence: *evidence}
	if *fromTables {
		if given(f, "results") {
			return a.refused("gate tolerance", fmt.Errorf("-tables reads %s, and -results names directories: "+
				"give one", tolerance.TablesDir))
		}
		if r.Evidence == "" {
			r.Evidence = tolerance.TablesEvidence
		}
	}
	if r.Results == "" {
		r.Results = gateResultsDefault // gate/results, unless -results names another
	}
	// load/thresholds.sh reads none of the knobs gate tolerance maps, so it gets none. gate tolerance's flags
	// -results, -pair and -evidence (the knobs GATE_RESULTS, PAIR and EVIDENCE) and the gate ids, its arguments
	// (the knob GATES), go into r. Clear names the four, so a copy in the caller's environment is named as a stray.
	// THRESHOLDS and THRESHOLDS_RELEASES unset, so the set is gate run's; LC_ALL=C (toleranceEnv).
	c := script.Command{Script: "load/thresholds.sh", Args: []script.Value{{Text: "", Quote: true}},
		Clear: []string{"EVIDENCE", "GATE_RESULTS", "GATES", "PAIR"}}
	c, env, code, ok := a.prepare("gate tolerance", c, false)
	if !ok {
		return code
	}
	dir := a.abs(r.Results)
	var rows *tolerance.Rows
	if *fromTables {
		var err error
		if rows, err = tolerance.ReadRows(os.DirFS(a.abs("."))); err != nil {
			fmt.Fprintf(a.Stderr, "laug · gate tolerance: %v\n", a.errShown(err))
			return exitFailed // the tables did not read: not a usage error
		}
		err = tolerance.CheckRows(rows, r)
		if err != nil {
			fmt.Fprintln(a.Stderr, a.errShown(err))
			return exitUsage
		}
	} else if err := tolerance.Check(dir, r); err != nil {
		fmt.Fprintln(a.Stderr, a.errShown(err))
		return exitUsage
	}
	set, ok := a.toleranceSet(ctx, c, toleranceEnv(env))
	if !ok {
		fmt.Fprintln(a.Stderr, tolerance.NoBase)
		return exitUsage
	}
	if rows != nil {
		return tolerance.RunRows(rows, r, set, a.Stdout, a.Stderr)
	}
	return tolerance.Run(dir, r, set, a.Stdout, a.Stderr)
}

// toleranceEnv is env, a script's environment as internal/script builds it, made the one the set resolves under:
// LC_ALL=C, and THRESHOLDS_RELEASES unset, which internal/script passes on as a seam. THRESHOLDS, a knob
// gate tolerance does not map, is not there.
func toleranceEnv(env []string) []string {
	out := []string{}
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name != "LC_ALL" && name != "THRESHOLDS_RELEASES" {
			out = append(out, kv)
		}
	}
	return append(out, "LC_ALL=C")
}

// toleranceSet is the threshold set, resolved: load/thresholds.sh "", by internal/script, piped through yq
// -o=json -I=0 '.', each with env from the checkout's root, their stderr laug's. ok is false when either exits other
// than 0, as pipefail makes a pipe's line fail; yq reads whatever load/thresholds.sh printed, as in the pipe.
func (a *App) toleranceSet(ctx context.Context, c script.Command, env []string) (set []byte, ok bool) {
	var yaml bytes.Buffer
	code, err := a.Runner.Run(c, env, &yaml, a.Stderr)
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate tolerance: could not run %s: %v\n", c.Script, a.errShown(err))
		return nil, false
	}
	yq := a.lookPath("yq") // PATH passes to the script as it is, so its yq is this one
	if yq == "" {
		fmt.Fprintln(a.Stderr, "laug · gate tolerance: no yq on PATH, which reads load/thresholds.sh's set")
		return nil, false
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, yq, "-o=json", "-I=0", ".")
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = a.Root, env, &yaml, &out, a.Stderr
	yqErr := cmd.Run()
	return out.Bytes(), code == 0 && yqErr == nil
}

func runGateOpen(_ context.Context, a *App, args []string) int {
	f := a.newFlags("gate open", "<gate id> [-results gate/results]")
	results := resultsFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.noRun(f, *results, oneRun(*results), true, "")
	case len(pos) > 1:
		return a.usageError(f, "takes one gate run, got %q", pos)
	}
	r, err := gateRun(*results, pos[0])
	if err != nil {
		return a.refused("gate open", err)
	}
	dir := a.abs(r.path)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		fmt.Fprintf(a.Stderr, "laug · gate open: no gate run %s: %s is not a directory\n", r.arg, a.pathShown(dir))
		return exitFailed
	}
	v, err := readVerdict(dir)
	if err != nil || (v.Verdict != "ADVANCE" && v.Verdict != "BLOCK") {
		why := "its verdict.json holds no ADVANCE or BLOCK"
		switch {
		case errors.Is(err, os.ErrNotExist):
			why = "there is no verdict.json in it"
		case err != nil:
			why = "its verdict.json: " + a.errShown(err)
		}
		fmt.Fprintf(a.Stderr, "laug · gate open %s: no verdict: %s; a gate that runs, or stopped before it judged, "+
			"has none yet\ndirectory   %s\n", r.arg, why, a.pathShown(dir))
		return exitFailed
	}
	id := v.GateID
	if id == "" {
		id = filepath.Base(dir)
	}
	about := "server " + plain(v.ServerTag)
	if v.Class != nil {
		about += ", class " + *v.Class
	}
	if v.StoppedAt != nil {
		about += ", stopped at " + *v.StoppedAt
	}
	if v.Fake {
		about += ", on fakes"
	}
	w := bufio.NewWriter(a.Stdout)
	fmt.Fprintf(w, "laug · gate open %s: %s\n", id, about)
	fmt.Fprintf(w, "directory   %s\n", a.pathShown(dir))
	fmt.Fprintf(w, "verdict.md  %s\n", a.pathShown(filepath.Join(dir, "verdict.md")))
	err = w.Flush()
	if err == nil {
		err = a.writeVerdict(a.Stdout, v.Verdict, false)
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · gate open: writing: %v\n", a.errShown(err))
		return exitFailed
	}
	return exitDone
}
