package cellstat

// Tests 10 and 15 of the plan: nothing in a line, a report or the usage text names a path or the product, and the
// report over testdata/gate, the trimmed g20261015-144517, gives the numbers the lines were built to give. Test 18,
// from the review of its step 4: a window needs 2 lines, and the text report counts the lines it skipped.

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func u(x uint64) *uint64 { return &x }

// Test 10: no byte of a line, of the text report or of the JSON report holds "/", "docker", "slice" or "scope"; the
// usage text holds none of the words, nor "sys/".
func TestNoPathOrProductInOutput(t *testing.T) {
	root := devTree(t)
	write(t, filepath.Join(root, "proc/606/cgroup"), "0::/docker/"+idA+"\n")
	write(t, filepath.Join(root, "proc/607/cgroup"), "0::/system.slice/../docker-"+idB+".scope\n")
	l := &fakeLister{cs: []fc{laug(idA, "temporal-matching", 101), laug(idB, "temporal-history", 202),
		laug(idC, "temporal-ui", 303), laug(idD, "postgresql", 404), laug(idK, "temporal-frontend", 505),
		laug(strings.Repeat("a", 64), "temporal-worker", 606), laug(strings.Repeat("ab", 32), "temporal-admin-tools", 607),
		{strings.Repeat("f", 64), "not-laug", "web", 101, true}}}
	s := testSampler(root, l, true)
	var lines bytes.Buffer
	for i := 0; i < 3; i++ {
		if i == 1 {
			l.listErr = errFixed("list exit 1")
		} else {
			l.listErr = nil
		}
		if i == 2 {
			l.cs = l.cs[1:] // A leaves the list
		}
		lines.Write(append(marshal(t, s.sample()), '\n'))
	}
	file := filepath.Join(t.TempDir(), "usage.jsonl")
	write(t, file, lines.String())
	var text, js bytes.Buffer
	if err := runReport(file, window{gate: "testdata/gate"}, false, &text); err != nil {
		t.Fatal(err)
	}
	if err := runReport(file, window{gate: "testdata/gate"}, true, &js); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"lines": lines.Bytes(), "text report": text.Bytes(), "JSON report": js.Bytes()} {
		for _, w := range []string{"/", "docker", "slice", "scope"} {
			if i := bytes.Index(b, []byte(w)); i >= 0 {
				t.Errorf("%s holds %q at byte %d: %s", name, w, i, b[max(0, i-40):min(len(b), i+40)])
			}
		}
	}
	if !bytes.Contains(lines.Bytes(), []byte(`"errors":["list exit 1"]`)) || !bytes.Contains(lines.Bytes(), []byte(`"left the list"`)) {
		t.Errorf("the lines lack the error or the gone container this test means to check")
	}
	var usage bytes.Buffer
	if code := run(context.Background(), []string{"-h"}, &bytes.Buffer{}, &usage); code != 2 || !bytes.Contains(usage.Bytes(), []byte("-cli")) {
		t.Errorf("-h: exit %d, usage %q; want 2 and the flags", code, usage.String())
	}
	for _, w := range []string{"docker", "slice", "scope", "sys/"} {
		if bytes.Contains(usage.Bytes(), []byte(w)) {
			t.Errorf("the usage text holds %q: %s", w, usage.String())
		}
	}
}

type errFixed string

func (e errFixed) Error() string { return string(e) }

// reportLines builds lines inside and outside g20261015-144517's wave. Per 5 s interval: host 2 400 busy ticks of
// 12 000 and 120 steal, so 4.8 busy cores and 1% steal; s1's history 1 core, recreated after line 1 with its counter
// above the old id's, so only the id drops that interval; s1's matching 0.5 cores, 1, 1 and 6% cpu some, 1 MB/s and
// 10 writes a second; s2's matching 0.2 cores, with no io_wbytes in its last line; s1's Omes worker from line 1, 0.2
// cores, 10 ms of run-queue wait a second, 50 us a run on a CPU; cellstat 0.5% of a CPU. Core-seconds over the 15 s:
// history 10, matching 7.5, s2 3, the worker 2, so 1.5 cores of the 4.8 and 3.3 left over. gap drops matching's
// cpu_us from line 2.
func reportLines(t *testing.T, gap bool) string {
	t.Helper()
	var b bytes.Buffer
	add := func(wall, mono float64, i uint64, scale uint64) {
		l := Line{V: 1, Seq: int(i), Wall: wall, Mono: &mono, SelfCPU: u(scale * 20000 * i), ChildrenCPU: u(scale * 5000 * i)}
		l.Host = Host{VCPU: u(24), RunqWait: u(scale * 1200000000 * i), CPUSome: u(scale * 50000 * i), IOSome: u(0),
			IOFull: u(0), MemSome: u(0), MemFull: u(0), Ticks: &Ticks{User: u(1000000 + scale*2000*i), Nice: u(0),
				System: u(500000 + scale*400*i), Idle: u(50000000 + scale*9480*i), IOWait: u(0), IRQ: u(0), SoftIRQ: u(0),
				Steal: u(1000 + scale*120*i)}}
		hist := Container{Project: "laug-1dafb33b", Service: "temporal-history", ID: "a1a1a1a1a1a1", Via: "pid", State: "ok",
			CPU: u(scale * 5000000 * i)}
		if i >= 2 {
			hist.ID, hist.CPU = "a2a2a2a2a2a2", u(scale*5000000*i+1000000)
			l.Containers = append(l.Containers, Container{Project: "laug-1dafb33b", Service: "temporal-history",
				ID: "a1a1a1a1a1a1", Via: "pid", State: "gone", Why: "left the list"})
		}
		some := []uint64{0, 50000, 100000, 400000}[min(int(i), 3)] * scale
		anon := []uint64{100, 120, 110, 105}[min(int(i), 3)] << 20
		match := Container{Project: "laug-1dafb33b", Service: "temporal-matching", ID: "b1b1b1b1b1b1", Via: "pid",
			State: "ok", CPU: u(scale * 2500000 * i), CPUSome: u(some), IOWBytes: u(scale * 5000000 * i),
			IOWIOs: u(scale * 50 * i), Anon: u(anon), File: u(64 << 20), ThrottledN: u(0), OOMKill: u(0)}
		s2 := Container{Project: "laug-54cf13a9", Service: "temporal-matching", ID: "c1c1c1c1c1c1", Via: "pid",
			State: "ok", CPU: u(scale * 1000000 * i)}
		if i < 3 {
			s2.IOWBytes = u(scale * 1000000 * i)
		}
		if gap && i == 2 && scale == 1 {
			match.CPU = nil
		}
		l.Containers = append(l.Containers, hist, match, s2)
		if i >= 1 {
			l.Procs = &[]Proc{{Role: "omes-worker", RunID: runID, PID: 4242, Threads: u(2), RunNs: u(scale * 1000000000 * i),
				WaitNs: u(scale * 50000000 * i), Runs: u(scale * 1000 * i), CPUTicks: u(scale * 100 * i)}}
		}
		b.Write(append(marshal(t, l), '\n'))
	}
	add(1792075690.000, 7570.0, 1, 1000) // before the wave: 1 000 times the in-window scale; none of it may count
	for i := uint64(0); i < 4; i++ {
		add(1792075705.778+5*float64(i), 7586.47+5*float64(i), i, 1)
	}
	add(1792076010.000, 7890.0, 9, 1000)                         // after the wave end
	b.WriteString("{\"v\":1,\"wall\":17920757\n")                // a torn line: skipped and counted
	b.WriteString("{\"v\":1,\"wall\":1792075720,\"host\":{}}\n") // inside the wave with no mono: skipped and counted
	return b.String()
}

// Test 15: the report over testdata/gate.
func TestReportOverGateWave(t *testing.T) {
	file := filepath.Join(t.TempDir(), "usage.jsonl")
	write(t, file, reportLines(t, false))
	var js, text bytes.Buffer
	if err := runReport(file, window{gate: "testdata/gate"}, true, &js); err != nil {
		t.Fatal(err)
	}
	if err := runReport(file, window{gate: "testdata/gate"}, false, &text); err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(js.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, js.Bytes())
	}
	eq := func(name string, p *f3, w float64) {
		t.Helper()
		if p == nil {
			t.Errorf("%s: absent, want %v", name, w)
		} else if float64(*p) != w {
			t.Errorf("%s: got %v, want %v", name, *p, w)
		}
	}
	w := r.Window
	if w.From != 1792075700.573 || w.To != 1792076002.483 || w.Seconds != 301.91 || w.MonoSecs != 15 || w.Lines != 4 || w.Skipped != 2 ||
		w.Resets != 1 || strings.Join(w.Items, " ") != "s1 s2 s3" {
		t.Errorf("window %+v, want 1792075700.573 to 1792076002.483, 301.91 s, 4 lines over 15 s mono, 2 skipped, 1 reset, items s1 s2 s3", w)
	}
	rows := map[string]itemRow{}
	for _, x := range r.Items {
		rows[x.Item+" "+x.Service] = x
	}
	if len(rows) != 3 {
		t.Fatalf("item rows %v, want 3", rows)
	}
	h, m, s2 := rows["s1 temporal-history"], rows["s1 temporal-matching"], rows["s2 temporal-matching"]
	eq("history cores", h.Cores, 1)
	if h.Resets != 1 {
		t.Errorf("history resets %d, want 1 for the recreate", h.Resets)
	}
	eq("matching cores", m.Cores, 0.5)
	eq("matching cpu some %", m.CPUSome, 2.667)
	eq("matching cpu some highest 5 s %", m.CPUSomeMax, 6)
	eq("matching write MB/s", m.WriteMBps, 1)
	eq("matching write IOPS", m.WriteIOPS, 10)
	eq("matching anon MiB", m.AnonMiB, 120)
	eq("matching file MiB", m.FileMiB, 64)
	eq("matching throttled", m.Throttled, 0)
	eq("s2 matching cores", s2.Cores, 0.2)
	if s2.WriteMBps != nil || m.ReadMBps != nil {
		t.Errorf("a key missing at an end must be null: s2 write %v, s1 read %v", s2.WriteMBps, m.ReadMBps)
	}
	if len(r.Omes) != 1 || r.Omes[0].Item != "s1" {
		t.Fatalf("omes rows %+v, want one for s1", r.Omes)
	}
	o := r.Omes[0]
	eq("omes cores", o.Cores, 0.2)
	eq("omes wait ms per s", o.WaitMsPerS, 10)
	eq("omes wait us per run", o.WaitUsPerRun, 50)
	eq("omes run against ticks %", o.RunVsTicksPct, 100)
	eq("omes threads", o.Threads, 2)
	eq("host vcpu", r.Host.VCPU, 24)
	eq("host busy cores", r.Host.BusyCores, 4.8)
	eq("host steal %", r.Host.StealPct, 1)
	eq("host run-queue wait ms per s per CPU", r.Host.RunqMsPerSPerCPU, 10)
	eq("host cpu some %", r.Host.CPUSomePct, 1)
	eq("cores left over", r.Host.LeftOverCores, 3.3)
	eq("cellstat %", r.Cellstat.CPUPct, 0.5)
	for _, x := range append(r.Items, itemRow{}) {
		for _, p := range []*f3{x.Cores, x.CPUSome, x.WriteMBps} {
			if p != nil && *p < 0 {
				t.Errorf("negative rate in %+v", x)
			}
		}
	}

	txt := text.String()
	if !strings.Contains(txt, "301.9 s") {
		t.Errorf("text has no 301.9 s window:\n%s", txt)
	}
	for _, line := range strings.Split(strings.TrimSpace(txt), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 0 || (f[0] != "item" && f[0] != "omes" && f[0] != "host" && f[0] != "cellstat"):
			t.Errorf("text line starts with neither item, omes, host nor cellstat: %q", line)
		case f[0] == "item" && f[1] == "s2":
			if f[11] != "-" || f[3] != "0.200" {
				t.Errorf("s2 row: wMBps %q, cores %q; want - and 0.200: %q", f[11], f[3], line)
			}
		}
	}

	// A row with a line lacking its cpu counter: its own cores still show, the cores left over do not.
	write(t, file, reportLines(t, true))
	js.Reset()
	if err := runReport(file, window{gate: "testdata/gate"}, true, &js); err != nil {
		t.Fatal(err)
	}
	r = report{}
	if err := json.Unmarshal(js.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Host.BusyCores == nil || r.Host.LeftOverCores != nil {
		t.Errorf("with a gap: busy %v, left over %v; want busy and no left over", r.Host.BusyCores, r.Host.LeftOverCores)
	}
}

// Test 18: a window of 1 line, or of none, exits 1 with the count on stderr and nothing on stdout, text and JSON
// alike. The text report over the wave has a line for the 2 lines of the file it skipped; a file with none has none.
func TestReportNeedsTwoLines(t *testing.T) {
	file := filepath.Join(t.TempDir(), "usage.jsonl")
	write(t, file, reportLines(t, false))
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	write(t, empty, "")
	for _, c := range []struct {
		args []string
		want string
	}{
		// 1792075705.778 is the first line inside the wave, and the only one up to 1792075706.
		{[]string{"-report", file, "-from", "1792075705", "-to", "1792075706"},
			"cellstat: report: 1 line in the window; a rate needs 2 (2 lines of the file skipped: not JSON, or no mono)\n"},
		{[]string{"-report", empty}, "cellstat: report: 0 lines in the window; a rate needs 2\n"},
	} {
		for _, form := range [][]string{nil, {"-json"}} {
			args := append(append([]string{}, c.args...), form...)
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), args, &stdout, &stderr)
			if code != 1 || stdout.Len() > 0 || !strings.HasSuffix(stderr.String(), " "+c.want) {
				t.Errorf("%v: exit %d, %d bytes out, stderr %q; want 1, none and %q", args[2:], code, stdout.Len(), stderr.String(), c.want)
			}
		}
	}

	var text bytes.Buffer
	if err := runReport(file, window{gate: "testdata/gate"}, false, &text); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(text.String(), "\n"); lines[1] != "cellstat 2 lines of the file skipped: not JSON, or no mono" {
		t.Errorf("text report, second line %q; want the skipped count:\n%s", lines[1], text.String())
	}
	clean := strings.Join(strings.Split(reportLines(t, false), "\n")[:6], "\n") + "\n" // the torn and the mono-less lines cut
	write(t, file, clean)
	text.Reset()
	if err := runReport(file, window{gate: "testdata/gate"}, false, &text); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text.String(), "skipped") {
		t.Errorf("text report with none skipped names skipped lines:\n%s", text.String())
	}
}
