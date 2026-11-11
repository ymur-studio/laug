package report

// lessonmicrovm.go draws two figures of microVM cells on the lab host, each from the pack it names. restore-timing,
// stop 8, is boot 9's (cells/results/2026-10-29-boot9.txt), under the final setting: snapshots frozen, the drive's
// cache_type Unsafe sent and read back, io_engine Sync. It draws three cold boots against twelve restores from a
// snapshot, each restore's thaw a segment, and a cell's disk copied plainly against a reflink. clock-step-timers,
// stop 9, is batch E's (cells/results/2026-10-24-batchE.txt): durable timers across a step of the guest's clock. It
// stays batch E's because boot 9's stepped timer read 124.071 s late with a server view of 62.898 s, 2.9 s past due
// on its own clock that no step file explains, where batch E's read 60.021 s. Each figure reads its figure table, the
// lines of the pack's files it names one by one, never a glob, which report -tables keeps after reading the
// files as the figure reads the lines, and its host from src/hosts.tsv. restore-timing's table, fig-restore-timing,
// holds the lines of the step files of the cold boots, the restores and the copies; the figure checks them against
// the pack's readings in its readings table where both hold a value, as -tables checks the files against
// readings.json. clock-step-timers' table, fig-clock-step-timers, holds the lines of the timers' step files and the
// transcript's line of the step's size.

import (
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// A pack is one batch's record: its name as a figure prints it, its transcript, which names the host and each step,
// the directory of its step files and readings, the readings table cut from them, and the figure table cut from its
// files.
type pack struct {
	name, transcript, dir, readingsTable, table string
}

var (
	// microvmPack is the record restore-timing reads.
	microvmPack = pack{"boot 9", "cells/results/2026-10-29-boot9.txt", "cells/results/2026-10-29-boot9/",
		"readings-2026-10-29-boot9", "fig-restore-timing"}
	// timerPack is the record clock-step-timers reads.
	timerPack = pack{"batch E", "cells/results/2026-10-24-batchE.txt", "cells/results/2026-10-24-batchE/",
		"readings-2026-10-24-batche", "fig-clock-step-timers"}
)

const microvmGen = "_report/lessonmicrovm.go"

// readings is the pack's readings file.
func (p pack) readings() string { return p.dir + "readings.json" }

// step is the step file of step in the pack.
func (p pack) step(step string) string { return p.dir + "steps/" + step + ".txt" }

// textOf gives a reader a record's text by its path, and the record's name in an error: the file itself (fileTexts),
// or the lines of it a figure table kept, under the prefix of its file's name (rowTexts).
type textOf func(p string) (text, name string, err error)

func fileTexts(fsys fs.FS) textOf {
	return func(p string) (string, string, error) {
		text, err := readText(fsys, p)
		return text, p, err
	}
}

func rowTexts(t figTable) textOf {
	return func(p string) (string, string, error) {
		text, err := t.text(path.Base(p))
		return text, t.cite(path.Base(p) + "/"), err
	}
}

// lookup gives a reader a batch's reading by its name, and where it was read: from its readings.json
// (fileReadings), or from its readings table, kept whole (figTable.reading).
type lookup func(name string) (jsonx.Value, string, error)

func fileReadings(fsys fs.FS, p string) lookup {
	return func(name string) (jsonx.Value, string, error) {
		v, err := readingIn(fsys, p, name)
		return v, p, err
	}
}

// read reads the pack's figure table and readings table, and places the pack on the lab host by src/hosts.tsv: the
// tables, and the figure's words for the host.
func (p pack) read(fsys fs.FS) (fig, readings figTable, host string, err error) {
	ts, err := readFigTables(fsys, p.table, p.readingsTable)
	if err != nil {
		return figTable{}, figTable{}, "", err
	}
	if err := batchOnLabHost(fsys, p.transcript); err != nil {
		return figTable{}, figTable{}, "", err
	}
	return ts[0], ts[1], labHostWords, nil
}

// oneIn is the submatches of re's one match in text, an error naming p when there is none or more than one.
func oneIn(text, p string, re *regexp.Regexp, what string) ([]string, error) {
	found := re.FindAllStringSubmatch(text, -1)
	if len(found) != 1 {
		return nil, fmt.Errorf("%s: %d lines of %s, want 1", p, len(found), what)
	}
	return found[0], nil
}

// sameNum checks the recorded text v is the number reading's member key holds in the readings file p, both named in
// the error.
func sameNum(p, v, reading string, val jsonx.Value, key string) error {
	n, err := need(val, p, key)
	if err != nil {
		return fmt.Errorf("%s's %w", reading, err)
	}
	if f, perr := strconv.ParseFloat(v, 64); perr != nil || f != n {
		return fmt.Errorf("%s: %s's %s is %s, the step file's %s", p, reading, key, Num(n), v)
	}
	return nil
}

// sameStr checks the recorded text v is the string reading's member key holds in the readings file p.
func sameStr(p, v, reading string, val jsonx.Value, key string) error {
	if got, ok := at(val, key).Str(); !ok || got != v {
		return fmt.Errorf("%s: %s's %s is %q, the step file's %q", p, reading, key, got, v)
	}
	return nil
}

// The lines the cells/rootfs scripts print, as the step files keep them: snapshot.sh's of a cold boot and restore.sh's
// of each restore. A time is in seconds as the script prints it.
const secs = `([0-9]+\.[0-9]+)`

var (
	coldCopy    = regexp.MustCompile(`(?m)^disk: copy of the image, ` + secs + ` s$`)
	coldSSH     = regexp.MustCompile(`(?m)^boot to ssh ` + secs + ` s$`)
	coldDockerd = regexp.MustCompile(`(?m)^dockerd ready ` + secs + ` s$`)
	coldUp      = regexp.MustCompile(`(?m)^compose up returned ` + secs + ` s; from compose up:$`)
	warmChecks  = regexp.MustCompile(`(?m)^cluster health ` + secs + ` s, namespace ` + secs + ` s, shards owned ` +
		secs + ` s, HealthCheck done ` + secs + ` s, attempt ([0-9]+)$`)
	// coldFroze, coldPaused and coldCreate are the snapshot's parts after warm, and coldSnap its closing line, which
	// names the drive's cache mode and io engine as sent and whether the containers froze.
	coldFroze  = regexp.MustCompile(`(?m)^froze ([0-9]+) containers of project laug in ` + secs + ` s$`)
	coldPaused = regexp.MustCompile(`(?m)^paused ` + secs + ` s$`)
	coldCreate = regexp.MustCompile(`(?m)^snapshot/create ` + secs + ` s$`)
	coldSnap   = regexp.MustCompile(`(?m)^laug · snapshot in \S+: vm\.mem (\S+), vm\.snap \S+; golden disk \S+; ` +
		`cache_type (\S+); io_engine (\S+); (frozen|not frozen)$`)
	// restoreCache and restoreFrozen are restore.sh's reading of the snapshot's snapshot.json, before the first.
	restoreCache  = regexp.MustCompile(`(?m)^snapshot cache_type (\S+)$`)
	restoreFrozen = regexp.MustCompile(`(?m)^snapshot frozen (\S+)$`)
	restoreHead   = regexp.MustCompile(`(?m)^=== restore ([0-9]+) of ([0-9]+)$`)
	restoreDisk   = regexp.MustCompile(`(?m)^disk: (reflink|copy), ` + secs + ` s$`)
	restoreLoad   = regexp.MustCompile(`(?m)^snapshot/load returned ` + secs + ` s$`)
	restoreSSH    = regexp.MustCompile(`(?m)^ssh ` + secs + ` s$`)
	restoreBehnd  = regexp.MustCompile(`(?m)^guest clock behind host by ` + secs + ` s$`)
	restoreSet    = regexp.MustCompile(`(?m)^clock set ` + secs + ` s$`)
	restoreThaw   = regexp.MustCompile(`(?m)^thawed ([0-9]+) containers in ` + secs + ` s, ` + secs +
		` s from snapshot/load$`)
	restoreAfter = regexp.MustCompile(`(?m)^guest clock offset after set ([-+]?[0-9]+\.[0-9]+) s \(round trip ` + secs +
		` s\); from snapshot/load:$`)
	restoreCall = regexp.MustCompile(`(?m)^  health-[0-9]+  completed ([0-9]+) ms  visible [0-9]+ ms `)
	// copyStamps is the line the disk copy steps print: $EPOCHREALTIME before cp and after it.
	copyStamps = regexp.MustCompile(`(?m)^cp ([0-9]+\.[0-9]+) ([0-9]+\.[0-9]+)$`)
	stepExit0  = regexp.MustCompile(`(?m)^--- exit 0 in [0-9.]+ s$`)
)

// coldSteps are the three cold boots, each snapshotted, the third's snapshot the one the restores load.
var coldSteps = []string{"K1", "K2", "K3"}

// restoreStep is the step of the twelve restores; copySteps the plain copies, then the reflinks, of the golden disk.
const restoreStep = "R1"

var copySteps = []string{"D1-1", "D1-2", "D1-3", "D2-1", "D2-2", "D2-3"}

// coldBoot is a cold boot as snapshot.sh printed it: copy is the image's copy, before the VM starts; ssh and dockerd
// are from the VM's start; up and the checks are from compose up, which starts at dockerd ready (the sum the
// transcript's cold to warm takes). After warm the snapshot: froze containers in freeze, paused, snapshot/create in
// create; cache and engine the drive's settings it names. Each value is its recorded text.
type coldBoot struct {
	step                                     string
	copy, ssh, dockerd, up                   string
	health, namespace, shards, done, attempt string
	froze, freeze, paused, create            string
	mem, cache, engine                       string
}

// warm is the cold boot's time to warm from the VM's start: dockerd ready plus compose up to HealthCheck done.
func (c coldBoot) warm() float64 { return secsOf(c.dockerd) + secsOf(c.done) }

// restore is one restore as restore.sh printed it, each time from snapshot/load but disk, the reflink copy before it,
// and thaw, how long the thaw of thawed containers took, which ended at thawDone.
type restore struct {
	n                                        int
	disk, load, ssh, behind, set, after, rtt string
	thawed, thaw, thawDone                   string
	health, namespace, shards, done, attempt string
	call                                     string // the HealthCheck that passed, its completed ms
}

// thawFrom is when the restore's thaw began, from snapshot/load.
func (r restore) thawFrom() float64 { return secsOf(r.thawDone) - secsOf(r.thaw) }

// readCold reads the cold boot of step from tx and checks it against the reading cold_boot_<n> look gives.
func readCold(tx textOf, look lookup, step string, n int) (coldBoot, error) {
	text, p, err := tx(microvmPack.step(step))
	if err != nil {
		return coldBoot{}, err
	}
	c := coldBoot{step: step}
	for _, f := range []struct {
		re   *regexp.Regexp
		what string
		to   *string
	}{
		{coldCopy, "the image's copy", &c.copy}, {coldSSH, "boot to ssh", &c.ssh},
		{coldDockerd, "dockerd ready", &c.dockerd}, {coldUp, "compose up", &c.up},
		{coldPaused, "the pause", &c.paused}, {coldCreate, "snapshot/create", &c.create},
	} {
		m, err := oneIn(text, p, f.re, f.what)
		if err != nil {
			return coldBoot{}, err
		}
		*f.to = m[1]
	}
	m, err := oneIn(text, p, warmChecks, "the warm checks")
	if err != nil {
		return coldBoot{}, err
	}
	c.health, c.namespace, c.shards, c.done, c.attempt = m[1], m[2], m[3], m[4], m[5]
	if m, err = oneIn(text, p, coldFroze, "the freeze"); err != nil {
		return coldBoot{}, err
	}
	c.froze, c.freeze = m[1], m[2]
	if m, err = oneIn(text, p, coldSnap, "the snapshot's setting"); err != nil {
		return coldBoot{}, err
	}
	if m[4] != "frozen" {
		return coldBoot{}, fmt.Errorf("%s: the snapshot is %s, want frozen", p, m[4])
	}
	c.mem, c.cache, c.engine = m[1], m[2], m[3]
	if _, err := oneIn(text, p, stepExit0, "exit 0"); err != nil {
		return coldBoot{}, err
	}
	name := "cold_boot_" + strconv.Itoa(n)
	v, rp, err := look(name)
	if err != nil {
		return coldBoot{}, err
	}
	for _, k := range []struct{ text, key string }{
		{c.copy, "disk_s"}, {c.ssh, "boot_to_ssh_s"}, {c.dockerd, "dockerd_s"}, {c.up, "compose_up_s"},
		{c.done, "warm_s"}, {c.attempt, "healthcheck_attempt"}, {c.froze, "froze_containers"},
		{c.freeze, "freeze_s"}, {c.paused, "paused_s"}, {c.create, "snapshot_create_s"},
	} {
		if err := sameNum(rp, k.text, name, v, k.key); err != nil {
			return coldBoot{}, err
		}
	}
	for _, k := range []struct{ text, key string }{
		{c.mem, "vm_mem"}, {c.cache, "cache_type"}, {c.engine, "io_engine"}, {"frozen", "frozen_line"},
	} {
		if err := sameStr(rp, k.text, name, v, k.key); err != nil {
			return coldBoot{}, err
		}
	}
	if !(secsOf(c.ssh) < secsOf(c.dockerd) && secsOf(c.up) <= secsOf(c.health) && secsOf(c.health) < secsOf(c.done) &&
		secsOf(c.namespace) < secsOf(c.done) && secsOf(c.shards) < secsOf(c.done)) {
		return coldBoot{}, fmt.Errorf("%s: the boot's times are out of their order", p)
	}
	return c, nil
}

// readRestores reads R1's restores from tx, as many as its heads say, and checks each against the reading
// restore_<n> look gives, and the snapshot's cache mode and freeze, as R1 read them, against restore_snapshot. It
// returns the cache mode.
func readRestores(tx textOf, look lookup) ([]restore, string, error) {
	text, p, err := tx(microvmPack.step(restoreStep))
	if err != nil {
		return nil, "", err
	}
	mc, err := oneIn(text, p, restoreCache, "the snapshot's cache_type")
	if err != nil {
		return nil, "", err
	}
	mf, err := oneIn(text, p, restoreFrozen, "the snapshot frozen")
	if err != nil {
		return nil, "", err
	}
	if mf[1] != "true" {
		return nil, "", fmt.Errorf("%s: the snapshot is frozen %s, want true", p, mf[1])
	}
	snap, rp, err := look("restore_snapshot")
	if err != nil {
		return nil, "", err
	}
	for _, k := range []struct{ text, key string }{{mc[1], "cache_type"}, {mf[1], "frozen"}} {
		if err := sameStr(rp, k.text, "restore_snapshot", snap, k.key); err != nil {
			return nil, "", err
		}
	}
	heads := restoreHead.FindAllStringSubmatchIndex(text, -1)
	if len(heads) == 0 {
		return nil, "", fmt.Errorf("%s: no restore", p)
	}
	if heads[0][0] < strings.Index(text, mf[0]) {
		return nil, "", fmt.Errorf("%s: the snapshot's reading comes after its first restore", p)
	}
	var rs []restore
	for i, h := range heads {
		n, _ := strconv.Atoi(text[h[2]:h[3]])
		of, _ := strconv.Atoi(text[h[4]:h[5]])
		if n != i+1 || of != len(heads) {
			return nil, "", fmt.Errorf("%s: restore %d of %d is the %d of %d heads", p, n, of, i+1, len(heads))
		}
		end := len(text)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		block, at := text[h[1]:end], fmt.Sprintf("%s, restore %d", p, n)
		r := restore{n: n}
		m, err := oneIn(block, at, restoreDisk, "the disk")
		if err != nil {
			return nil, "", err
		}
		if m[1] != "reflink" {
			return nil, "", fmt.Errorf("%s: the disk is a %s, not a reflink", at, m[1])
		}
		r.disk = m[2]
		for _, f := range []struct {
			re   *regexp.Regexp
			what string
			to   *string
		}{
			{restoreLoad, "snapshot/load", &r.load}, {restoreSSH, "ssh", &r.ssh},
			{restoreBehnd, "the clock behind", &r.behind}, {restoreSet, "the clock set", &r.set},
			{restoreCall, "the HealthCheck that passed", &r.call},
		} {
			m, err := oneIn(block, at, f.re, f.what)
			if err != nil {
				return nil, "", err
			}
			*f.to = m[1]
		}
		if m, err = oneIn(block, at, restoreThaw, "the thaw"); err != nil {
			return nil, "", err
		}
		r.thawed, r.thaw, r.thawDone = m[1], m[2], m[3]
		if m, err = oneIn(block, at, restoreAfter, "the offset after the set"); err != nil {
			return nil, "", err
		}
		r.after, r.rtt = m[1], m[2]
		if m, err = oneIn(block, at, warmChecks, "the warm checks"); err != nil {
			return nil, "", err
		}
		r.health, r.namespace, r.shards, r.done, r.attempt = m[1], m[2], m[3], m[4], m[5]
		name := "restore_" + strconv.Itoa(n)
		v, rp, err := look(name)
		if err != nil {
			return nil, "", err
		}
		for _, k := range []struct{ text, key string }{
			{r.load, "load_s"}, {r.ssh, "ssh_s"}, {r.behind, "clock_behind_s"}, {r.thaw, "thaw_s"},
			{r.after, "offset_after_s"}, {r.done, "warm_s"}, {r.attempt, "healthcheck_attempt"},
			{r.call, "first_healthcheck_ms"},
		} {
			if err := sameNum(rp, k.text, name, v, k.key); err != nil {
				return nil, "", err
			}
		}
		if !(secsOf(r.load) <= secsOf(r.ssh) && secsOf(r.ssh) < secsOf(r.set) && secsOf(r.set) < r.thawFrom() &&
			secsOf(r.thawDone) < secsOf(r.health) && secsOf(r.health) < secsOf(r.done) &&
			secsOf(r.namespace) < secsOf(r.done) && secsOf(r.shards) < secsOf(r.done)) {
			return nil, "", fmt.Errorf("%s: the restore's times are out of their order", at)
		}
		rs = append(rs, r)
	}
	if _, err := oneIn(text, p, stepExit0, "exit 0"); err != nil {
		return nil, "", err
	}
	return rs, mc[1], nil
}

// diskCopy is one copy of the golden disk: plain, cp --reflink=never, or a reflink, its seconds by the step's stamps.
type diskCopy struct {
	step    string
	reflink bool
	s       float64
}

// readCopies reads the six copies from tx and checks each against its reading look gives, which the batch wrote to 2
// decimals for a plain copy and 3 for a reflink.
func readCopies(tx textOf, look lookup) ([]diskCopy, error) {
	plain, rp, err := look("disk_copy_plain_s")
	if err != nil {
		return nil, err
	}
	reflink, _, err := look("disk_copy_reflink_s")
	if err != nil {
		return nil, err
	}
	pa, _ := plain.Arr()
	ra, _ := reflink.Arr()
	if len(pa) != 3 || len(ra) != 3 {
		return nil, fmt.Errorf("%s: %d plain and %d reflink copies, want 3 and 3", rp, len(pa), len(ra))
	}
	var cs []diskCopy
	for i, step := range copySteps {
		text, p, err := tx(microvmPack.step(step))
		if err != nil {
			return nil, err
		}
		c := diskCopy{step: step, reflink: i >= 3}
		flag, want := "--reflink=never", pa
		if c.reflink {
			flag, want = "--reflink=always", ra
		}
		if !strings.Contains(strings.SplitN(text, "\n", 2)[0], "cp "+flag+" ") {
			return nil, fmt.Errorf("%s: the command is no cp %s", p, flag)
		}
		m, err := oneIn(text, p, copyStamps, "the copy's stamps")
		if err != nil {
			return nil, err
		}
		if _, err := oneIn(text, p, stepExit0, "exit 0"); err != nil {
			return nil, err
		}
		c.s = secsOf(m[2]) - secsOf(m[1])
		rec := num(want[i%3])
		if !rec.OK || c.s < 0 || math.Abs(c.s-rec.F) > 0.005+1e-9 {
			return nil, fmt.Errorf("%s: the stamps give %s s, the reading %s", p, Num(c.s), rec)
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// noteGap is how far past its bar's end a note starts, in user units.
const noteGap = 6.0

// axisTop is the top of a seconds axis drawn from left to right for bars ending at ends, each with a note widths[i]
// wide set noteGap past it: the next 10 s above what each note needs to end inside right, so the axis grows for the
// note, not only for the bar. A note too wide to fit at any top is an error.
func axisTop(ends, widths []float64, left, right float64) (float64, error) {
	span, need := right-left, 0.0
	for i, e := range ends {
		room := span - noteGap - widths[i]
		if room <= 0 {
			return 0, fmt.Errorf("a note %v wide does not fit beside a bar in %v", widths[i], span)
		}
		need = math.Max(need, float64(e*span)/room)
	}
	return float64(10 * (math.Floor(need/10) + 1)), nil
}

// restoreTiming draws boot 9's three cold boots and twelve restores as bars of their phases against seconds from
// each one's start, and the six copies of the golden disk, plain and reflink, on the same axis.
func restoreTiming(fsys fs.FS) (drawn, error) {
	fig, readings, host, err := microvmPack.read(fsys)
	if err != nil {
		return drawn{}, err
	}
	tx, look := rowTexts(fig), lookup(readings.reading)
	var cold []coldBoot
	for i, step := range coldSteps {
		c, err := readCold(tx, look, step, i+1)
		if err != nil {
			return drawn{}, err
		}
		cold = append(cold, c)
	}
	rs, cache, err := readRestores(tx, look)
	if err != nil {
		return drawn{}, err
	}
	// The restores load the last cold boot's snapshot: its setting and its containers are the ones they read.
	k := cold[len(cold)-1]
	if k.cache != cache {
		return drawn{}, fmt.Errorf("%s: the snapshot's cache_type is %s, %s's %s", fig.cite(restoreStep+".txt/"),
			cache, k.step, k.cache)
	}
	for _, r := range rs {
		if r.thawed != k.froze {
			return drawn{}, fmt.Errorf("%s: restore %d thawed %s containers, %s froze %s",
				fig.cite(restoreStep+".txt/"), r.n, r.thawed, k.step, k.froze)
		}
	}
	cs, err := readCopies(tx, look)
	if err != nil {
		return drawn{}, err
	}
	size, rp, err := look("golden_size")
	if err != nil {
		return drawn{}, err
	}
	apparent, err := need(size, rp, "apparent_bytes")
	if err != nil {
		return drawn{}, err
	}
	allocated, err := need(size, rp, "allocated_bytes")
	if err != nil {
		return drawn{}, err
	}
	sources := append(tableSources(fig, readings), hostsTSV)

	var warmCold, warmRestore, behind, thaw, plain, refl []float64
	for _, c := range cold {
		warmCold = append(warmCold, c.warm())
	}
	for _, r := range rs {
		warmRestore = append(warmRestore, secsOf(r.done))
		behind = append(behind, secsOf(r.behind))
		thaw = append(thaw, secsOf(r.thaw))
	}
	for _, c := range cs {
		if c.reflink {
			refl = append(refl, c.s)
		} else {
			plain = append(plain, c.s)
		}
	}
	gib := func(b float64) string {
		return strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(b/(1<<30), 'f', 2, 64), "0"), ".")
	}
	of := func(f func(c coldBoot) string) []float64 {
		var xs []float64
		for _, c := range cold {
			xs = append(xs, secsOf(f(c)))
		}
		return xs
	}
	setting := "snapshots frozen, cache_type " + cache
	title := fmt.Sprintf("A restore reached warm in %s s; a cold boot in %s s", spanTo(warmRestore, 2),
		spanTo(warmCold, 2))
	sub := "microVM cells of " + microvmPack.name + " on " + host + ", " + setting + ": seconds"
	desc := fmt.Sprintf("Three groups of bars against one axis, seconds, from %s's snapshots, taken with the "+
		"cell's containers frozen and the drive's cache_type %s, io_engine %s. The top group is the three cold "+
		"boots, K1 to K3, each from the VM's start, as cells/rootfs/snapshot.sh printed them, a bar of four "+
		"segments, left to right: boot to ssh, then dockerd ready, then compose up, then the warm checks to "+
		"HealthCheck done; compose up starts at dockerd ready, so a bar's end is dockerd ready plus compose up to "+
		"HealthCheck done. Each one's copy of the image ran before its start, and after warm it froze its %s "+
		"containers in %s s and took its snapshot in %s s, none of them drawn. The middle group is "+
		"R1's %d restores of K3's snapshot, each from snapshot/load, as cells/rootfs/restore.sh printed them: "+
		"a bar of four segments: snapshot/load and ssh, the guest clock read, set and read again, to the thaw's "+
		"start, the "+
		"thaw of the %s containers, %s s, the warm checks; each restore's reflink copy of the golden disk, %s s, "+
		"ran before it. Beside each: the guest clock behind the host before the set, %s s, and its offset after "+
		"it. The bottom group is the golden disk, %s GiB allocated of %s GiB, copied plainly after a cache drop, "+
		"three times, and as a reflink on the same XFS volume, three times, a bar of one segment each, by each "+
		"step's own stamps. Each time is the step file's; readings.json's entry of each agrees.",
		microvmPack.name, cache, k.engine, k.froze, span(of(func(c coldBoot) string { return c.freeze })),
		span(of(func(c coldBoot) string { return c.create })), len(rs), k.froze, span(thaw),
		span(col2(rs, func(r restore) string { return r.disk })), span(behind), gib(allocated), gib(apparent))

	type seg struct {
		from, to float64
		fill     Colour
		name     string
	}
	type bar struct {
		label string
		segs  []seg
		note  string
	}
	type group struct {
		head, name string
		bars       []bar
	}
	coldG := group{head: fmt.Sprintf("%d cold boots, K1 to K%d, from the VM's start", len(cold), len(cold)),
		name: "cold boots"}
	for _, c := range cold {
		d := secsOf(c.dockerd)
		coldG.bars = append(coldG.bars, bar{c.step, []seg{
			{0, secsOf(c.ssh), Water, "boot to ssh"},
			{secsOf(c.ssh), d, Basalt, "dockerd ready"},
			{d, d + secsOf(c.up), Silica, "compose up returned"},
			{d + secsOf(c.up), c.warm(), Warm, fmt.Sprintf("the warm checks: cluster health +%s s, shards owned +%s "+
				"s, namespace +%s s, HealthCheck done +%s s from compose up", c.health, c.shards, c.namespace, c.done)},
		}, fmt.Sprintf("%s s, HealthCheck attempt %s", Num(c.warm()), c.attempt)})
	}
	restoreG := group{head: fmt.Sprintf("%d restores of K3's snapshot, R1, from snapshot/load", len(rs)),
		name: "restores"}
	for _, r := range rs {
		restoreG.bars = append(restoreG.bars, bar{"restore " + strconv.Itoa(r.n), []seg{
			{0, secsOf(r.ssh), Water, "snapshot/load " + r.load + " s, then ssh"},
			{secsOf(r.ssh), r.thawFrom(), Silica, "the guest clock read, set " + r.set + " s, and read again, round " +
				"trip " + r.rtt + " s, to the thaw's start"},
			{r.thawFrom(), secsOf(r.thawDone), Basalt, fmt.Sprintf("the thaw of %s containers, %s s, done %s s",
				r.thawed, r.thaw, r.thawDone)},
			{secsOf(r.thawDone), secsOf(r.done), Warm, fmt.Sprintf("the warm checks: cluster health %s s, namespace "+
				"%s s, shards owned %s s, HealthCheck done %s s", r.health, r.namespace, r.shards, r.done)},
		}, fmt.Sprintf("%s s; the guest clock %s s behind, %s s after the set", r.done, r.behind, r.after)})
	}
	copyG := group{head: fmt.Sprintf("The golden disk copied, %s GiB allocated of %s GiB, D1 and D2", gib(allocated),
		gib(apparent)), name: "disk copies"}
	for i, c := range cs {
		label, what := fmt.Sprintf("plain %d", i+1), "cp --reflink=never after a cache drop"
		if c.reflink {
			label, what = fmt.Sprintf("reflink %d", i-2), "cp --reflink=always"
		}
		copyG.bars = append(copyG.bars, bar{label, []seg{{0, c.s, Deep, what}}, Num(c.s) + " s"})
	}
	groups := []group{coldG, restoreG, copyG}

	const labelR, axisL, rowH, rowStep, headH = 228.0, 236.0, 14.0, 20.0, 26.0
	var ends, widths []float64
	rows := 0
	for _, g := range groups {
		for _, b := range g.bars {
			ends = append(ends, b.segs[len(b.segs)-1].to)
			widths = append(widths, textW(b.note, 10, false))
			rows++
		}
	}
	hi, err := axisTop(ends, widths, axisL, marginR)
	if err != nil {
		return drawn{}, fmt.Errorf("figure restore-timing: %w", err)
	}
	var ticks []float64
	for k := 0; float64(10*k) <= hi; k++ {
		ticks = append(ticks, float64(10*k))
	}
	x := Scale{0, hi, axisL, marginR}
	top := laneTop - 10
	axisY := top + float64(float64(len(groups))*headH) + float64(float64(rows)*rowStep) + 10
	height := axisY + 40 + 34
	s := lessonFigure("restore-timing", title, sub, desc, sources, height)
	for _, t := range ticks {
		s.Line(x.At(t), top, x.At(t), axisY, Style{Stroke: Silica})
	}
	y := top
	for _, g := range groups {
		s.Text(marginL, y+16, g.head, Font{Size: 11, Bold: true})
		if textW(g.head, 11, true) > marginR-marginL {
			s.fail("the heading %q is wider than the figure", g.head)
		}
		y += headH
		s.Group(g.name)
		for _, b := range g.bars {
			s.Text(labelR, y+11, b.label, Font{Size: 11, Anchor: AnchorEnd, Mono: true})
			for _, sg := range b.segs {
				s.Rect(x.At(sg.from), y, math.Max(x.At(sg.to)-x.At(sg.from), 1), rowH, Style{Fill: sg.fill,
					Stroke: Deep, Width: 0.5, Title: fmt.Sprintf("%s, %s to %s s", sg.name, Num(sg.from), Num(sg.to))})
			}
			end := x.At(b.segs[len(b.segs)-1].to) + noteGap
			if end+textW(b.note, 10, false) > marginR {
				s.fail("the note %q runs past the figure", b.note)
			}
			s.Text(end, y+11, b.note, Font{Size: 10})
			y += rowStep
		}
		s.EndGroup()
	}

	s.AxisX(x, axisY, ticks, "seconds from the start: the VM's, snapshot/load's, or the copy's")
	items := []legendItem{
		{"to ssh", swatch(Water)}, {"dockerd ready, or the thaw", swatch(Basalt)},
		{"compose up, or the clock set", swatch(Silica)}, {"the warm checks", swatch(Warm)},
		{"a disk copy", swatch(Deep)},
	}
	lessonLegend(s, marginL, height-18, items)
	if legendW(items) > marginR-marginL {
		s.fail("the legend is wider than the figure")
	}
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}

// col2 is f of each restore, as numbers.
func col2(rs []restore, f func(r restore) string) []float64 {
	var xs []float64
	for _, r := range rs {
		xs = append(xs, secsOf(f(r)))
	}
	return xs
}

// The timers of T, each laug health timer on one leased microVM cell: T1 the control; T2 one timer with the guest's
// clock stepped back while it ran; T3 three armed together, the same step, then the guest's clock set right. Each
// step file holds the timer's JSON line; T2-step, T2-set, T3-step and T3-set hold the date command that stepped or set
// the guest's clock, and its target.
var (
	timerControl = "T1"
	timerStep    = "T2"
	timerSets    = []string{"T3-30", "T3-45", "T3-60"}
	// timerJSON is the JSON line laug health timer prints, and clockCmd the command that steps or sets the guest's
	// clock to the target, in Unix seconds; the step's size is the transcript's.
	timerJSON = regexp.MustCompile(`(?m)^\{"server_at_start":.*\}$`)
	clockCmd  = regexp.MustCompile(`(?m)^\$ gsh \S+ date -u -s @([0-9]+\.[0-9]+) > /dev/null && echo (stepped|set)$`)
	stepSize  = regexp.MustCompile(`the guest's clock set to the host's less ([0-9]+\.[0-9]+) s`)
)

// timer is one laug health timer: its length, and by host time its arm and its fire, in Unix seconds; late and the
// server's view, fire less start on the server's clock, as its JSON gives them.
type timer struct {
	step             string
	length           float64
	armed, fired     float64
	late, serverView float64
}

// readTimer reads the timer of step from tx: its JSON line, whose host times must agree with its length and its late.
func readTimer(tx textOf, step string) (timer, error) {
	text, p, err := tx(timerPack.step(step))
	if err != nil {
		return timer{}, err
	}
	m, err := oneIn(text, p, timerJSON, "the timer's JSON")
	if err != nil {
		return timer{}, err
	}
	if _, err := oneIn(text, p, stepExit0, "exit 0"); err != nil {
		return timer{}, err
	}
	v, err := jsonx.Parse([]byte(m[0]))
	if err != nil {
		return timer{}, fmt.Errorf("%s: %w", p, err)
	}
	t := timer{step: step}
	for _, f := range []struct {
		key string
		to  *float64
	}{{"timer_s", &t.length}, {"late_s", &t.late}, {"server_timer_s", &t.serverView}} {
		if *f.to, err = need(v, p, f.key); err != nil {
			return timer{}, err
		}
	}
	for _, f := range []struct {
		key string
		to  *float64
	}{{"host_at_start", &t.armed}, {"host_at_fire", &t.fired}} {
		at, err := time.Parse(time.RFC3339Nano, str(at(v, f.key)))
		if err != nil {
			return timer{}, fmt.Errorf("%s: %s is no time", p, f.key)
		}
		*f.to = unixOf(at)
	}
	if got := t.fired - t.armed; math.Abs(got-(t.length+t.late)) > 0.001 {
		return timer{}, fmt.Errorf("%s: fired %s s after it was armed, not its %s s and %s s late", p, Num(got),
			Num(t.length), Num(t.late))
	}
	return t, nil
}

// readClock is the target of the date command in step's file, read from tx, in Unix seconds, which must be the word it
// echoes.
func readClock(tx textOf, step, word string) (float64, error) {
	text, p, err := tx(timerPack.step(step))
	if err != nil {
		return 0, err
	}
	m, err := oneIn(text, p, clockCmd, "the date command")
	if err != nil {
		return 0, err
	}
	if m[2] != word {
		return 0, fmt.Errorf("%s: the command echoes %s, not %s", p, m[2], word)
	}
	if _, err := oneIn(text, p, stepExit0, "exit 0"); err != nil {
		return 0, err
	}
	return secsOf(m[1]), nil
}

// clockRun is T as the step files hold it: the timers, and by host time when T2's and T3's step and set ran. A step
// sets the guest's clock to the host's less size, so it ran at its target plus size; a set's target is the host's
// time.
type clockRun struct {
	control, stepped timer
	sets             []timer
	size             string
	step2, set2      float64
	step3, set3      float64
}

// readClockRun reads T's step files and the step's size from the transcript, each from tx; steps names the step
// files in an error.
func readClockRun(tx textOf, steps string) (clockRun, error) {
	var r clockRun
	transcript, p, err := tx(timerPack.transcript)
	if err != nil {
		return clockRun{}, err
	}
	m, err := oneIn(transcript, p, stepSize, "the step's size")
	if err != nil {
		return clockRun{}, err
	}
	r.size = m[1]
	if r.control, err = readTimer(tx, timerControl); err != nil {
		return clockRun{}, err
	}
	if r.stepped, err = readTimer(tx, timerStep); err != nil {
		return clockRun{}, err
	}
	for _, step := range timerSets {
		t, err := readTimer(tx, step)
		if err != nil {
			return clockRun{}, err
		}
		r.sets = append(r.sets, t)
	}
	for _, f := range []struct {
		step, word string
		to         *float64
	}{
		{"T2-step", "stepped", &r.step2}, {"T2-set", "set", &r.set2},
		{"T3-step", "stepped", &r.step3}, {"T3-set", "set", &r.set3},
	} {
		if *f.to, err = readClock(tx, f.step, f.word); err != nil {
			return clockRun{}, err
		}
	}
	r.step2 += secsOf(r.size)
	r.step3 += secsOf(r.size)
	a3 := r.armed3()
	if !(r.stepped.armed < r.step2 && r.step2 < r.stepped.armed+r.stepped.length && r.stepped.fired <= r.set2 &&
		a3 < r.step3 && r.step3 < r.set3 && slices.Max(timerCol(r.sets, func(t timer) float64 { return t.length })) <
		r.set3-a3 && r.set3 < slices.Min(timerCol(r.sets, func(t timer) float64 { return t.fired }))) {
		return clockRun{}, fmt.Errorf("%s: T's steps, sets and timers are out of their order", steps)
	}
	return r, nil
}

// armed3 is when the first of T3's timers was armed, by host time.
func (r clockRun) armed3() float64 {
	return slices.Min(timerCol(r.sets, func(t timer) float64 { return t.armed }))
}

// timerCol is f of each timer.
func timerCol(ts []timer, f func(t timer) float64) []float64 {
	var xs []float64
	for _, t := range ts {
		xs = append(xs, f(t))
	}
	return xs
}

// clockStepTimers draws T's five timers against host seconds from each one's arm: when each was due, how late it
// fired, and when the guest's clock was stepped back and set right.
func clockStepTimers(fsys fs.FS) (drawn, error) {
	ts, err := readFigTables(fsys, timerPack.table)
	if err != nil {
		return drawn{}, err
	}
	if err := batchOnLabHost(fsys, timerPack.transcript); err != nil {
		return drawn{}, err
	}
	host := labHostWords
	r, err := readClockRun(rowTexts(ts[0]), ts[0].files[0])
	if err != nil {
		return drawn{}, err
	}
	sources := append(tableSources(ts...), hostsTSV)

	a3 := r.armed3()
	sinceSet := timerCol(r.sets, func(t timer) float64 { return t.fired - r.set3 })
	fired3 := timerCol(r.sets, func(t timer) float64 { return t.fired })
	title := fmt.Sprintf("After a %s s step back, a %s s timer fired %s s late; the server saw %s s", r.size,
		Num(r.stepped.length), Num(r.stepped.late), Num(r.stepped.serverView))
	sub := "laug health timer on a microVM cell of " + timerPack.name + " on " + host +
		": host seconds from each arm"
	desc := fmt.Sprintf("Three lanes, each laug health timer on one leased microVM cell of batch E; across, "+
		"seconds by the host's clock from the moment each timer was armed. A bar is one durable timer in two "+
		"segments: the first to when it was due, its length, then the second to when it fired by host time, its "+
		"lateness. The dashed line is the guest's clock stepped back %s s, the batch's own date command in the "+
		"guest; the solid line the "+
		"guest's clock set to the host's time. The top lane, T1, is the control: no step, %s s late. In the "+
		"middle, T2: the step at +%s s, never set right while the timer ran; it fired %s s late by host time "+
		"while the server, on the guest's clock, recorded %s s. At the bottom, T3: three timers armed within "+
		"%s ms, a step of the same size at +%s s, the clock set right at +%s s; all three fired %s s after the set, "+
		"within %s ms of each other, the server's view %s s each. T3's lines are from its first arm.",
		r.size, Num(r.control.late), Num(r.step2-r.stepped.armed), Num(r.stepped.late),
		Num(r.stepped.serverView), Num(float64(1000*(slices.Max(timerCol(r.sets,
			func(t timer) float64 { return t.armed }))-a3))), Num(r.step3-a3),
		Num(r.set3-a3), span(sinceSet), Num(float64(1000*(slices.Max(fired3)-slices.Min(fired3)))),
		spanTo(timerCol(r.sets, func(t timer) float64 { return t.serverView }), 3))

	const labelR, barH, rowStep, laneGap = 228.0, 16.0, 36.0, 34.0
	last := slices.Max([]float64{r.control.fired - r.control.armed, r.stepped.fired - r.stepped.armed,
		r.set2 - r.stepped.armed, slices.Max(fired3) - a3})
	hi := float64(30 * math.Ceil(last/30))
	var ticks []float64
	for k := 0; float64(30*k) <= hi; k++ {
		ticks = append(ticks, float64(30*k))
	}
	x := Scale{0, hi, 236, marginR}
	top := laneTop - 6
	rows := 2 + len(r.sets)
	axisY := top + float64(3*laneGap) + float64(float64(rows)*rowStep) + 8
	height := axisY + 40 + 34
	s := lessonFigure("clock-step-timers", title, sub, desc, sources, height)
	for _, t := range ticks {
		s.Line(x.At(t), top, x.At(t), axisY, Style{Stroke: Silica})
	}
	y := top
	// A mark is the guest's clock stepped back, dashed, or set to the host's time, drawn across each bar of its lane.
	type mark struct {
		at      float64
		stepped bool
		label   string
	}
	lane := func(head string) float64 {
		s.Text(marginL, y+16, head, Font{Size: 11, Bold: true})
		if textW(head, 11, true) > marginR-marginL {
			s.fail("the lane's heading %q is wider than the figure", head)
		}
		y += laneGap
		return y
	}
	row := func(t timer, from float64, marks []mark) {
		a := t.armed - from
		s.Text(labelR, y+12, t.step+", "+Num(t.length)+" s", Font{Size: 11, Anchor: AnchorEnd, Mono: true})
		s.Rect(x.At(a), y, x.At(a+t.length)-x.At(a), barH, Style{Fill: Silica, Stroke: Deep, Width: 0.5,
			Title: fmt.Sprintf("%s: armed, due %s s later", t.step, Num(t.length))})
		s.Rect(x.At(a+t.length), y, math.Max(x.At(t.fired-from)-x.At(a+t.length), 1), barH, Style{Fill: Warm,
			Stroke: Deep, Width: 0.5, Title: fmt.Sprintf("%s: fired %s s late by host time", t.step, Num(t.late))})
		note := fmt.Sprintf("late %s s by host time; the server's view %s s", Num(t.late), Num(t.serverView))
		if w := textW(note, 10, false); x.At(a)+w > marginR {
			s.fail("the note %q runs past the figure", note)
		}
		s.Text(x.At(a), y+barH+12, note, Font{Size: 10})
		for _, m := range marks {
			st := Style{Stroke: Basalt, Width: 1.5, Title: m.label}
			if m.stepped {
				st.Dash = []float64{4, 3}
			}
			s.Line(x.At(m.at), y-4, x.At(m.at), y+barH+2, st)
		}
		y += rowStep
	}

	s.Group("T1, the control")
	lane("T1, the control: no step")
	row(r.control, r.control.armed, nil)
	s.EndGroup()

	s.Group("T2, a step")
	lane(fmt.Sprintf("T2: the guest's clock stepped back %s s at +%s s, set right only after the fire",
		r.size, Num(r.step2-r.stepped.armed)))
	row(r.stepped, r.stepped.armed, []mark{
		{r.step2 - r.stepped.armed, true, "the guest's clock stepped back " + r.size + " s"},
		{r.set2 - r.stepped.armed, false, "the guest's clock set to the host's time, " +
			Num(r.set2-r.stepped.fired) + " s after the fire"},
	})
	s.EndGroup()

	s.Group("T3, a step and a set")
	lane(fmt.Sprintf("T3: three timers, a step back of %s s at +%s s, the guest's clock set right at +%s s",
		r.size, Num(r.step3-a3), Num(r.set3-a3)))
	for _, t := range r.sets {
		row(t, a3, []mark{
			{r.step3 - a3, true, "the guest's clock stepped back " + r.size + " s"},
			{r.set3 - a3, false, "the guest's clock set to the host's time; the timers fired " + span(sinceSet) +
				" s after"},
		})
	}
	s.EndGroup()

	s.AxisX(x, axisY, ticks, "seconds by the host's clock after the timer was armed")
	items := []legendItem{
		{"armed to due", swatch(Silica)}, {"late, by host time", swatch(Warm)},
		{"the guest's clock stepped back", stroke(Basalt, []float64{4, 3}, 1.5)},
		{"set to the host's time", stroke(Basalt, nil, 1.5)},
	}
	lessonLegend(s, marginL, height-18, items)
	if legendW(items) > marginR-marginL {
		s.fail("the legend is wider than the figure")
	}
	svg, err := s.Bytes()
	return drawn{svg: svg, sources: sources}, err
}
