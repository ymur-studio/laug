// Package script runs laug's shell scripts for the verbs that wrap them, and the programs the Makefile's recipes ran
// beside them, go and temporal: the knob table, the environment a script gets, the -dry-run line and the run itself,
// by exec or as a child. It imports the standard library only.
//
// A knob is a name a script read through make: one of the Makefile's 65 TYPED_VARS, a ?= variable outside that list,
// a name a recipe set on a script's line, or a name make passed on from its command line or its environment without
// naming it. Knobs holds each one with its type, the Makefile's default, the scripts that read it and the laug flag
// that sets it. A script gets a knob only from the verb that runs it, so a SERVER_TAG or a RUN_ID left in the caller's
// shell never reaches one (Env). Everything else a script reads is a seam (Seams), for tests and experiments, and
// passes through unread, as do the names every child needs (Base) and the tools' own (Tools).
package script

import "strings"

// From says where a knob came from in the Makefile laug replaced.
type From int

const (
	_      From = iota
	Typed       // one of TYPED_VARS: taken as typed from the command line or the environment
	Make        // a ?= variable that TYPED_VARS leaves out: an environment value reached it unchecked
	Recipe      // a recipe set it on a script's line, or for a program it ran
	Passed      // no line names it: make passed it on from its command line, which it exports, or its environment
)

// Type is what a knob's value is.
type Type string

const (
	Word     Type = "word"          // letters, digits, ., _ and -, from a letter or digit (the Makefile's WORD_RE)
	Number   Type = "whole number"  // a count or a port
	Seconds  Type = "whole seconds" // seconds, no unit
	Duration Type = "duration"      // a Go duration, 5s or 20m; INTERVAL and USAGE_FOR also took bare seconds
	Tag      Type = "x.y.z"         // a temporalio/server release tag
	URL      Type = "URL"           // http://host:port
	Address  Type = "host:port"     // a listener or a cell's frontend
	Path     Type = "path"          // a file or a directory, relative to the checkout unless absolute
	GateID   Type = "gate id"       // g<UTC date>-<UTC time>, as gate/step.sh check makes one
	Switch   Type = "1 or empty"    // 1 turns it on
	Time     Type = "time"          // RFC 3339, Unix seconds, or what docker logs --since takes
	Text     Type = "text"          // free text, checked by its recipe
)

// Knob is one name a script read through make.
type Knob struct {
	Name    string
	From    From
	Type    Type
	Default string // the Makefile's default as make resolved it; for a Passed knob, the scripts' own; "" for none
	// Readers are the files that read it from their environment, repository-relative: scripts, compose files and Go
	// programs. None means no file in the tree does. make pasted it into a command line, set it for docker compose
	// (COMPOSE_PROFILES), or, for PAIR and EVIDENCE, set it on the line of gate/tolerance.sh, which internal/tolerance
	// replaced. laug reads -pair and -evidence itself.
	Readers []string
	// Flag is what sets it on laug's command line: a flag, or an argument in <>. "" when laug sets it itself, or, for
	// RESTORES, when it stays make's.
	Flag string
}

// Knobs is the table, TYPED_VARS first in the Makefile's order, then the rest by where they came from. A verb that runs
// a script sets only names from this table, and a new script verb adds its rows here.
var Knobs = []Knob{
	// TYPED_VARS, as the Makefile laug replaced held it: 65 names.
	{"A", Typed, GateID, "", nil, "gate compare <a>"},
	{"B", Typed, GateID, "", nil, "gate compare <b>"},
	{"BASE", Typed, Word, "main", []string{"gate/lib.sh", "internal/orchestrator/starter.go"}, "-base"},
	{"BIND_ADDR", Typed, Address, "127.0.0.1", []string{"cells/backend-compose.sh", "cells/compose/cell.yml"}, "-bind"},
	{"CANDIDATE", Typed, Word, "", nil, "candidate <name>"},
	{"CELL", Typed, Word, "", nil, "-cell"},
	{"CELL_ADDRESS", Typed, Address, "127.0.0.1:7233", []string{"load/run-omes.sh", "cells/up.sh", "cells/health.sh"},
		"-address"},
	{"CUTOFF", Typed, Time, "", nil, "-cutoff"},
	{"EVIDENCE", Typed, Path, "", nil, "-evidence"},
	{"FAULT", Typed, Word, "persistence-latency", nil, "fault <fault>"},
	{"FC_POOL_MAX", Typed, Number, "2", nil, "-max"},
	{"FC_POOL_MIN", Typed, Number, "1", nil, "-min"},
	{"FRONTEND_PORT", Typed, Number, "7233", []string{"cells/compose/cell.yml"}, "-block"},
	{"GATE", Typed, GateID, "", []string{"_obs/obs.sh"}, "<gate id>"},
	{"GATE_FAKE", Typed, Switch, "", []string{"gate/lib.sh", "internal/orchestrator/starter.go"}, "-fake"},
	{"GATE_POST", Typed, Word, "local", []string{"gate/lib.sh", "gate/post.sh", "internal/orchestrator/starter.go"},
		"-post"},
	{"GATE_RESULTS", Typed, Path, "gate/results", []string{"gate/lib.sh", "_obs/obs.sh",
		"internal/orchestrator/starter.go"}, "-results"},
	{"GATES", Typed, Text, "", nil, "gate tolerance <gate id>..."},
	{"GRAFANA_PORT", Typed, Number, "3000", []string{"cells/compose/cell.yml", "cells/up.sh"}, "-block"},
	{"HISTORY", Typed, Path, "", nil, "health replay <file>"},
	{"INTERVAL", Typed, Duration, "5s", nil, "-interval"},
	{"ITEMS", Typed, Number, "", []string{"gate/lib.sh", "internal/orchestrator/starter.go"}, "-items"},
	{"JSON", Typed, Switch, "", []string{"_obs/obs.sh", "gate/host-type.sh"}, "-json"},
	{"NAME", Typed, Word, "", nil, "orchestrator record <name>"},
	{"NAMESPACE", Typed, Word, "default", []string{"load/run-omes.sh", "load/sweep.sh", "cells/up.sh",
		"cells/health.sh"}, "-namespace"},
	{"NOCELL", Typed, Switch, "", []string{"cells/dynconfig-check.sh"}, "-template"},
	{"OMES_REF", Typed, Word, "f0de20635c8204b0c9b8277ce00493204a3c335a", nil, "-ref"},
	{"OMES_URL", Typed, URL, "https://github.com/temporalio/omes.git", nil, "-url"},
	{"ONCE", Typed, Switch, "", nil, "usage sample"},
	{"ORCH_ADDRESS", Typed, Address, "127.0.0.1:7400", nil, "-orchestrator"},
	{"ORCH_HTTP_PORT", Typed, Number, "7401", nil, "-http-port"},
	{"ORCH_METRICS_PORT", Typed, Number, "7402", nil, "-metrics-port"},
	{"ORCH_OTLP", Typed, Address, "", nil, "-otlp"},
	{"ORCH_UI_PORT", Typed, Number, "8400", nil, "-ui-port"},
	{"PAIR", Typed, Text, "", nil, "-pair"},
	{"PLAN", Typed, Path, "gate/plan.yaml", []string{"gate/lib.sh", "internal/orchestrator/starter.go"}, "-plan"},
	{"POOL_ADDR", Typed, URL, "http://127.0.0.1:7700", []string{"faults/lib.sh", "faults/candidate.sh",
		"cells/pool-bench.sh"}, "-pool"},
	{"POOL_BACKEND", Typed, Path, "../cells/backend-compose.sh", nil, "-backend"},
	{"POOL_LISTEN", Typed, Address, "127.0.0.1:7700", nil, "-listen"},
	{"POOL_MAX", Typed, Number, "6", nil, "-max"},
	{"POOL_MAX_LEASE", Typed, Duration, "2h", nil, "-max-lease"},
	{"POOL_MIN", Typed, Number, "3", nil, "-min"},
	{"POOL_QUOTA", Typed, Number, "2", nil, "-quota"},
	{"PROJECT", Typed, Word, "laug", []string{"load/run-omes.sh", "load/baseline.sh", "cells/up.sh", "cells/down.sh",
		"cells/mem.sh", "cells/dynconfig-check.sh"}, "-project"},
	{"PROM_URL", Typed, URL, "http://127.0.0.1:9090", []string{"load/run-omes.sh", "load/verdict.sh", "load/baseline.sh",
		"load/read-thresholds.sh", "cells/up.sh", "cells/health.sh"}, "-prom"},
	{"PROMETHEUS_PORT", Typed, Number, "9090", []string{"cells/compose/cell.yml"}, "-block"},
	{"REASON", Typed, Text, "", nil, "-reason"},
	{"REQUESTER", Typed, Word, "$(USER)", nil, "-requester"},
	{"RESTORES", Typed, Number, "1", nil, ""},
	{"RUN_ID", Typed, Word, "the UTC time, yyyymmdd-hhmmss", []string{"load/run-omes.sh", "faults/run.sh",
		"faults/candidate.sh"}, "-run-id"},
	{"SCENARIO", Typed, Word, "workflow_with_single_noop_activity", []string{"faults/candidate.sh"}, "-scenario"},
	{"SECONDS", Typed, Seconds, "60", nil, "-for, -idle"},
	{"SERVER_FI", Typed, Switch, "", []string{"cells/up.sh", "cells/down.sh", "cells/mem.sh"}, "-server-fi"},
	{"SERVER_TAG", Typed, Tag, "1.31.0", []string{"load/run-omes.sh", "cells/backend-compose.sh",
		"cells/fi-template-check.sh", "cells/compose/cell.yml", "gate/lib.sh", "internal/orchestrator/starter.go",
		"cells/up.sh", "cells/dynconfig-check.sh"}, "-server-tag"},
	{"SINCE", Typed, Time, "", nil, "-since"},
	{"TIMER", Typed, Duration, "60s", nil, "health timer <duration>"},
	{"TOXIPROXY_PORT", Typed, Number, "8474", []string{"cells/compose/cell.yml"}, "-block"},
	{"TTL", Typed, Seconds, "1800", nil, "-ttl"},
	{"UI_PORT", Typed, Number, "8080", []string{"cells/compose/cell.yml", "cells/up.sh"}, "-block"},
	{"UNPOLLED", Typed, Switch, "", nil, "-unpolled"},
	{"UNTIL", Typed, Time, "", nil, "-until"},
	{"USAGE_FOR", Typed, Duration, "20m", nil, "-for"},
	{"USAGE_OUT", Typed, Path, "cells/run/cellstat/usage.jsonl", nil, "-out"},
	{"VISIBILITY", Typed, Word, "postgres", []string{"cells/backend-compose.sh", "cells/up.sh"}, "-visibility"},
	{"WF", Typed, Word, "", nil, "gate terminate <gate id>"},

	// ?= variables outside TYPED_VARS.
	{"LISTEN", Make, Address, "", nil, "-listen"},
	{"PIDS", Make, Path, "", nil, "-pids"},

	// Set by a recipe on a script's line, or for a program it ran.
	{"OMES_DIR", Recipe, Path, ".upstream/omes", []string{"load/run-omes.sh", "gate/lib.sh",
		"internal/orchestrator/starter.go"}, ""},
	{"FALLBACK_PROM_URL", Recipe, URL, "the lease's Prometheus, else PROM_URL's default", []string{"load/verdict.sh",
		"load/baseline.sh"}, ""},
	{"CANDIDATE_RELEASE", Recipe, Switch, "1", []string{"faults/candidate.sh"}, ""},
	{"ENABLE_ES", Recipe, Word, "false", []string{"cells/compose/cell.yml", "cells/fi-template-check.sh"}, "-visibility"},
	{"COMPOSE_PROFILES", Recipe, Word, "", nil, "-visibility"}, // docker compose reads it
	{"REPLAY_HISTORY", Recipe, Path, "", []string{"internal/health/replay_test.go"}, "health replay <file>"},
	{"TEMPORAL_ADDRESS", Recipe, Address, "127.0.0.1:7400", []string{"internal/orchestrator/starter.go",
		"internal/orchestrator/prober.go", "internal/orchestrator/recorder.go", "internal/orchestrator/worker.go"},
		"-orchestrator"},
	{"TEMPORAL_NAMESPACE", Recipe, Word, "default", []string{"internal/orchestrator/starter.go",
		"internal/orchestrator/prober.go", "internal/orchestrator/recorder.go", "internal/orchestrator/worker.go"}, ""},
	{"OBS_FOLLOW_BUILD", Recipe, Word, "", []string{"_obs/obs.sh"}, ""},

	// Passed on by make unnamed: what a person typed on make's command line, which the Makefile exports.
	{"DURATION", Passed, Duration, "run-omes.sh none (ITERATIONS then), faults/candidate.sh 5m",
		[]string{"load/run-omes.sh", "faults/candidate.sh"}, "-duration"},
	{"ITERATIONS", Passed, Number, "1000", []string{"load/run-omes.sh"}, "-iterations"},
	{"RATE", Passed, Number, "run-omes.sh uncapped, faults/run.sh and faults/candidate.sh 20",
		[]string{"load/run-omes.sh", "faults/run.sh", "faults/candidate.sh"}, "-rate"},
	{"MAX_CONCURRENT", Passed, Number, "run-omes.sh Omes's own, faults/run.sh and faults/candidate.sh 50",
		[]string{"load/run-omes.sh", "faults/run.sh", "faults/candidate.sh"}, "-max-concurrent"},
	{"CONTINUE_ON_FAILURE", Passed, Switch, "0", []string{"load/run-omes.sh"}, "-continue-on-failure"},
	{"POOL_QUIET_WAIT_S", Passed, Seconds, "300", []string{"faults/run.sh", "faults/lib.sh", "faults/candidate.sh"},
		"-quiet-wait"},
	{"THRESHOLDS", Passed, Path, "the run's release: load/steady-state.yaml plus load/releases/<x.y>.yaml",
		[]string{"load/thresholds.sh", "load/read-thresholds.sh"}, "-thresholds"},
	{"SWEEP_MARGIN_S", Passed, Seconds, "30", []string{"load/verdict.sh", "faults/run.sh"}, "-sweep-margin"},
	{"VERDICT_JSON", Passed, Path, "", []string{"load/verdict.sh"}, "-json"},
	{"CYCLES", Passed, Number, "20", []string{"cells/pool-bench.sh"}, "-cycles"},
	{"TTL_SHORT", Passed, Seconds, "30", []string{"cells/pool-bench.sh"}, "-ttl-short"},
	{"FORCE", Passed, Switch, "", []string{"_obs/obs.sh", "load/baseline.sh"},
		"-overwrite, -while-gate-runs, -remove-packs"},
	{"ONLY", Passed, Word, "", []string{"_obs/obs.sh"}, "-only"},
	{"OBS_CELLSTAT", Passed, Switch, "", []string{"_obs/obs.sh"}, "-no-cellstat"},
	{"OBS_DIR", Passed, Path, "$XDG_STATE_HOME/laug-obs, else ~/.local/state/laug-obs", []string{"_obs/obs.sh"}, "-dir"},
	{"RUN", Passed, Word, "", []string{"_obs/obs.sh"}, "-run-id"},
	{"PACK", Passed, Path, "", []string{"_obs/obs.sh"}, "-pack"},
}

// Seams pass through to a script unread: for tests and experiments, with no laug flag. A name that ends in * is a
// prefix; a knob that shares one, as FC_POOL_MIN does FC_*, is a knob. ARM_SLOW_* is the arm-known-slow control's
// dose, which gate/controls/arm-known-slow.diff's gate/lib.sh reads. LAUG_COMPOSE_EXTRA is a second compose file that
// cells/backend-compose.sh lays over cell.yml at create, which laug pool serve passes on and the gate unsets.
// LAUG_DENYLIST names the deny list _obs/obs.sh's pack check greps, when one is kept outside the tree.
var Seams = []string{
	"FAKE_*", "FC_*", "LAUG_FI_*", "XDG_*", "ARM_SLOW_*",
	"GATE_LOCK", "GATE_HOST_LOCK", "GATE_POOL_LISTEN", "GATE_BACKEND", "GATE_WORKER_COMMIT", "GATE_WORKFLOW_ID",
	"GATE_WORKFLOW_RUN_ID",
	"THRESHOLDS_RELEASES", "MIN_WINDOW_S", "RESULTS_DIR", "LEASE_FILE", "CATALOG", "CANDIDATES", "CANDIDATE_BEFORE_S",
	"CANDIDATE_REREAD_S", "SWEEP_EXPECTED", "SWEEP_RETRIES", "SWEEP_RETRY_S", "SWEEP_ADDRESS", "DYNCONFIG_TEMPLATE",
	"CLIENT_METRICS_ADDR", "WORKER_METRICS_ADDR", "TDBG_IMAGE", "GUEST_DYNCONFIG_ORIG", "SA_SLICE_S", "ROTATE_SLACK_S",
	"SKIP_WARMUP", "OBS_MIN_FREE_GIB", "OBS_BIN", "LINT_JQ", "LAUG_ROOT", "RUN_DIR", "SUDO_USER", "LAUG_COMPOSE_EXTRA",
	"LAUG_DENYLIST",
}

// Base is what every child needs, and Tools is the tools' own configuration: Go, Docker and ssh. Both pass through
// as the caller has them. TEMPORAL_*, OTEL_*, USER and LOGNAME do not: a script names its cell's address itself, and
// a login name has no way into a result.
var (
	Base  = []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_*", "TERM", "TZ"}
	Tools = []string{"GO*", "DOCKER_*", "SSH_AUTH_SOCK"}
)

// OrchCLI is the flags every temporal CLI call on the orchestrator's dev server takes: the ORCH_CLI of the Makefile
// laug replaced, its two %s the server's address and namespace, which the Makefile wrote in single quotes. The two
// --disable flags keep a config file and the caller's TEMPORAL_* out, --env default keeps TEMPORAL_ENV from naming
// another environment, and --identity laug-cli keeps the CLI's default identity, temporal-cli:<user>@<host>, out of
// history (the Makefile's comment above ORCH_CLI, CLI 1.9.1). orchestrator.CLIFlags prints the same flags, and
// internal/orchestrator's TestCLIFlagsAreORCHCLI holds it to this.
const OrchCLI = "--address %s --namespace %s --env default --identity laug-cli --disable-config-file " +
	"--disable-config-env"

// Lookup returns the knob named name.
func Lookup(name string) (Knob, bool) {
	for _, k := range Knobs {
		if k.Name == name {
			return k, true
		}
	}
	return Knob{}, false
}

// order is a knob's place in the table, which the -dry-run line follows for the knobs make passed unnamed.
func order(name string) int {
	for i, k := range Knobs {
		if k.Name == name {
			return i
		}
	}
	return len(Knobs)
}

// passes reports whether name passes through to a script as the caller has it: a base name, a tool's, or a seam, and
// no knob.
func passes(name string) bool {
	if _, ok := Lookup(name); ok {
		return false
	}
	for _, list := range [][]string{Base, Tools, Seams} {
		for _, p := range list {
			if pre, ok := strings.CutSuffix(p, "*"); ok && strings.HasPrefix(name, pre) || p == name {
				return true
			}
		}
	}
	return false
}
