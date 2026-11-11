package cellstat

// -report: the lines inside a window become one row per item and service, one per Omes process, one for the host
// and one for cellstat. The window is a gate's wave (timeline.jsonl, first wave to first wave end), -from and -to,
// or the whole file. Rates use mono, interval by interval: an interval where a row's container id changed or one of
// its counters fell is dropped and counted as a reset, so no rate is negative. A key missing at the row's first or
// last line prints "-", never 0. A window with fewer than 2 lines is an error, text and JSON alike: a rate needs 2.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// window is what -report was given: a gate dir, or a wall range with either end open.
type window struct {
	gate     string
	from, to *float64
}

// gauges are readings that are levels, not counters: the report shows their highest value, never a rate.
var gauges = map[string]bool{"cpu_max_us": true, "cpu_period_us": true, "mem_max_b": true, "anon_b": true,
	"file_b": true, "io_devs": true, "threads": true, "vcpu": true}

// point is one row's numbers in one line. id is the container id or the Omes pid.
type point struct {
	mono float64
	id   string
	v    map[string]float64
}

// stat is one row walked interval by interval.
type stat struct {
	first, last          map[string]float64
	sum, dur, peak, high map[string]float64
	resets               int
}

func walk(ps []point) stat {
	s := stat{sum: map[string]float64{}, dur: map[string]float64{}, peak: map[string]float64{}, high: map[string]float64{}}
	if len(ps) == 0 {
		return s
	}
	s.first, s.last = ps[0].v, ps[len(ps)-1].v
	for _, p := range ps {
		for k, x := range p.v {
			if h, ok := s.high[k]; gauges[k] && (!ok || x > h) {
				s.high[k] = x
			}
		}
	}
	for i := 1; i < len(ps); i++ {
		a, b := ps[i-1], ps[i]
		dt := b.mono - a.mono
		if dt <= 0 {
			continue
		}
		fell := a.id != b.id
		for k, y := range b.v {
			if x, ok := a.v[k]; ok && !gauges[k] && y < x {
				fell = true
			}
		}
		if fell {
			s.resets++
			continue
		}
		for k, y := range b.v {
			x, ok := a.v[k]
			if !ok || gauges[k] {
				continue
			}
			s.sum[k] += y - x
			s.dur[k] += dt
			if r := (y - x) / dt; r > s.peak[k] {
				s.peak[k] = r
			}
		}
	}
	return s
}

// shown says a counter can be reported: present at the row's first and last line, over some kept interval.
func (s stat) shown(k string) bool {
	_, a := s.first[k]
	_, b := s.last[k]
	return a && b && s.dur[k] > 0
}

// rate is a counter's mean per second times scale.
func (s stat) rate(k string, scale float64) *f3 {
	if !s.shown(k) {
		return nil
	}
	return ptr(s.sum[k] / s.dur[k] * scale)
}

// peakRate is the highest one-interval rate times scale: "highest 5 s" at the default interval.
func (s stat) peakRate(k string, scale float64) *f3 {
	if !s.shown(k) {
		return nil
	}
	return ptr(s.peak[k] * scale)
}

// change is a counter's total change over the kept intervals.
func (s stat) change(k string) *f3 {
	if !s.shown(k) {
		return nil
	}
	return ptr(s.sum[k])
}

// ratio is the change of a over the change of b, times scale.
func (s stat) ratio(a, b string, scale float64) *f3 {
	if !s.shown(a) || !s.shown(b) || s.sum[b] == 0 {
		return nil
	}
	return ptr(s.sum[a] / s.sum[b] * scale)
}

// highest is a gauge's highest value times scale.
func (s stat) highest(k string, scale float64) *f3 {
	h, ok := s.high[k]
	if !ok {
		return nil
	}
	return ptr(h * scale)
}

func ptr(x float64) *f3 { v := f3(x); return &v }

// f3 is a report number. JSON keeps 3 decimals, so it reads like the text.
type f3 float64

func (x f3) MarshalJSON() ([]byte, error) {
	return strconv.AppendFloat(nil, math.Round(float64(x)*1000)/1000, 'f', -1, 64), nil
}

// report is -report's output; -json writes it as is.
type report struct {
	Window   reportWindow `json:"window"`
	Items    []itemRow    `json:"items"`
	Omes     []omesRow    `json:"omes"`
	Host     hostRow      `json:"host"`
	Cellstat cellstatRow  `json:"cellstat"`
}

type reportWindow struct {
	From     float64  `json:"from"` // wall seconds
	To       float64  `json:"to"`
	Seconds  f3       `json:"seconds"`      // to minus from, on the wall clock
	MonoSecs f3       `json:"mono_seconds"` // first to last line inside, on mono: the dev host's wall clock steps (seen)
	Lines    int      `json:"lines"`        // inside the window
	Skipped  int      `json:"skipped"`      // lines in the file that did not parse or carry no mono
	Resets   int      `json:"resets"`
	Items    []string `json:"items,omitempty"` // the gate's item keys
}

type itemRow struct {
	Item       string `json:"item"` // the gate's item key, else the project
	Project    string `json:"project"`
	Service    string `json:"service"`
	Cores      *f3    `json:"cores"`
	CPUSome    *f3    `json:"cpu_some_pct"`
	CPUSomeMax *f3    `json:"cpu_some_max5s_pct"`
	CPUFull    *f3    `json:"cpu_full_pct"`
	IOSome     *f3    `json:"io_some_pct"`
	IOSomeMax  *f3    `json:"io_some_max5s_pct"`
	ReadMBps   *f3    `json:"read_mbps"`
	WriteMBps  *f3    `json:"write_mbps"`
	WriteIOPS  *f3    `json:"write_iops"`
	AnonMiB    *f3    `json:"anon_mib"` // highest in the window
	FileMiB    *f3    `json:"file_mib"`
	Throttled  *f3    `json:"throttled"` // change in throttled periods
	OOMKill    *f3    `json:"oom_kill"`  // change
	Resets     int    `json:"resets"`
}

type omesRow struct {
	Item          string `json:"item"` // the gate's item key, else the run id
	Role          string `json:"role"`
	RunID         string `json:"run_id"`
	PID           int    `json:"pid"`
	Cores         *f3    `json:"cores"`
	WaitMsPerS    *f3    `json:"wait_ms_per_s"`
	WaitUsPerRun  *f3    `json:"wait_us_per_run"` // per time on a CPU
	Threads       *f3    `json:"threads"`         // highest
	RunVsTicksPct *f3    `json:"run_vs_ticks_pct"`
	Resets        int    `json:"resets"`
}

type hostRow struct {
	VCPU             *f3 `json:"vcpu"`
	BusyCores        *f3 `json:"busy_cores"`
	StealPct         *f3 `json:"steal_pct"`
	IOWaitPct        *f3 `json:"iowait_pct"`
	RunqMsPerSPerCPU *f3 `json:"runq_ms_per_s_per_cpu"`
	CPUSomePct       *f3 `json:"cpu_some_pct"`
	IOSomePct        *f3 `json:"io_some_pct"`
	MemSomePct       *f3 `json:"mem_some_pct"`
	LeftOverCores    *f3 `json:"left_over_cores"` // host busy minus every row's core-seconds over the host's span
}

type cellstatRow struct {
	CPUPct *f3 `json:"cpu_pct_of_one"` // itself and the docker calls it ran
}

// rawLine decodes a line with every reading as a float64, so the report finds counters by their line keys.
type rawLine struct {
	Wall       float64          `json:"wall"`
	Mono       *float64         `json:"mono"`
	Self       *float64         `json:"self_cpu_us"`
	Children   *float64         `json:"children_cpu_us"`
	Host       map[string]any   `json:"host"`
	Containers []map[string]any `json:"containers"`
	Procs      []map[string]any `json:"procs"`
}

// numbers keeps an object's numeric keys; ticks' keys come up as ticks_<name>.
func numbers(o map[string]any, skip ...string) map[string]float64 {
	v := map[string]float64{}
	for k, x := range o {
		switch x := x.(type) {
		case float64:
			v[k] = x
		case map[string]any:
			for k2, x2 := range numbers(x) {
				v[k+"_"+k2] = x2
			}
		}
	}
	for _, k := range skip {
		delete(v, k)
	}
	return v
}

func str(o map[string]any, k string) string { s, _ := o[k].(string); return s }

const ticksPerSec = 100 // USER_HZ (seen: getconf CLK_TCK 100 on the dev host and the lab host)

// skippedLines counts the lines of the whole file the report could not use: such a line has no place in a window.
func skippedLines(n int) string {
	s := "s"
	if n == 1 {
		s = ""
	}
	return fmt.Sprintf("%d line%s of the file skipped: not JSON, or no mono", n, s)
}

// readGate takes the window from timeline.jsonl's first wave and wave end, the item keys from verdict.json and each
// item's project from <key>/lease.json (all seen on g20261015-144517).
func readGate(dir string) (from, to float64, keys []string, projects map[string]string, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "timeline.jsonl"))
	if err != nil {
		return 0, 0, nil, nil, errors.New("the gate dir has no timeline.jsonl")
	}
	var gotFrom, gotTo bool
	for _, l := range bytes.Split(b, []byte("\n")) {
		var e struct {
			Step string  `json:"step"`
			Wall float64 `json:"wall"`
		}
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		if e.Step == "wave" && !gotFrom {
			from, gotFrom = e.Wall, true
		}
		if e.Step == "wave end" && !gotTo {
			to, gotTo = e.Wall, true
		}
	}
	if !gotFrom || !gotTo {
		return 0, 0, nil, nil, errors.New("timeline.jsonl has no wave and wave end")
	}
	var v struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	if b, err = os.ReadFile(filepath.Join(dir, "verdict.json")); err != nil || json.Unmarshal(b, &v) != nil {
		return 0, 0, nil, nil, errors.New("the gate dir has no readable verdict.json")
	}
	projects = map[string]string{}
	for _, it := range v.Items {
		keys = append(keys, it.Key)
		var l struct {
			Project string `json:"project"`
		}
		if b, err := os.ReadFile(filepath.Join(dir, it.Key, "lease.json")); err == nil && json.Unmarshal(b, &l) == nil && l.Project != "" {
			projects[l.Project] = it.Key
		}
	}
	return from, to, keys, projects, nil
}

// Report is -report as bin/obs usage report runs it: the lines in file over gate's wave, a gate result dir, or over the
// whole file when gate is empty, as text or JSON. The error says what could not be read, or that the window holds
// fewer than 2 lines, and names no path.
func Report(file, gate string, asJSON bool, out io.Writer) error {
	return runReport(file, window{gate: gate}, asJSON, out)
}

// runReport reads the file and the window and writes the report.
func runReport(file string, w window, asJSON bool, out io.Writer) error {
	var keys []string
	projects := map[string]string{}
	if w.gate != "" {
		from, to, k, p, err := readGate(w.gate)
		if err != nil {
			return err
		}
		w.from, w.to, keys, projects = &from, &to, k, p
	}
	f, err := os.Open(file)
	if err != nil {
		return errors.New("the file of lines cannot be read")
	}
	defer f.Close()
	var lines []rawLine
	skipped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20) // about 20 KB a line at 33 containers
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var l rawLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Mono == nil {
			skipped++
			continue
		}
		lines = append(lines, l)
	}
	if sc.Err() != nil {
		return errors.New("the file of lines cannot be read to the end")
	}
	r := summarize(lines, w, keys, projects)
	r.Window.Skipped = skipped
	if n := r.Window.Lines; n < 2 {
		s, why := "s", ""
		if n == 1 {
			s = ""
		}
		if skipped > 0 {
			why = " (" + skippedLines(skipped) + ")"
		}
		return fmt.Errorf("%d line%s in the window; a rate needs 2%s", n, s, why)
	}
	if asJSON {
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		_, err = out.Write(append(b, '\n'))
		return err
	}
	return writeText(out, r)
}

// summarize turns the lines inside the window into the report.
func summarize(all []rawLine, w window, keys []string, projects map[string]string) report {
	var lines []rawLine
	for _, l := range all {
		if (w.from == nil || l.Wall >= *w.from) && (w.to == nil || l.Wall <= *w.to) {
			lines = append(lines, l)
		}
	}
	r := report{Items: []itemRow{}, Omes: []omesRow{}}
	r.Window.Lines, r.Window.Items = len(lines), keys
	switch {
	case w.from != nil:
		r.Window.From = *w.from
	case len(lines) > 0:
		r.Window.From = lines[0].Wall
	}
	switch {
	case w.to != nil:
		r.Window.To = *w.to
	case len(lines) > 0:
		r.Window.To = lines[len(lines)-1].Wall
	}
	r.Window.Seconds = f3(r.Window.To - r.Window.From)
	if len(lines) > 0 {
		r.Window.MonoSecs = f3(*lines[len(lines)-1].Mono - *lines[0].Mono)
	}

	type cKey struct{ project, service string }
	type oKey struct {
		role, runID string
		pid         int
	}
	cs, om := map[cKey][]point{}, map[oKey][]point{}
	var host, self []point
	for _, l := range lines {
		taken := map[cKey]bool{} // one container a service: the first ok one in the line's order
		for _, c := range l.Containers {
			k := cKey{str(c, "project"), str(c, "service")}
			if str(c, "state") != "ok" || taken[k] {
				continue
			}
			taken[k] = true
			cs[k] = append(cs[k], point{*l.Mono, str(c, "id"), numbers(c)})
		}
		for _, p := range l.Procs {
			pid, _ := p["pid"].(float64)
			k := oKey{str(p, "role"), str(p, "run_id"), int(pid)}
			om[k] = append(om[k], point{*l.Mono, "", numbers(p, "pid")})
		}
		hv := numbers(l.Host)
		if busy, total, ok := busyTicks(hv); ok {
			hv["busy"], hv["total"] = busy, total
		}
		host = append(host, point{*l.Mono, "", hv})
		sv := map[string]float64{}
		if l.Self != nil && l.Children != nil {
			sv["cpu"] = *l.Self + *l.Children
		}
		self = append(self, point{*l.Mono, "", sv})
	}

	// Left over is host busy minus each row's core-seconds over the host's span, not minus its mean: a row present
	// for part of the window counts for that part. A row with a line that lacks its counter leaves left over absent.
	var used float64
	allRead := true
	use := func(s stat, ps []point, k string, scale float64) {
		for _, p := range ps {
			if _, ok := p.v[k]; !ok {
				allRead = false
			}
		}
		used += s.sum[k] * scale
	}
	for k, ps := range cs {
		s := walk(ps)
		item := k.project
		if key, ok := projects[k.project]; ok {
			item = key
		}
		row := itemRow{Item: item, Project: k.project, Service: k.service,
			Cores: s.rate("cpu_us", 1e-6), CPUSome: s.rate("cpu_some_us", 1e-4), CPUSomeMax: s.peakRate("cpu_some_us", 1e-4),
			CPUFull: s.rate("cpu_full_us", 1e-4), IOSome: s.rate("io_some_us", 1e-4), IOSomeMax: s.peakRate("io_some_us", 1e-4),
			ReadMBps: s.rate("io_rbytes", 1e-6), WriteMBps: s.rate("io_wbytes", 1e-6), WriteIOPS: s.rate("io_wios", 1),
			AnonMiB: s.highest("anon_b", 1.0/(1<<20)), FileMiB: s.highest("file_b", 1.0/(1<<20)),
			Throttled: s.change("throttled_n"), OOMKill: s.change("oom_kill"), Resets: s.resets}
		use(s, ps, "cpu_us", 1e-6)
		r.Window.Resets += s.resets
		r.Items = append(r.Items, row)
	}
	for k, ps := range om {
		s := walk(ps)
		item := k.runID
		for _, key := range keys {
			if strings.HasSuffix(k.runID, "-"+key) {
				item = key
				break
			}
		}
		row := omesRow{Item: item, Role: k.role, RunID: k.runID, PID: k.pid,
			Cores: s.rate("run_ns", 1e-9), WaitMsPerS: s.rate("wait_ns", 1e-6), WaitUsPerRun: s.ratio("wait_ns", "runs", 1e-3),
			Threads: s.highest("threads", 1), RunVsTicksPct: s.ratio("run_ns", "cpu_ticks", 100/(1e9/ticksPerSec)), Resets: s.resets}
		use(s, ps, "run_ns", 1e-9)
		r.Window.Resets += s.resets
		r.Omes = append(r.Omes, row)
	}
	sort.Slice(r.Items, func(i, j int) bool {
		a, b := r.Items[i], r.Items[j]
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		return a.Service < b.Service
	})
	sort.Slice(r.Omes, func(i, j int) bool {
		a, b := r.Omes[i], r.Omes[j]
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.PID < b.PID
	})

	h := walk(host)
	r.Host = hostRow{VCPU: h.highest("vcpu", 1), StealPct: h.ratio("ticks_steal", "total", 100),
		IOWaitPct: h.ratio("ticks_iowait", "total", 100), CPUSomePct: h.rate("cpu_some_us", 1e-4),
		IOSomePct: h.rate("io_some_us", 1e-4), MemSomePct: h.rate("mem_some_us", 1e-4)}
	if share := h.ratio("busy", "total", 1); share != nil && r.Host.VCPU != nil {
		r.Host.BusyCores = ptr(float64(*share * *r.Host.VCPU))
		if allRead {
			r.Host.LeftOverCores = ptr(float64(*r.Host.BusyCores) - used/h.dur["busy"])
		}
	}
	if q := h.rate("runq_wait_ns", 1e-6); q != nil && r.Host.VCPU != nil && *r.Host.VCPU > 0 {
		r.Host.RunqMsPerSPerCPU = ptr(float64(*q / *r.Host.VCPU))
	}
	r.Cellstat.CPUPct = walk(self).rate("cpu", 1e-4)
	r.Window.Resets += h.resets
	return r
}

// busyTicks adds a line's ticks: busy is user, nice, system, irq and softirq; total adds idle, iowait and steal.
func busyTicks(v map[string]float64) (busy, total float64, ok bool) {
	for _, k := range []string{"user", "nice", "system", "irq", "softirq", "idle", "iowait", "steal"} {
		x, has := v["ticks_"+k]
		if !has {
			return 0, 0, false
		}
		if k != "idle" && k != "iowait" && k != "steal" {
			busy += x
		}
		total += x
	}
	return busy, total, true
}

// writeText writes the report as aligned columns. Every line starts item, omes, host or cellstat, the skipped count's
// line too.
func writeText(w io.Writer, r report) error {
	f := func(p *f3, prec int) string {
		if p == nil {
			return "-"
		}
		return strconv.FormatFloat(float64(*p), 'f', prec, 64)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	items := ""
	if len(r.Window.Items) > 0 {
		items = ", items " + strings.Join(r.Window.Items, " ")
	}
	fmt.Fprintf(tw, "cellstat window %.3f to %.3f wall, %.1f s, %d lines over %.1f s mono, %d resets%s\n",
		r.Window.From, r.Window.To, r.Window.Seconds, r.Window.Lines, r.Window.MonoSecs, r.Window.Resets, items)
	if r.Window.Skipped > 0 {
		fmt.Fprintln(tw, "cellstat "+skippedLines(r.Window.Skipped))
	}
	fmt.Fprintln(tw, "item\tkey\tservice\tcores\tcpu_some%\tmax5s%\tcpu_full%\tio_some%\tmax5s%\trMBps\twMBps\twIOPS\tanon_MiB\tfile_MiB\tthrottled\toom_kill\tresets")
	for _, x := range r.Items {
		fmt.Fprintf(tw, "item\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", x.Item, x.Service,
			f(x.Cores, 3), f(x.CPUSome, 2), f(x.CPUSomeMax, 2), f(x.CPUFull, 2), f(x.IOSome, 2), f(x.IOSomeMax, 2),
			f(x.ReadMBps, 2), f(x.WriteMBps, 2), f(x.WriteIOPS, 1), f(x.AnonMiB, 1), f(x.FileMiB, 1),
			f(x.Throttled, 0), f(x.OOMKill, 0), x.Resets)
	}
	tw.Flush()
	fmt.Fprintln(tw, "omes\tkey\trole\tpid\tcores\twait_ms_per_s\twait_us_per_run\tthreads\trun_vs_ticks%\tresets")
	for _, x := range r.Omes {
		fmt.Fprintf(tw, "omes\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%d\n", x.Item, x.Role, x.PID, f(x.Cores, 3),
			f(x.WaitMsPerS, 2), f(x.WaitUsPerRun, 2), f(x.Threads, 0), f(x.RunVsTicksPct, 1), x.Resets)
	}
	tw.Flush()
	h := r.Host
	fmt.Fprintln(tw, "host\tvcpu\tbusy_cores\tsteal%\tiowait%\trunq_ms_per_s_per_cpu\tcpu_some%\tio_some%\tmem_some%\tleft_over_cores")
	fmt.Fprintf(tw, "host\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", f(h.VCPU, 0), f(h.BusyCores, 2), f(h.StealPct, 2),
		f(h.IOWaitPct, 2), f(h.RunqMsPerSPerCPU, 2), f(h.CPUSomePct, 2), f(h.IOSomePct, 2), f(h.MemSomePct, 2),
		f(h.LeftOverCores, 2))
	tw.Flush()
	pct := "-"
	if r.Cellstat.CPUPct != nil {
		pct = f(r.Cellstat.CPUPct, 2) + "%"
	}
	_, err := fmt.Fprintf(w, "cellstat cpu %s of one CPU, itself and the calls it ran\n", pct)
	return err
}
