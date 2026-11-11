package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/script"
)

// TestHost: the env matrix of host type and host clock, then both through the built binary in this checkout, where
// their scripts run for real: host type with XDG_CONFIG_HOME in a temp dir, bare, with -declare and with -json, and
// host clock for 2 seconds.
func TestHost(t *testing.T) {
	typeFirst := "laug · host type: the type this host declares, its size and the threshold set's host, as a gate " +
		"checks them"
	runGateMatrix(t, []envCase{
		{name: "type", args: []string{"host", "type"}, how: script.Exec, script: "gate/host-type.sh", out: typeFirst},
		{name: "type -declare", args: []string{"host", "type", "-declare", "m8i.8xlarge"}, how: script.Exec,
			script: "gate/host-type.sh", argv: []string{"m8i.8xlarge"}},
		{name: "type -json", args: []string{"host", "type", "-json"}, how: script.Run, script: "gate/host-type.sh",
			knobs: []string{"JSON=1"}},
		{name: "type -declare -json", args: []string{"host", "type", "-json", "-declare", "m8i.8xlarge"},
			how: script.Run, script: "gate/host-type.sh", argv: []string{"m8i.8xlarge"}, knobs: []string{"JSON=1"}},
		{name: "type -json -dry-run", args: []string{"host", "type", "-json", "-dry-run"},
			out: "JSON=1 gate/host-type.sh"},
		{name: "-declare, not a type name", args: []string{"host", "type", "-declare", "M8I 8XL"}, code: 2,
			errOut: "laug · host type: -declare 'M8I 8XL': a type name, lowercase letters, digits, . and -, 32 at " +
				"most, as EC2 names one (m8i.8xlarge)"},
		{name: "-declare, empty", args: []string{"host", "type", "-declare", ""}, code: 2},
		{name: "type, an argument", args: []string{"host", "type", "m8i.8xlarge"}, code: 2,
			errOut: `laug · host type: takes no arguments, got ["m8i.8xlarge"]; -declare declares a type`},
		{name: "clock", args: []string{"host", "clock"}, how: script.Exec, script: "cells/clock-check.sh",
			argv: []string{"60"}, strays: []string{"SECONDS"},
			errOut: "laug · host clock: SECONDS=5 in the environment is not passed on; -for sets it"},
		{name: "clock -for 5s", args: []string{"host", "clock", "-for", "5s"}, how: script.Exec,
			script: "cells/clock-check.sh", argv: []string{"5"}, strays: []string{"SECONDS"}},
		{name: "clock -for 2m", args: []string{"host", "clock", "-for", "2m"}, how: script.Exec,
			script: "cells/clock-check.sh", argv: []string{"120"}, strays: []string{"SECONDS"}},
		{name: "clock -for 1s", args: []string{"host", "clock", "-for", "1s"}, code: 2,
			errOut: "laug · host clock: -for 1s: 2s or more"},
		{name: "clock -for 2.5s", args: []string{"host", "clock", "-for", "2.5s"}, code: 2,
			errOut: "laug · host clock: -for 2.5s: whole seconds, 0 or more"},
	})

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	laug := filepath.Join(laugBinary(t), "laug")
	xdg := t.TempDir()
	host := func(args ...string) (int, string, string) {
		t.Helper()
		cmd := exec.Command(laug, append([]string{"host"}, args...)...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + xdg}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), stdout.String(), stderr.String()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0, stdout.String(), stderr.String()
	}
	t.Run("type, the script", func(t *testing.T) {
		needTools(t, "gate/host-type.sh runs it", "yq", "jq", "nproc", "free")
		code, out, errOut := host("type")
		lines := strings.Split(out, "\n")
		if code != 0 || len(lines) != 7 || lines[0] != typeFirst || lines[1] != "declared          none" ||
			!strings.HasPrefix(lines[3], "virtualization    ") || !strings.Contains(lines[3], "hypervisor flag in /proc/cpuinfo") ||
			!strings.HasPrefix(lines[5], "laug gate run     refuses this host: this host declares no host type") {
			t.Errorf("bare: exit %d\n%s%s", code, out, errOut)
		}
		code, out, errOut = host("type", "-json")
		var doc struct {
			Declared *string        `json:"declared"`
			VCPU     int            `json:"vcpu"`
			MemGiB   int            `json:"mem_gib"`
			Virt     *bool          `json:"virtualized"` // a pointer: the key must be there, and a boolean, on a VM or not
			Set      map[string]any `json:"set"`
			Passes   bool           `json:"passes"`
			Reason   *string        `json:"reason"`
		}
		dec := json.NewDecoder(strings.NewReader(out))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil || code != 0 || errOut != "" || strings.Count(out, "\n") != 1 ||
			doc.Declared != nil || doc.VCPU < 1 || doc.Virt == nil || doc.Set["type"] != "m8i.8xlarge" || doc.Passes || doc.Reason == nil ||
			!strings.HasPrefix(*doc.Reason, "this host declares no host type") {
			t.Errorf("-json: exit %d, %v\n%s%s", code, err, out, errOut)
		}
		code, out, errOut = host("type", "-declare", "m8i.8xlarge")
		b, err := os.ReadFile(filepath.Join(xdg, "laug", "host-type"))
		if code != 0 || string(b) != "m8i.8xlarge\n" || firstLine(out) != "laug · host type: declared m8i.8xlarge" ||
			!strings.Contains(out, "\ndeclared          m8i.8xlarge, in (outside the repository)/host-type\n") {
			t.Errorf("-declare: exit %d, the file %q %v\n%s%s", code, b, err, out, errOut)
		}
		if strings.Contains(out, xdg) {
			t.Errorf("host type printed the path of its declaration:\n%s", out)
		}
		code, out, errOut = host("type", "-json", "-declare", "m7i.8xlarge")
		b, _ = os.ReadFile(filepath.Join(xdg, "laug", "host-type"))
		if code != 0 || errOut != "" || string(b) != "m7i.8xlarge\n" ||
			!strings.HasPrefix(out, `{"declared":"m7i.8xlarge","vcpu":`) || strings.Contains(out, "laug ·") {
			t.Errorf("-json -declare: exit %d, the file %q\n%s%s", code, b, out, errOut)
		}
	})
	if b, err := os.ReadFile(filepath.Join(root, "cells", "clock-check.sh")); err != nil ||
		!bytes.Contains(b, []byte(`secs="${1:-60}"`)) || clockSecondsDefault != 60 {
		t.Errorf("host clock's -for default, %ds, is not cells/clock-check.sh's own, 60: %v", clockSecondsDefault, err)
	}
	t.Run("clock, the script", func(t *testing.T) {
		if _, err := os.Stat("/proc/uptime"); err != nil {
			t.Skip("no /proc/uptime: cells/clock-check.sh needs Linux")
		}
		code, out, errOut := host("clock", "-for", "2s")
		if code != 0 || firstLine(out) != "laug · clock-check: wall clock against /proc/uptime over 2 s, one sample a "+
			"second" || !strings.Contains(out, "\nlaug · uptime ") {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
}
