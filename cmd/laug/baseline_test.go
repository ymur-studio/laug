package main

import (
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// TestBaseline: the env matrix. baseline execs load/baseline.sh with the Prometheus given as PROM_URL and, for a run
// that recorded none, the one given, else the leased cell's, else block 0's; PROJECT only when -project is given, an
// empty one skipping the per-service rows; FORCE=1 only with -overwrite.
func TestBaseline(t *testing.T) {
	strays := []string{"FORCE", "PROJECT", "PROM_URL"}
	runMatrix(t, []envCase{
		{name: "bare", args: []string{"baseline"}, code: 2,
			errOut: "laug · baseline: names no run: give it a run id, the name of load/results/<run-id>.json"},
		{name: "no lease", args: []string{"baseline", "r1"}, how: script.Exec, script: "load/baseline.sh",
			argv: []string{"r1"}, knobs: []string{"PROM_URL=", "FALLBACK_PROM_URL=http://127.0.0.1:9090"},
			strays: strays},
		{name: "the lease's Prometheus", args: []string{"baseline", "r1"}, lease: true, how: script.Exec,
			script: "load/baseline.sh", argv: []string{"r1"},
			knobs: []string{"PROM_URL=", "FALLBACK_PROM_URL=http://127.0.0.1:9100"}, strays: strays},
		{name: "-prom and -project", args: []string{"baseline", "r1", "-prom", "http://127.0.0.1:19090", "-project",
			"laug-x"}, lease: true, how: script.Exec, script: "load/baseline.sh", argv: []string{"r1"},
			knobs: []string{"PROM_URL=http://127.0.0.1:19090", "FALLBACK_PROM_URL=http://127.0.0.1:19090",
				"PROJECT=laug-x"}, strays: strays},
		{name: "an empty -project", args: []string{"baseline", "r1", "-project", ""}, how: script.Exec,
			script: "load/baseline.sh", argv: []string{"r1"},
			knobs: []string{"PROM_URL=", "FALLBACK_PROM_URL=http://127.0.0.1:9090", "PROJECT="}, strays: strays},
		{name: "-overwrite", args: []string{"baseline", "r1", "-overwrite"}, how: script.Exec, script: "load/baseline.sh",
			argv: []string{"r1"}, knobs: []string{"PROM_URL=", "FALLBACK_PROM_URL=http://127.0.0.1:9090", "FORCE=1"},
			strays: []string{"PROJECT", "PROM_URL"}},
		{name: "-dry-run", args: []string{"baseline", "r1", "-overwrite", "-dry-run"}, strays: []string{"PROJECT",
			"PROM_URL"}, out: "FORCE=1 PROM_URL='' FALLBACK_PROM_URL='http://127.0.0.1:9090' load/baseline.sh r1"},
		{name: "-dry-run, LEASE_FILE set", args: []string{"baseline", "r1", "-dry-run"},
			more: []string{"LEASE_FILE=other.json"}, strays: []string{"FORCE", "PROJECT", "PROM_URL"},
			out: "LEASE_FILE=other.json PROM_URL='' FALLBACK_PROM_URL='http://127.0.0.1:9090' load/baseline.sh r1"},
		{name: "a project of two words", args: []string{"baseline", "r1", "-project", "a b"}, code: 2,
			errOut: "laug · baseline: -project 'a b': not one word of letters, digits, ., _ and -, from a letter or digit"},
		{name: "two run ids", args: []string{"baseline", "r1", "r2"}, code: 2,
			errOut: `laug · baseline: takes one run id, got ["r1" "r2"]`},
	})
}
