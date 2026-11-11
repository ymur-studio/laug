package follow

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// doneFollower is a follower that read the whole copied gate, 1 minute after its done line, and its body.
func doneFollower(t *testing.T) (f *follower, dir, obs string) {
	t.Helper()
	dir = gateCopy(t)
	obs = obsDir(t)
	root := fakeRoot(t)
	write(t, filepath.Join(dir, "laugd.log"), "")
	addLaugd(t, root, 9001, "127.0.0.1:7700", filepath.Join(dir, "laugd.log"))
	now := time.Unix(1792203254+60, 0)
	f = testFollower(t, obs, root, "127.0.0.1:7700", nil, &now)
	f.poll()
	return f, dir, obs
}

// TestNoSlashInOutput: no byte of a gate follower's live body, of -openmetrics' output, of a hand follower's body (a
// /status whose URLs, paths and requesters hold slashes) or of a HELP text is "/"; a plan whose scenario holds a slash
// has that series dropped and counted once, at every poll and in -openmetrics, never served.
func TestNoSlashInOutput(t *testing.T) {
	f, dir, obs := doneFollower(t)
	g := newGateDir(gateID, dir)
	g.refresh()
	aa, budget := readConf(obs)
	om, dropped := openMetrics(g, aa, budget)
	root := fakeRoot(t)
	pool, _, status := countingLaugd(t)
	addLaugd(t, root, 2001, pool, "/dev/pts/0")
	status.Store("testdata/status/hand.json")
	now := time.Date(2026, 10, 17, 3, 5, 0, 0, time.UTC)
	hand := testFollower(t, obsDir(t), root, pool, nil, &now)
	hand.poll()
	if !strings.Contains(body(hand), "laug_lease_info{") {
		t.Fatalf("the hand follower serves no lease")
	}
	for name, b := range map[string][]byte{"the live body": []byte(body(f)), "-openmetrics": om, "the hand body": []byte(body(hand))} {
		if i := bytes.IndexByte(b, '/'); i >= 0 {
			t.Errorf("%s holds / at byte %d: %s", name, i, b[max(0, i-60):min(len(b), i+20)])
		}
	}
	if dropped != 0 {
		t.Errorf("%d series dropped from the copy", dropped)
	}
	p := read(t, filepath.Join(dir, "plan.json"))
	write(t, filepath.Join(dir, "plan.json"), strings.Replace(p, `"scenario": "workflow_with_single_noop_activity",`, `"scenario": "a/b",`, 1))
	for i := 1; i <= 3; i++ {
		f.poll()
		if b := body(f); strings.Contains(b, "/") || !strings.Contains(b, "laug_follow_dropped_series 1\n") {
			t.Errorf("poll %d with a scenario a/b: dropped 1 %v, a slash in the body %v", i, strings.Contains(b, "laug_follow_dropped_series 1\n"), strings.Contains(b, "/"))
		}
	}
	g = newGateDir(gateID, dir)
	g.refresh()
	if om, dropped = openMetrics(g, aa, budget); dropped != 1 || bytes.Contains(om, []byte("/")) {
		t.Errorf("-openmetrics with a scenario a/b: %d series left out, want 1; a slash %v", dropped, bytes.Contains(om, []byte("/")))
	}
	for _, fam := range families {
		if strings.Contains(fam.help, "/") {
			t.Errorf("%s's HELP holds a slash", fam.name)
		}
	}
}

// fakeCopy is the gate copy with gate.json's fake true, as a GATE_FAKE=1 gate writes it.
func fakeCopy(t *testing.T) string {
	t.Helper()
	dir := gateCopy(t)
	gj := read(t, filepath.Join(dir, "gate.json"))
	if !strings.Contains(gj, `"fake": false`) {
		t.Fatalf("gate.json has no \"fake\": false")
	}
	write(t, filepath.Join(dir, "gate.json"), strings.Replace(gj, `"fake": false`, `"fake": true`, 1))
	return dir
}

// TestFakeGateLabelsOnce: a fake gate's series each carry fake="true" once, laug_gate_info's own fake label among
// them, live and in -openmetrics, so no sample has a label name twice; a series built with one twice is dropped and
// counted, never served.
func TestFakeGateLabelsOnce(t *testing.T) {
	dir := fakeCopy(t)
	write(t, filepath.Join(dir, "laugd.log"), "")
	root := fakeRoot(t)
	addLaugd(t, root, 9001, "127.0.0.1:7700", filepath.Join(dir, "laugd.log"))
	now := time.Unix(1792203254+60, 0)
	obs := obsDir(t)
	f := testFollower(t, obs, root, "127.0.0.1:7700", nil, &now)
	f.poll()
	var om bytes.Buffer
	if code := runIn(t, []string{"-openmetrics", dir, "-dir", obs}, &om, &bytes.Buffer{}); code != 0 {
		t.Fatalf("-openmetrics: exit %d", code)
	}
	parseBody(t, body(f)) // fails on a label name twice
	info := `laug_gate_info{gate_id="` + gateID + `",server_tag="1.31.0",plan="plan.yaml",driver="step.sh",fake="true",host_type="vcpu-32",laug_commit="715f517",tree_dirty="false"} 1`
	for name, b := range map[string]string{"the live body": body(f), "-openmetrics": om.String()} {
		gate := map[string]bool{}
		for _, line := range strings.Split(b, "\n") {
			end := strings.LastIndexByte(line, '}')
			if strings.HasPrefix(line, "#") || end < 0 {
				continue
			}
			set := line[strings.IndexByte(line, '{') : end+1]
			if n := repeatedLabel(set); n != "" {
				t.Errorf("%s: the label %s twice in %s", name, n, line)
			}
			if strings.Contains(set, `gate_id="`+gateID+`"`) && !strings.HasPrefix(line, "laug_follow_") && !strings.HasPrefix(line, "laug_lease_info") {
				gate[line[:end+1]] = true
				if strings.Count(set, `fake="true"`) != 1 {
					t.Errorf("%s: %s has fake=\"true\" %d times, want once", name, line, strings.Count(set, `fake="true"`))
				}
			}
		}
		if len(gate) < 180 {
			t.Errorf("%s: %d series of the gate, want the copy's 180 and more", name, len(gate))
		}
		if !strings.Contains(b, info) {
			t.Errorf("%s lacks %s", name, info)
		}
	}
	if !strings.Contains(body(f), "laug_follow_dropped_series 0\n") {
		t.Errorf("the fake gate's series were dropped")
	}
	if clean([][2]string{{"gate_id", gateID}, {"fake", "true"}, {"fake", "true"}}) {
		t.Errorf("clean passes a label name twice")
	}
}

// TestOpenMetricsSameRecords: -openmetrics gives every series the live body gives for the gate, laug_follow_* and
// laug_lease_info aside, and the last sample of each is the live value; every sample's time is a whole second inside
// the gate, at or after the series appears.
func TestOpenMetricsSameRecords(t *testing.T) {
	f, dir, obs := doneFollower(t)
	live := parseBody(t, body(f))
	var out bytes.Buffer
	if code := runIn(t, []string{"-openmetrics", dir, "-dir", obs}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("-openmetrics: exit %d", code)
	}
	last := map[string]float64{}
	sample := regexp.MustCompile(`^(.*\}|\S+) (\S+) ([0-9]+)$`)
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		m := sample.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("not a sample with a whole-second time: %q", line)
		}
		ts := m[3]
		if ts < "1792202658" || ts > "1792203255" {
			t.Errorf("a sample at %s, outside the gate's 1792202658 to 1792203255: %s", ts, line)
		}
		v := parse(t, m[2])
		last[m[1]] = v
	}
	n := 0
	for k, v := range live {
		if strings.HasPrefix(k, "laug_follow_") || strings.HasPrefix(k, "laug_lease_info") {
			continue
		}
		n++
		if got, ok := last[k]; !ok || got != v {
			t.Errorf("%s: live %v, -openmetrics' last %v (%v)", k, v, got, ok)
		}
	}
	if len(last) != n {
		t.Errorf("-openmetrics has %d series, the live body %d", len(last), n)
	}
	if !strings.HasSuffix(out.String(), "# EOF\n") {
		t.Errorf("-openmetrics does not end with # EOF")
	}
}

// promtool is the one bin/obs up fetched and checked under OBS_DIR (its tree has .checked), or $PROMTOOL; "" when
// neither is there.
func promtool(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("PROMTOOL"); p != "" {
		return p
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	obs := os.Getenv("OBS_DIR")
	if obs == "" {
		obs = filepath.Join(state, "laug-obs")
	}
	trees, _ := filepath.Glob(filepath.Join(obs, "bin", "prometheus-*"))
	for _, tr := range trees {
		if _, err := os.Stat(filepath.Join(tr, ".checked")); err != nil {
			continue
		}
		if ps, _ := filepath.Glob(filepath.Join(tr, "*", "promtool")); len(ps) == 1 {
			return ps[0]
		}
	}
	return ""
}

// TestOpenMetricsPassesPromtool: -openmetrics' output passes promtool check metrics with no problem, and promtool tsdb
// create-blocks-from openmetrics makes a block of it, as bin/obs backfill will; the live body passes check metrics
// too; both for the copy and for the copy with gate.json's fake true. promtool is $PROMTOOL or the one bin/obs up
// fetched; with neither the test is skipped and says so, and make obs-test, which runs it with the fetched promtool,
// fails on that skip.
func TestOpenMetricsPassesPromtool(t *testing.T) {
	pt := promtool(t)
	if pt == "" {
		t.Skip("no promtool: bin/obs up fetches and checks it into OBS_DIR, or PROMTOOL names one; make obs-test runs this test with it")
	}
	f, dir, obs := doneFollower(t)
	fake := fakeCopy(t)
	write(t, filepath.Join(fake, "laugd.log"), "")
	root := fakeRoot(t)
	addLaugd(t, root, 9002, "127.0.0.1:7700", filepath.Join(fake, "laugd.log"))
	now := time.Unix(1792203254+60, 0)
	ff := testFollower(t, obsDir(t), root, "127.0.0.1:7700", nil, &now)
	ff.poll()
	for _, c := range []struct {
		name, dir string
		f         *follower
	}{{"the copy", dir, f}, {"the copy with fake true", fake, ff}} {
		var om bytes.Buffer
		if code := runIn(t, []string{"-openmetrics", c.dir, "-dir", obs}, &om, &bytes.Buffer{}); code != 0 {
			t.Fatalf("%s: -openmetrics: exit %d", c.name, code)
		}
		for name, b := range map[string][]byte{"-openmetrics": om.Bytes(), "the live body": []byte(body(c.f))} {
			cmd := exec.Command(pt, "check", "metrics")
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			cmd.Stdin = bytes.NewReader(b)
			out, err := cmd.CombinedOutput()
			if err != nil || len(out) > 0 {
				t.Errorf("%s: promtool check metrics, %s: %v, %s", c.name, name, err, out)
			}
		}
		file := filepath.Join(t.TempDir(), "g.om")
		write(t, file, om.String())
		cmd := exec.Command(pt, "tsdb", "create-blocks-from", "openmetrics", file, t.TempDir())
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil || !regexp.MustCompile(`1792202658000\s+1792203255001`).Match(out) {
			t.Errorf("%s: promtool tsdb create-blocks-from openmetrics: %v\n%s", c.name, err, out)
		} else {
			t.Logf("%s: %s", c.name, bytes.TrimSpace(out))
		}
	}
}

func parse(t *testing.T, s string) float64 {
	t.Helper()
	m := parseBody(t, "# TYPE x gauge\nx "+s+"\n")
	return m["x"]
}
