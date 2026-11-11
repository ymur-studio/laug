package script

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// typedVars is TYPED_VARS as the Makefile laug replaced held it, in its order: 65 names.
var typedVars = strings.Fields(`A B BASE BIND_ADDR CANDIDATE CELL CELL_ADDRESS CUTOFF EVIDENCE FAULT FC_POOL_MAX
	FC_POOL_MIN FRONTEND_PORT GATE GATE_FAKE GATE_POST GATE_RESULTS GATES GRAFANA_PORT HISTORY INTERVAL ITEMS JSON NAME
	NAMESPACE NOCELL OMES_REF OMES_URL ONCE ORCH_ADDRESS ORCH_HTTP_PORT ORCH_METRICS_PORT ORCH_OTLP ORCH_UI_PORT PAIR
	PLAN POOL_ADDR POOL_BACKEND POOL_LISTEN POOL_MAX POOL_MAX_LEASE POOL_MIN POOL_QUOTA PROJECT PROM_URL PROMETHEUS_PORT
	REASON REQUESTER RESTORES RUN_ID SCENARIO SECONDS SERVER_FI SERVER_TAG SINCE TIMER TOXIPROXY_PORT TTL UI_PORT
	UNPOLLED UNTIL USAGE_FOR USAGE_OUT VISIBILITY WF`)

// TestKnobsTyped: the Typed rows are TYPED_VARS as the Makefile laug replaced held it, all 65 in its order. The
// Makefile no longer reads knobs, so this list, frozen, is the record.
func TestKnobsTyped(t *testing.T) {
	if len(typedVars) != 65 {
		t.Fatalf("typedVars holds %d names, want 65", len(typedVars))
	}
	var typed []string
	for _, k := range Knobs {
		if k.From == Typed {
			typed = append(typed, k.Name)
		}
	}
	if !slices.Equal(typed, typedVars) {
		t.Errorf("the Typed rows are not TYPED_VARS in order:\n got %q\nwant %q", typed, typedVars)
	}
}

// flagRE is a knob's Flag: one or more flags, a verb with its argument, or an argument alone.
var flagRE = regexp.MustCompile(`^(-[a-z][a-z0-9-]*(, -[a-z][a-z0-9-]*)*` + `|[a-z]+( [a-z]+)*( <[a-z .]+>(\.\.\.)?)?` +
	`|<[a-z .]+>)$`)

var types = []Type{Word, Number, Seconds, Duration, Tag, URL, Address, Path, GateID, Switch, Time, Text}

// TestKnobRows: each row has a name once, where it came from and a type, and no name passes through as the caller has
// it, since a knob reaches a script only from its verb.
func TestKnobRows(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Knobs {
		if !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(k.Name) || seen[k.Name] {
			t.Errorf("%q: a name is upper case and appears once", k.Name)
		}
		seen[k.Name] = true
		if k.From < Typed || k.From > Passed {
			t.Errorf("%s: where it came from is not set", k.Name)
		}
		if !slices.Contains(types, k.Type) {
			t.Errorf("%s: type %q is not one of the package's", k.Name, k.Type)
		}
		if passes(k.Name) {
			t.Errorf("%s: a knob passes through as the caller has it", k.Name)
		}
		if k.Flag != "" && !flagRE.MatchString(k.Flag) {
			t.Errorf("%s: flag %q is not -kebab-case flags, a verb, or an <argument>", k.Name, k.Flag)
		}
	}
	if len(Knobs) != 65+2+9+17 {
		t.Errorf("%d knobs: 65 typed, 2 ?= ones, 9 a recipe sets and 17 make passed on; a row was added or dropped "+
			"without this count", len(Knobs))
	}
}

// TestKnobReaders: each file a knob names as a reader is in the tree and names the knob. A reader under _obs/, the
// stack's module, is read by _obs's own copy of this test (_obs/cmd/obs, TestKnobReadersHere), since this module never
// reads a file there.
func TestKnobReaders(t *testing.T) {
	for _, k := range Knobs {
		word := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + k.Name + `([^A-Za-z0-9_]|$)`)
		for _, r := range k.Readers {
			if strings.HasPrefix(r, "_obs/") {
				continue
			}
			b, err := os.ReadFile(filepath.Join("..", "..", r))
			if err != nil {
				t.Errorf("%s: reader %s: %v", k.Name, r, err)
				continue
			}
			if !word.Match(b) {
				t.Errorf("%s: reader %s never names it", k.Name, r)
			}
		}
	}
}

// TestSeams: a seam, a base name or a tool's passes through, and so does a name its prefix matches; a knob that
// shares a seam's prefix does not.
func TestSeams(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"PATH", true}, {"HOME", true}, {"TMPDIR", true}, {"LANG", true}, {"LC_ALL", true}, {"TERM", true},
		{"GOFLAGS", true}, {"DOCKER_HOST", true}, {"SSH_AUTH_SOCK", true},
		{"FAKE_GATE_DIR", true}, {"FC_DIR", true}, {"LAUG_FI_METHOD", true}, {"XDG_RUNTIME_DIR", true},
		{"GATE_LOCK", true}, {"CATALOG", true}, {"RESULTS_DIR", true}, {"ARM_SLOW_US", true}, {"LAUG_COMPOSE_EXTRA", true},
		{"LAUG_DENYLIST", true},
		{"FC_POOL_MIN", false}, {"GATE_RESULTS", false}, {"SERVER_TAG", false}, {"RUN_ID", false},
		{"USER", false}, {"LOGNAME", false}, {"TEMPORAL_ADDRESS", false}, {"OTEL_SERVICE_NAME", false},
		{"PATHS", false}, {"LANGUAGE", false}, {"GATE_LOCKS", false},
	} {
		if got := passes(c.name); got != c.want {
			t.Errorf("passes(%s) = %t, want %t", c.name, got, c.want)
		}
	}
}
