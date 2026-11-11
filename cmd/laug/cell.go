package main

// laug cell: one compose cell on this host. up, down, mem, health and dynconfig-check run cells/up.sh, down.sh, mem.sh,
// health.sh and dynconfig-check.sh; fi-template-check and softasserts run cells/fi-template-check.sh and
// cells/softasserts.sh. Each sets or clears its script's knobs, and -dry-run prints the line.

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ymur-studio/laug/internal/pool"
	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// cellPorts are the five ports a compose cell publishes, block 0's as internal/script's knob table holds them: block N
// publishes each at 10N over block 0's, as cells/backend-compose.sh lays out a pool cell's LAUG_BLOCK.
// TestCellDefaultsMatchKnobs holds them to the knob table, cells/compose/cell.yml and cells/backend-compose.sh.
var cellPorts = []struct {
	name string
	base int
}{{"FRONTEND_PORT", 7233}, {"UI_PORT", 8080}, {"PROMETHEUS_PORT", 9090}, {"GRAFANA_PORT", 3000},
	{"TOXIPROXY_PORT", 8474}}

// cellBlockMax is the last block -block takes. Up to 100 blocks no two of their ports meet: the ports a block adds 10N
// to end in 0, 3 or 4, and two that end in 0 meet first at block 101, whose UI port, 9090, is block 0's Prometheus.
const cellBlockMax = 99

// cellUpWait is how long cells/up.sh waits for SERVING.
const cellUpWait = "300 s"

// The visibility and bind defaults.
const (
	visibilityDefault = "postgres"
	bindDefault       = "loopback"
)

// visibilities are -visibility's values: postgres, the default, or es, which turns Elasticsearch on.
var visibilities = []string{"postgres", "es"}

// bindAddrs are -bind's values and the BIND_ADDR each sets: loopback, the default, or all. cells/up.sh waits on
// 127.0.0.1, so no single interface's address is one, as cells/backend-compose.sh refuses one (SECURITY.md).
var bindAddrs = map[string]string{"loopback": "127.0.0.1", "all": "0.0.0.0"}

// cellCompose is what docker compose reads for a cell: the server tag, the visibility, the bind, the port block, the
// project and whether server fault injection is on. Its zero value is not a cell; cellComposeDefault holds the
// defaults.
type cellCompose struct {
	tag, visibility, bind, project string
	block                          int
	serverFI                       bool
}

var cellComposeDefault = cellCompose{tag: serverTagDefault, visibility: visibilityDefault, bind: bindDefault,
	project: projectDefault}

func (c cellCompose) port(name string) int {
	for _, p := range cellPorts {
		if p.name == name {
			return p.base + 10*c.block
		}
	}
	panic("no port " + name)
}

// vars is the cell's compose environment, in order: ENABLE_ES, COMPOSE_PROFILES, BIND_ADDR, SERVER_TAG and the five
// ports, then PROJECT and SERVER_FI.
func (c cellCompose) vars() []script.Var {
	es, profiles := "false", ""
	if c.visibility == "es" {
		es, profiles = "true", "es"
	}
	v := []script.Var{script.Bare("ENABLE_ES", es), script.Bare("COMPOSE_PROFILES", profiles),
		script.Quoted("BIND_ADDR", bindAddrs[c.bind]), script.Bare("SERVER_TAG", c.tag)}
	for _, p := range cellPorts {
		v = append(v, script.Bare(p.name, strconv.Itoa(c.port(p.name))))
	}
	fi := ""
	if c.serverFI {
		fi = "1"
	}
	return append(v, script.Bare("PROJECT", c.project), script.Quoted("SERVER_FI", fi))
}

// check returns an error naming the first flag whose value laug refuses: a -server-tag that is not x.y.z, a
// -visibility, -bind or -block outside the values each takes, or a -project that is not one word.
func (c cellCompose) check() error {
	switch {
	case !poolapi.TagRE.MatchString(c.tag):
		return fmt.Errorf("-server-tag %s: not a release tag, x.y.z", shellWord(c.tag))
	case !slices.Contains(visibilities, c.visibility):
		return fmt.Errorf("-visibility %s: postgres or es", shellWord(c.visibility))
	case bindAddrs[c.bind] == "":
		return fmt.Errorf("-bind %s: loopback, 127.0.0.1, or all, 0.0.0.0; laug dials a cell on 127.0.0.1",
			shellWord(c.bind))
	case c.block < 0 || c.block > cellBlockMax:
		return fmt.Errorf("-block %d: 0 to %d, the ports at 10 a block over block 0's", c.block, cellBlockMax)
	}
	return checkWords("-project", c.project)
}

// setByLaug says, for the line a stray copy gets, what sets each of names in a verb with no flag for it: laug, at its
// default.
func setByLaug(names ...string) map[string]string {
	by := map[string]string{}
	for _, n := range names {
		by[n] = "laug"
	}
	return by
}

// composeNames are the knobs of the compose environment whose value is the default in a verb with only -project.
var composeNames = []string{"ENABLE_ES", "COMPOSE_PROFILES", "BIND_ADDR", "SERVER_TAG", "FRONTEND_PORT", "UI_PORT",
	"PROMETHEUS_PORT", "GRAFANA_PORT", "TOXIPROXY_PORT", "SERVER_FI"}

func runCellUp(_ context.Context, a *App, args []string) int {
	o := cellComposeDefault
	f := a.newFlags("cell up", "[-server-tag "+serverTagDefault+"] [-visibility postgres|es] [-bind loopback|all] "+
		"[-block 0] [-project "+projectDefault+"] [-namespace "+namespaceDefault+"] [-server-fi] [-dry-run]")
	f.StringVar(&o.tag, "server-tag", o.tag, "the temporalio/server and admin-tools `tag` the cell runs, x.y.z")
	f.StringVar(&o.visibility, "visibility", o.visibility, "the visibility `store`: postgres, or es for Elasticsearch")
	f.StringVar(&o.bind, "bind", o.bind, "the `interfaces` the five ports listen on: loopback, 127.0.0.1, or all, "+
		"0.0.0.0; nothing in a cell has auth")
	f.IntVar(&o.block, "block", o.block, "the port `block`, 0 to "+strconv.Itoa(cellBlockMax)+": block N publishes "+
		"7233, 8080, 9090, 3000 and 8474 plus 10N")
	f.StringVar(&o.project, "project", o.project, "the compose `project`")
	namespace := f.String("namespace", namespaceDefault, "the `namespace` it waits for")
	f.BoolVar(&o.serverFI, "server-fi", false, "lay cells/compose/fi.yml over cell.yml: server fault injection set "+
		"by LAUG_FI_* in the environment; only on the tag laug-server.yaml was vendored from")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	err := o.check()
	if err == nil {
		err = checkWords("-namespace", *namespace)
	}
	if err != nil {
		return a.refused("cell up", err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", o.port("FRONTEND_PORT"))
	prom := fmt.Sprintf("http://127.0.0.1:%d", o.port("PROMETHEUS_PORT"))
	c := script.Command{Script: "cells/up.sh", Set: append(o.vars(), script.Quoted("CELL_ADDRESS", address),
		script.Quoted("PROM_URL", prom), script.Bare("NAMESPACE", *namespace), script.Bare("VISIBILITY", o.visibility)),
		By: map[string]string{"CELL_ADDRESS": "-block", "PROM_URL": "-block"}}
	ports := make([]string, 0, len(cellPorts))
	for _, p := range cellPorts {
		ports = append(ports, strconv.Itoa(o.port(p.name)))
	}
	fi := ""
	if o.serverFI {
		fi = ", server fault injection from LAUG_FI_*"
	}
	first := fmt.Sprintf("laug · cell up: project %s, server %s, visibility %s, block %d (%s) on %s%s; it waits %s "+
		"at most for SERVING; %s removes it", o.project, o.tag, o.visibility, o.block, strings.Join(ports, ", "),
		bindAddrs[o.bind], fi, cellUpWait, downLine(a.self(), o.project))
	return a.runScript("cell up", c, *dry, script.Exec, first)
}

// downLine is the line that tears project down, its volumes with it, starting with self.
func downLine(self, project string) string {
	if project == projectDefault {
		return self + " cell down -remove-volumes"
	}
	return self + " cell down -project " + project + " -remove-volumes"
}

// cellGone is what laug cell down removes: the containers, volumes and networks that carry the project's compose label.
type cellGone struct {
	Project    string   `json:"project"`
	Containers []string `json:"containers"`
	Volumes    []string `json:"volumes"`
	Networks   []string `json:"networks"`
}

// cellListWait bounds the three docker calls that list what cell down removes.
const cellListWait = 20 * time.Second

func runCellDown(ctx context.Context, a *App, args []string) int {
	o := cellComposeDefault
	f := a.newFlags("cell down", "-remove-volumes [-project "+projectDefault+"] [-json] [-dry-run]")
	f.StringVar(&o.project, "project", o.project, "the compose `project` to remove, its containers, volumes and "+
		"networks")
	remove := f.Bool("remove-volumes", false, "remove the cell, its volumes with it: Postgres's and Prometheus's "+
		"data, which no command brings back; unset, it lists what would go and removes nothing")
	jsonOut := f.Bool("json", false, "with -remove-volumes, print what went, one JSON object: project, containers, "+
		"volumes and networks; the lines before it go to stderr")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -project names the cell", pos)
	}
	if err := o.check(); err != nil {
		return a.refused("cell down", err)
	}
	c := script.Command{Script: "cells/down.sh", Set: o.vars(), By: setByLaug(composeNames...)}
	if *dry && *remove {
		return a.runScript("cell down", c, true, script.Exec, "")
	}
	if !a.inCheckout("cell down", c.Script) {
		return exitUsage
	}
	gone, code, ok := a.cellGone(ctx, o.project)
	if !ok {
		return code
	}
	// A pool cell removed behind the pool is one the pool goes on to hand out, or a gate goes on to judge. An orphan,
	// a cell no pool lists, as a crashed pool leaves one, is this verb's to remove.
	listed, instead := a.poolCell(ctx, o.project)
	if !*remove {
		gone.report(a.Stderr, a.self())
		if listed != "" {
			fmt.Fprintf(a.Stderr, "But %s, so -remove-volumes refuses it: %s\n", listed, instead)
		}
		return exitUsage
	}
	if listed != "" {
		return a.refused("cell down", fmt.Errorf("%s, and removed behind the pool it would stay on the pool's list: %s",
			listed, instead))
	}
	first := fmt.Sprintf("laug · cell down: project %s, %s, %s and %s go, its data with them", o.project,
		plural(len(gone.Containers), "container"), plural(len(gone.Volumes), "volume"),
		plural(len(gone.Networks), "network"))
	if !*jsonOut {
		return a.runScript("cell down", c, false, script.Exec, first)
	}
	c, env, code, ok := a.prepare("cell down", c, false)
	if !ok {
		return code
	}
	fmt.Fprintln(a.Stderr, first)
	if code := a.start("cell down", c, env, script.Run, "", a.Stderr); code != exitDone {
		return code
	}
	if err := writeJSON(a.Stdout, gone); err != nil {
		fmt.Fprintf(a.Stderr, "laug · cell down: %v\n", a.errShown(err))
		return exitFailed
	}
	return exitDone
}

// cellPoolAddr is the pool cell down asks whether it lists a project: the pool serve's and a gate's, by default.
// Tests point it at a server of their own.
var cellPoolAddr = pool.DefaultListen

// poolCellRE is a pool cell's compose project, laug-<id>, as cells/backend-compose.sh names it.
var poolCellRE = regexp.MustCompile(`^laug-[0-9a-f]{8}$`)

// poolCell says which cell of the pool on cellPoolAddr project is, and the line to type instead of cell down, when
// the pool lists it; "" when project is not a pool cell's name, or no pool answers there.
func (a *App) poolCell(ctx context.Context, project string) (listed, instead string) {
	if !poolCellRE.MatchString(project) {
		return "", ""
	}
	c, err := poolapi.New(cellPoolAddr)
	if err != nil {
		return "", ""
	}
	st, _, err := c.Status(ctx)
	if err != nil {
		return "", ""
	}
	for _, cell := range st.Cells {
		if cell.Project != project && "laug-"+cell.ID != project {
			continue
		}
		instead = a.self() + " pool down -remove-cells stops the pool and removes its cells"
		if cell.State == "leased" {
			instead = a.self() + " release " + cell.ID + " gives the lease back, and the pool removes the cell"
		}
		return fmt.Sprintf("project %s is cell %s of the pool on %s, %s", project, cell.ID, cellPoolAddr,
			cell.State), instead
	}
	return "", ""
}

// report writes what cell down -remove-volumes would remove, with counts and names, and the line that removes it,
// starting with self: bare cell down's answer.
func (g cellGone) report(w io.Writer, self string) {
	n := len(g.Containers) + len(g.Volumes) + len(g.Networks)
	if n == 0 {
		fmt.Fprintf(w, "laug · cell down: project %s has no container, volume or network: nothing to remove\n",
			g.Project)
		return
	}
	fmt.Fprintf(w, "laug · cell down: would remove project %s, docker compose down -v: %s, %s and %s, its data with "+
		"them\n", g.Project, plural(len(g.Containers), "container"), plural(len(g.Volumes), "volume"),
		plural(len(g.Networks), "network"))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, l := range []struct {
		name  string
		names []string
	}{{"containers", g.Containers}, {"volumes", g.Volumes}, {"networks", g.Networks}} {
		names := "none"
		if len(l.names) > 0 {
			names = strings.Join(l.names, ", ")
		}
		fmt.Fprintf(tw, "  %s\t%s\n", l.name, names)
	}
	tw.Flush()
	fmt.Fprintf(w, "%s removes them\n", downLine(self, g.Project))
}

// cellGone lists what docker compose down -v removes for project: the containers, volumes and networks that carry its
// compose label, by name. Compose labels the containers it creates com.docker.compose.project=<project>, which
// cells/shards-ready.sh's docker ps filter reads on every cell, and its volumes and networks the same way
// (docker compose's labels; docker volume ls and docker network ls, --filter label= and --format {{.Name}}). down -v
// removes the named volumes, pgdata and promdata, and any anonymous one a container holds (docker compose down
// reference); cell.yml names one for each image that declares a VOLUME, so none is anonymous (cells/compose/cell.yml).
// With ok false it printed why: 2 with no docker on PATH, 1 when a listing fails.
func (a *App) cellGone(ctx context.Context, project string) (cellGone, int, bool) {
	gone := cellGone{Project: project}
	docker := a.lookPath("docker")
	if docker == "" {
		fmt.Fprintf(a.Stderr, "laug · cell down: no docker on PATH, so nothing can say what goes, and docker compose "+
			"down cannot run\n")
		return gone, exitUsage, false
	}
	ctx, cancel := context.WithTimeout(ctx, cellListWait)
	defer cancel()
	label := "label=com.docker.compose.project=" + project
	for _, l := range []struct {
		into *[]string
		name string
		args []string
	}{
		{&gone.Containers, "docker ps", []string{"ps", "-a", "--filter", label, "--format", "{{.Names}}"}},
		{&gone.Volumes, "docker volume ls", []string{"volume", "ls", "--filter", label, "--format", "{{.Name}}"}},
		{&gone.Networks, "docker network ls", []string{"network", "ls", "--filter", label, "--format", "{{.Name}}"}},
	} {
		cmd := exec.CommandContext(ctx, docker, l.args...)
		cmd.Dir, cmd.Env = a.Root, append([]string{}, a.Environ...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			why, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
			if why == "" {
				why = err.Error()
			}
			fmt.Fprintf(a.Stderr, "laug · cell down: %s could not say what goes, so nothing was removed: %s\n",
				l.name, a.textShown(why))
			return gone, exitFailed, false
		}
		names := strings.Fields(string(out))
		slices.Sort(names)
		*l.into = append([]string{}, names...)
	}
	return gone, exitDone, true
}

func runCellMem(_ context.Context, a *App, args []string) int {
	o := cellComposeDefault
	f := a.newFlags("cell mem", "[-project "+projectDefault+"] [-dry-run]")
	f.StringVar(&o.project, "project", o.project, "the compose `project` whose containers it reads")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -project names the cell", pos)
	}
	if err := o.check(); err != nil {
		return a.refused("cell mem", err)
	}
	c := script.Command{Script: "cells/mem.sh", Set: o.vars(), By: setByLaug(composeNames...)}
	first := fmt.Sprintf("laug · cell mem: the memory project %s's containers hold, one docker stats; a row a "+
		"container on stderr, the total on stdout", o.project)
	return a.runScript("cell mem", c, *dry, script.Exec, first)
}

func runCellHealth(_ context.Context, a *App, args []string) int {
	f := a.newFlags("cell health", "[-block 0] [-address <host:port>] [-prom <url>] [-namespace "+namespaceDefault+
		"] [-dry-run]")
	block := f.Int("block", 0, "the port `block` of the cell, 0 to "+strconv.Itoa(cellBlockMax)+": its frontend is "+
		"7233 plus 10N and its Prometheus 9090 plus 10N")
	address := f.String("address", "", "the cell's frontend, `host:port`; unset, 127.0.0.1 and -block's")
	prom := f.String("prom", "", "the cell's Prometheus `url`; unset, http://127.0.0.1 and -block's")
	namespace := f.String("namespace", namespaceDefault, "the `namespace` HealthCheck runs in")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	o := cellComposeDefault
	o.block = *block
	err := o.check()
	if err == nil {
		err = checkWords("-namespace", *namespace)
	}
	if err != nil {
		return a.refused("cell health", err)
	}
	if !given(f, "address") {
		*address = fmt.Sprintf("127.0.0.1:%d", o.port("FRONTEND_PORT"))
	}
	if !given(f, "prom") {
		*prom = fmt.Sprintf("http://127.0.0.1:%d", o.port("PROMETHEUS_PORT"))
	}
	c := script.Command{Script: "cells/health.sh", Set: []script.Var{script.Quoted("CELL_ADDRESS", *address),
		script.Bare("NAMESPACE", *namespace), script.Quoted("PROM_URL", *prom)},
		By: map[string]string{"CELL_ADDRESS": "-address or -block", "PROM_URL": "-prom or -block"}}
	first := fmt.Sprintf("laug · cell health: the cell at %s, namespace %s, Prometheus %s: cluster health, one "+
		"HealthCheck workflow, 5 server targets up", shellWord(*address), *namespace, shellWord(*prom))
	return a.runScript("cell health", c, *dry, script.Exec, first)
}

// cellProject is the compose project a check reads: laug-<id> for -cell <id>, a pool cell's, else -project. Both given
// is a usage error.
func cellProject(f *flags, cell, project string) (string, error) {
	if given(f, "cell") && given(f, "project") {
		return "", fmt.Errorf("takes -cell or -project, not both: -cell <id> reads project laug-<id>")
	}
	if given(f, "cell") {
		if err := checkWords("-cell", cell); err != nil {
			return "", err
		}
		return "laug-" + cell, nil
	}
	return project, checkWords("-project", project)
}

func runCellDynconfigCheck(_ context.Context, a *App, args []string) int {
	f := a.newFlags("cell dynconfig-check", "[-cell <id> | -project "+projectDefault+"] | -template [-server-tag "+
		serverTagDefault+"] [-dry-run]")
	cell := f.String("cell", "", "the pool cell `id` whose laug.yaml it checks, compose project laug-<id>, or laug "+
		"in its guest when the last lease is that microVM cell")
	project := f.String("project", projectDefault, "the compose `project` whose laug.yaml it checks")
	template := f.Bool("template", false, "check cells/compose/dynamicconfig/laug.yaml, the template, with no cell: "+
		"the validator in temporalio/server:<tag>, which docker may pull")
	tag := f.String("server-tag", serverTagDefault, "with -template, the server `tag` whose validator checks it, x.y.z")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	switch {
	case *template && (given(f, "cell") || given(f, "project")):
		return a.usageError(f, "-template checks the template with no cell, so it takes no -cell or -project")
	case !*template && given(f, "server-tag"):
		return a.usageError(f, "-server-tag names the image -template checks with; a cell's own image checks its "+
			"laug.yaml")
	}
	p, err := cellProject(f, *cell, *project)
	if err == nil && !poolapi.TagRE.MatchString(*tag) {
		err = fmt.Errorf("-server-tag %s: not a release tag, x.y.z", shellWord(*tag))
	}
	if err != nil {
		return a.refused("cell dynconfig-check", err)
	}
	nocell := ""
	first := fmt.Sprintf("laug · cell dynconfig-check: project %s's laug.yaml, with its own server's validator", p)
	if *template {
		nocell = "1"
		first = fmt.Sprintf("laug · cell dynconfig-check: the template, cells/compose/dynamicconfig/laug.yaml, with "+
			"the validator in temporalio/server:%s, no cell", *tag)
	}
	// A microVM cell's containers run in its guest's Docker, not as laug-<id> here: when the last lease (LEASE_FILE,
	// as the scripts read it) is this cell and names a guest, the script checks it there over the lease's ssh.
	var lease []script.Value
	if given(f, "cell") {
		file := cmp.Or(a.getenv("LEASE_FILE"), poolapi.LastLeaseFile)
		if l := a.leaseAt(file); l.ID == *cell && l.SSH != "" {
			lease = []script.Value{{Text: file}}
			first = fmt.Sprintf("laug · cell dynconfig-check: cell %s's laug.yaml, in its guest at %s over the "+
				"lease's ssh, with its own server's validator", *cell, l.SSH)
		}
	}
	c := script.Command{Script: "cells/dynconfig-check.sh", Args: lease, Set: []script.Var{
		script.Quoted("NOCELL", nocell), script.Bare("SERVER_TAG", *tag), script.Bare("PROJECT", p)},
		Clear: []string{"CELL"}, By: map[string]string{"PROJECT": "-project or -cell"}}
	return a.runScript("cell dynconfig-check", c, *dry, script.Exec, first)
}

func runCellFiTemplateCheck(_ context.Context, a *App, args []string) int {
	f := a.newFlags("cell fi-template-check", "[-server-tag "+serverTagDefault+"] [-dry-run]")
	tag := f.String("server-tag", serverTagDefault, "the server `tag` to check cells/compose/config/laug-server.yaml "+
		"against, x.y.z; docker may pull its image")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -server-tag names the release", pos)
	}
	if !poolapi.TagRE.MatchString(*tag) {
		return a.refused("cell fi-template-check", fmt.Errorf("-server-tag %s: not a release tag, x.y.z",
			shellWord(*tag)))
	}
	// The script's first line is a laug · line of its own, naming the tag it checks and the one the file came from.
	c := script.Command{Script: "cells/fi-template-check.sh", Args: []script.Value{{Text: *tag}},
		Clear: []string{"SERVER_TAG"}}
	return a.runScript("cell fi-template-check", c, *dry, script.Exec, "")
}

func runCellSoftasserts(_ context.Context, a *App, args []string) int {
	f := a.newFlags("cell softasserts", "[-cell <id> | -project "+projectDefault+"] [-since <time>] [-until <time>] "+
		"[-dry-run]")
	cell := f.String("cell", "", "the pool cell `id` whose roles it counts, compose project laug-<id>")
	project := f.String("project", projectDefault, "the compose `project` whose roles it counts")
	since := f.String("since", "", "count from this `time`, as docker logs takes it: RFC 3339, Unix seconds or 10m; "+
		"unset, each role's container's last start")
	until := f.String("until", "", "count to this `time`, as -since takes it; unset, now")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q", pos)
	}
	p, err := cellProject(f, *cell, *project)
	if err != nil {
		return a.refused("cell softasserts", err)
	}
	c := script.Command{Script: "cells/softasserts.sh", Clear: []string{"CELL", "PROJECT", "SINCE", "UNTIL"},
		By: map[string]string{"PROJECT": "-project or -cell"}}
	from, to := "its container's last start", "now"
	if *since != "" {
		c.Args, from = append(c.Args, script.Value{Text: "--since"}, script.Value{Text: *since, Quote: true}),
			shellWord(*since)
	}
	if *until != "" {
		c.Args, to = append(c.Args, script.Value{Text: "--until"}, script.Value{Text: *until, Quote: true}),
			shellWord(*until)
	}
	c.Args = append(c.Args, script.Value{Text: p})
	first := fmt.Sprintf("laug · cell softasserts: project %s, each server role's soft assertions from %s to %s, one "+
		"JSON object", p, from, to)
	return a.runScript("cell softasserts", c, *dry, script.Exec, first)
}
