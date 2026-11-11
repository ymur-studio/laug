package cellstat

// The readers: one function per file of the plan's reader table. Every reading is a *uint64. A missing file, a read
// error, a missing key, a number that does not parse or the word max leaves it nil, and the line leaves the key out.
// A 0 that was read is written 0. Files are read with os.ReadFile to EOF: cgroup and proc files stat at size 0
// (seen on the dev host's 6.6 kernel).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Container is one compose container in a line. Counters are raw and cumulative, in the kernel's units.
type Container struct {
	Project string `json:"project"`
	Service string `json:"service"`
	ID      string `json:"id"`            // the first 12 of the 64 hex
	Via     string `json:"via,omitempty"` // pid: the group came from /proc/<pid>/cgroup
	State   string `json:"state"`         // ok, gone or unresolved
	Why     string `json:"why,omitempty"`

	CPU         *uint64  `json:"cpu_us,omitempty"`
	CPUUser     *uint64  `json:"cpu_user_us,omitempty"`
	CPUSystem   *uint64  `json:"cpu_system_us,omitempty"`
	ThrottledN  *uint64  `json:"throttled_n,omitempty"`
	ThrottledUs *uint64  `json:"throttled_us,omitempty"`
	CPUMax      *uint64  `json:"cpu_max_us,omitempty"`
	CPUPeriod   *uint64  `json:"cpu_period_us,omitempty"`
	MemMax      *uint64  `json:"mem_max_b,omitempty"`
	Anon        *uint64  `json:"anon_b,omitempty"`
	File        *uint64  `json:"file_b,omitempty"`
	OOMKill     *uint64  `json:"oom_kill,omitempty"`
	IODevs      *uint64  `json:"io_devs,omitempty"`
	IORBytes    *uint64  `json:"io_rbytes,omitempty"`
	IOWBytes    *uint64  `json:"io_wbytes,omitempty"`
	IORIOs      *uint64  `json:"io_rios,omitempty"`
	IOWIOs      *uint64  `json:"io_wios,omitempty"`
	CPUSome     *uint64  `json:"cpu_some_us,omitempty"`
	CPUFull     *uint64  `json:"cpu_full_us,omitempty"`
	IOSome      *uint64  `json:"io_some_us,omitempty"`
	IOFull      *uint64  `json:"io_full_us,omitempty"`
	MemSome     *uint64  `json:"mem_some_us,omitempty"`
	MemFull     *uint64  `json:"mem_full_us,omitempty"`
	Missing     []string `json:"missing,omitempty"` // base names of the files that could not be read
}

// Host is the host's own counters: /proc/stat ticks, /proc/schedstat and /proc/pressure.
type Host struct {
	VCPU     *uint64 `json:"vcpu,omitempty"` // cpuN lines in /proc/stat
	Ticks    *Ticks  `json:"ticks,omitempty"`
	RunqWait *uint64 `json:"runq_wait_ns,omitempty"`
	CPUSome  *uint64 `json:"cpu_some_us,omitempty"` // host cpu full is not written: 0 at the root (seen; why: psi.rst)
	IOSome   *uint64 `json:"io_some_us,omitempty"`
	IOFull   *uint64 `json:"io_full_us,omitempty"`
	MemSome  *uint64 `json:"mem_some_us,omitempty"`
	MemFull  *uint64 `json:"mem_full_us,omitempty"`
}

// Ticks is the first line of /proc/stat, user to steal, in USER_HZ ticks (100 a second, seen with getconf CLK_TCK).
type Ticks struct {
	User    *uint64 `json:"user,omitempty"`
	Nice    *uint64 `json:"nice,omitempty"`
	System  *uint64 `json:"system,omitempty"`
	Idle    *uint64 `json:"idle,omitempty"`
	IOWait  *uint64 `json:"iowait,omitempty"`
	IRQ     *uint64 `json:"irq,omitempty"`
	SoftIRQ *uint64 `json:"softirq,omitempty"`
	Steal   *uint64 `json:"steal,omitempty"`
}

// Proc is one Omes client or worker process, or with -pids one recorded process or an orchestrator process. Only its
// run id (Omes) or its role is written, never its argv.
type Proc struct {
	Role     string  `json:"role"`             // omes-client or omes-worker; a record's file name; orchestrator-server or orchestrator-worker
	RunID    string  `json:"run_id,omitempty"` // Omes only: every Omes process has one, so its lines are as before -pids
	PID      int     `json:"pid"`
	Threads  *uint64 `json:"threads,omitempty"`   // threads whose schedstat was read; absent when none was
	RunNs    *uint64 `json:"run_ns,omitempty"`    // on a CPU, summed over those threads
	WaitNs   *uint64 `json:"wait_ns,omitempty"`   // runnable and waiting on a run queue, summed the same way (sched-stats.rst)
	Runs     *uint64 `json:"runs,omitempty"`      // times on a CPU (sched-stats.rst: timeslices; "runs", so no key holds the word slice)
	CPUTicks *uint64 `json:"cpu_ticks,omitempty"` // utime plus stime of the whole process, exited threads included (seen; proc(5))
	// With -pids only, for its processes (readMemIO): VmRSS and VmHWM of /proc/<pid>/status, and read_bytes and
	// write_bytes of /proc/<pid>/io, the bytes that went to or came from the storage layer for the whole process
	// (proc(5)). Omes processes do not read them, so their lines and their cost stay as they were.
	RSS      *uint64 `json:"rss_b,omitempty"`
	RSSPeak  *uint64 `json:"rss_peak_b,omitempty"`
	IORBytes *uint64 `json:"io_rbytes,omitempty"`
	IOWBytes *uint64 `json:"io_wbytes,omitempty"`
}

// num parses a decimal counter. Anything else, the word max included, is absent.
func num(s string) *uint64 {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

// keyed splits a file of "key value" lines: cpu.stat, memory.stat, memory.events, cgroup.events.
func keyed(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}

// psi reads a pressure file's some and full totals, in microseconds (psi.rst). A file with no full line
// gives no full total.
func psi(b []byte) (some, full *uint64) {
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		var total *uint64
		for _, kv := range f[1:] {
			if v, ok := strings.CutPrefix(kv, "total="); ok {
				total = num(v)
			}
		}
		switch f[0] {
		case "some":
			some = total
		case "full":
			full = total
		}
	}
	return some, full
}

// ioStat counts the MAJ:MIN lines as io_devs and sums each of the 4 keys over the lines that carry it. A line with
// no keys is a device with no reading (22 of 31 lines in the root's io.stat, seen; that it means no IO yet is from
// block/blk-cgroup.c). An empty file gives io_devs 0 and no sums.
func ioStat(b []byte, c *Container) {
	var devs uint64
	sums := map[string]*uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		devs++
		for _, kv := range f[1:] {
			k, v, _ := strings.Cut(kv, "=")
			n := num(v)
			if n == nil || (k != "rbytes" && k != "wbytes" && k != "rios" && k != "wios") {
				continue
			}
			if sums[k] == nil {
				sums[k] = new(uint64)
			}
			*sums[k] += *n
		}
	}
	c.IODevs = &devs
	c.IORBytes, c.IOWBytes, c.IORIOs, c.IOWIOs = sums["rbytes"], sums["wbytes"], sums["rios"], sums["wios"]
}

// readCgroup reads one container's group dir into c, the files in name order. A file it cannot read goes in
// missing. populated 0 makes the container gone, why empty, with no reading. It returns false when the dir went
// away during the read: that container sits out this line. memory.current and memory.peak are never opened: they
// charge page cache (seen: current 433 MB against anon 294 and file 125 MB in one container).
func readCgroup(dir string, c *Container) bool {
	read := func(name string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			c.Missing = append(c.Missing, name)
			return nil, false
		}
		return b, true // an empty file is a read: io.stat with no devices
	}
	if b, ok := read("cgroup.events"); ok && keyed(b)["populated"] == "0" {
		c.State, c.Why, c.Missing = "gone", "empty", nil
		return true
	}
	if b, ok := read("cpu.max"); ok {
		// "max 100000" is no quota, "150000 100000" 1.5 CPUs (both seen);
		// the period is written only beside a numeric quota.
		if f := strings.Fields(string(b)); len(f) == 2 {
			if q := num(f[0]); q != nil {
				c.CPUMax, c.CPUPeriod = q, num(f[1])
			}
		}
	}
	if b, ok := read("cpu.pressure"); ok {
		c.CPUSome, c.CPUFull = psi(b)
	}
	if b, ok := read("cpu.stat"); ok {
		// The first 3 keys are in every group; the throttle keys only where the cpu controller is on (seen).
		m := keyed(b)
		c.CPU, c.CPUUser, c.CPUSystem = num(m["usage_usec"]), num(m["user_usec"]), num(m["system_usec"])
		c.ThrottledN, c.ThrottledUs = num(m["nr_throttled"]), num(m["throttled_usec"])
	}
	if b, ok := read("io.pressure"); ok {
		c.IOSome, c.IOFull = psi(b)
	}
	if b, ok := read("io.stat"); ok {
		ioStat(b, c)
	}
	if b, ok := read("memory.events"); ok {
		// The key max counts hits of memory.max (cgroup-v2.rst); it is not the word max.
		c.OOMKill = num(keyed(b)["oom_kill"])
	}
	if b, ok := read("memory.max"); ok {
		c.MemMax = num(strings.TrimSpace(string(b)))
	}
	if b, ok := read("memory.pressure"); ok {
		c.MemSome, c.MemFull = psi(b)
	}
	if b, ok := read("memory.stat"); ok {
		m := keyed(b)
		c.Anon, c.File = num(m["anon"]), num(m["file"])
	}
	if len(c.Missing) > 0 && !isDir(dir) {
		return false
	}
	return true
}

// isDir says whether path is a directory now.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// isCPUN matches cpu0, cpu1 and on: one line per CPU in /proc/stat and /proc/schedstat.
func isCPUN(s string) bool {
	d, ok := strings.CutPrefix(s, "cpu")
	if !ok || d == "" {
		return false
	}
	for _, r := range d {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// procStat reads /proc/stat: the cpu line's 8 fields, user to steal (seen: 10 fields, steal 0 since boot on the
// dev host), and the count of cpuN lines.
func procStat(b []byte) (vcpu *uint64, t *Ticks) {
	var n uint64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) > 0 && f[0] == "cpu":
			t = &Ticks{}
			for i, p := range []**uint64{&t.User, &t.Nice, &t.System, &t.Idle, &t.IOWait, &t.IRQ, &t.SoftIRQ, &t.Steal} {
				if i+1 < len(f) {
					*p = num(f[i+1])
				}
			}
		case len(f) > 0 && isCPUN(f[0]):
			n++
		}
	}
	if n > 0 {
		vcpu = &n
	}
	return vcpu, t
}

// schedstat sums the 8th of the 9 numbers on each cpuN line of /proc/schedstat: nanoseconds tasks waited on that
// CPU's run queue (meaning: sched-stats.rst; seen filled with kernel.sched_schedstats 0). Absent unless
// every cpuN line has 9 numbers: later versions changed the domain lines (sched-stats.rst), and the reading lives here.
func schedstat(b []byte) *uint64 {
	var sum uint64
	cpus := 0
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !isCPUN(f[0]) {
			continue
		}
		if len(f) != 10 {
			return nil
		}
		v := num(f[8])
		if v == nil {
			return nil
		}
		sum += *v
		cpus++
	}
	if cpus == 0 {
		return nil
	}
	return &sum
}

// readHost reads the host's files under root.
func readHost(root string) Host {
	var h Host
	file := func(name string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(root, "proc", name))
		return b, err == nil
	}
	if b, ok := file("stat"); ok {
		h.VCPU, h.Ticks = procStat(b)
	}
	if b, ok := file("schedstat"); ok {
		h.RunqWait = schedstat(b)
	}
	if b, ok := file("pressure/cpu"); ok {
		h.CPUSome, _ = psi(b)
	}
	if b, ok := file("pressure/io"); ok {
		h.IOSome, h.IOFull = psi(b)
	}
	if b, ok := file("pressure/memory"); ok {
		h.MemSome, h.MemFull = psi(b)
	}
	return h
}

// readUptime reads the first number of /proc/uptime: the gate's mono clock (gate/lib.sh, stamp).
func readUptime(root string) *float64 {
	b, err := os.ReadFile(filepath.Join(root, "proc", "uptime"))
	if err != nil {
		return nil
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil
	}
	return &v
}

// readCmdline reads a process's argv. A process that is gone gives none.
func readCmdline(root string, pid int) []string {
	b, err := os.ReadFile(filepath.Join(root, "proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// readProc sums task/*/schedstat over a process's live threads: /proc/<pid>/schedstat is the leader thread alone
// (seen: 42 threads, the leader 17.3 s, all 611.3 s, equal to utime plus stime). cpu_ticks, from
// /proc/<pid>/stat, lets the report check the sum for threads that exited. It returns false when the process went.
func readProc(root string, pid int) (Proc, bool) {
	var p Proc
	dir := filepath.Join(root, "proc", strconv.Itoa(pid))
	tasks, err := os.ReadDir(filepath.Join(dir, "task"))
	if err != nil {
		return p, false
	}
	var run, wait, runs, n uint64
	for _, t := range tasks {
		b, err := os.ReadFile(filepath.Join(dir, "task", t.Name(), "schedstat"))
		if err != nil {
			continue // the thread exited between the listing and the read
		}
		f := strings.Fields(string(b))
		if len(f) != 3 {
			continue
		}
		r, w, s := num(f[0]), num(f[1]), num(f[2])
		if r == nil || w == nil || s == nil {
			continue
		}
		run, wait, runs, n = run+*r, wait+*w, runs+*s, n+1
	}
	if n > 0 {
		p.Threads, p.RunNs, p.WaitNs, p.Runs = &n, &run, &wait, &runs
	}
	if b, err := os.ReadFile(filepath.Join(dir, "stat")); err == nil {
		p.CPUTicks = statTicks(b)
	}
	return p, true
}

// statTicks adds utime and stime, fields 14 and 15 of /proc/<pid>/stat, counted after the last ")" since the
// command name in parentheses may hold spaces (seen: 52 fields).
func statTicks(b []byte) *uint64 {
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3, the state
	if len(f) < 13 {
		return nil
	}
	u, st := num(f[11]), num(f[12])
	if u == nil || st == nil {
		return nil
	}
	v := *u + *st
	return &v
}

// readMemIO adds VmRSS and VmHWM (kB in the file, bytes in the line) and read_bytes and write_bytes to p. A file it
// cannot read, /proc/<pid>/io of another user's process among them, leaves its keys absent.
func readMemIO(root string, pid int, p *Proc) {
	dir := filepath.Join(root, "proc", strconv.Itoa(pid))
	if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if f := strings.Fields(v); ok && len(f) == 2 && f[1] == "kB" && (k == "VmRSS" || k == "VmHWM") {
				if n := num(f[0]); n != nil {
					*n *= 1024
					if k == "VmRSS" {
						p.RSS = n
					} else {
						p.RSSPeak = n
					}
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "io")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, _ := strings.Cut(line, ":")
			switch k {
			case "read_bytes":
				p.IORBytes = num(strings.TrimSpace(v))
			case "write_bytes":
				p.IOWBytes = num(strings.TrimSpace(v))
			}
		}
	}
}

// statStart is field 22 of /proc/<pid>/stat, the start time in clock ticks after boot, counted after the last ")" as
// statTicks counts. A record's start is this number (gate/lib.sh, proc_start; _obs/obs.sh, rec_write).
func statStart(b []byte) *uint64 {
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3
	if len(f) < 20 {
		return nil
	}
	return num(f[19])
}

// recordName is a record's file name without .pid: the role it reads as.
var recordName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// readRecords reads each <role>.pid in dir, {pid, start, boot_id} (gate/lib.sh's record shape, as _obs/obs.sh writes
// the stack's), and the process it names when the record holds now: boot_id this boot's (<root>/proc/sys/kernel/random/
// boot_id) and start the process's own. A record of another boot, of a process that ended, of a pid that another
// process took since, or one that does not parse is not read and not written: nothing in it is a reading. A dir that is
// not there yet gives none.
func readRecords(root, dir string) []Proc {
	out := []Proc{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	b, err := os.ReadFile(filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id"))
	boot := strings.TrimSpace(string(b))
	if err != nil || boot == "" {
		return out
	}
	for _, e := range ents {
		role, ok := strings.CutSuffix(e.Name(), ".pid")
		if !ok || !recordName.MatchString(role) || !e.Type().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec struct {
			PID    int     `json:"pid"`
			Start  *uint64 `json:"start"`
			BootID string  `json:"boot_id"`
		}
		if json.Unmarshal(raw, &rec) != nil || rec.PID <= 0 || rec.Start == nil || rec.BootID != boot {
			continue
		}
		st, err := os.ReadFile(filepath.Join(root, "proc", strconv.Itoa(rec.PID), "stat"))
		if err != nil {
			continue
		}
		if s := statStart(st); s == nil || *s != *rec.Start {
			continue
		}
		p, ok := readProc(root, rec.PID)
		if !ok {
			continue
		}
		p.Role, p.PID = role, rec.PID
		readMemIO(root, rec.PID, &p)
		out = append(out, p)
	}
	return out
}
