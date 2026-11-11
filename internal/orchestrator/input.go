package orchestrator

import "strings"

// GateInput is laug gate run's environment (gate/run.sh, Environment), carried as workflow input so that history
// records what the gate was asked to judge. No step reads the worker's environment for any of it.
type GateInput struct {
	ServerTag  string `json:"server_tag"`
	Plan       string `json:"plan"`
	Items      string `json:"items"`
	Fake       bool   `json:"fake"`
	Post       string `json:"gate_post"`
	Base       string `json:"base"`
	OmesDir    string `json:"omes_dir"`
	Results    string `json:"gate_results"`
	Lock       string `json:"gate_lock"`
	HostLock   string `json:"gate_host_lock"`
	PoolListen string `json:"gate_pool_listen"`
	// Extra is the fake gate's knobs, GATE_FAKE=1 only: GATE_BACKEND and FAKE_GATE_*, as make harness-test sets them.
	Extra map[string]string `json:"extra,omitempty"`
}

// scrubbed is what Env takes out of the worker's environment before it adds the inputs: every TEMPORAL_*, FAKE_*,
// GATE_* and LAUG_* name, make's own, and the names gate/run.sh and gate/step.sh read. StepEnv takes out the items'
// knobs, and gate/lib.sh's gate_env unsets them again in both drivers.
var scrubbed = map[string]bool{"SERVER_TAG": true, "PLAN": true, "ITEMS": true, "BASE": true, "OMES_DIR": true,
	"MAKEFLAGS": true, "MAKELEVEL": true, "MFLAGS": true, "MAKE_TERMOUT": true, "MAKE_TERMERR": true}

// Env builds laug gate run's environment from the worker's, scrubbed, plus the inputs. StepEnv starts from it for
// gate/step.sh, and the parity tests run gate/run.sh under it, as laug gate run would.
func (in GateInput) Env(environ []string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if scrubbed[name] || strings.HasPrefix(name, "TEMPORAL_") || strings.HasPrefix(name, "FAKE_") ||
			strings.HasPrefix(name, "GATE_") || strings.HasPrefix(name, "LAUG_") {
			continue
		}
		env = append(env, kv)
	}
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("SERVER_TAG", in.ServerTag)
	set("PLAN", in.Plan)
	set("ITEMS", in.Items)
	if in.Fake {
		set("GATE_FAKE", "1")
	} else {
		set("GATE_FAKE", "")
	}
	set("GATE_POST", in.Post)
	set("BASE", in.Base)
	set("OMES_DIR", in.OmesDir)
	set("GATE_RESULTS", in.Results)
	set("GATE_LOCK", in.Lock)
	set("GATE_HOST_LOCK", in.HostLock)
	set("GATE_POOL_LISTEN", in.PoolListen)
	if in.Fake {
		for k, v := range in.Extra {
			if k == "GATE_BACKEND" || strings.HasPrefix(k, "FAKE_GATE_") {
				set(k, v)
			}
		}
	}
	return env
}
