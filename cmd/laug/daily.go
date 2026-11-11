package main

// What the daily path's script verbs share: the checks on a value pasted bare onto a script's line, the last lease, a
// run id's default, whole seconds, the docker lookup of a local cell's server tag, the names a fault or a candidate may
// take, the release raun and candidate make after a signal, and a raun's length.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ymur-studio/laug/internal/poolapi"
)

// wordRE is the shape of a word pasted bare onto a script's line: a run id, a scenario, a fault, a project or a
// namespace passes this check first.
var wordRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// checkWords returns an error naming the first flag, of pairs given as flag then value, whose value is not one word.
func checkWords(pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		if !wordRE.MatchString(pairs[i+1]) {
			return fmt.Errorf("%s %s: not one word of letters, digits, ., _ and -, from a letter or digit", pairs[i],
				shellWord(pairs[i+1]))
		}
	}
	return nil
}

// usageError prints why on one stderr line, its paths as textShown writes them, then f's synopsis and the line that
// prints its flags, and returns 2.
func (a *App) usageError(f *flags, format string, v ...any) int {
	fmt.Fprintf(a.Stderr, "laug · %s: %s\n", f.cmd, a.textShown(fmt.Sprintf(format, v...)))
	f.mistake(a.Stderr)
	return exitUsage
}

// refused prints why cmd refuses, one stderr line, its paths as textShown writes them, and returns 2.
func (a *App) refused(cmd string, err error) int {
	fmt.Fprintf(a.Stderr, "laug · %s: %s\n", cmd, a.errShown(err))
	return exitUsage
}

// given reports whether the flag called name was set on the command line.
func given(f *flags, name string) bool {
	set := false
	f.Visit(func(fl *flag.Flag) { set = set || fl.Name == name })
	return set
}

// leaseFields is the last lease, each field read as jq -r '.<field> // empty' reads it: a file that is missing or does
// not parse reads as no lease, every field empty.
type leaseFields struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	Prometheus string `json:"prometheus"`
	Project    string `json:"project"`
	ServerTag  string `json:"server_tag"`
	ExpiresAt  string `json:"expires_at"`
	SSH        string `json:"ssh"` // a microVM cell's guest, user@address; empty for a compose cell
}

// lastLease reads the last-lease file at the checkout's root, where laug lease keeps it and the scripts read it.
func (a *App) lastLease() leaseFields {
	return a.leaseAt(poolapi.LastLeaseFile)
}

// leaseAt reads the lease in file, from the checkout's root unless absolute (lastLeasePath), as lastLease reads the
// last one.
func (a *App) leaseAt(file string) leaseFields {
	var l leaseFields
	b, err := os.ReadFile(lastLeasePath(a, file))
	if err != nil || json.Unmarshal(b, &l) != nil {
		return leaseFields{}
	}
	return l
}

// now is the App's clock, or time.Now.
func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// runIDNow is a run id's default: the UTC time to the second, in date -u +%Y%m%d-%H%M%S's form.
func (a *App) runIDNow() string {
	return a.now().UTC().Format("20060102-150405")
}

// seconds is d as a knob in whole seconds takes it, or an error that names flagName when d is negative or not whole.
func seconds(flagName string, d time.Duration) (string, error) {
	if d < 0 || d%time.Second != 0 {
		return "", fmt.Errorf("%s %s: whole seconds, 0 or more", flagName, d)
	}
	return strconv.FormatInt(int64(d/time.Second), 10), nil
}

// lengthRE is a load's length as Omes and load/lost-work.sh's lw_dur_s both read it: whole hours, minutes and
// seconds, in that order.
var lengthRE = regexp.MustCompile(`^([0-9]+h)?([0-9]+m)?([0-9]+s)?$`)

// checkLength returns an error naming flagName when v is not such a length over 0. The script gets v as typed.
func checkLength(flagName, v string) error {
	if d, err := time.ParseDuration(v); err != nil || d <= 0 || !lengthRE.MatchString(v) {
		return fmt.Errorf("%s %s: a length in whole h, m and s, over 0: 5m, 90s, 1h30m", flagName, shellWord(v))
	}
	return nil
}

// checkCount returns an error naming flagName when n, given, is not over 0.
func checkCount(flagName string, n int) error {
	if n <= 0 {
		return fmt.Errorf("%s %d: a whole number over 0", flagName, n)
	}
	return nil
}

// poolURL is -pool as the scripts take POOL_ADDR, an http:// URL with no path; its host must be a loopback IP.
func poolURL(addr string) (string, error) {
	c, err := poolapi.New(addr)
	if err != nil {
		return "", fmt.Errorf("-pool %s: %w", addr, err)
	}
	return c.Base(), nil
}

// tmpDir is where laug puts a file it removes once read: TMPDIR, the one variable laug reads for itself, else the
// system's.
func (a *App) tmpDir() string {
	if d := a.getenv("TMPDIR"); d != "" {
		return d
	}
	return os.TempDir()
}

// lookPath finds an executable called name on the App's PATH, or returns "". The process's own PATH is never read, so
// a test's stub is the only docker a test can run.
func (a *App) lookPath(name string) string {
	for _, dir := range filepath.SplitList(a.getenv("PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

var localTagRE = regexp.MustCompile(`^temporalio/server:([0-9][0-9.]*)$`)

// localServerTag is the server tag the temporal-history container of compose project project runs: docker ps for the
// container, docker inspect for its image, and the x.y.z of temporalio/server:x.y.z. "" when there is no docker on
// PATH, no such container, another image, or no answer in 10 s.
func (a *App) localServerTag(ctx context.Context, project string) string {
	docker := a.lookPath("docker")
	if docker == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output := func(args ...string) string {
		cmd := exec.CommandContext(ctx, docker, args...)
		cmd.Dir, cmd.Env = a.Root, append([]string{}, a.Environ...)
		b, _ := cmd.Output()
		return strings.TrimSpace(string(b))
	}
	id, _, _ := strings.Cut(output("ps", "-q", "--filter", "label=com.docker.compose.project="+project, "--filter",
		"label=com.docker.compose.service=temporal-history"), "\n")
	if id == "" {
		return ""
	}
	if m := localTagRE.FindStringSubmatch(output("inspect", "-f", "{{.Config.Image}}", id)); m != nil {
		return m[1]
	}
	return ""
}

// A choice is a name a fault or a candidate verb takes, read from the file its script reads: faults/run.sh reads
// CATALOG, else faults/catalog.yaml; faults/candidate.sh reads CANDIDATES, else faults/candidates.yaml. Both are seams
// the harness points at fixtures, so laug reads them for this check alone. The YAML is read as a line scan: under the
// top-level key, a fault is a key two spaces in, and a candidate a "  - name: " line.
type choice struct {
	seam, file, section string
	item                *regexp.Regexp
	what                string // "fault", "candidate"
}

var (
	faultChoice = choice{"CATALOG", "faults/catalog.yaml", "faults",
		regexp.MustCompile(`^  ([A-Za-z0-9][A-Za-z0-9._-]*):\s*(#.*)?$`), "fault"}
	candidateChoice = choice{"CANDIDATES", "faults/candidates.yaml", "candidates",
		regexp.MustCompile(`^  - name: ([A-Za-z0-9][A-Za-z0-9._-]*)\s*(#.*)?$`), "candidate"}
)

// where is the file the script reads, as the user would name it, and its path: the seam's file when the environment
// sets it, else the checkout's; a relative one starts at the checkout's root, where the script runs.
func (ch choice) where(a *App) (file, path string) {
	file = ch.file
	if s := a.getenv(ch.seam); s != "" {
		file = s
	}
	path = file
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.Root, path)
	}
	return file, path
}

// names returns the file the script reads, as the user would name it, and the names it holds.
func (ch choice) names(a *App) (string, []string, error) {
	file, path := ch.where(a)
	f, err := os.Open(path)
	if err != nil {
		return file, nil, err
	}
	defer f.Close()
	top := regexp.MustCompile(`^` + ch.section + `:\s*(#.*)?$`)
	var names []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch trimmed := strings.TrimSpace(line); {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		case !strings.HasPrefix(line, " "):
			in = top.MatchString(line)
		case in:
			if m := ch.item.FindStringSubmatch(line); m != nil {
				names = append(names, m[1])
			}
		}
	}
	if err := sc.Err(); err != nil {
		return file, nil, err
	}
	if len(names) == 0 {
		return file, nil, fmt.Errorf("no %s under %s:", ch.what, ch.section)
	}
	return file, names, nil
}

// list says what the file holds, for the line that names the choices: how many, then the names in rows rows at most
// (inRows).
func (ch choice) list(a *App, rows int) string {
	file, names, err := ch.names(a)
	if err != nil {
		return fmt.Sprintf("%s lists the %ss, and it cannot be read: %s", file, ch.what, a.errShown(err))
	}
	return fmt.Sprintf("%s holds %d:\n%s", file, len(names), inRows(names, rows))
}

// noChoice prints that f's verb names no choice of ch's, the choices and its synopsis, in mistakeRows rows at most,
// and returns 2.
func (a *App) noChoice(f *flags, ch choice) int {
	var tail strings.Builder
	f.mistake(&tail)
	rows := mistakeRows - 1 - strings.Count(tail.String(), "\n")
	fmt.Fprintf(a.Stderr, "laug · %s: names no %s: %s\n%s", f.cmd, ch.what, ch.list(a, rows), tail.String())
	return exitUsage
}

// check returns an error when name is not one of the file's: with the nearest names when one is near, else with every
// name, in the rows its first line leaves, which may take two.
func (ch choice) check(a *App, name string) error {
	file, names, err := ch.names(a)
	if err != nil {
		return fmt.Errorf("%s %s: %s lists the %ss, and it cannot be read: %v", ch.what, shellWord(name), file, ch.what,
			err)
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	if near := didYouMean("", name, names); near != "" {
		return fmt.Errorf("no %s %s in %s%s", ch.what, shellWord(name), file, near)
	}
	return fmt.Errorf("no %s %s in %s, which holds %d:\n%s", ch.what, shellWord(name), file, len(names),
		inRows(names, mistakeRows-2))
}

// mistakeRows is the most rows a mistake writes: 24, less the prompt after it, so its first line stays in sight.
const mistakeRows = 23

// inRows is names two spaces in, with no newline after the last: a line each when they fit in rows, else down and
// across in as few columns as fit, two spaces apart, each as wide as its longest name; never wider than 79 columns.
func inRows(names []string, rows int) string {
	cols := 1
	if rows > 0 && len(names) > rows {
		cols = (len(names) + rows - 1) / rows
	}
	for ; cols > 1; cols-- {
		if _, wide := colWidths(names, cols); wide <= 79 {
			break
		}
	}
	if cols == 1 {
		return "  " + strings.Join(names, "\n  ")
	}
	down := (len(names) + cols - 1) / cols
	widths, _ := colWidths(names, cols)
	lines := make([]string, down)
	for i, n := range names {
		lines[i%down] += "  " + n
		if i+down < len(names) {
			lines[i%down] += strings.Repeat(" ", widths[i/down]-utf8.RuneCountInString(n))
		}
	}
	return strings.Join(lines, "\n")
}

// colWidths is the width of each column when names go down and across in cols columns, its longest name's, and the
// width of the widest line.
func colWidths(names []string, cols int) (widths []int, wide int) {
	down := (len(names) + cols - 1) / cols
	widths = make([]int, (len(names)+down-1)/down)
	for i, n := range names {
		widths[i/down] = max(widths[i/down], utf8.RuneCountInString(n))
	}
	for _, w := range widths {
		wide += 2 + w
	}
	return widths, wide
}

// releaseWait bounds the release raun and candidate make on their way out, a signal or not: the pool's own bound on
// a destroy is its -create-timeout, 5m by default.
const releaseWait = 6 * time.Minute

// giveBack releases lease id for cmd once its work is over, whether or not a signal ended ctx, and removes the
// last-lease file at path when it still names id. It prints one line, on stdout when the cell went back, with tail at
// its end, and reports whether it did.
func (a *App) giveBack(ctx context.Context, cmd string, c *poolapi.Client, pool, id, path, file, tail string) bool {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseWait)
	defer cancel()
	if _, err := c.Release(rctx, id); err != nil {
		poolFailedAs(rctx, a, cmd, "release", pool, id, a.releaseLine(id, pool, file)+" gives it back", err)
		return false
	}
	note := ""
	switch removed, err := poolapi.RemoveLast(path, id); {
	case err != nil:
		note = "; " + file + ", which names it, stays: " + a.errShown(err)
	case removed:
		note = "; " + file + " removed"
	}
	fmt.Fprintf(a.Stdout, "laug · %s: %s given back, its cell destroyed%s%s\n", cmd, id, note, tail)
	return true
}

// raunJSON is where faults/run.sh writes run id's raun JSON, RESULTS_DIR, else faults/results, and the run id, as laug
// prints a path, when the file is there and was written since. "" when not: the script stopped before it wrote one.
func (a *App) raunJSON(runID string, since time.Time) string {
	dir := a.getenv("RESULTS_DIR")
	if dir == "" {
		dir = "faults/results"
	}
	path := filepath.Join(dir, runID+".json")
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.Root, path)
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() || fi.ModTime().Before(since) {
		return ""
	}
	return a.pathShown(path)
}

// catalogField is a line of the catalog that sets a number: two spaces in under defaults:, four under a fault's row,
// a whole number, then perhaps a comment.
var catalogField = regexp.MustCompile(`^( {2}| {4})([a-z_]+):\s*([0-9]+)\s*(#.*)?$`)

// raunLength is how long a raun of fault runs, in seconds, as faults/run.sh reads its catalog: windows is steady_s,
// hold_s, settle_s and after_s summed, and budget is budget_s, the most a recovery may add before the after window.
// Each field is the row's own, else the defaults'; settle_s is 30 when neither sets it. ok is false when the catalog
// cannot be read or a field other than settle_s is set nowhere.
func (a *App) raunLength(fault string) (windows, budget int, ok bool) {
	_, path := faultChoice.where(a)
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	defaults, row := map[string]int{}, map[string]int{}
	section, in := "", false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // a hypothesis is one long line
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		case !strings.HasPrefix(line, " "):
			section, _, _ = strings.Cut(line, ":")
		case section == "faults" && !strings.HasPrefix(line, "    "):
			name, _, _ := strings.Cut(trimmed, ":")
			in = name == fault
		default:
			m := catalogField.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[3])
			switch {
			case section == "defaults" && m[1] == "  ":
				defaults[m[2]] = n
			case section == "faults" && in && m[1] == "    ":
				row[m[2]] = n
			}
		}
	}
	if sc.Err() != nil {
		return 0, 0, false
	}
	get := func(key string) (int, bool) {
		if n, ok := row[key]; ok {
			return n, true
		}
		n, ok := defaults[key]
		return n, ok
	}
	for _, key := range []string{"steady_s", "hold_s", "settle_s", "after_s"} {
		n, ok := get(key)
		switch {
		case !ok && key == "settle_s":
			n = 30
		case !ok:
			return 0, 0, false
		}
		windows += n
	}
	budget, ok = get("budget_s")
	return windows, budget, ok
}
