package main

// laug judge: a gate run's directory judged in Go by internal/judge, under any judge version a committed run records.
// Judged under the version it records, each committed run a judge judged gets its verdict.json and verdict.md back
// byte for byte; a trap verdict, which the gate wrote, is left out.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/rejudge"
)

// versionNames is the registry's versions, oldest first, for a line that lists them.
func versionNames() string {
	var names []string
	for _, v := range judge.Versions() {
		names = append(names, v.Name)
	}
	return strings.Join(names, ", ")
}

func runJudge(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("judge", "<dir> [-write] [-json | -md] [-recorded | -version <name>] | -classify <reason> | "+
		"-identity [-json]")
	write := f.Bool("write", false, "write verdict.json and verdict.md into the run's directory, a temp file and a "+
		"rename each, and print the summary, the line that names the two files and the verdict")
	overwrite := f.Bool("overwrite-verdict", false, "with -write, replace a verdict another judge wrote, or a trap "+
		"verdict")
	jsonOut := f.Bool("json", false, "print verdict.json on one line and nothing else; with -identity, the identity")
	md := f.Bool("md", false, "print verdict.md and nothing else")
	recorded := f.Bool("recorded", false, "judge under the version the run's gate.json records, by its shell judge's "+
		"sha256 or the internal/judge entry, as gate rejudge does")
	version := f.String("version", "", "judge under this `version`, one of "+versionNames()+"; unset, "+
		judge.Current+", the one new runs get")
	classify := f.String("classify", "", "print the class a `reason` gets under "+judge.Current+", alone, for "+
		"a script to read")
	identity := f.Bool("identity", false, "print this judge's identity: its version, the sha256 of its sources "+
		"(tests left out), the Go that built it, laug's commit and whether the checkout holds this judge, committed")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	alone := func(name string, also ...string) bool {
		others := false
		f.Visit(func(fl *flag.Flag) { others = others || fl.Name != name && !slices.Contains(also, fl.Name) })
		return !others && len(pos) == 0
	}
	switch {
	case given(f, "classify"):
		if !alone("classify") {
			return a.usageError(f, "-classify takes a reason and nothing else")
		}
		if _, err := a.Stdout.Write(judge.Classify(*classify)); err != nil {
			fmt.Fprintf(a.Stderr, "laug · judge: writing: %v\n", a.errShown(err))
			return exitFailed
		}
		return exitDone
	case *identity:
		if !alone("identity", "json") {
			return a.usageError(f, "-identity takes -json and nothing else")
		}
		return a.judgeIdentity(ctx, *jsonOut)
	case len(pos) == 0:
		return a.noRun(f, gateResultsDefault, "give it a gate run's directory, or a gate id in "+gateResultsDefault+
			"; or -classify <reason>, or -identity", true, "")
	case len(pos) > 1:
		return a.usageError(f, "takes one gate run, got %q", pos)
	case *jsonOut && *md:
		return a.usageError(f, "-json and -md each print a file; give one")
	case *recorded && given(f, "version"):
		return a.usageError(f, "-recorded and -version each name the judge version; give one")
	case *overwrite && !*write:
		return a.usageError(f, "-overwrite-verdict goes with -write")
	}
	r, err := gateRun(gateResultsDefault, pos[0])
	if err != nil {
		return a.refused("judge", err)
	}
	dir := a.abs(r.path)
	var rec rejudge.Recorded
	if data, err := os.ReadFile(filepath.Join(dir, "gate.json")); err == nil {
		rec = rejudge.ReadRecorded(data)
	}
	v, _ := judge.Named(judge.Current)
	how := "the current judge"
	switch {
	case given(f, "version"):
		if v, ok = judge.Named(*version); !ok {
			return a.refused("judge", fmt.Errorf("-version %s: no such judge version; the versions are %s",
				shellWord(*version), versionNames()))
		}
		how = "as -version asks"
	case *recorded:
		if v, ok = rec.Version(); !ok {
			why := "its gate.json records no gate/combine.sh sha256 and no internal/judge version"
			switch {
			case rec.SHA256 != "":
				why = "its gate.json records gate/combine.sh sha256 " + rec.SHA256[:12] + ", which no version names"
			case rec.Entry != "":
				why = "its gate.json records internal/judge version " + rec.Entry + ", which no version names"
			}
			return a.refused("judge", fmt.Errorf("-recorded: %s: %s; -version names one of %s", r.path, why,
				versionNames()))
		}
	}
	// other says what gate.json records when it is not v, in a clause; "" when it records v, or none, as a run made
	// before a judge was recorded and make harness-test's fixture do. A fresh run records the judge that judges it
	// (gate/lib.sh, judge_input), so the gate's own judge -write never meets one.
	other, recName := "", ""
	if rv, known := rec.Version(); known && rv.Name != v.Name {
		other, recName = "gate.json records "+rv.Name, rv.Name
	} else if !known && (rec.SHA256 != "" || rec.Entry != "") {
		other = rec.Why()
	}
	// keep says why -write would replace a record it should not, and brief says it in a few words: verdict.json is
	// the gate's trap verdict, which no judge gives back, or gate.json records another version that versions.txt
	// names, as every committed run records one. Both are "" when nothing stands in the way. A judge no version
	// names, which make harness-test's rejudge cases record, gets the stderr line alone.
	keep, brief, fix := "", "", ""
	switch step := trapStep(dir); {
	case step != "":
		keep = "verdict.json is the trap verdict of a gate stopped at step " + step + ", which no judge gives back"
		brief = "verdict.json is a trap verdict"
	case recName != "":
		keep, brief = other+", and this judge is "+v.Name, other
		fix = "-recorded judges under " + recName + "; "
	}
	if *write && keep != "" && !*overwrite {
		fmt.Fprintf(a.Stderr, "laug · judge: %s: no verdict written: %s; %s-overwrite-verdict replaces the run's "+
			"verdict\n", r.path, keep, fix)
		return exitUsage
	}
	switch {
	case recName != "":
		fmt.Fprintf(a.Stderr, "laug · judge: judged under %s, %s; %s, and -recorded judges under %s\n", v.Name, how,
			other, recName)
	case other != "":
		fmt.Fprintf(a.Stderr, "laug · judge: judged under %s, %s; %s\n", v.Name, how, other)
	}
	res, err := judge.Judge(os.DirFS(dir), v, judge.Options{})
	switch {
	case errors.Is(err, judge.ErrNoRunDir):
		fmt.Fprintf(a.Stderr, "laug · judge: no results directory at %s\n", r.path)
		return exitUsage
	case err != nil:
		fmt.Fprintf(a.Stderr, "laug · judge: judge %s on %s: %v\n", v.Name, r.path, a.errShown(err))
		return exitUsage
	}
	if *write {
		if *overwrite {
			if line := replacedLine(r.path, dir); line != "" {
				fmt.Fprintf(a.Stderr, "laug · judge: -overwrite-verdict replaces %s\n", line)
			}
		}
		if err := writeVerdictFiles(dir, res); err != nil {
			fmt.Fprintf(a.Stderr, "laug · judge: %s: no verdict written: %v\n", r.path, a.errShown(err))
			return exitUsage
		}
	}
	switch {
	case *jsonOut:
		doc, err := jsonx.Parse(res.JSON)
		if err == nil {
			_, err = a.Stdout.Write(jsonx.MarshalCompact(doc))
		}
		if err != nil {
			fmt.Fprintf(a.Stderr, "laug · judge: writing: %v\n", a.errShown(err))
			return exitFailed
		}
	case *md:
		if _, err := a.Stdout.Write(res.MD); err != nil {
			fmt.Fprintf(a.Stderr, "laug · judge: writing: %v\n", a.errShown(err))
			return exitFailed
		}
	default:
		files := r.path + "/verdict.json, " + r.path + "/verdict.md"
		line := "written          " + files
		switch {
		case !*write && keep != "":
			line = "not written      judge " + v.Name + " in Go; -write refuses: " + brief
		case !*write:
			line = "not written      judge " + v.Name + " in Go; -write writes " + files
		}
		_, err := a.Stdout.Write(append(res.Summary(), line+"\n"...))
		if err == nil {
			err = a.writeVerdict(a.Stdout, res.Verdict, false)
		}
		if err != nil {
			fmt.Fprintf(a.Stderr, "laug · judge: writing: %v\n", a.errShown(err))
			return exitFailed
		}
	}
	return res.Exit()
}

// trapStep is the step verdict.json says the gate stopped at, when it is the gate's trap verdict (gate/lib.sh,
// trap_verdict); "" when the file is missing, does not parse, or is a judge's verdict, whose stopped_at is null or
// absent.
func trapStep(dir string) string {
	var v struct {
		StoppedAt json.RawMessage `json:"stopped_at"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil || json.Unmarshal(b, &v) != nil || len(v.StoppedAt) == 0 || string(v.StoppedAt) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(v.StoppedAt, &s) != nil || s == "" {
		return string(v.StoppedAt)
	}
	return s
}

// replacedLine is what -overwrite-verdict says it replaces before -write writes: how many of the run's verdict.json
// and verdict.md there are, and the verdict the first says; "" when there are none.
func replacedLine(path, dir string) string {
	var have []string
	for _, name := range []string{"verdict.json", "verdict.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			have = append(have, name)
		}
	}
	if len(have) == 0 {
		return ""
	}
	if v, err := readVerdict(dir); err == nil && v.Verdict != "" && have[0] == "verdict.json" {
		have[0] += " (" + v.Verdict + ")"
	}
	return fmt.Sprintf("%s in %s: %s", plural(len(have), "file"), path, strings.Join(have, " and "))
}

// writeVerdictFiles writes res's two files into dir: each to a temp file in dir,
// .verdict.json.* and .verdict.md.*, made with mode 0600 as mktemp makes one, then both renamed into place, so a
// reader sees the old file or the new one whole. A temp file is removed when anything fails.
func writeVerdictFiles(dir string, res *judge.Result) error {
	var tmps []string
	defer func() {
		for _, t := range tmps {
			os.Remove(t)
		}
	}()
	for _, f := range []struct {
		name string
		data []byte
	}{{"verdict.json", res.JSON}, {"verdict.md", res.MD}} {
		t, err := os.CreateTemp(dir, "."+f.name+".")
		if err != nil {
			return err
		}
		tmps = append(tmps, t.Name())
		_, err = t.Write(f.data)
		if cerr := t.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	for i, name := range []string{"verdict.json", "verdict.md"} {
		if err := os.Rename(tmps[i], filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	tmps = nil
	return nil
}

// judgeIdentity is what judge -identity prints: the judge's version, the sha256 of its sources (judge.Sources: the
// .go files of internal/judge and internal/jsonx that are not tests, and versions.txt), the Go that built it,
// laug's commit, and whether the checkout's internal/judge and internal/jsonx hold this judge, committed: null, with
// a stderr line, when that cannot be told.
func (a *App) judgeIdentity(ctx context.Context, jsonOut bool) int {
	id := judge.Self()
	commit := buildCommit()
	var clean *bool
	why, known := "", false
	if a.Root == "" {
		why = "laug runs outside a checkout"
	} else {
		why, known = a.judgeTreeChanged(ctx)
	}
	if known {
		c := why == ""
		clean = &c
	}
	if !known {
		fmt.Fprintf(a.Stderr, "laug · judge -identity: tree_clean is not known: %s\n", why)
	}
	var err error
	if jsonOut {
		err = writeJSON(a.Stdout, struct {
			Version   string `json:"version"`
			SHA256    string `json:"sha256"`
			Go        string `json:"go"`
			Commit    string `json:"commit"`
			TreeClean *bool  `json:"tree_clean"`
		}{id.Version, id.SHA256, id.Go, commit, clean})
	} else {
		tree := "not known"
		switch {
		case clean != nil && *clean:
			tree = "yes: internal/judge and internal/jsonx hold this judge, committed"
		case clean != nil:
			tree = "no: " + why
		}
		_, err = fmt.Fprintf(a.Stdout, "laug · judge -identity: %s, sha256 %s, %s, laug %s\ntree clean  %s\n",
			id.Version, id.SHA256, id.Go, short(commit), tree)
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "laug · judge: writing: %v\n", a.errShown(err))
		return exitFailed
	}
	return exitDone
}

// judgeNotHeld says why the judge this binary embeds, which gate rejudge runs, is not held to the one HEAD holds, ""
// when it is. Three things hold it: laug was built from HEAD (its commit, with -dirty or without, is git rev-parse
// HEAD's), git status lists nothing under internal/judge and internal/jsonx, and the sources this binary embeds are
// the ones the checkout holds there (judge.SourcesIn). When one fails, no run is judged
// (rejudge.Checkout's NotHeld). Tests replace it.
var judgeNotHeld = func(ctx context.Context, a *App) string {
	if a.Root == "" {
		return "laug runs outside a checkout, so its judge cannot be held to HEAD's"
	}
	stamp := buildCommit()
	if stamp == "dev" {
		return "this laug carries no commit, so its judge cannot be held to HEAD's: make build stamps HEAD's"
	}
	head, err := a.gitOut(ctx, "rev-parse", "HEAD")
	if err != nil || head == "" {
		return fmt.Sprintf("git rev-parse HEAD: %v, so laug's judge cannot be held to HEAD's", err)
	}
	if sha, _ := strings.CutSuffix(stamp, "-dirty"); sha != head {
		return fmt.Sprintf("laug was built from %s, and HEAD is %s: make build builds it from HEAD", short(stamp),
			short(head))
	}
	if why, _ := a.judgeTreeChanged(ctx); why != "" {
		return why
	}
	return ""
}

// judgeTreeChanged says why the checkout's internal/judge and internal/jsonx are not this binary's judge, committed:
// git status lists a change there, or the files are not the ones embedded. why is "" when they are; known is false
// when git could not tell, and why says so.
func (a *App) judgeTreeChanged(ctx context.Context) (why string, known bool) {
	status, err := a.gitOut(ctx, "status", "--porcelain", "--", "internal/judge", "internal/jsonx")
	if err != nil {
		return fmt.Sprintf("git status: %v, so whether internal/judge and internal/jsonx are HEAD's is not known",
			err), false
	}
	if status != "" {
		return fmt.Sprintf("internal/judge and internal/jsonx have changes not committed: git status lists %d",
			strings.Count(status, "\n")+1), true
	}
	srcs, err := judge.SourcesIn(os.DirFS(a.Root))
	if err != nil {
		return fmt.Sprintf("the checkout's judge sources: %v", err), false
	}
	if disk, self := judge.Sum(srcs), judge.Self().SHA256; disk != self {
		return fmt.Sprintf("laug's judge (sources' sha256 %s) is not the one the checkout holds (%s): make build "+
			"builds it", self[:12], disk[:12]), true
	}
	return "", true
}

// gitOut runs git from the App's PATH in the checkout, with the App's environment and 30 s at most, and returns its
// stdout less the space around it.
func (a *App) gitOut(ctx context.Context, args ...string) (string, error) {
	git := a.lookPath("git")
	if git == "" {
		return "", errors.New("no git on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Dir, cmd.Env = a.Root, firstWins(a.Environ)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
