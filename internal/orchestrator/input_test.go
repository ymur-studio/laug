package orchestrator

import (
	"strings"
	"testing"
)

// The fake gate's knobs travel through Extra, which Env passes on only for a fake gate; the tests set Fake.
func withFake(in GateInput, kv ...string) GateInput {
	in.Fake = true
	if in.Extra == nil {
		in.Extra = map[string]string{}
	}
	for _, s := range kv {
		k, v, _ := strings.Cut(s, "=")
		in.Extra["FAKE_GATE_"+k] = v
	}
	return in
}

func TestGateInputEnvScrubsTheWorkersAndSetsTheGates(t *testing.T) {
	in := GateInput{ServerTag: "1.31.0", Plan: "p.yaml", Items: "2", Post: "local", Base: "main", OmesDir: "o", Results: "r",
		Lock: "l", HostLock: "h", PoolListen: "127.0.0.1:7700", Fake: true, Extra: map[string]string{"FAKE_GATE_ITEM_S": "3", "GATE_BACKEND": "", "OTHER": "x"}}
	got := in.Env([]string{"PATH=/bin", "TEMPORAL_ADDRESS=127.0.0.1:7400", "TEMPORAL_API_KEY=k", "FAKE_GATE_HANG=pl", "GATE_LOCK=/elsewhere",
		"LAUG_FI_X=1", "MAKEFLAGS=-s", "SERVER_TAG=9.9.9", "HOME=/h"})
	want := map[string]string{"PATH": "/bin", "HOME": "/h", "SERVER_TAG": "1.31.0", "PLAN": "p.yaml", "ITEMS": "2", "GATE_FAKE": "1",
		"GATE_POST": "local", "BASE": "main", "OMES_DIR": "o", "GATE_RESULTS": "r", "GATE_LOCK": "l", "GATE_HOST_LOCK": "h",
		"GATE_POOL_LISTEN": "127.0.0.1:7700", "FAKE_GATE_ITEM_S": "3", "GATE_BACKEND": ""}
	have := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s is set twice", k)
		}
		have[k] = v
	}
	for k, v := range want {
		if hv, ok := have[k]; !ok || hv != v {
			t.Errorf("%s = %q, %v; want %q", k, hv, ok, v)
		}
	}
	for k := range have {
		if _, ok := want[k]; !ok {
			t.Errorf("%s=%s leaked into the gate's environment", k, have[k])
		}
	}
	in.Fake = false
	for _, kv := range in.Env(nil) {
		if strings.HasPrefix(kv, "FAKE_GATE_") || strings.HasPrefix(kv, "GATE_BACKEND=") {
			t.Errorf("a real gate got %s", kv)
		}
		if kv == "GATE_FAKE=" {
			return
		}
	}
	t.Error("a real gate did not get GATE_FAKE= (empty)")
}
