package cellstat

// Tests 2 to 6 and 11 of the plan: the readers on the dev host's files, copied verbatim from a 6.6 kernel.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reading keys a container can carry; the labels, state and missing are not readings.
var readingKeys = []string{"cpu_us", "cpu_user_us", "cpu_system_us", "throttled_n", "throttled_us", "cpu_max_us",
	"cpu_period_us", "mem_max_b", "anon_b", "file_b", "oom_kill", "io_devs", "io_rbytes", "io_wbytes", "io_rios",
	"io_wios", "cpu_some_us", "cpu_full_us", "io_some_us", "io_full_us", "mem_some_us", "mem_full_us"}

// Test 2: absent, not 0. A group with no controllers has the 3 cpu.stat keys and 6 pressure totals, nothing else, and
// names the 5 files it could not read.
func TestAbsentNotZero(t *testing.T) {
	c := one(t, devTree(t), laug(idC, "temporal-ui", 303))
	want(t, c, "state", `"ok"`, "cpu_us", "224237867", "cpu_user_us", "84056262", "cpu_system_us", "140181604",
		"cpu_some_us", "1001214", "cpu_full_us", "992089", "io_some_us", "1266074", "io_full_us", "1265892",
		"mem_some_us", "71", "mem_full_us", "71",
		"missing", `["cpu.max","io.stat","memory.events","memory.max","memory.stat"]`)
	n := 0
	for _, k := range readingKeys {
		if _, ok := c[k]; ok {
			n++
		}
	}
	if n != 9 {
		t.Errorf("%d reading keys, want 9: %v", n, c)
	}
}

// Test 3: max is absent; numbers write all three keys.
func TestMaxIsAbsent(t *testing.T) {
	root := devTree(t)
	a := laug(idA, "temporal-matching", 101)
	absent(t, one(t, root, a), "cpu_max_us", "cpu_period_us", "mem_max_b")
	dir := cgroupDir(root, "docker/"+idA)
	write(t, filepath.Join(dir, "cpu.max"), "50000 100000\n")
	write(t, filepath.Join(dir, "memory.max"), "536870912\n")
	want(t, one(t, root, a), "cpu_max_us", "50000", "cpu_period_us", "100000", "mem_max_b", "536870912")
}

// Test 4: anon and file come from memory.stat; with memory.current and memory.peak deleted the line is byte for
// byte the same, and their values are nowhere in it.
func TestAnonNotCurrent(t *testing.T) {
	rootA, rootB := devTree(t), devTree(t)
	for _, f := range []string{"memory.current", "memory.peak"} {
		if err := os.Remove(filepath.Join(cgroupDir(rootB, "docker/"+idA), f)); err != nil {
			t.Fatal(err)
		}
	}
	cs := []fc{laug(idA, "temporal-matching", 101)}
	a := marshal(t, testSampler(rootA, &fakeLister{cs: cs}, true).sample())
	b := marshal(t, testSampler(rootB, &fakeLister{cs: cs}, true).sample())
	if !bytes.Equal(a, b) {
		t.Errorf("lines differ without memory.current and memory.peak:\n%s\n%s", a, b)
	}
	want(t, containers(t, testSampler(rootA, &fakeLister{cs: cs}, false).sample())[idA[:12]],
		"anon_b", "16539648", "file_b", "41648128")
	for _, v := range []string{"81068032", "351084544"} {
		if bytes.Contains(a, []byte(v)) {
			t.Errorf("the line holds %s, a value of memory.current or memory.peak", v)
		}
	}
}

// Test 5: pressure totals equal total=; a file with only a some line has no full key; a 0 is written 0.
func TestPressureTotals(t *testing.T) {
	root := devTree(t)
	a := laug(idA, "temporal-matching", 101)
	want(t, one(t, root, a), "cpu_some_us", "14576217", "cpu_full_us", "14204796", "io_some_us", "34812608",
		"io_full_us", "34578646", "mem_some_us", "66389", "mem_full_us", "66373")
	write(t, filepath.Join(cgroupDir(root, "docker/"+idA), "cpu.pressure"), "some avg10=0.00 avg60=0.00 avg300=0.00 total=14576217\n")
	c := one(t, root, a)
	want(t, c, "cpu_some_us", "14576217")
	absent(t, c, "cpu_full_us")
	// D's io.pressure, verbatim: both totals 0.
	want(t, one(t, root, laug(idD, "postgresql", 404)), "io_some_us", "0", "io_full_us", "0")
}

// Test 6: io.stat. A sums 2 lines; D's empty file is io_devs 0 and no sums; K, the root's file, counts all 31 lines
// and sums the 9 that carry keys; a file of keyless lines counts them and sums nothing.
func TestIOStat(t *testing.T) {
	root := devTree(t)
	want(t, one(t, root, laug(idA, "temporal-matching", 101)), "io_devs", "2", "io_rbytes", "975941632",
		"io_wbytes", "21889019904", "io_rios", "17469", "io_wios", "433331")
	d := one(t, root, laug(idD, "postgresql", 404))
	want(t, d, "io_devs", "0")
	absent(t, d, "io_rbytes", "io_wbytes", "io_rios", "io_wios")
	k := laug(idK, "temporal-history", 505)
	want(t, one(t, root, k), "io_devs", "31", "io_rbytes", "230273580032", "io_wbytes", "545840455680",
		"io_rios", "6013769", "io_wios", "30037080")
	write(t, filepath.Join(cgroupDir(root, "docker/"+idK), "io.stat"), "7:0 \n7:1 \n7:2 \n")
	c := one(t, root, k)
	want(t, c, "io_devs", "3")
	absent(t, c, "io_rbytes", "io_wbytes", "io_rios", "io_wios")
}

// Test 11: the host. Ticks from /proc/stat's cpu line, vcpu from its 24 cpuN lines, 5 pressure totals and no host
// cpu full, run-queue wait summed over the 8th number of each cpuN line; one cpuN line with 10 numbers, none.
func TestHost(t *testing.T) {
	root := devTree(t)
	host := func() string {
		var v struct {
			Host json.RawMessage `json:"host"`
		}
		if err := json.Unmarshal(marshal(t, testSampler(root, &fakeLister{}, false).sample()), &v); err != nil {
			t.Fatal(err)
		}
		return string(v.Host)
	}
	full := `{"vcpu":24,"ticks":{"user":16658198,"nice":2559,"system":13572047,"idle":1590003349,"iowait":2712941,"irq":0,"softirq":2980743,"steal":0},` +
		`"runq_wait_ns":8473828878630,"cpu_some_us":974824330,"io_some_us":1991755255,"io_full_us":1824063773,"mem_some_us":2084482,"mem_full_us":2070749}`
	if got := host(); got != full {
		t.Errorf("host:\n got %s\nwant %s", got, full)
	}
	p := filepath.Join(root, "proc", "schedstat")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "cpu3 ") {
			line += " 7"
		}
		out = append(out, line)
	}
	write(t, p, strings.Join(out, "\n"))
	if got := host(); strings.Contains(got, "runq_wait_ns") {
		t.Errorf("a cpuN line with 10 numbers still gives runq_wait_ns: %s", got)
	}
}
