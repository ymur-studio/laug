package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The check of the workflows: .github/workflows/*.yml, which GitHub runs on the events under each file's on:, and
// which make test-go checks first, before a push reaches GitHub. TestWorkflows holds each to these rules, each named
// by the kind of problem it reports:
//
//   listed  every entry under .github/workflows that git does not ignore is a .yml file.
//   yaml    yq v4 reads the file as a mapping with triggers under on and at least one job.
//   runner  every job's runs-on is one GitHub-hosted Ubuntu image, ubuntu-<version> or ubuntu-latest: never a
//           self-hosted runner, a label list or a group. A pull request's workflow files run from its own merge
//           commit, so a fork can send its code to any runner a workflow file names.
//   pin     every uses: names an action by its commit, 40 hex, and the line's comment is its tag alone, v<n>.
//   perms   permissions at the top, none of them write; a job that grants a write has no uses: and runs neither make
//           nor laug, so it runs no file of the checkout.
//   secret  no pull_request_target trigger; a workflow that names a secret has no pull_request trigger, and each job
//           that names one has an if:.
//   inject  no ${{ in a run: script, where an expression's text would be pasted into the shell; it goes in env.
//   make    in a run: script, make's target is one of the Makefile's .PHONY targets.
//   laug    in a run: script, laug <command> [<verb>] names a row of laug help, and a verb laug <command> -h prints.
//   flag    each flag after them is one laug <command> [<verb>] -h prints.
//   agent   a command that starts a coding agent keeps the agent rules, when a test file of the tree holds them
//           (workflowAgent); a tree with no agent holds none.
//
// A run: script is read as a shell block: shellCommands splits it into commands, quotes kept.

const workflowsDir = ".github/workflows"

// workflowAgent, when a test file of the tree sets it, holds a command of a run: script, f its words past assignments
// and wrappers, to the agent rules; where names the step. workflowAgentPlants are its plants for TestWorkflowPlants,
// each built with set, which puts a line of workflowBody in place.
var (
	workflowAgent       func(s *site, where string, f []string, add func(int, string, string, ...any))
	workflowAgentPlants func(set func(int, string) func([]string) []string) []workflowPlant
)

// workflowPlant is one planted change: its name, the kind it fails with, and the edit of workflowBody's lines.
type workflowPlant struct {
	name, kind string
	edit       func([]string) []string
}

var (
	runnerRE   = regexp.MustCompile(`^ubuntu-(?:latest|[0-9]+\.[0-9]+)$`)
	usesLineRE = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)\s*(.*)$`)
	pinnedRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_./-]+)?@[0-9a-f]{40}$`)
	tagNoteRE  = regexp.MustCompile(`^#\s*v[0-9]+(?:\.[0-9]+)*\s*$`)
	flagRE     = regexp.MustCompile(`^--?([a-z][a-z0-9-]*)(?:=.*)?$`)
	helpFlagRE = regexp.MustCompile(`^  -([a-z][a-z0-9-]*)(?:\s|$)`)
)

// wfJob and wfStep are the parts of a job the rules read, from yq's JSON.
type wfJob struct {
	RunsOn      any      `json:"runs-on"`
	If          any      `json:"if"`
	Permissions any      `json:"permissions"`
	Steps       []wfStep `json:"steps"`
}

type wfStep struct {
	Name string `json:"name"`
	Uses string `json:"uses"`
	Run  string `json:"run"`
}

// helpFlags returns the flags laug <command> [<verb>] -h prints, from a laug built into dir, each read once.
func helpFlags(t *testing.T) func(key ...string) (map[string]bool, error) {
	t.Helper()
	dir := t.TempDir()
	laug := filepath.Join(dir, "laug")
	build := exec.Command("go", "build", "-o", laug, "./cmd/laug")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/laug: %v\n%s", err, out)
	}
	seen := map[string]map[string]bool{}
	return func(key ...string) (map[string]bool, error) {
		k := strings.Join(key, " ")
		if f, ok := seen[k]; ok {
			return f, nil
		}
		h, err := run(dir, laug, append(key, "-h")...)
		if err != nil {
			return nil, err
		}
		f := map[string]bool{"h": true, "help": true}
		for _, l := range strings.Split(h, "\n") {
			if m := helpFlagRE.FindStringSubmatch(l); m != nil {
				f[m[1]] = true
			}
		}
		seen[k] = f
		return f, nil
	}
}

// yqJSON is the workflow as JSON, as yq v4 reads it.
func yqJSON(raw []byte) ([]byte, error) {
	cmd := exec.Command("yq", "-o=json", ".", "-")
	cmd.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yq does not read it: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// triggers are the event names under on, which takes a name, a list of names or a mapping.
func triggers(raw json.RawMessage) []string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var out []string
	switch on := v.(type) {
	case string:
		out = []string{on}
	case []any:
		for _, e := range on {
			out = append(out, fmt.Sprint(e))
		}
	case map[string]any:
		for k := range on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// writes are the scopes a permissions value grants write, sorted; write-all is "all".
func writes(v any) []string {
	var out []string
	switch p := v.(type) {
	case string:
		if p == "write-all" {
			out = []string{"all"}
		}
	case map[string]any:
		for k, a := range p {
			if a == "write" {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// unq takes a shell word's quotes off.
func unq(w string) string { return strings.Trim(w, `"'`) }

// runCommands are the commands of a run: script, with their words past assignments and wrappers.
func runCommands(script string) [][]string {
	var out [][]string
	for _, c := range shellCommands(scan("```sh\n" + script + "\n```\n")) {
		f := c.words
		for len(f) > 0 && before.MatchString(f[0]) {
			f = f[1:]
		}
		if len(f) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// checkWorkflow returns the problems of a workflow, raw its bytes; flags reads laug's -h. A problem that no one line
// holds has line 0 and names its job and step.
func (s *site) checkWorkflow(raw []byte, flags func(...string) (map[string]bool, error)) []problem {
	var out []problem
	add := func(n int, kind, f string, a ...any) { out = append(out, problem{n, kind, fmt.Sprintf(f, a...)}) }

	for i, l := range strings.Split(string(raw), "\n") {
		m := usesLineRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if !pinnedRE.MatchString(unq(m[1])) {
			add(i+1, "pin", "%s: an action is named by its commit, owner/repo@<40 hex>", m[1])
		}
		if !tagNoteRE.MatchString(m[2]) {
			add(i+1, "pin", "%s: the line's comment is the tag alone, # v<n>", m[1])
		}
	}

	js, err := yqJSON(raw)
	if err != nil {
		add(1, "yaml", "%v", err)
		return out
	}
	var top map[string]json.RawMessage
	var jobsRaw map[string]json.RawMessage
	if json.Unmarshal(js, &top) != nil || json.Unmarshal(top["jobs"], &jobsRaw) != nil || len(jobsRaw) == 0 {
		add(1, "yaml", "not a mapping with a job under jobs")
		return out
	}
	on := triggers(top["on"])
	if len(on) == 0 {
		add(1, "yaml", "no trigger under on")
	}

	if p, ok := top["permissions"]; !ok {
		add(1, "perms", "no permissions at the top; without them a job gets the repository's default token")
	} else {
		var v any
		_ = json.Unmarshal(p, &v)
		if w := writes(v); len(w) > 0 {
			add(1, "perms", "the top grants %s write; a write goes in the one job that needs it",
				strings.Join(w, ", "))
		}
	}
	if slices.Contains(on, "pull_request_target") {
		add(1, "secret", "pull_request_target runs on a pull request's event with the repository's secrets and "+
			"token")
	}
	secret := bytes.Contains(js, []byte("secrets."))
	if secret && slices.Contains(on, "pull_request") {
		add(1, "secret", "names a secret and runs on pull_request")
	}

	ids := make([]string, 0, len(jobsRaw))
	for id := range jobsRaw {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var j wfJob
		if err := json.Unmarshal(jobsRaw[id], &j); err != nil {
			add(1, "yaml", "job %s: %v", id, err)
			continue
		}
		if ro, ok := j.RunsOn.(string); !ok || !runnerRE.MatchString(ro) {
			add(0, "runner", "job %s: runs-on %v; a GitHub-hosted Ubuntu image, ubuntu-<version>, alone", id, j.RunsOn)
		}
		if secret && bytes.Contains(jobsRaw[id], []byte("secrets.")) && (j.If == nil || j.If == "") {
			add(0, "secret", "job %s names a secret and has no if:", id)
		}
		w := writes(j.Permissions)
		for _, st := range j.Steps {
			where := fmt.Sprintf("job %s, step %q", id, st.Name)
			if st.Uses != "" && len(w) > 0 {
				add(0, "perms", "%s: the job grants %s write and runs an action", where, strings.Join(w, ", "))
			}
			if strings.Contains(st.Run, "${{") {
				add(0, "inject", "%s: ${{ in a run: script; give the value to the step in env", where)
			}
			for _, f := range runCommands(st.Run) {
				s.checkRunCommand(where, f, len(w) > 0, flags, add)
			}
		}
	}
	return out
}

// checkRunCommand holds one command of a run: script, its words past assignments and wrappers, to the make, laug,
// flag and agent rules, and to perms when its job grants a write.
func (s *site) checkRunCommand(where string, f []string, writer bool, flags func(...string) (map[string]bool, error),
	add func(int, string, string, ...any)) {
	if workflowAgent != nil {
		workflowAgent(s, where, f, add)
	}
	switch prog := unq(f[0]); prog {
	case "make":
		if writer {
			add(0, "perms", "%s: make in a job that grants a write", where)
		}
		for _, w := range f[1:] {
			if w = unq(w); before.MatchString(w) {
				continue
			}
			if word.MatchString(w) && !s.targets[w] {
				add(0, "make", "%s: make %s: no such make target (make help lists them)", where, w)
			}
			break
		}
	case "laug", "bin/laug", "./bin/laug":
		if writer {
			add(0, "perms", "%s: laug in a job that grants a write", where)
		}
		if len(f) < 2 || !word.MatchString(unq(f[1])) {
			return
		}
		cmd, rest := unq(f[1]), f[2:]
		verbs, ok := s.commands[cmd]
		if !ok {
			add(0, "laug", "%s: laug %s: no such command (laug help lists them)", where, cmd)
			return
		}
		key := []string{cmd}
		if len(verbs) > 0 {
			if len(rest) == 0 || !word.MatchString(unq(rest[0])) {
				return
			}
			if v := unq(rest[0]); !slices.Contains(verbs, v) {
				add(0, "laug", "%s: laug %s %s: %s has no such verb (laug %s -h lists them)", where, cmd, v, cmd, cmd)
				return
			}
			key, rest = append(key, unq(rest[0])), rest[1:]
		}
		fl, err := flags(key...)
		if err != nil {
			add(0, "flag", "%s: %v", where, err)
			return
		}
		for _, w := range rest {
			if m := flagRE.FindStringSubmatch(unq(w)); m != nil && !fl[m[1]] {
				add(0, "flag", "%s: laug %s -%s: no such flag (laug %s -h lists them)", where,
					strings.Join(key, " "), m[1], strings.Join(key, " "))
			}
		}
	}
}

// Every workflow under .github/workflows keeps the workflow rules.
func TestWorkflows(t *testing.T) {
	s := readSite(t)
	flags := helpFlags(t)
	entries, err := os.ReadDir(filepath.Join("..", filepath.FromSlash(workflowsDir)))
	if err != nil {
		t.Fatalf("the workflows: %v", err)
	}
	n := 0
	for _, e := range entries {
		page := workflowsDir + "/" + e.Name()
		if s.isIgnored(page) {
			continue
		}
		if e.IsDir() || path.Ext(page) != ".yml" {
			t.Errorf("%s: listed: a workflow here is one .yml file", page)
			continue
		}
		raw, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(page)))
		if err != nil {
			t.Errorf("%s: listed: %v", page, err)
			continue
		}
		n++
		for _, pr := range s.checkWorkflow(raw, flags) {
			t.Errorf("%s:%d: %s: %s", page, pr.line, pr.kind, pr.what)
		}
	}
	if n == 0 {
		t.Errorf("no workflow under %s", workflowsDir)
	}
	t.Logf("%d workflows under %s", n, workflowsDir)
}

// workflowBody is a workflow that keeps every rule; edit, when given, changes its lines first. It names no secret; its
// read job has an if:, under which workflowSecret names one. Its post job grants a write and runs no action and neither
// make nor laug, and its scripts hold a make target, laug commands with flags, a pipe, an || and a joined line.
func workflowBody(edit func([]string) []string) []byte {
	lines := []string{
		"name: plant",
		"on:",
		"  push:",
		"    branches: [main]",
		"  workflow_dispatch:",
		"permissions:",
		"  contents: read",
		"jobs:",
		"  test:",
		"    runs-on: ubuntu-24.04",
		"    steps:",
		"      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2",
		"        with:",
		"          persist-credentials: false",
		"      - name: build",
		"        env:",
		"          REF: ${{ github.ref_name }}",
		"        run: |",
		"          make build",
		`          bin/laug gate run 1.31.0 -fake -base "origin/$REF" -results "$RUNNER_TEMP/g" || rc=$?`,
		`          bin/laug gate rejudge -all | tee "$RUNNER_TEMP/rejudge.txt"`,
		"  read:",
		"    if: vars.LAUG_OWNER != ''",
		"    runs-on: ubuntu-24.04",
		"    permissions:",
		"      contents: read",
		"    steps:",
		"      - name: draft",
		"        env:",
		"          TZ: UTC",
		"        run: |",
		`          bin/laug gate rejudge -all \`,
		`            -results gate/results \`,
		`            >"$RUNNER_TEMP/draft.md"`,
		"  post:",
		"    needs: read",
		"    runs-on: ubuntu-latest",
		"    permissions:",
		"      issues: write",
		"    steps:",
		"      - name: post",
		"        env:",
		"          GH_TOKEN: ${{ github.token }}",
		"        run: gh issue comment 1 --body-file -",
	}
	if edit != nil {
		lines = edit(lines)
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// workflowSecret names a secret in the env of workflowBody's read job, which has an if:.
func workflowSecret(l []string) []string {
	l[29] = "          TOKEN: ${{ secrets.TOKEN }}"
	return l
}

// A workflow that keeps every rule holds no problem, and so does one that names a secret in a job with an if:.
func TestWorkflowClean(t *testing.T) {
	s := readSite(t)
	flags := helpFlags(t)
	for _, edit := range []func([]string) []string{nil, workflowSecret} {
		for _, pr := range s.checkWorkflow(workflowBody(edit), flags) {
			t.Errorf("a clean workflow has a problem at line %d: %s: %s", pr.line, pr.kind, pr.what)
		}
	}
}

// One planted change of each kind fails a workflow with that kind and no other.
func TestWorkflowPlants(t *testing.T) {
	s := readSite(t)
	flags := helpFlags(t)
	set := func(n int, line string) func([]string) []string {
		return func(l []string) []string { l[n] = line; return l }
	}
	insert := func(n int, line ...string) func([]string) []string {
		return func(l []string) []string { return slices.Insert(l, n, line...) }
	}
	cases := []workflowPlant{
		{"not YAML", "yaml", set(9, "    runs-on: [ubuntu-24.04")},
		{"no jobs", "yaml", func(l []string) []string { return l[:7] }},
		{"a self-hosted runner", "runner", set(9, "    runs-on: self-hosted")},
		{"a label list", "runner", set(9, "    runs-on: [self-hosted, laug]")},
		{"a runner group", "runner", set(9, "    runs-on: {group: lab}")},
		{"another image", "runner", set(9, "    runs-on: windows-2025")},
		{"an action by its tag", "pin", set(11, "      - uses: actions/checkout@v4 # v4")},
		{"no tag beside the commit", "pin",
			set(11, "      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683")},
		{"more than the tag beside the commit", "pin",
			set(11, "      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2, the latest")},
		{"no permissions at the top", "perms", func(l []string) []string { return slices.Delete(l, 5, 7) }},
		{"a write at the top", "perms", set(6, "  contents: write")},
		{"write-all at the top", "perms", func(l []string) []string {
			return slices.Insert(slices.Delete(l, 5, 7), 5, "permissions: write-all")
		}},
		{"a writing job that runs an action", "perms", insert(40,
			"      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2")},
		{"a writing job that runs make", "perms", set(43, "        run: make build")},
		{"a writing job that runs laug", "perms", set(43, "        run: bin/laug version")},
		{"pull_request_target", "secret", insert(5, "  pull_request_target:")},
		{"a secret on pull_request", "secret", func(l []string) []string {
			return slices.Insert(workflowSecret(l), 5, "  pull_request:")
		}},
		{"a secret in a job with no if", "secret", func(l []string) []string {
			return slices.Delete(workflowSecret(l), 22, 23)
		}},
		{"an expression in a script", "inject", set(18, "          make build ${{ github.ref_name }}")},
		{"a make target make lacks", "make", set(18, "          make frob")},
		{"a laug command laug lacks", "laug", set(18, "          bin/laug frob")},
		{"a verb gate lacks", "laug", set(18, "          bin/laug gate frob 1.31.0")},
		{"a flag gate run lacks", "flag", set(18, "          bin/laug gate run 1.31.0 -fakes")},
		{"a flag deps lacks", "flag", set(18, "          bin/laug deps -all")},
	}
	if workflowAgentPlants != nil {
		cases = append(cases, workflowAgentPlants(set)...)
	}
	for _, c := range cases {
		got := s.checkWorkflow(workflowBody(c.edit), flags)
		if len(got) == 0 {
			t.Errorf("%s: no problem; want %s", c.name, c.kind)
		}
		for _, pr := range got {
			if pr.kind != c.kind {
				t.Errorf("%s: line %d: %s: %s; want only %s", c.name, pr.line, pr.kind, pr.what, c.kind)
			}
		}
	}
}
