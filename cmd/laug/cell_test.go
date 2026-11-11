package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ymur-studio/laug/internal/script"
)

// cellStrays is a stray copy of each knob the cell verbs map beyond strayEnviron's.
var cellStrays = []string{"ENABLE_ES=stray", "COMPOSE_PROFILES=stray", "BIND_ADDR=0.0.0.0", "FRONTEND_PORT=1",
	"UI_PORT=1", "PROMETHEUS_PORT=1", "GRAFANA_PORT=1", "TOXIPROXY_PORT=1", "VISIBILITY=stray", "SERVER_FI=1",
	"CELL=stray", "NOCELL=1", "SINCE=stray", "UNTIL=stray"}

// composeKnobs is the Makefile's CELL_ENV for a cell on block, with the knobs past it.
func composeKnobs(block int, project, tag, visibility, bind, fi string, more ...string) []string {
	es, profiles := "false", ""
	if visibility == "es" {
		es, profiles = "true", "es"
	}
	k := []string{"ENABLE_ES=" + es, "COMPOSE_PROFILES=" + profiles, "BIND_ADDR=" + bind, "SERVER_TAG=" + tag}
	for _, p := range cellPorts {
		k = append(k, p.name+"="+strconv.Itoa(p.base+10*block))
	}
	return append(append(k, "PROJECT="+project, "SERVER_FI="+fi), more...)
}

// TestCell: the env matrix of the cell verbs, cell down's listing of what goes, then every verb but fi-template-check
// through the built binary in this checkout, with stubs for docker, temporal and curl first on PATH and one for
// bin/laug in a checkout LAUG_ROOT names, so the scripts run for real and touch no cell.
func TestCell(t *testing.T) {
	upStrays := []string{"BIND_ADDR", "CELL_ADDRESS", "COMPOSE_PROFILES", "ENABLE_ES", "FRONTEND_PORT", "GRAFANA_PORT",
		"NAMESPACE", "PROJECT", "PROMETHEUS_PORT", "PROM_URL", "SERVER_FI", "SERVER_TAG", "TOXIPROXY_PORT", "UI_PORT",
		"VISIBILITY"}
	composeStrays := []string{"BIND_ADDR", "COMPOSE_PROFILES", "ENABLE_ES", "FRONTEND_PORT", "GRAFANA_PORT", "PROJECT",
		"PROMETHEUS_PORT", "SERVER_FI", "SERVER_TAG", "TOXIPROXY_PORT", "UI_PORT"}
	upKnobs := func(block int, project, tag, visibility, bind, fi, namespace string) []string {
		return composeKnobs(block, project, tag, visibility, bind, fi,
			"CELL_ADDRESS=127.0.0.1:"+strconv.Itoa(7233+10*block), "PROM_URL=http://127.0.0.1:"+strconv.Itoa(9090+10*block),
			"NAMESPACE="+namespace, "VISIBILITY="+visibility)
	}
	defaults := composeKnobs(0, "laug", "1.31.0", "postgres", "127.0.0.1", "")
	runMatrix(t, []envCase{
		{name: "up", args: []string{"cell", "up"}, more: cellStrays, how: script.Exec, script: "cells/up.sh",
			knobs: upKnobs(0, "laug", "1.31.0", "postgres", "127.0.0.1", "", "default"), strays: upStrays,
			out: "laug · cell up: project laug, server 1.31.0, visibility postgres, block 0 (7233, 8080, 9090, 3000, " +
				"8474) on 127.0.0.1; it waits 300 s at most for SERVING; laug cell down -remove-volumes removes it",
			errOut: "laug · cell up: FRONTEND_PORT=1 in the environment is not passed on; -block sets it"},
		{name: "up, every flag", args: []string{"cell", "up", "-server-tag", "1.32.1", "-visibility", "es", "-bind",
			"all", "-block", "2", "-project", "laug2", "-namespace", "ns1", "-server-fi"}, more: cellStrays,
			how: script.Exec, script: "cells/up.sh", knobs: upKnobs(2, "laug2", "1.32.1", "es", "0.0.0.0", "1", "ns1"),
			// BIND_ADDR=0.0.0.0 and SERVER_FI=1 are what -bind all and -server-fi set, so neither is a stray.
			strays: slices.DeleteFunc(slices.Clone(upStrays), func(n string) bool {
				return n == "BIND_ADDR" || n == "SERVER_FI"
			}), out: "laug · cell up: project laug2, server 1.32.1, visibility es, block 2 (7253, 8100, " +
				"9110, 3020, 8494) on 0.0.0.0, server fault injection from LAUG_FI_*; it waits 300 s at most for " +
				"SERVING; laug cell down -project laug2 -remove-volumes removes it",
			errOut: "laug · cell up: CELL_ADDRESS=127.0.0.1:1 in the environment is not passed on; -block sets it"},
		{name: "up, block 99", args: []string{"cell", "up", "-block", "99"}, how: script.Exec, script: "cells/up.sh",
			knobs:  upKnobs(99, "laug", "1.31.0", "postgres", "127.0.0.1", "", "default"),
			strays: []string{"CELL_ADDRESS", "NAMESPACE", "PROJECT", "PROM_URL", "SERVER_TAG"}},
		{name: "up, a tag that is no release", args: []string{"cell", "up", "-server-tag", "1.32"}, code: 2,
			errOut: "laug · cell up: -server-tag 1.32: not a release tag, x.y.z"},
		{name: "up, -visibility mysql", args: []string{"cell", "up", "-visibility", "mysql"}, code: 2,
			errOut: "laug · cell up: -visibility mysql: postgres or es"},
		{name: "up, -bind an address", args: []string{"cell", "up", "-bind", "10.0.0.1"}, code: 2,
			errOut: "laug · cell up: -bind 10.0.0.1: loopback, 127.0.0.1, or all, 0.0.0.0; laug dials a cell on " +
				"127.0.0.1"},
		{name: "up, -block 100", args: []string{"cell", "up", "-block", "100"}, code: 2,
			errOut: "laug · cell up: -block 100: 0 to 99, the ports at 10 a block over block 0's"},
		{name: "up, -block -1", args: []string{"cell", "up", "-block", "-1"}, code: 2},
		{name: "up, two words", args: []string{"cell", "up", "-project", "a b"}, code: 2,
			errOut: "laug · cell up: -project 'a b': not one word of letters, digits, ., _ and -, from a letter or digit"},
		{name: "up, -namespace empty", args: []string{"cell", "up", "-namespace", ""}, code: 2},
		{name: "up, an argument", args: []string{"cell", "up", "laug"}, code: 2,
			errOut: `laug · cell up: takes no arguments, got ["laug"]`},
		{name: "down -remove-volumes -dry-run", args: []string{"cell", "down", "-remove-volumes", "-dry-run"},
			more: cellStrays, strays: composeStrays, out: "ENABLE_ES=false COMPOSE_PROFILES= BIND_ADDR='127.0.0.1' " +
				"SERVER_TAG=1.31.0 FRONTEND_PORT=7233 UI_PORT=8080 PROMETHEUS_PORT=9090 GRAFANA_PORT=3000 " +
				"TOXIPROXY_PORT=8474 PROJECT=laug SERVER_FI='' cells/down.sh",
			errOut: "laug · cell down: SERVER_TAG=9.9.9 in the environment is not passed on; laug sets it"},
		{name: "down", args: []string{"cell", "down"}, code: 2,
			errOut: "laug · cell down: project laug has no container, volume or network: nothing to remove"},
		{name: "down -dry-run", args: []string{"cell", "down", "-dry-run"}, code: 2,
			errOut: "laug · cell down: project laug has no container, volume or network: nothing to remove"},
		{name: "down -remove-volumes", args: []string{"cell", "down", "-remove-volumes"}, how: script.Exec,
			script: "cells/down.sh", knobs: defaults, strays: []string{"PROJECT", "SERVER_TAG"},
			out: "laug · cell down: project laug, 0 containers, 0 volumes and 0 networks go, its data with them"},
		{name: "down -project -remove-volumes", args: []string{"cell", "down", "-project", "laug2", "-remove-volumes"},
			how: script.Exec, script: "cells/down.sh",
			knobs:  composeKnobs(0, "laug2", "1.31.0", "postgres", "127.0.0.1", ""),
			strays: []string{"PROJECT", "SERVER_TAG"}},
		{name: "down, an argument", args: []string{"cell", "down", "laug2"}, code: 2,
			errOut: `laug · cell down: takes no arguments, got ["laug2"]; -project names the cell`},
		{name: "mem", args: []string{"cell", "mem"}, more: cellStrays, how: script.Exec, script: "cells/mem.sh",
			knobs: defaults, strays: composeStrays, out: "laug · cell mem: the memory project laug's containers hold, " +
				"one docker stats; a row a container on stderr, the total on stdout"},
		{name: "mem -project", args: []string{"cell", "mem", "-project", "laug-c0ffee01"}, how: script.Exec,
			script: "cells/mem.sh", knobs: composeKnobs(0, "laug-c0ffee01", "1.31.0", "postgres", "127.0.0.1", ""),
			strays: []string{"PROJECT", "SERVER_TAG"}},
		{name: "health", args: []string{"cell", "health"}, how: script.Exec, script: "cells/health.sh",
			knobs:  []string{"CELL_ADDRESS=127.0.0.1:7233", "NAMESPACE=default", "PROM_URL=http://127.0.0.1:9090"},
			strays: []string{"CELL_ADDRESS", "NAMESPACE", "PROM_URL"},
			out: "laug · cell health: the cell at 127.0.0.1:7233, namespace default, Prometheus " +
				"http://127.0.0.1:9090: cluster health, one HealthCheck workflow, 5 server targets up",
			errOut: "laug · cell health: CELL_ADDRESS=127.0.0.1:1 in the environment is not passed on; -address or " +
				"-block sets it"},
		{name: "health -block", args: []string{"cell", "health", "-block", "2", "-namespace", "ns1"}, how: script.Exec,
			script: "cells/health.sh", knobs: []string{"CELL_ADDRESS=127.0.0.1:7253", "NAMESPACE=ns1",
				"PROM_URL=http://127.0.0.1:9110"}, strays: []string{"CELL_ADDRESS", "NAMESPACE", "PROM_URL"}},
		{name: "health -address -prom", args: []string{"cell", "health", "-block", "2", "-address", "127.0.0.1:17233",
			"-prom", "http://127.0.0.1:19090"}, how: script.Exec, script: "cells/health.sh",
			knobs:  []string{"CELL_ADDRESS=127.0.0.1:17233", "NAMESPACE=default", "PROM_URL=http://127.0.0.1:19090"},
			strays: []string{"CELL_ADDRESS", "NAMESPACE", "PROM_URL"}},
		{name: "health -block 100", args: []string{"cell", "health", "-block", "100"}, code: 2},
		{name: "dynconfig-check", args: []string{"cell", "dynconfig-check"}, more: cellStrays, how: script.Exec,
			script: "cells/dynconfig-check.sh", knobs: []string{"NOCELL=", "SERVER_TAG=1.31.0", "PROJECT=laug"},
			strays: []string{"CELL", "NOCELL", "PROJECT", "SERVER_TAG"},
			out:    "laug · cell dynconfig-check: project laug's laug.yaml, with its own server's validator",
			errOut: "laug · cell dynconfig-check: PROJECT=stray in the environment is not passed on; -project or -cell " +
				"sets it"},
		{name: "dynconfig-check -cell", args: []string{"cell", "dynconfig-check", "-cell", "c0ffee01"},
			how: script.Exec, script: "cells/dynconfig-check.sh",
			knobs:  []string{"NOCELL=", "SERVER_TAG=1.31.0", "PROJECT=laug-c0ffee01"},
			strays: []string{"PROJECT", "SERVER_TAG"}},
		// The last lease is that cell and names a guest: the script gets the lease, and checks over its ssh. LEASE_FILE
		// names another file, as the scripts take it. A lease of another cell, or of none, leaves the host's Docker.
		{name: "dynconfig-check -cell, a microVM lease", args: []string{"cell", "dynconfig-check", "-cell", "c0ffee01"},
			files: map[string]string{".last-lease.json": guestLease("c0ffee01")}, how: script.Exec,
			script: "cells/dynconfig-check.sh", argv: []string{".last-lease.json"},
			knobs:  []string{"NOCELL=", "SERVER_TAG=1.31.0", "PROJECT=laug-c0ffee01"},
			strays: []string{"PROJECT", "SERVER_TAG"},
			out: "laug · cell dynconfig-check: cell c0ffee01's laug.yaml, in its guest at root@192.0.2.10 over the " +
				"lease's ssh, with its own server's validator"},
		{name: "dynconfig-check -cell, LEASE_FILE a microVM lease", args: []string{"cell", "dynconfig-check", "-cell",
			"c0ffee01"}, files: map[string]string{"other.json": guestLease("c0ffee01")},
			more: []string{"LEASE_FILE=other.json"}, how: script.Exec, script: "cells/dynconfig-check.sh",
			argv: []string{"other.json"}, knobs: []string{"NOCELL=", "SERVER_TAG=1.31.0", "PROJECT=laug-c0ffee01"},
			strays: []string{"PROJECT", "SERVER_TAG"}},
		{name: "dynconfig-check -cell, a microVM lease of another cell", args: []string{"cell", "dynconfig-check",
			"-cell", "c0ffee01"}, files: map[string]string{".last-lease.json": guestLease("c0ffee02")},
			how: script.Exec, script: "cells/dynconfig-check.sh",
			knobs:  []string{"NOCELL=", "SERVER_TAG=1.31.0", "PROJECT=laug-c0ffee01"},
			strays: []string{"PROJECT", "SERVER_TAG"},
			out:    "laug · cell dynconfig-check: project laug-c0ffee01's laug.yaml, with its own server's validator"},
		{name: "dynconfig-check -cell, a compose lease", args: []string{"cell", "dynconfig-check", "-cell", "c0ffee01"},
			lease: true, how: script.Exec, script: "cells/dynconfig-check.sh",
			knobs:  []string{"NOCELL=", "SERVER_TAG=1.31.0", "PROJECT=laug-c0ffee01"},
			strays: []string{"PROJECT", "SERVER_TAG"}},
		{name: "dynconfig-check -template", args: []string{"cell", "dynconfig-check", "-template", "-server-tag",
			"1.32.1"}, how: script.Exec, script: "cells/dynconfig-check.sh",
			knobs: []string{"NOCELL=1", "SERVER_TAG=1.32.1", "PROJECT=laug"}, strays: []string{"PROJECT", "SERVER_TAG"},
			out: "laug · cell dynconfig-check: the template, cells/compose/dynamicconfig/laug.yaml, with the validator " +
				"in temporalio/server:1.32.1, no cell"},
		{name: "dynconfig-check, -cell and -project", args: []string{"cell", "dynconfig-check", "-cell", "c0ffee01",
			"-project", "laug"}, code: 2, errOut: "laug · cell dynconfig-check: takes -cell or -project, not both: " +
			"-cell <id> reads project laug-<id>"},
		{name: "dynconfig-check, -template and -cell", args: []string{"cell", "dynconfig-check", "-template", "-cell",
			"c0ffee01"}, code: 2, errOut: "laug · cell dynconfig-check: -template checks the template with no cell, " +
			"so it takes no -cell or -project"},
		{name: "dynconfig-check, -server-tag alone", args: []string{"cell", "dynconfig-check", "-server-tag", "1.32.1"},
			code: 2, errOut: "laug · cell dynconfig-check: -server-tag names the image -template checks with; a " +
				"cell's own image checks its laug.yaml"},
		{name: "dynconfig-check, a tag that is no release", args: []string{"cell", "dynconfig-check", "-template",
			"-server-tag", "latest"}, code: 2,
			errOut: "laug · cell dynconfig-check: -server-tag latest: not a release tag, x.y.z"},
		{name: "fi-template-check", args: []string{"cell", "fi-template-check"}, how: script.Exec,
			script: "cells/fi-template-check.sh", argv: []string{"1.31.0"}, strays: []string{"SERVER_TAG"},
			errOut: "laug · cell fi-template-check: SERVER_TAG=9.9.9 in the environment is not passed on; -server-tag " +
				"sets it"},
		{name: "fi-template-check -server-tag", args: []string{"cell", "fi-template-check", "-server-tag", "1.32.1"},
			how: script.Exec, script: "cells/fi-template-check.sh", argv: []string{"1.32.1"},
			strays: []string{"SERVER_TAG"}},
		{name: "fi-template-check, a tag that is no release", args: []string{"cell", "fi-template-check",
			"-server-tag", "1.32"}, code: 2,
			errOut: "laug · cell fi-template-check: -server-tag 1.32: not a release tag, x.y.z"},
		{name: "softasserts", args: []string{"cell", "softasserts"}, more: cellStrays, how: script.Exec,
			script: "cells/softasserts.sh", argv: []string{"laug"}, strays: []string{"CELL", "PROJECT", "SINCE", "UNTIL"},
			out: "laug · cell softasserts: project laug, each server role's soft assertions from its container's last " +
				"start to now, one JSON object"},
		{name: "softasserts, a window", args: []string{"cell", "softasserts", "-cell", "c0ffee01", "-since", "10m",
			"-until", "2026-10-22T12:00:00Z"}, how: script.Exec, script: "cells/softasserts.sh",
			argv:   []string{"--since", "10m", "--until", "2026-10-22T12:00:00Z", "laug-c0ffee01"},
			strays: []string{"PROJECT"}, out: "laug · cell softasserts: project laug-c0ffee01, each server role's soft " +
				"assertions from 10m to 2026-10-22T12:00:00Z, one JSON object"},
		{name: "softasserts, -cell not a word", args: []string{"cell", "softasserts", "-cell", "a;b"}, code: 2,
			errOut: "laug · cell softasserts: -cell 'a;b': not one word of letters, digits, ., _ and -, from a letter " +
				"or digit"},
	})

	// Bare, down removes nothing: it lists what -remove-volumes would remove, each with its count and names, and the
	// line that removes it, on stderr, exit 2, with or without -json.
	t.Run("down, bare, lists what would go", func(t *testing.T) {
		for _, args := range [][]string{{"cell", "down", "-project", "laug2"}, {"cell", "down", "-project", "laug2",
			"-json"}} {
			a, rec := matrixApp(t, envCase{})
			bin := t.TempDir()
			writeExec(t, filepath.Join(bin, "docker"), cellDockerStub)
			a.Environ = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "FAKE_STUB_LOG=" + filepath.Join(bin, "log")},
				a.Environ...)
			code, out, errOut := runApp(t, a, args...)
			want := "laug · cell down: would remove project laug2, docker compose down -v: 2 containers, 2 volumes " +
				"and 1 network, its data with them\n" +
				"  containers  laug-postgresql-1, laug-temporal-history-1\n" +
				"  volumes     laug_pgdata, laug_promdata\n" +
				"  networks    laug_cell\n" +
				"laug cell down -project laug2 -remove-volumes removes them\n"
			if code != 2 || len(rec.calls) != 0 || out != "" || errOut != want {
				t.Errorf("%q: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s\nwant:\n%s", args, code, len(rec.calls), out,
					errOut, want)
			}
		}
	})

	// A project in a pool cell's shape is asked of the pool first: one the pool lists is refused, a leased cell's line
	// naming release and a warm one's pool down; bare says so after its list. A cell no pool lists, or a pool that does
	// not answer, is removed; a project of another shape asks nothing.
	t.Run("down, a pool's cell", func(t *testing.T) {
		asked := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked++
			fmt.Fprint(w, `{"min_warm":1,"max_cells":2,"quota":2,"max_lease":"2h0m0s","cells":[`+
				`{"id":"0123abcd","project":"laug-0123abcd","block":1,"state":"leased","requester":"ci"},`+
				`{"id":"0a0b0c0d","block":2,"state":"warm"}]}`)
		}))
		defer srv.Close()
		saved := cellPoolAddr
		defer func() { cellPoolAddr = saved }()
		addr := strings.TrimPrefix(srv.URL, "http://")
		const listed = "project %s is cell %s of the pool on %s, %s"
		for _, c := range []struct {
			name    string
			project string
			args    []string
			pool    string // the address asked; "" the test server
			code    int
			asks    int
			errOut  string // the first stderr line, or the last when it starts with "..."
		}{
			{"leased", "laug-0123abcd", []string{"-remove-volumes"}, "", 2, 1, "laug · cell down: " +
				fmt.Sprintf(listed, "laug-0123abcd", "0123abcd", addr, "leased") + ", and removed behind the pool it " +
				"would stay on the pool's list: laug release 0123abcd gives the lease back, and the pool removes the cell"},
			{"warm, by its id", "laug-0a0b0c0d", []string{"-remove-volumes"}, "", 2, 1, "laug · cell down: " +
				fmt.Sprintf(listed, "laug-0a0b0c0d", "0a0b0c0d", addr, "warm") + ", and removed behind the pool it " +
				"would stay on the pool's list: laug pool down -remove-cells stops the pool and removes its cells"},
			{"leased, bare", "laug-0123abcd", nil, "", 2, 1, "...But " + fmt.Sprintf(listed, "laug-0123abcd",
				"0123abcd", addr, "leased") + ", so -remove-volumes refuses it: laug release 0123abcd gives the lease " +
				"back, and the pool removes the cell"},
			{"one the pool does not list", "laug-00000000", []string{"-remove-volumes"}, "", 0, 1,
				"laug · cell down: project laug-00000000, 2 containers, 2 volumes and 1 network go, its data with them"},
			{"no pool answers", "laug-0123abcd", []string{"-remove-volumes"}, "127.0.0.1:1", 0, 0,
				"laug · cell down: project laug-0123abcd, 2 containers, 2 volumes and 1 network go, its data with them"},
			{"another shape", "laug-0123abcde", []string{"-remove-volumes"}, "", 0, 0,
				"laug · cell down: project laug-0123abcde, 2 containers, 2 volumes and 1 network go, its data with them"},
			{"-dry-run asks nothing", "laug-0123abcd", []string{"-remove-volumes", "-dry-run"}, "", 0, 0, ""},
		} {
			t.Run(c.name, func(t *testing.T) {
				asked, cellPoolAddr = 0, addr
				if c.pool != "" {
					cellPoolAddr = c.pool
				}
				a, rec := matrixApp(t, envCase{})
				bin := t.TempDir()
				writeExec(t, filepath.Join(bin, "docker"), cellDockerStub)
				a.Environ = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "FAKE_STUB_LOG=" + filepath.Join(bin,
					"log")}, a.Environ...)
				code, out, errOut := runApp(t, a, append([]string{"cell", "down", "-project", c.project},
					c.args...)...)
				var lines []string // past the strays' lines, matrixApp's
				for _, l := range strings.Split(errOut+out, "\n") {
					if !strayLine.MatchString(l) {
						lines = append(lines, l)
					}
				}
				line := lines[0]
				if strings.HasPrefix(c.errOut, "...") {
					line = "..." + lastLine(errOut)
				}
				ran := map[bool]int{true: 1, false: 0}[c.code == 0 && !slices.Contains(c.args, "-dry-run")]
				if code != c.code || len(rec.calls) != ran || asked != c.asks || (c.errOut != "" && line != c.errOut) {
					t.Errorf("exit %d, %d calls, the pool asked %d times\nstdout:\n%s\nstderr:\n%s\nwant exit %d, "+
						"%d asks, %q", code, len(rec.calls), asked, out, errOut, c.code, c.asks, c.errOut)
				}
			})
		}
	})

	t.Run("down lists what goes", func(t *testing.T) {
		a, rec := matrixApp(t, envCase{})
		bin := t.TempDir()
		log := filepath.Join(bin, "docker.log")
		writeExec(t, filepath.Join(bin, "docker"), cellDockerStub)
		a.Environ = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "FAKE_STUB_LOG=" + log}, a.Environ...)
		code, out, errOut := runApp(t, a, "cell", "down", "-project", "laug2", "-remove-volumes")
		if code != 0 || len(rec.calls) != 1 || rec.hows[0] != script.Exec ||
			firstLine(out) != "laug · cell down: project laug2, 2 containers, 2 volumes and 1 network go, its data "+
				"with them" {
			t.Fatalf("exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", code, len(rec.calls), out, errOut)
		}
		b, _ := os.ReadFile(log)
		want := "docker ps -a --filter label=com.docker.compose.project=laug2 --format {{.Names}}\n" +
			"docker volume ls --filter label=com.docker.compose.project=laug2 --format {{.Name}}\n" +
			"docker network ls --filter label=com.docker.compose.project=laug2 --format {{.Name}}\n"
		if string(b) != want {
			t.Errorf("docker was asked:\n%s\nwant:\n%s", b, want)
		}
	})

	// -json keeps the script a child, its lines and laug's first on stderr, and prints what went once it exits 0.
	t.Run("down -json", func(t *testing.T) {
		for _, c := range []struct {
			name string
			exit int
		}{{"removed", 0}, {"compose fails", 3}} {
			t.Run(c.name, func(t *testing.T) {
				a, rec := matrixApp(t, envCase{})
				bin := t.TempDir()
				writeExec(t, filepath.Join(bin, "docker"), cellDockerStub)
				a.Environ = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "FAKE_STUB_LOG=" + filepath.Join(bin, "log")},
					a.Environ...)
				rec.run = func(_ script.Command, _ []string, stdout, _ io.Writer) int {
					fmt.Fprintln(stdout, "compose's line")
					return c.exit
				}
				code, out, errOut := runApp(t, a, "cell", "down", "-json", "-remove-volumes")
				if code != c.exit || len(rec.calls) != 1 || rec.hows[0] != script.Run {
					t.Fatalf("exit %d, want %d; %d calls\nstdout:\n%s\nstderr:\n%s", code, c.exit, len(rec.calls), out,
						errOut)
				}
				if !strings.Contains(errOut, "\nlaug · cell down: project laug, 2 containers, 2 volumes and 1 network "+
					"go, its data with them\ncompose's line\n") {
					t.Errorf("stderr:\n%s", errOut)
				}
				if c.exit != 0 {
					if out != "" {
						t.Errorf("compose failed and -json printed:\n%s", out)
					}
					return
				}
				var got cellGone
				if err := json.Unmarshal([]byte(out), &got); err != nil || strings.Count(out, "\n") != 1 {
					t.Fatalf("not one JSON line: %v\n%s", err, out)
				}
				want := cellGone{Project: "laug", Containers: []string{"laug-postgresql-1", "laug-temporal-history-1"},
					Volumes: []string{"laug_pgdata", "laug_promdata"}, Networks: []string{"laug_cell"}}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("-json printed %+v, want %+v", got, want)
				}
			})
		}
	})

	t.Run("down, docker cannot say", func(t *testing.T) {
		for _, c := range []struct {
			name, path, errOut string
			code               int
		}{
			{"no docker on PATH", "/nonexistent", "laug · cell down: no docker on PATH, so nothing can say what goes, " +
				"and docker compose down cannot run", 2},
			{"no daemon", "", "laug · cell down: docker ps could not say what goes, so nothing was removed: Cannot " +
				"connect to the Docker daemon", 1},
		} {
			t.Run(c.name, func(t *testing.T) {
				for _, args := range [][]string{{"cell", "down"}, {"cell", "down", "-remove-volumes"}} {
					a, rec := matrixApp(t, envCase{})
					bin := t.TempDir()
					writeExec(t, filepath.Join(bin, "docker"), "#!/bin/sh\necho 'Cannot connect to the Docker daemon' "+
						">&2\nexit 1\n")
					path := bin + ":/usr/bin:/bin"
					if c.path != "" {
						path = c.path
					}
					a.Environ = append([]string{"PATH=" + path}, a.Environ...)
					code, out, errOut := runApp(t, a, args...)
					if code != c.code || len(rec.calls) != 0 || out != "" || firstLine(errOut) != c.errOut {
						t.Errorf("%q: exit %d, want %d; %d calls\nstdout:\n%s\nstderr:\n%s", args, code, c.code,
							len(rec.calls), out, errOut)
					}
				}
			})
		}
	})

	t.Run("outside a checkout", func(t *testing.T) {
		for _, v := range []string{"up", "down", "mem", "health", "dynconfig-check", "fi-template-check",
			"softasserts"} {
			rec := &recorder{}
			code, out, errOut := runApp(t, &App{Runner: rec}, "cell", v)
			if code != 2 || len(rec.calls) != 0 || out != "" ||
				!strings.HasPrefix(errOut, "laug · cell "+v+": runs cells/") {
				t.Errorf("cell %s: exit %d, %d calls\nstdout:\n%s\nstderr:\n%s", v, code, len(rec.calls), out, errOut)
			}
		}
	})

	t.Run("the scripts", cellScripts)
}

// cellDockerStub answers what the cell verbs and their scripts ask docker: compose up, down and ps; docker ps for a
// project's containers, with --format for their names; volume and network ls; inspect, logs with history's shards
// line, stats and the validator, whose line FAKE_STUB_VALIDATE sets. It writes its arguments to FAKE_STUB_LOG, and for
// a compose call the compose environment too.
const cellDockerStub = `#!/bin/sh
case "$1" in
compose) echo "docker $* | ENABLE_ES=$ENABLE_ES BIND_ADDR=$BIND_ADDR SERVER_TAG=$SERVER_TAG" \
  "FRONTEND_PORT=$FRONTEND_PORT" >> "$FAKE_STUB_LOG" ;;
*) echo "docker $*" >> "$FAKE_STUB_LOG" ;;
esac
case "$1" in
compose) case "$*" in *" ps -q"*) printf 'c1\nc2\n' ;; esac ;;
ps) case "$*" in *--format*) printf 'laug-temporal-history-1\nlaug-postgresql-1\n' ;; *) echo cid1 ;; esac ;;
volume) printf 'laug_promdata\nlaug_pgdata\n' ;;
network) echo laug_cell ;;
inspect) case "$*" in
  *Config.Image*) echo temporalio/server:1.31.0 ;;
  *State.Status*) echo "running 2026-10-22T12:00:00Z" ;;
  *StartedAt*) echo 2026-10-22T12:00:00Z ;;
  esac ;;
logs) echo '{"msg":"initial shards ready","total":512,"ts":"2026-10-22T12:00:01Z"}' ;;
stats) printf 'laug-b-1\t1.5GiB / 4GiB\nlaug-a-1\t100MiB / 1GiB\n' ;;
exec|run) echo "${FAKE_STUB_VALIDATE:-validated}" ;;
esac
exit 0
`

// cellScripts runs the verbs through the built binary in this checkout, so each execs its script for real against
// stubs for docker, temporal, curl and bin/laug: what the scripts print and ask the stubs, with a stray SERVER_TAG and
// FRONTEND_PORT in the caller's environment that never reach them. TestCell runs it as its own subtest, last, so
// without yq or jq on PATH needTools skips only this part, and LAUG_TEST_SHELL=require fails it.
func cellScripts(t *testing.T) {
	needTools(t, "the cell scripts run it", "yq", "jq")
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	laug := filepath.Join(laugBinary(t), "laug")
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "docker"), cellDockerStub)
	writeExec(t, filepath.Join(bin, "temporal"), "#!/bin/sh\necho \"temporal $*\" >> \"$FAKE_STUB_LOG\"\n")
	// bin/laug, whose health check cells/health.sh runs, in a checkout of the stubs' own that LAUG_ROOT, a seam,
	// names: it would dial the cell. It logs its arguments and whether CELL_ADDRESS or NAMESPACE reached it.
	stubRoot := t.TempDir()
	writeExec(t, filepath.Join(stubRoot, "bin", "laug"), "#!/bin/sh\necho \"laug $* (CELL_ADDRESS ${CELL_ADDRESS-unset},"+
		" NAMESPACE ${NAMESPACE-unset})\" >> \"$FAKE_STUB_LOG\"\necho health check passed\n")
	// curl answers Prometheus's targets: the five server roles, each up.
	writeExec(t, filepath.Join(bin, "curl"), `#!/bin/sh
echo "curl $*" >> "$FAKE_STUB_LOG"
t=''
for r in frontend frontend2 history matching worker; do
  t="$t{\"labels\":{\"job\":\"temporal-$r\",\"instance\":\"i\"},\"health\":\"up\"},"
done
echo "{\"data\":{\"activeTargets\":[${t%,}]}}"
`)
	cell := func(t *testing.T, env []string, args ...string) (int, string, string, string) {
		t.Helper()
		log := filepath.Join(t.TempDir(), "stub.log")
		// A script that waits, cells/up.sh for SERVING, would wait 300 s for a stub that fails; 60 s stops it first.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, laug, append([]string{"cell"}, args...)...)
		cmd.Dir = root
		cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "FAKE_STUB_LOG=" + log,
			"SERVER_TAG=9.9.9", "FRONTEND_PORT=1"}, env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		b, _ := os.ReadFile(log)
		return code, stdout.String(), stderr.String(), string(b)
	}
	compose := "| ENABLE_ES=false BIND_ADDR=127.0.0.1 SERVER_TAG=1.31.0 FRONTEND_PORT=7233"
	// The binary sets LAUG for its script to the word its PATH gives, this process's with bin before it.
	self := selfWord(exec.LookPath)
	for _, c := range []struct {
		name string
		args []string
		env  []string
		code int
		out  []string // stdout's lines, after laug's first
		log  []string // lines the stubs' log holds
		err  string   // what stderr holds, when not empty
	}{
		{name: "up", args: []string{"up"}, out: []string{
			"cell laug SERVING, server 1.31.0, namespace default registered, all shards owned, visibility postgres, " +
				"after 0 s",
			"UI http://localhost:8080  Prometheus http://127.0.0.1:9090  Grafana http://localhost:3000"},
			log: []string{"docker compose -p laug --project-directory cells/compose -f cells/compose/cell.yml up -d " +
				compose, "temporal operator cluster health --address 127.0.0.1:7233",
				"temporal operator namespace describe -n default --address 127.0.0.1:7233"}},
		{name: "up -server-fi on another tag", args: []string{"up", "-server-fi", "-server-tag", "1.32.1"}, code: 2,
			err: "compose/config/laug-server.yaml is the 1.31.0 template and this cell would run 1.32.1; " + self +
				" cell fi-template-check -server-tag 1.32.1 shows what changed\n"},
		{name: "down", args: []string{"down", "-remove-volumes"}, log: []string{"docker compose -p laug " +
			"--project-directory cells/compose " +
			"-f cells/compose/cell.yml --profile es down -v --remove-orphans " + compose}},
		{name: "mem", args: []string{"mem"}, out: []string{"total 1636 MiB across 2 containers"},
			log: []string{"docker stats --no-stream --format {{.Name}}\t{{.MemUsage}} c1 c2"}},
		{name: "health", args: []string{"health"}, env: []string{"LAUG_ROOT=" + stubRoot}, out: []string{
			"temporal operator cluster health --address '127.0.0.1:7233'",
			stubRoot + `/bin/laug health check -address '127.0.0.1:7233' -namespace default`,
			"health check passed", "temporal-frontend\ti\tup", "temporal-frontend2\ti\tup", "temporal-history\ti\tup",
			"temporal-matching\ti\tup", "temporal-worker\ti\tup"},
			log: []string{"laug health check -address 127.0.0.1:7233 -namespace default (CELL_ADDRESS unset, " +
				"NAMESPACE unset)",
				"curl -sf http://127.0.0.1:9090/api/v1/targets"}},
		{name: "dynconfig-check", args: []string{"dynconfig-check", "-cell", "c0ffee01"}, out: []string{
			"temporalio/server:1.31.0 validate-dynamic-config on laug-c0ffee01's laug.yaml", "validated"},
			log: []string{"docker exec cid1 temporal-server validate-dynamic-config " +
				"/etc/temporal/config/dynamicconfig/laug.yaml"}},
		{name: "dynconfig-check, a warning", args: []string{"dynconfig-check"},
			env: []string{"FAKE_STUB_VALIDATE=warning: unknown key"}, code: 1},
		{name: "dynconfig-check -template", args: []string{"dynconfig-check", "-template"}, out: []string{
			"temporalio/server:1.31.0 validate-dynamic-config on cells/compose/dynamicconfig/laug.yaml, the template " +
				"(no cell)", "validated"},
			log: []string{"docker run --rm --network none --entrypoint temporal-server -v " + root +
				"/cells/compose/dynamicconfig:/d:ro temporalio/server:1.31.0 validate-dynamic-config /d/laug.yaml"}},
		{name: "softasserts", args: []string{"softasserts"}, out: []string{`{"temporal-frontend":0,` +
			`"temporal-frontend2":0,"temporal-history":0,"temporal-matching":0,"temporal-worker":0,"first":[]}`}},
	} {
		t.Run("the script: "+c.name, func(t *testing.T) {
			code, out, errOut, log := cell(t, c.env, c.args...)
			if code != c.code {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s\nstubs:\n%s", code, c.code, out, errOut, log)
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			// cells/up.sh counts whole seconds from date +%s, so a wait under one second reads 1 s when it crosses a
			// second, or on a busy runner; 0 s and 1 s both match "after 0 s".
			for i, l := range lines {
				lines[i] = afterSlack.ReplaceAllString(l, "after 0 s")
			}
			if c.code == 0 && (!strings.HasPrefix(lines[0], "laug · cell "+c.args[0]+": ") ||
				!reflect.DeepEqual(lines[1:], append([]string{}, c.out...))) {
				t.Errorf("stdout:\n%s\nwant laug's first line, then:\n%s", out, strings.Join(c.out, "\n"))
			}
			for _, l := range c.log {
				if !strings.Contains(log, l+"\n") {
					t.Errorf("the stubs were not asked %q:\n%s", l, log)
				}
			}
			if !strings.Contains(errOut, c.err) {
				t.Errorf("stderr lacks %q:\n%s", c.err, errOut)
			}
			if strings.Contains(log, "9.9.9") || regexp.MustCompile(`FRONTEND_PORT=1\b`).MatchString(log) {
				t.Errorf("a stray reached a script:\n%s", log)
			}
		})
	}
}

// afterSlack matches the wait cells/up.sh reports, a second's slack over the stubs' 0 s.
var afterSlack = regexp.MustCompile(`after [01] s$`)

// TestCellDefaultsMatchKnobs: block 0's ports, the visibility and the bind are the defaults of the Makefile laug
// replaced, as internal/script's knob table keeps them, cell.yml's, and, with 10 a block, cells/backend-compose.sh's.
func TestCellDefaultsMatchKnobs(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	yml, be := read("cells/compose/cell.yml"), read("cells/backend-compose.sh")
	for _, p := range cellPorts {
		base := strconv.Itoa(p.base)
		lower := strings.ToLower(strings.TrimSuffix(p.name, "_PORT"))
		if k, ok := script.Lookup(p.name); !ok || k.Default != base {
			t.Errorf("knob %s: default %q, laug's block 0 %s", p.name, k.Default, base)
		}
		for _, c := range []struct{ file, text, re string }{
			{"cell.yml", yml, `\$\{` + p.name + `:-([0-9]+)\}`},
			{"backend-compose.sh", be, `(?m)^` + lower + `_port=\$\(\( ([0-9]+) \+ offset \)\)$`},
		} {
			if m := regexp.MustCompile(c.re).FindStringSubmatch(c.text); m == nil || m[1] != base {
				t.Errorf("%s: %s is %q, laug's block 0 %s", c.file, p.name, m, base)
			}
		}
	}
	if !strings.Contains(be, "offset=$(( block * 10 ))\n") {
		t.Errorf("cells/backend-compose.sh lays its blocks out at another step than 10")
	}
	for name, want := range map[string]string{"VISIBILITY": visibilityDefault, "BIND_ADDR": bindAddrs[bindDefault]} {
		if k, ok := script.Lookup(name); !ok || k.Default != want {
			t.Errorf("knob %s: default %q, laug %q", name, k.Default, want)
		}
	}
}

// guestLease is a microVM cell's lease as laug pool serve -backend firecracker answers one: the guest as ssh.
func guestLease(id string) string {
	return `{"id":"` + id + `","address":"192.0.2.10:7233","ssh":"root@192.0.2.10",` +
		`"dynconfig":"/opt/laug/cells/compose/dynamicconfig/laug.yaml","backend":"firecracker"}`
}
