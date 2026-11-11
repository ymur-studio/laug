package main

import (
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// TestSweep: the env matrix, with the resolve the sweep recipe ran in its shell. The cell is the one the run recorded,
// a raun's in faults/results before a load run's in load/results, the first file there deciding; a -address given
// wins; a run that recorded none reads the lease's, else block 0's. The cutoff is -sweep-margin before the run's end,
// else before now, unless -cutoff is given. load/sweep.sh gets the three as arguments and NAMESPACE.
func TestSweep(t *testing.T) {
	strays := []string{"CELL_ADDRESS", "CUTOFF", "NAMESPACE", "SWEEP_MARGIN_S"}
	row := func(name string, args []string, lease bool, files map[string]string, addr, cutoff, from string) envCase {
		return envCase{name: name, args: append([]string{"sweep"}, args...), lease: lease, files: files,
			how: script.Exec, script: "load/sweep.sh", argv: []string{addr, "omes-r1", cutoff},
			knobs: []string{"NAMESPACE=default"}, strays: strays,
			errOut: "laug · sweep omes-r1 on " + addr + " (" + from + "), workflows started before " + cutoff}
	}
	load := map[string]string{"load/results/r1.json": `{"cell":"127.0.0.1:7253","end":"2026-10-22T11:50:00Z"}`}
	runMatrix(t, []envCase{
		{name: "bare", args: []string{"sweep"}, code: 2,
			errOut: "laug · sweep: names no run: give it the run id whose task queue, omes-<run-id>, to count"},
		row("no record, no lease", []string{"r1"}, false, nil, "127.0.0.1:7233", "2026-10-22T11:59:30Z",
			"the default address: the run recorded no address"),
		row("no record, a lease", []string{"r1"}, true, nil, "127.0.0.1:7243", "2026-10-22T11:59:30Z",
			"the current lease in .last-lease.json: the run recorded no address"),
		row("a load run's record", []string{"r1"}, true, load, "127.0.0.1:7253", "2026-10-22T11:49:30Z",
			"recorded in load/results/r1.json"),
		row("a load run's record, -sweep-margin 60s", []string{"r1", "-sweep-margin", "60s"}, false, load,
			"127.0.0.1:7253", "2026-10-22T11:49:00Z", "recorded in load/results/r1.json"),
		row("a raun's record first", []string{"r1"}, true, map[string]string{
			"faults/results/r1.json": `{"address":"127.0.0.1:7263","after_end":1792668600}`,
			"load/results/r1.json":   `{"cell":"127.0.0.1:7253","end":"2026-10-22T11:50:00Z"}`},
			"127.0.0.1:7263", "2026-10-22T11:29:30Z", "recorded in faults/results/r1.json"),
		row("a raun that recorded no address", []string{"r1"}, true, map[string]string{
			"faults/results/r1.json": `{"after_end":"1792668600"}`,
			"load/results/r1.json":   `{"cell":"127.0.0.1:7253","end":"2026-10-22T11:50:00Z"}`},
			"127.0.0.1:7243", "2026-10-22T11:29:30Z", "the current lease in .last-lease.json: the run recorded no address"),
		{name: "given", args: []string{"sweep", "r1", "-address", "127.0.0.1:17233", "-cutoff", "2026-10-22T11:00:00Z",
			"-namespace", "ns1"}, lease: true, files: load, how: script.Exec, script: "load/sweep.sh",
			argv:  []string{"127.0.0.1:17233", "omes-r1", "2026-10-22T11:00:00Z"},
			knobs: []string{"NAMESPACE=ns1"}, strays: strays,
			errOut: "laug · sweep omes-r1 on 127.0.0.1:17233 (-address, given), workflows started before " +
				"2026-10-22T11:00:00Z"},
		{name: "-dry-run", args: []string{"sweep", "r1", "-dry-run"}, strays: strays,
			out: "NAMESPACE=default load/sweep.sh 127.0.0.1:7233 omes-r1 2026-10-22T11:59:30Z"},
		{name: "a cutoff that is not RFC 3339", args: []string{"sweep", "r1", "-cutoff", "11:00"}, code: 2,
			errOut: "laug · sweep: -cutoff 11:00: not RFC 3339, 2026-10-14T02:25:41Z"},
		{name: "-sweep-margin not whole", args: []string{"sweep", "r1", "-sweep-margin", "1.5s"}, code: 2,
			errOut: "laug · sweep: -sweep-margin 1.5s: whole seconds, 0 or more"},
	})
}
