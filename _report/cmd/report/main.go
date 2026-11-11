// Command report is the renderer's command line, run from the checkout as go -C _report run ./cmd/report: -docs writes
// the committed documents under docs/, and the rows the summary tables lack, which make docs runs; -tables writes the
// committed records' summary tables under docs/tables/, refused while a record the records table marks kept false has
// no file here; and -tables -check writes nothing, saying how the tables on disk differ from a build. Bare, it names
// neither: its usage, exit 2. It reads the committed records, so it needs the checkout: the first directory up from
// the working directory that holds go.mod and gate/run.sh, the parent of _report when go -C runs it.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/ymur-studio/laug/_report"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/rejudge"
	"github.com/ymur-studio/laug/internal/tables"
)

// The exit codes, cmd/laug's: done; failed, or the negative answer the command exists to give; usage, or refused
// before acting.
const (
	exitDone   = 0
	exitFailed = 1
	exitUsage  = 2
)

// self is how a line to type names this command, from the checkout's root.
const self = "go -C _report run ./cmd/report"

// gateResults is where the committed gate runs are, from the checkout's root.
const gateResults = "gate/results"

// summary is what -h prints first, and the synopsis follows it.
const (
	summary  = "write the committed documents or tables from the records"
	synopsis = "-docs | -tables [-check]"
)

// An app is one run: the checkout it reads and writes, the two streams and the home directory a shown path is made
// relative to. main fills it from the process; a test fills it with a temporary checkout and buffers.
type app struct {
	root   string
	home   string
	stdout io.Writer
	stderr io.Writer
}

func main() {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "laug · report: %v\n", err)
		os.Exit(exitUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{root: findRoot(wd), home: os.Getenv("HOME"), stdout: os.Stdout, stderr: os.Stderr}
	os.Exit(a.run(ctx, os.Args[1:]))
}

// findRoot returns the first of dir and its parents that holds go.mod and gate/run.sh, or "" when none does or dir
// is not absolute. _report holds a go.mod and no gate/run.sh, so from there it is the parent.
func findRoot(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	for {
		if isFile(filepath.Join(dir, "go.mod")) && isFile(filepath.Join(dir, "gate", "run.sh")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// run parses args and runs the one of -docs and -tables they name. Go's flag package writes nothing: -h goes to stdout
// with the summary first, exit 0; a mistake to stderr with the synopsis, exit 2.
func (a *app) run(ctx context.Context, args []string) int {
	f := flag.NewFlagSet("report", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.Usage = func() {}
	docs := f.Bool("docs", false, "write the committed documents: docs/"+report.Evidence+" and docs/img's figures, "+
		"from "+gateResults+", and the rows docs/"+report.TablesDir+" lacks, never changing or removing one; "+
		"make docs runs it")
	tablesFlag := f.Bool("tables", false, "write the summary tables: docs/"+report.TablesDir+"/*.jsonl and its "+
		"README, from the committed records, the pages that cite them and docs/"+report.TablesDir+"/src")
	check := f.Bool("check", false, "with -tables, write nothing: build the tables in memory, name what differs "+
		"from docs/"+report.TablesDir+", exit 1 on a difference")
	err := f.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(a.stdout, "laug · report: %s\n", summary)
		usage(f, a.stdout)
		return exitDone
	}
	if err != nil {
		return a.usageError(f, "%v", err)
	}
	if pos := f.Args(); len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	if *docs && *tablesFlag {
		return a.usageError(f, "-docs writes docs/ and -tables writes docs/%s/; name one", report.TablesDir)
	}
	if given(f, "check") && !*tablesFlag {
		return a.usageError(f, "-check checks the tables -tables writes, so it needs -tables")
	}
	if !*docs && !*tablesFlag {
		return a.usageError(f, "writes docs/ with -docs or docs/%s/ with -tables; name one", report.TablesDir)
	}
	if a.root == "" {
		fmt.Fprintln(a.stderr, "laug · report: reads the committed records, so it needs a laug checkout: no "+
			"directory above this one holds go.mod and gate/run.sh")
		return exitUsage
	}
	if *docs {
		return a.reportDocs(ctx)
	}
	return a.reportTables(ctx, *check)
}

// usage writes the synopsis and the flags, each flag's name, then its text under it.
func usage(f *flag.FlagSet, w io.Writer) {
	fmt.Fprintf(w, "usage: %s %s\n", self, synopsis)
	f.VisitAll(func(fl *flag.Flag) {
		fmt.Fprintf(w, "  -%s\n        %s\n", fl.Name, fl.Usage)
	})
}

// usageError prints why on one stderr line, its paths as shown writes them, then the synopsis, and returns 2.
func (a *app) usageError(f *flag.FlagSet, format string, v ...any) int {
	fmt.Fprintf(a.stderr, "laug · report: %s\n", a.shown(fmt.Sprintf(format, v...)))
	fmt.Fprintf(a.stderr, "usage: %s %s (-h lists the flags)\n", self, synopsis)
	return exitUsage
}

// refused prints why the command refuses, one stderr line, and returns 2.
func (a *app) refused(err error) int {
	fmt.Fprintf(a.stderr, "laug · report: %s\n", a.errShown(err))
	return exitUsage
}

// given reports whether the flag called name was set on the command line.
func given(f *flag.FlagSet, name string) bool {
	set := false
	f.Visit(func(fl *flag.Flag) { set = set || fl.Name == name })
	return set
}

// shown is s, a line this command prints, with each absolute path in it written relative to the checkout's root when
// below it, and as ~/ and the rest when below the home directory, as cmd/laug's textShown writes one. A path counts
// where it starts s or follows a space, a quote, =, :, ( or [, as an error's text names its paths.
func (a *app) shown(s string) string {
	if a.root != "" && filepath.IsAbs(a.root) {
		s = prefixShown(s, filepath.Clean(a.root)+string(filepath.Separator), "")
	}
	if filepath.IsAbs(a.home) && filepath.Clean(a.home) != "/" {
		s = prefixShown(s, filepath.Clean(a.home)+string(filepath.Separator), "~/")
	}
	return s
}

// prefixShown is s with each prefix that starts a path written as with.
func prefixShown(s, prefix, with string) string {
	re := regexp.MustCompile(`(^|[\s"'=:(\[,])` + regexp.QuoteMeta(prefix))
	return re.ReplaceAllStringFunc(s, func(m string) string { return m[:len(m)-len(prefix)] + with })
}

// errShown is err's text as shown writes it.
func (a *app) errShown(err error) string { return a.shown(err.Error()) }

// plural is n and noun, with an s past 1 or at 0.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "s") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// reportDocs is -docs: every run in gate/results judged again by this binary's judge, which must be the one the
// checkout holds, committed or not, as the renderer's TestDocs judges them with the tree's own; then the summary
// tables built from the committed records laid beside docs/tables/ (report.AddRows): a row the build gives with other
// bytes than the one on disk is named and refused, exit 1, nothing written; else each table that gains a row is
// written, a row on disk the build does not give kept, and each of the documents report.Docs renders from the tables
// written under docs/. An SVG in docs/img that no figure writes is named, exit 1, and left where it is.
func (a *app) reportDocs(ctx context.Context) int {
	if why := a.judgeChanged(); why != "" {
		return a.refused(fmt.Errorf("-docs: %s", why))
	}
	resDir := filepath.Join(a.root, filepath.FromSlash(gateResults))
	rs, err := report.Load(a.root, resDir, rejudge.Checkout{Root: a.root})
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: %v\n", a.errShown(err))
		return exitUsage
	}
	fsys := os.DirFS(a.root)
	built, err := report.Tables(rs, fsys, judge.Self().SHA256)
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: %v\n", a.errShown(err))
		return exitFailed
	}
	dir := report.DocsDir + "/" + report.TablesDir
	ts, changes, err := report.AddRows(fsys, built)
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: reading %s: %v\n", dir, a.errShown(err))
		return exitFailed
	}
	if len(changes) > 0 {
		for _, c := range changes {
			fmt.Fprintf(a.stderr, "laug · report: %s/%s: the records give it other bytes; -docs adds rows and "+
				"never changes one\n", dir, c)
		}
		fmt.Fprintf(a.stderr, "laug · report: -docs refused: %s would change, nothing written; %s -tables "+
			"rewrites the tables whole\n", plural(len(changes), "row"), self)
		return exitFailed
	}
	var merged []report.Table
	for _, t := range ts {
		merged = append(merged, t.Table)
	}
	files, err := report.Docs(rs, merged)
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: %v\n", a.errShown(err))
		return exitFailed
	}
	figures := 0
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".svg") {
			figures++
		}
	}
	fmt.Fprintf(a.stdout, "laug · report: docs/%s, %s and %s from the %s in %s; Ctrl-C stops it\n",
		report.Evidence, plural(figures, "figure"), plural(len(ts), "table"), plural(len(rs.Runs), "gate run"),
		gateResults)
	fmt.Fprintf(a.stdout, "laug · rejudge, %s\n", rs.Counts)
	code := exitDone
	for _, r := range rs.Runs {
		for _, p := range r.Problems {
			fmt.Fprintf(a.stderr, "laug · report: %s: %s\n", r.ID, p)
			code = exitFailed
		}
	}
	changed := 0
	for _, t := range ts {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: stopped before %s/%s: %v\n", dir, t.Files[0].Name, a.errShown(err))
			return exitFailed
		}
		d, err := tableDiff(fsys, dir, t.Table)
		if err != nil {
			fmt.Fprintf(a.stderr, "laug · report: reading %s: %v\n", dir, a.errShown(err))
			return exitFailed
		}
		what := "the same bytes, left as it was"
		if !d.same() {
			if err := writeTable(filepath.Join(a.root, filepath.FromSlash(dir)), t.Table, d.stale); err != nil {
				fmt.Fprintf(a.stderr, "laug · report: writing %s/%s: %v\n", dir, t.Name, a.errShown(err))
				return exitFailed
			}
			changed++
			switch {
			case len(t.Added) > 0:
				what = "written: " + keysClause(t.Added, "added")
			case d.none:
				what = "written: no rows, a new file"
			default:
				what = "written: the same rows, laid out in other files"
			}
		}
		if len(t.Kept) > 0 {
			what += "; " + keysClause(t.Kept, "kept") + " that the records no longer give"
		}
		fmt.Fprintf(a.stdout, "laug · report: %s/%s: %s; %s\n", dir, tableFiles(t.Table), plural(t.Rows, "row"),
			what)
	}
	readme := dir + "/" + report.TablesReadme
	data := report.TablesReadmeData()
	if was, err := fs.ReadFile(fsys, readme); err == nil && bytes.Equal(was, data) {
		fmt.Fprintf(a.stdout, "laug · report: %s: the same bytes, left as it was\n", readme)
	} else {
		p := filepath.Join(a.root, filepath.FromSlash(readme))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: writing %s: %v\n", readme, a.errShown(err))
			return exitFailed
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: writing %s: %v\n", readme, a.errShown(err))
			return exitFailed
		}
		changed++
		fmt.Fprintf(a.stdout, "laug · report: %s: written\n", readme)
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: stopped before docs/%s: %v\n", f.Path, a.errShown(err))
			return exitFailed
		}
		p := filepath.Join(a.root, report.DocsDir, filepath.FromSlash(f.Path))
		was, err := os.ReadFile(p)
		same := err == nil && bytes.Equal(was, f.Data)
		if !same {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				fmt.Fprintf(a.stderr, "laug · report: writing docs/%s: %v\n", f.Path, a.errShown(err))
				return exitFailed
			}
			if err := os.WriteFile(p, f.Data, 0o644); err != nil {
				fmt.Fprintf(a.stderr, "laug · report: writing docs/%s: %v\n", f.Path, a.errShown(err))
				return exitFailed
			}
			changed++
		}
		what := "the same bytes, left as it was"
		if !same {
			what = "written"
		}
		fmt.Fprintf(a.stdout, "laug · report: docs/%s: %s\n", f.Path, what)
	}
	stale, err := report.Stale(os.DirFS(a.root), files)
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: reading docs/img: %v\n", a.errShown(err))
		return exitFailed
	}
	for _, p := range stale {
		fmt.Fprintf(a.stderr, "laug · report: %s: no figure writes it now; git rm it, or add the figure back\n", p)
		code = exitFailed
	}
	fmt.Fprintf(a.stdout, "laug · report: %s of %d written into docs/\n", plural(changed, "file"),
		len(files)+len(ts)+1)
	return code
}

// keysClause is the rows of keys and what was done to them, up to 3 of keys named: "2 rows added (a, b)".
func keysClause(keys []string, what string) string {
	s := plural(len(keys), "row") + " " + what + " (" + strings.Join(keys[:min(3, len(keys))], ", ")
	if len(keys) > 3 {
		s += ", ..."
	}
	return s + ")"
}

// reportTables is -tables: every run in gate/results judged again by this binary's judge, which must be the
// checkout's, as -docs requires; then every table report.Tables builds from the committed records, each compared with
// its files under docs/tables/ and written where it differs, a file of the table's that the build no longer gives
// removed; then any other .jsonl file there, which no table writes, removed; then docs/tables/README.md. One line a
// table: its rows, its bytes and what was done; one a file no table writes; one for the README. With check it writes
// nothing: each table that differs names its rows added, changed and removed, by key, each file no table writes would
// go, and the exit is 1. Without check it refuses, writing nothing, while a record the records table marks kept false
// has no file here: the build would drop that record's rows.
func (a *app) reportTables(ctx context.Context, check bool) int {
	if why := a.judgeChanged(); why != "" {
		return a.refused(fmt.Errorf("-tables: %s", why))
	}
	if !check {
		gone, err := report.Archived(os.DirFS(a.root))
		if err != nil {
			fmt.Fprintf(a.stderr, "laug · report: %v\n", a.errShown(err))
			return exitFailed
		}
		if len(gone) > 0 {
			return a.refused(fmt.Errorf("-tables: the records table marks %s kept false with no file "+
				"here, %s first: a build would drop their rows; nothing written (-tables -check names what differs)",
				plural(len(gone), "record"), gone[0]))
		}
	}
	resDir := filepath.Join(a.root, filepath.FromSlash(gateResults))
	rs, err := report.Load(a.root, resDir, rejudge.Checkout{Root: a.root})
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: %v\n", a.errShown(err))
		return exitUsage
	}
	ts, err := report.Tables(rs, os.DirFS(a.root), judge.Self().SHA256)
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: %v\n", a.errShown(err))
		return exitFailed
	}
	dir := report.DocsDir + "/" + report.TablesDir
	what := "into " + dir + "/"
	if check {
		what = "checked against " + dir + "/, nothing written"
	}
	fmt.Fprintf(a.stdout, "laug · report: %s from the committed records (%s in %s), %s; Ctrl-C stops it\n",
		plural(len(ts), "table"), plural(len(rs.Runs), "gate run"), gateResults, what)
	fmt.Fprintf(a.stdout, "laug · rejudge, %s\n", rs.Counts)
	code := exitDone
	for _, r := range rs.Runs {
		for _, p := range r.Problems {
			fmt.Fprintf(a.stderr, "laug · report: %s: %s\n", r.ID, p)
			code = exitFailed
		}
	}
	fsys := os.DirFS(a.root)
	differ := 0
	for _, t := range ts {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: stopped before %s/%s: %v\n", dir, t.Files[0].Name, a.errShown(err))
			return exitFailed
		}
		d, err := tableDiff(fsys, dir, t)
		if err != nil {
			fmt.Fprintf(a.stderr, "laug · report: reading %s: %v\n", dir, a.errShown(err))
			return exitFailed
		}
		size := 0
		for _, f := range t.Files {
			size += len(f.Data)
		}
		head := fmt.Sprintf("laug · report: %s/%s: %s, %s", dir, tableFiles(t), plural(t.Rows, "row"),
			plural(size, "byte"))
		if d.same() && check {
			fmt.Fprintf(a.stdout, "%s; the same bytes\n", head)
			continue
		}
		if d.same() {
			fmt.Fprintf(a.stdout, "%s; the same bytes, left as it was\n", head)
			continue
		}
		differ++
		if check {
			fmt.Fprintf(a.stdout, "%s; differs: %s\n", head, d.clause("would go"))
			continue
		}
		if err := writeTable(filepath.Join(a.root, filepath.FromSlash(dir)), t, d.stale); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: writing %s/%s: %v\n", dir, t.Name, a.errShown(err))
			return exitFailed
		}
		fmt.Fprintf(a.stdout, "%s; written: %s\n", head, d.clause("removed"))
	}
	orphans, err := tableOrphans(fsys, dir, ts)
	if err != nil {
		fmt.Fprintf(a.stderr, "laug · report: reading %s: %v\n", dir, a.errShown(err))
		return exitFailed
	}
	for _, name := range orphans {
		differ++
		if check {
			fmt.Fprintf(a.stdout, "laug · report: %s/%s: no table writes it; would go\n", dir, name)
			continue
		}
		if err := os.Remove(filepath.Join(a.root, filepath.FromSlash(dir), name)); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: removing %s/%s: %v\n", dir, name, a.errShown(err))
			return exitFailed
		}
		fmt.Fprintf(a.stdout, "laug · report: %s/%s: no table writes it; removed\n", dir, name)
	}
	readme := dir + "/" + report.TablesReadme
	data := report.TablesReadmeData()
	was, err := fs.ReadFile(fsys, readme)
	switch same := err == nil && bytes.Equal(was, data); {
	case same && check:
		fmt.Fprintf(a.stdout, "laug · report: %s: the same bytes\n", readme)
	case same:
		fmt.Fprintf(a.stdout, "laug · report: %s: the same bytes, left as it was\n", readme)
	case check:
		differ++
		fmt.Fprintf(a.stdout, "laug · report: %s: differs\n", readme)
	default:
		differ++
		p := filepath.Join(a.root, filepath.FromSlash(readme))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: writing %s: %v\n", readme, a.errShown(err))
			return exitFailed
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			fmt.Fprintf(a.stderr, "laug · report: writing %s: %v\n", readme, a.errShown(err))
			return exitFailed
		}
		fmt.Fprintf(a.stdout, "laug · report: %s: written\n", readme)
	}
	files := plural(len(ts)+1, "file") + " (" + plural(len(ts), "table") + " and the README)"
	if len(orphans) > 0 {
		files = plural(len(ts)+1+len(orphans), "file") + " (" + plural(len(ts), "table") + ", the README and " +
			plural(len(orphans), "file") + " no table writes)"
	}
	if check {
		if differ == 0 {
			fmt.Fprintf(a.stdout, "laug · report: 0 of %s differ from a build\n", files)
			return code
		}
		fmt.Fprintf(a.stdout, "laug · report: %d of %s differ from a build; %s -tables writes them\n",
			differ, files, self)
		return exitFailed
	}
	fmt.Fprintf(a.stdout, "laug · report: %d of %s written into %s/\n", differ, files, dir)
	return code
}

// tableFiles names a table's files: <name>.jsonl, or the first and last when it has rolled.
func tableFiles(t report.Table) string {
	if len(t.Files) == 1 {
		return t.Files[0].Name
	}
	return t.Files[0].Name + " to " + t.Files[len(t.Files)-1].Name
}

// A diff is how a table on disk differs from a build: the files that differ in bytes or are not there, the files on
// disk the build no longer gives, and the rows by key, added, changed and removed.
type diff struct {
	files, stale            []string
	added, changed, removed []string
	unread                  string // why the files on disk could not be read as a table, "" when they could
	none                    bool   // no file of the table on disk
}

func (d diff) same() bool { return len(d.files) == 0 && len(d.stale) == 0 }

// clause is the difference in a clause: the rows added, changed and removed, up to 3 keys of each, and the files the
// build no longer gives, then gone, "would go" with -check and "removed" when written.
func (d diff) clause(gone string) string {
	var parts []string
	if d.unread != "" {
		parts = append(parts, "the files on disk do not read as a table: "+d.unread)
	} else {
		for _, p := range []struct {
			what string
			keys []string
		}{{"added", d.added}, {"changed", d.changed}, {"removed", d.removed}} {
			if len(p.keys) > 0 {
				parts = append(parts, keysClause(p.keys, p.what))
			}
		}
		switch {
		case len(parts) > 0:
		case d.none:
			parts = append(parts, "no rows, a new file")
		default:
			parts = append(parts, "the same rows, laid out in other files")
		}
	}
	if len(d.stale) > 0 {
		parts = append(parts, strings.Join(d.stale, ", ")+" "+gone)
	}
	return strings.Join(parts, "; ")
}

// tableDiff compares the build t with its files under dir of fsys.
func tableDiff(fsys fs.FS, dir string, t report.Table) (diff, error) {
	var d diff
	onDisk, err := tables.OnDisk(fsys, dir, t.Name)
	if err != nil {
		return d, err
	}
	built := map[string]bool{}
	for _, f := range t.Files {
		built[f.Name] = true
		was, err := fs.ReadFile(fsys, dir+"/"+f.Name)
		if err != nil || !bytes.Equal(was, f.Data) {
			d.files = append(d.files, f.Name)
		}
	}
	for _, name := range onDisk {
		if !built[name] {
			d.stale = append(d.stale, name)
		}
	}
	if d.same() {
		return d, nil
	}
	var old []tables.Row
	d.none = len(onDisk) == 0
	if !d.none {
		if old, err = tables.Read(fsys, dir, t.Name); err != nil {
			d.unread = err.Error()
			return d, nil
		}
	}
	was := map[string][]byte{}
	for _, r := range old {
		was[r.Key] = r.Line
	}
	now := map[string]bool{}
	for _, f := range t.Files {
		rows, err := tables.Parse(f.Data)
		if err != nil {
			return d, fmt.Errorf("%s as built: %w", f.Name, err)
		}
		for _, r := range rows {
			now[r.Key] = true
			line, ok := was[r.Key]
			switch {
			case !ok:
				d.added = append(d.added, r.Key)
			case !bytes.Equal(line, r.Line):
				d.changed = append(d.changed, r.Key)
			}
		}
	}
	for _, r := range old {
		if !now[r.Key] {
			d.removed = append(d.removed, r.Key)
		}
	}
	return d, nil
}

// tableOrphans are the .jsonl files under dir of fsys that belong to no table of ts: neither a file the build gives nor
// one of a table's own files on disk, which tableDiff names as stale. Sorted.
func tableOrphans(fsys fs.FS, dir string, ts []report.Table) ([]string, error) {
	owned := map[string]bool{}
	for _, t := range ts {
		for _, f := range t.Files {
			owned[f.Name] = true
		}
		onDisk, err := tables.OnDisk(fsys, dir, t.Name)
		if err != nil {
			return nil, err
		}
		for _, name := range onDisk {
			owned[name] = true
		}
	}
	ents, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") && !owned[e.Name()] {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// writeTable writes t's files into dir, made when it is not there, and removes stale, the table's files on disk the
// build no longer gives.
func writeTable(dir string, t report.Table, stale []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range t.Files {
		if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
			return err
		}
	}
	for _, name := range stale {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// judgeChanged says why the judge this binary embeds is not the one the checkout holds, "" when it is: the sources'
// sha256 of internal/judge and internal/jsonx on disk, committed or not, is this binary's. go run builds from the
// checkout, and a test binary compiles the tree's own, so TestDocs and -docs judge with the same code; the check
// catches a binary built elsewhere and copied in.
func (a *app) judgeChanged() string {
	srcs, err := judge.SourcesIn(os.DirFS(a.root))
	if err != nil {
		return fmt.Sprintf("the checkout's judge sources: %v", err)
	}
	if disk, self := judge.Sum(srcs), judge.Self().SHA256; disk != self {
		return fmt.Sprintf("the judge this binary holds (sources' sha256 %s) is not the one the checkout holds "+
			"(%s): run it from the checkout, go -C _report run ./cmd/report", self[:12], disk[:12])
	}
	return ""
}
