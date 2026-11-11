package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/poolapi"
	"github.com/ymur-studio/laug/internal/script"
)

// candidateStrays are the knobs candidate maps that strayEnviron holds with another value.
var candidateStrays = []string{"CANDIDATE_RELEASE", "DURATION", "MAX_CONCURRENT", "NAMESPACE", "OMES_DIR", "POOL_ADDR",
	"POOL_QUIET_WAIT_S", "RATE", "RUN_ID", "SCENARIO", "THRESHOLDS"}

// TestCandidate: the env matrix, with a pool. candidate checks every flag and the name, against the file
// faults/candidate.sh reads, before it leases, so a typo leases nothing; leases a cell and keeps it in
// .last-lease.json; then execs faults/candidate.sh with CANDIDATE_RELEASE=1, so the script gives the cell back on
// every exit. When the exec fails, candidate gives the cell back itself.
func TestCandidate(t *testing.T) {
	knobs := func(more ...string) []string {
		return append([]string{"RUN_ID=20261022-120000", "POOL_ADDR=http://$POOL", "OMES_DIR=.upstream/omes",
			"NAMESPACE=default", "SCENARIO=" + scenarioDefault, "CANDIDATE_RELEASE=1"}, more...)
	}
	for _, c := range []struct {
		envCase
		requests int    // requests the pool takes
		leased   string // the lease the pool holds after: its requester, "" for none
	}{
		{envCase{name: "a candidate", args: []string{"candidate", "benign", "-pool", "$POOL"}, how: script.Exec,
			script: "faults/candidate.sh", argv: []string{"benign", ".last-lease.json"}, knobs: knobs(),
			strays: candidateStrays}, 1, "laug"},
		{envCase{name: "every flag", args: []string{"candidate", "frontend-starved", "-pool", "$POOL", "-duration",
			"10m", "-rate", "30", "-max-concurrent", "10", "-scenario", "throughput_stress", "-namespace", "ns1",
			"-run-id", "r1", "-ttl", "10m", "-requester", "lab", "-server-tag", "1.32.1", "-quiet-wait", "0s",
			"-thresholds", "load/steady-state.yaml"}, how: script.Exec, script: "faults/candidate.sh",
			argv: []string{"frontend-starved", ".last-lease.json"},
			knobs: []string{"RUN_ID=r1", "POOL_ADDR=http://$POOL", "OMES_DIR=.upstream/omes", "NAMESPACE=ns1",
				"SCENARIO=throughput_stress", "CANDIDATE_RELEASE=1", "DURATION=10m", "RATE=30", "MAX_CONCURRENT=10",
				"POOL_QUIET_WAIT_S=0", "THRESHOLDS=load/steady-state.yaml"},
			strays: candidateStrays}, 1, "lab"},
		{envCase{name: "a candidate of the file CANDIDATES names", args: []string{"candidate", "bool-off", "-pool",
			"$POOL"}, files: map[string]string{"cands.yaml": "candidates:\n  - name: bool-off\n    key: x\n"},
			more: []string{"CANDIDATES=cands.yaml"}, how: script.Exec, script: "faults/candidate.sh",
			argv: []string{"bool-off", ".last-lease.json"}, knobs: knobs(), strays: candidateStrays}, 1, "laug"},
		{envCase{name: "-dry-run", args: []string{"candidate", "benign", "-pool", "$POOL", "-dry-run"},
			strays: candidateStrays,
			out: "RUN_ID=20261022-120000 POOL_ADDR='http://$POOL' OMES_DIR='.upstream/omes' NAMESPACE=default " +
				"SCENARIO=" + scenarioDefault + " CANDIDATE_RELEASE=1 faults/candidate.sh benign .last-lease.json"}, 0, ""},
		{envCase{name: "bare", args: []string{"candidate"}, code: 2, errOut: "laug · candidate: names no candidate: " +
			"faults/candidates.yaml holds 4:"}, 0, ""},
		{envCase{name: "a name the file lacks leases nothing", args: []string{"candidate", "nope", "-pool", "$POOL"},
			code: 2, errOut: "laug · candidate: no candidate nope in faults/candidates.yaml, which holds 4:"}, 0, ""},
		{envCase{name: "a name near one leases nothing", args: []string{"candidate", "bengin", "-pool", "$POOL"},
			code: 2, errOut: "laug · candidate: no candidate bengin in faults/candidates.yaml; did you mean benign?"},
			0, ""},
		{envCase{name: "a length lw_dur_s cannot read", args: []string{"candidate", "benign", "-pool", "$POOL",
			"-duration", "1.5h"}, code: 2,
			errOut: "laug · candidate: -duration 1.5h: a length in whole h, m and s, over 0: 5m, 90s, 1h30m"}, 0, ""},
		{envCase{name: "two names", args: []string{"candidate", "benign", "nope"}, code: 2,
			errOut: `laug · candidate: takes one candidate, got ["benign" "nope"]`}, 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, rec := matrixApp(t, c.envCase)
			addr, n, _, out, _ := poolRow(t, c.envCase, a, rec)
			if int(n.Load()) != c.requests {
				t.Errorf("%d requests reached the pool, want %d", n.Load(), c.requests)
			}
			ids := leasedIDs(t, addr)
			if c.leased == "" {
				if len(ids) != 0 {
					t.Errorf("leased %q", ids)
				}
				return
			}
			l, err := poolapi.ReadLast(filepath.Join(a.Root, ".last-lease.json"))
			if err != nil || len(ids) != 1 || l.ID != ids[0] || l.Requester != c.leased {
				t.Fatalf("leased %q; the file %+v, %v", ids, l, err)
			}
			want := "laug · candidate: " + c.argv[0] + " on lease " + l.ID + " (" + l.Address + ") until " +
				utc(l.ExpiresAt) + ", run "
			if !strings.HasPrefix(firstLine(out), want) || !strings.HasSuffix(firstLine(out),
				"; faults/candidate.sh gives the cell back when it ends, on Ctrl-C too") {
				t.Errorf("first line %q, want it to start %q", firstLine(out), want)
			}
		})
	}

	t.Run("the exec failed", func(t *testing.T) {
		a, rec := matrixApp(t, envCase{})
		rec.execErr = errors.New("exec format error")
		addr, _ := leasePool(t)
		code, out, errOut := runApp(t, a, "candidate", "benign", "-pool", addr)
		if code != 1 || !strings.Contains(errOut, "laug · candidate: could not run faults/candidate.sh: exec format "+
			"error\n") || givenBack.FindString(out) == "" || len(leasedIDs(t, addr)) != 0 {
			t.Errorf("exit %d, leased %q\nstdout:\n%s\nstderr:\n%s", code, leasedIDs(t, addr), out, errOut)
		}
		if _, err := os.Stat(filepath.Join(a.Root, ".last-lease.json")); !os.IsNotExist(err) {
			t.Errorf("the lease file stays: %v", err)
		}
	})
}
