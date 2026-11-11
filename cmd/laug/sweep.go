package main

// laug sweep: one run's workflows counted by status on the cell it recorded. laug resolves the cell and the cutoff;
// load/sweep.sh runs by exec.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// cutoffRE is the cutoff load/sweep.sh takes, RFC 3339.
var cutoffRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?` +
	`(Z|[+-][0-9]{2}:[0-9]{2})$`)

func runSweep(_ context.Context, a *App, args []string) int {
	f := a.newFlags("sweep", "<run-id> [-address <host:port>] [-cutoff <rfc3339>] [-namespace <name>] "+
		"[-sweep-margin 30s] [-dry-run]")
	address := f.String("address", "", "the cell's frontend, `host:port`; unset, the one the run recorded, else the "+
		"leased cell's, else "+cellAddressDefault)
	cutoff := f.String("cutoff", "", "count workflows started before this `time`, RFC 3339; unset, -sweep-margin "+
		"before the run's end, else before now")
	namespace := f.String("namespace", namespaceDefault, "the Temporal `namespace`")
	margin := f.Duration("sweep-margin", 30*time.Second, "how long before the run's end the cutoff falls, whole seconds")
	dry := dryRunFlag(f)
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	switch {
	case len(pos) == 0:
		return a.usageError(f, "names no run: give it the run id whose task queue, omes-<run-id>, to count")
	case len(pos) > 1:
		return a.usageError(f, "takes one run id, got %q", pos)
	}
	runID := pos[0]
	err := checkWords("run id", runID, "-namespace", *namespace)
	if err == nil {
		_, err = seconds("-sweep-margin", *margin)
	}
	if err == nil && given(f, "cutoff") && !cutoffRE.MatchString(*cutoff) {
		err = fmt.Errorf("-cutoff %s: not RFC 3339, 2026-10-14T02:25:41Z", shellWord(*cutoff))
	}
	if err != nil {
		return a.refused("sweep", err)
	}
	if !a.inCheckout("sweep", "load/sweep.sh") {
		return exitUsage
	}

	// The cell the run recorded: a raun's address and after_end in faults/results, else a load run's cell and end
	// in load/results; the first file there decides. A -address given wins; a run that recorded no address reads
	// the lease's, else block 0's.
	addr, from, end := "", "", int64(-1)
	for _, rel := range []string{"faults/results/" + runID + ".json", "load/results/" + runID + ".json"} {
		b, err := os.ReadFile(filepath.Join(a.Root, rel))
		if err != nil {
			continue
		}
		var rec struct {
			Address  string          `json:"address"`
			AfterEnd json.RawMessage `json:"after_end"`
			Cell     string          `json:"cell"`
			End      string          `json:"end"`
		}
		_ = json.Unmarshal(b, &rec)
		if strings.HasPrefix(rel, "faults/") {
			addr, end = rec.Address, epoch(rec.AfterEnd)
		} else {
			addr = rec.Cell
			if t, err := time.Parse(time.RFC3339, rec.End); err == nil {
				end = t.Unix()
			}
		}
		if addr != "" {
			from = "recorded in " + rel
		}
		break
	}
	if given(f, "address") {
		addr, from = *address, "-address, given"
	}
	if addr == "" {
		if l := a.lastLease(); l.Address != "" {
			addr, from = l.Address, "the current lease in "+poolapi.LastLeaseFile+": the run recorded no address"
		} else {
			addr, from = cellAddressDefault, "the default address: the run recorded no address"
		}
	}
	at := *cutoff
	if at == "" {
		if end < 0 {
			end = a.now().Unix()
		}
		at = time.Unix(end-int64(margin.Seconds()), 0).UTC().Format("2006-01-02T15:04:05Z")
	}
	fmt.Fprintf(a.Stderr, "laug · sweep omes-%s on %s (%s), workflows started before %s\n", runID, addr, from, at)
	c := script.Command{Script: "load/sweep.sh",
		Args:  []script.Value{{Text: addr}, {Text: "omes-" + runID}, {Text: at}},
		Set:   []script.Var{script.Bare("NAMESPACE", *namespace)},
		Clear: []string{"CELL_ADDRESS", "CUTOFF", "SWEEP_MARGIN_S"}}
	return a.runScript("sweep", c, *dry, script.Exec, "")
}

// epoch reads a raun's after_end, Unix seconds as a number or a string of digits, as the shell's arithmetic took it;
// -1 when it is neither.
func epoch(raw json.RawMessage) int64 {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
