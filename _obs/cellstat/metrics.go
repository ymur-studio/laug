package cellstat

// The listener's exposition: one line, the last sample, as Prometheus text format 0.0.4 (the exposition
// format docs). The families mirror the line's keys in seconds and bytes, with _total on counters: laug_cell_* per
// container (labels project and service), laug_host_*, laug_proc_* per Omes process and per -pids process (labels
// role, run_id for Omes, and pid), and laug_cellstat_*, cellstat's own. Only an ok container is rendered, the first
// of a project and service in the line's order (a recreate is then a counter reset of the same series, as the report
// reads it). A reading the line leaves out has no sample: absent, never 0. Ticks are USER_HZ, 100 a second (seen with
// getconf CLK_TCK on the dev host; 100 on x86 by its asm/param.h, and on most architectures by proc(5)).

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
)

const userHZ = 100

type expSample struct {
	labels string
	value  float64
}

type expFamily struct {
	name, typ, help string
	samples         []expSample
}

// exposition collects families in the order they are first named, and writes each with its HELP and TYPE once it
// has a sample.
type exposition struct {
	fams  []*expFamily
	index map[string]*expFamily
}

// add adds a counter or gauge read as a whole number in the kernel's unit, divided by div into seconds or bytes: a
// division by the power of ten gives the double nearest the decimal, so 1219699 us renders 1.219699.
func (e *exposition) add(name, typ, help, labels string, v *uint64, div float64) {
	if v == nil {
		return
	}
	e.addFloat(name, typ, help, labels, float64(*v)/div)
}

func (e *exposition) addFloat(name, typ, help, labels string, v float64) {
	if e.index == nil {
		e.index = map[string]*expFamily{}
	}
	f := e.index[name]
	if f == nil {
		f = &expFamily{name: name, typ: typ, help: help}
		e.index[name] = f
		e.fams = append(e.fams, f)
	}
	f.samples = append(f.samples, expSample{labels, v})
}

func (e *exposition) bytes() []byte {
	var b bytes.Buffer
	for _, f := range e.fams {
		b.WriteString("# HELP " + f.name + " " + f.help + "\n# TYPE " + f.name + " " + f.typ + "\n")
		for _, s := range f.samples {
			b.WriteString(f.name + s.labels + " " + strconv.FormatFloat(s.value, 'f', -1, 64) + "\n")
		}
	}
	return b.Bytes()
}

// labelSet renders name="value" pairs in the order given, the value escaped as the text format asks.
func labelSet(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
		parts = append(parts, kv[i]+`="`+v+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// cellFamilies is laug_cell_*: each container key, its family, and the scale to seconds or bytes.
var cellFamilies = []struct {
	name, typ, help string
	get             func(*Container) *uint64
	div             float64
}{
	{"laug_cell_cpu_seconds_total", "counter", "CPU time of the container's group, cpu.stat usage_usec.", func(c *Container) *uint64 { return c.CPU }, 1e6},
	{"laug_cell_cpu_user_seconds_total", "counter", "User CPU time, cpu.stat user_usec.", func(c *Container) *uint64 { return c.CPUUser }, 1e6},
	{"laug_cell_cpu_system_seconds_total", "counter", "System CPU time, cpu.stat system_usec.", func(c *Container) *uint64 { return c.CPUSystem }, 1e6},
	{"laug_cell_cpu_throttled_periods_total", "counter", "Periods throttled by cpu.max, cpu.stat nr_throttled.", func(c *Container) *uint64 { return c.ThrottledN }, 1},
	{"laug_cell_cpu_throttled_seconds_total", "counter", "Time throttled by cpu.max, cpu.stat throttled_usec.", func(c *Container) *uint64 { return c.ThrottledUs }, 1e6},
	{"laug_cell_cpu_max_seconds", "gauge", "The cpu.max quota a period; absent when it is max.", func(c *Container) *uint64 { return c.CPUMax }, 1e6},
	{"laug_cell_cpu_period_seconds", "gauge", "The cpu.max period, beside a quota.", func(c *Container) *uint64 { return c.CPUPeriod }, 1e6},
	{"laug_cell_memory_max_bytes", "gauge", "memory.max; absent when it is max.", func(c *Container) *uint64 { return c.MemMax }, 1},
	{"laug_cell_memory_anon_bytes", "gauge", "memory.stat anon.", func(c *Container) *uint64 { return c.Anon }, 1},
	{"laug_cell_memory_file_bytes", "gauge", "memory.stat file, the page cache charged to the group.", func(c *Container) *uint64 { return c.File }, 1},
	{"laug_cell_oom_kills_total", "counter", "memory.events oom_kill.", func(c *Container) *uint64 { return c.OOMKill }, 1},
	{"laug_cell_io_devices", "gauge", "Devices listed in io.stat.", func(c *Container) *uint64 { return c.IODevs }, 1},
	{"laug_cell_io_read_bytes_total", "counter", "io.stat rbytes, summed over devices.", func(c *Container) *uint64 { return c.IORBytes }, 1},
	{"laug_cell_io_write_bytes_total", "counter", "io.stat wbytes, summed over devices.", func(c *Container) *uint64 { return c.IOWBytes }, 1},
	{"laug_cell_io_reads_total", "counter", "io.stat rios, summed over devices.", func(c *Container) *uint64 { return c.IORIOs }, 1},
	{"laug_cell_io_writes_total", "counter", "io.stat wios, summed over devices.", func(c *Container) *uint64 { return c.IOWIOs }, 1},
	{"laug_cell_cpu_some_seconds_total", "counter", "cpu.pressure some total.", func(c *Container) *uint64 { return c.CPUSome }, 1e6},
	{"laug_cell_cpu_full_seconds_total", "counter", "cpu.pressure full total.", func(c *Container) *uint64 { return c.CPUFull }, 1e6},
	{"laug_cell_io_some_seconds_total", "counter", "io.pressure some total.", func(c *Container) *uint64 { return c.IOSome }, 1e6},
	{"laug_cell_io_full_seconds_total", "counter", "io.pressure full total.", func(c *Container) *uint64 { return c.IOFull }, 1e6},
	{"laug_cell_memory_some_seconds_total", "counter", "memory.pressure some total.", func(c *Container) *uint64 { return c.MemSome }, 1e6},
	{"laug_cell_memory_full_seconds_total", "counter", "memory.pressure full total.", func(c *Container) *uint64 { return c.MemFull }, 1e6},
}

// procFamilies is laug_proc_*, for an Omes process and a -pids one alike.
var procFamilies = []struct {
	name, typ, help string
	get             func(*Proc) *uint64
	div             float64
}{
	{"laug_proc_cpu_seconds_total", "counter", "utime plus stime of the whole process, exited threads included.", func(p *Proc) *uint64 { return p.CPUTicks }, userHZ},
	{"laug_proc_run_seconds_total", "counter", "Time on a CPU, summed over its live threads.", func(p *Proc) *uint64 { return p.RunNs }, 1e9},
	{"laug_proc_wait_seconds_total", "counter", "Time runnable and waiting on a run queue, summed over its live threads.", func(p *Proc) *uint64 { return p.WaitNs }, 1e9},
	{"laug_proc_runs_total", "counter", "Times on a CPU, summed over its live threads.", func(p *Proc) *uint64 { return p.Runs }, 1},
	{"laug_proc_threads", "gauge", "Threads whose schedstat was read.", func(p *Proc) *uint64 { return p.Threads }, 1},
	{"laug_proc_rss_bytes", "gauge", "VmRSS.", func(p *Proc) *uint64 { return p.RSS }, 1},
	{"laug_proc_rss_peak_bytes", "gauge", "VmHWM, the highest VmRSS.", func(p *Proc) *uint64 { return p.RSSPeak }, 1},
	{"laug_proc_io_read_bytes_total", "counter", "read_bytes of its io file.", func(p *Proc) *uint64 { return p.IORBytes }, 1},
	{"laug_proc_io_write_bytes_total", "counter", "write_bytes of its io file.", func(p *Proc) *uint64 { return p.IOWBytes }, 1},
}

// renderMetrics turns one line into the listener's body.
func renderMetrics(l Line) []byte {
	var e exposition
	var cs []*Container
	taken := map[string]bool{}
	states := map[string]uint64{}
	for i := range l.Containers {
		c := &l.Containers[i]
		states[c.State]++
		k := c.Project + "\x00" + c.Service
		if c.State != "ok" || taken[k] {
			continue
		}
		taken[k] = true
		cs = append(cs, c)
	}
	for _, f := range cellFamilies {
		for _, c := range cs {
			e.add(f.name, f.typ, f.help, labelSet("project", c.Project, "service", c.Service), f.get(c), f.div)
		}
	}

	h := l.Host
	e.add("laug_host_vcpu", "gauge", "cpuN lines in the host's stat file.", "", h.VCPU, 1)
	if t := h.Ticks; t != nil {
		for _, m := range []struct {
			mode string
			v    *uint64
		}{{"user", t.User}, {"nice", t.Nice}, {"system", t.System}, {"idle", t.Idle}, {"iowait", t.IOWait},
			{"irq", t.IRQ}, {"softirq", t.SoftIRQ}, {"steal", t.Steal}} {
			e.add("laug_host_cpu_seconds_total", "counter", "The host's CPU time by mode, summed over CPUs.", labelSet("mode", m.mode), m.v, userHZ)
		}
	}
	e.add("laug_host_runq_wait_seconds_total", "counter", "Time tasks waited on a run queue, summed over CPUs (schedstat).", "", h.RunqWait, 1e9)
	e.add("laug_host_cpu_some_seconds_total", "counter", "The host's cpu pressure some total.", "", h.CPUSome, 1e6)
	e.add("laug_host_io_some_seconds_total", "counter", "The host's io pressure some total.", "", h.IOSome, 1e6)
	e.add("laug_host_io_full_seconds_total", "counter", "The host's io pressure full total.", "", h.IOFull, 1e6)
	e.add("laug_host_memory_some_seconds_total", "counter", "The host's memory pressure some total.", "", h.MemSome, 1e6)
	e.add("laug_host_memory_full_seconds_total", "counter", "The host's memory pressure full total.", "", h.MemFull, 1e6)

	var ps []*Proc
	if l.Procs != nil {
		for i := range *l.Procs {
			ps = append(ps, &(*l.Procs)[i])
		}
	}
	if l.Pids != nil {
		for i := range *l.Pids {
			ps = append(ps, &(*l.Pids)[i])
		}
	}
	for _, f := range procFamilies {
		for _, p := range ps {
			e.add(f.name, f.typ, f.help, labelSet("role", p.Role, "run_id", p.RunID, "pid", strconv.Itoa(p.PID)), f.get(p), f.div)
		}
	}

	seq := uint64(l.Seq + 1)
	e.add("laug_cellstat_samples_total", "counter", "Samples taken since cellstat started; this body is the last.", "", &seq, 1)
	e.addFloat("laug_cellstat_sample_time_seconds", "gauge", "The last sample's wall clock, Unix seconds.", "", l.Wall)
	e.add("laug_cellstat_cpu_seconds_total", "counter", "cellstat's own CPU time.", "", l.SelfCPU, 1e6)
	e.add("laug_cellstat_children_cpu_seconds_total", "counter", "The CPU time of the container CLI calls it waited for.", "", l.ChildrenCPU, 1e6)
	others := uint64(l.Others)
	e.add("laug_cellstat_other_containers", "gauge", "Containers of other compose projects in the last list, never named.", "", &others, 1)
	if l.OtherProcs != nil {
		n := uint64(*l.OtherProcs)
		e.add("laug_cellstat_other_procs", "gauge", "Omes processes of a run id it does not read, never named.", "", &n, 1)
	}
	errs := uint64(len(l.Errors))
	e.add("laug_cellstat_errors", "gauge", "Errors the last sample met.", "", &errs, 1)
	var names []string
	for s := range states {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		n := states[s]
		e.add("laug_cellstat_containers", "gauge", "laug containers in the last line, by state.", labelSet("state", s), &n, 1)
	}
	return e.bytes()
}
