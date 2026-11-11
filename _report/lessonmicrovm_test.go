package report

import (
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/tables"
)

// TestReadPack: src/hosts.tsv must place each pack on the lab host by the lines that cite its transcript, and each
// figure, which prints its words for the host, refuses a pack a line places on another, or no line cites.
func TestReadPack(t *testing.T) {
	for _, pk := range []pack{microvmPack, timerPack} {
		if _, _, host, err := pk.read(os.DirFS(root)); err != nil || host != "the lab host, an m8i.8xlarge" {
			t.Fatalf("%s: %q, %v", pk.name, host, err)
		}
	}
	const lab, dev = "\tthe lab host, an m8i.8xlarge (32 vCPU, 128 GiB)\t",
		"\tthe dev host, a Linux VM with 24 vCPU and 47 GiB running Docker\t"
	for name, c := range map[string]struct {
		pk   pack
		glob string
		draw func(fsys fs.FS) (drawn, error)
	}{"restore-timing": {microvmPack, "lab9-*", restoreTiming},
		"clock-step-timers": {timerPack, "raun-lab5-*", clockStepTimers}} {
		fsys := editFS(t, hostsTSV, func(s string) string {
			return strings.Replace(s, "\n"+c.glob+lab, "\n"+c.glob+dev, 1)
		})
		if _, err := c.draw(fsys); err == nil || !strings.Contains(err.Error(), "not the lab host") {
			t.Errorf("%s with %s on the dev host: %v", name, c.glob, err)
		}
		fsys = editFS(t, hostsTSV, func(s string) string {
			return strings.ReplaceAll(s, "\t"+c.pk.transcript+":", "\tcells/results/x.txt:")
		})
		if _, err := c.draw(fsys); err == nil || !strings.Contains(err.Error(), "no line cites "+c.pk.transcript) {
			t.Errorf("%s with no line citing %s: %v", name, c.pk.transcript, err)
		}
	}
}

// TestReadRestores reads boot 9's cold boots, restores and copies as their step files print them, the freeze and
// thaw lines among them, and refuses a reading that is not the step file's, a restore out of its place, a snapshot
// not frozen, a thaw of containers the snapshot did not freeze, or a copy that is not the cp it names.
func TestReadRestores(t *testing.T) {
	needRecords(t, microvmPack.step("K1"))
	fsys := os.DirFS(root)
	tx, look := fileTexts(fsys), fileReadings(fsys, microvmPack.readings())
	k1, err := readCold(tx, look, "K1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if k1.ssh != "2.87" || k1.dockerd != "3.55" || k1.up != "12.60" || k1.done != "27.36" || k1.attempt != "2" ||
		k1.copy != "22.14" || Num(k1.warm()) != "30.91" || k1.froze != "11" || k1.freeze != "0.31" ||
		k1.create != "33.77" || k1.cache != "Unsafe" || k1.engine != "Sync" {
		t.Errorf("K1: %+v", k1)
	}
	rs, cache, err := readRestores(tx, look)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 12 || cache != "Unsafe" || rs[0].behind != "35.2" || rs[0].after != "-0.114" ||
		rs[0].done != "4.71" || rs[0].call != "1543" || rs[0].thaw != "1.27" || rs[0].thawDone != "2.35" ||
		Num(rs[0].thawFrom()) != "1.08" || rs[1].thaw != "1.18" || rs[11].behind != "94.5" ||
		rs[11].done != "4.72" || rs[11].load != "0.01" || rs[11].thawed != "11" {
		t.Errorf("%d restores: %+v, %+v", len(rs), rs[0], rs[len(rs)-1])
	}
	cs, err := readCopies(tx, look)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 6 || Num(cs[0].s) != "46.793" || Num(cs[1].s) != "47.251" || cs[0].reflink ||
		Num(cs[3].s) != "0.007" || !cs[3].reflink {
		t.Errorf("copies: %+v", cs)
	}
	for _, c := range []struct {
		path string
		edit func(string) string
		want string
	}{
		{microvmPack.readings(), func(s string) string {
			return strings.Replace(s, `"ssh_s": 0.41,`, `"ssh_s": 0.42,`, 1)
		}, "restore_1's ssh_s is 0.42, the step file's 0.41"},
		{microvmPack.readings(), func(s string) string {
			return strings.Replace(s, `"thaw_s": 1.27,`, `"thaw_s": 1.28,`, 1)
		}, "restore_1's thaw_s is 1.28, the step file's 1.27"},
		{microvmPack.readings(), func(s string) string {
			return strings.Replace(s, `"freeze_s": 0.31,`, `"freeze_s": 0.32,`, 1)
		}, "cold_boot_1's freeze_s is 0.32, the step file's 0.31"},
		{microvmPack.step(restoreStep), func(s string) string {
			return strings.Replace(s, "=== restore 2 of 12", "=== restore 3 of 12", 1)
		}, "restore 3 of 12 is the 2 of 12 heads"},
		{microvmPack.step(restoreStep), func(s string) string {
			return strings.Replace(s, "disk: reflink, 0.02 s", "disk: copy, 0.02 s", 1)
		}, "not a reflink"},
		{microvmPack.step(restoreStep), func(s string) string {
			return strings.Replace(s, "snapshot frozen true", "snapshot frozen false", 1)
		}, "the snapshot is frozen false, want true"},
		{microvmPack.step("K3"), func(s string) string {
			return strings.Replace(s, "io_engine Sync; frozen", "io_engine Sync; not frozen", 1)
		}, "the snapshot is not frozen, want frozen"},
		{microvmPack.step(restoreStep), func(s string) string {
			return strings.Replace(s, "after set -0.114 s", "after set +0.114 s", 1)
		}, "restore_1's offset_after_s is -0.114, the step file's +0.114"},
		{microvmPack.step(restoreStep), func(s string) string {
			return strings.Replace(s, "thawed 11 containers in 1.27 s, 2.35 s", "", 1)
		}, "restore 1: 0 lines of the thaw"},
		{microvmPack.step(restoreStep), func(s string) string {
			return strings.Replace(s, "thawed 11 containers in 1.27 s, 2.35 s", "thawed 11 containers in 1.27 s, 2.55 s", 1)
		}, "restore 1: the restore's times are out of their order"},
		{microvmPack.step("K3"), func(s string) string {
			return strings.Replace(s, "froze 11 containers", "froze 10 containers", 1)
		}, "cold_boot_3's froze_containers is 11, the step file's 10"},
		{microvmPack.step("D1-1"), func(s string) string {
			return strings.Replace(s, "--reflink=never", "--reflink=always", 1)
		}, "no cp --reflink=never"},
		{microvmPack.step("D2-1"), func(s string) string {
			return strings.Replace(s, "1793248221.449913", "1793248221.349913", 1)
		}, "the stamps give 0.107 s"},
	} {
		if _, _, err := restoreTimingRows(editFS(t, c.path, c.edit)); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%s edited: %v, want %q", c.path, err, c.want)
		}
	}
	// The figure reads the lines the table kept as -tables read the files, and checks them against the readings table.
	fig, readings, _, err := microvmPack.read(fsys)
	if err != nil {
		t.Fatal(err)
	}
	rs2, _, err := readRestores(rowTexts(fig), readings.reading)
	if err != nil || !slices.Equal(rs2, rs) {
		t.Errorf("the rows' restores %+v, %v; the files' %+v", rs2, err, rs)
	}
	table := tablesPath + "/" + microvmPack.readingsTable + ".jsonl"
	if _, err := restoreTiming(editFS(t, table, func(s string) string {
		return strings.Replace(s, `"ssh_s":0.41,`, `"ssh_s":0.42,`, 1)
	})); err == nil || !strings.Contains(err.Error(), "restore_1's ssh_s is 0.42, the step file's 0.41") {
		t.Errorf("the readings table edited: %v", err)
	}
}

// TestAxisTop: the restore figure's axis grows for the widest-reaching note, not only for the longest bar, so a
// copy's note fits at boot 9's 47.25 s, where the next 10 s above the bar alone (50) ran it past the figure, and at
// batch E's 52.7 s; and the figure draws, every note inside, with either bar.
func TestAxisTop(t *testing.T) {
	const axisL = 236.0
	for _, c := range []struct {
		end  float64
		note string
		top  float64
	}{{47.251, "47.251 s", 60}, {52.711, "52.711 s", 60}, {30.91, "30.91 s, HealthCheck attempt 2", 50},
		{95, "95 s", 110}} {
		w := textW(c.note, 10, false)
		top, err := axisTop([]float64{c.end}, []float64{w}, axisL, marginR)
		x := Scale{0, top, axisL, marginR}
		if err != nil || top != c.top || x.At(c.end)+noteGap+w > marginR {
			t.Errorf("a %v s bar, its note %q: top %v, %v, want %v with the note inside", c.end, c.note, top, err, c.top)
		}
	}
	if _, err := axisTop([]float64{1}, []float64{marginR}, axisL, marginR); err == nil {
		t.Error("a note wider than the axis: no error")
	}
	if _, err := restoreTiming(os.DirFS(root)); err != nil {
		t.Errorf("boot 9's 47.25 s copy: %v", err)
	}
	needRecords(t, microvmPack.step("D1-2"))
	// D1-2 edited to batch E's longest plain copy, 52.711 s by its stamps, its reading to match: its rows cut again,
	// and the readings table's row edited the same.
	fsys := editFS(t, microvmPack.step("D1-2"), func(s string) string {
		return strings.Replace(s, "1793248173.476834", "1793248178.937026", 1)
	})
	fsys = editIn(t, fsys, microvmPack.readings(), func(s string) string {
		return strings.Replace(s, "      47.25,\n", "      52.71,\n", 1)
	})
	rows, _, err := restoreTimingRows(fsys)
	if err != nil {
		t.Fatal(err)
	}
	fsys = withTable(t, fsys, microvmPack.table, rows)
	fsys = editIn(t, fsys, tablesPath+"/"+microvmPack.readingsTable+".jsonl", func(s string) string {
		return strings.Replace(s, "[46.79,47.25,", "[46.79,52.71,", 1)
	})
	d, err := restoreTiming(fsys)
	if err != nil {
		t.Fatalf("batch E's 52.7 s copy: %v", err)
	}
	if !strings.Contains(string(d.svg), "52.711 s") {
		t.Errorf("batch E's 52.7 s copy: no note %q", "52.711 s")
	}
}

// withTable is fsys with figure table name's file holding rows.
func withTable(t *testing.T, fsys fs.FS, name string, rows []jsonx.Value) fs.FS {
	t.Helper()
	files, err := tables.Write(name, rows)
	if err != nil || len(files) != 1 {
		t.Fatalf("%s: %d files, %v", name, len(files), err)
	}
	return swapFS{fsys, tablesPath + "/" + files[0].Name, files[0].Data}
}

// editIn is fsys with the file p, as fsys holds it, read as edit makes it, which must change it.
func editIn(t *testing.T, fsys fs.FS, p string, edit func(string) string) fs.FS {
	t.Helper()
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		t.Fatal(err)
	}
	edited := edit(string(data))
	if edited == string(data) {
		t.Fatalf("the edit left %s as it was", p)
	}
	return swapFS{fsys, p, []byte(edited)}
}

// TestReadClockRun reads T's timers, steps and sets, and refuses a timer whose host times are not its length and its
// lateness, a transcript that gives no step size, or a set step that echoes a step.
func TestReadClockRun(t *testing.T) {
	needRecords(t, timerPack.dir+"steps/T1.txt")
	r, err := readClockRun(fileTexts(os.DirFS(root)), timerPack.dir+"steps")
	if err != nil {
		t.Fatal(err)
	}
	fig, err := readFigTable(os.DirFS(root), timerPack.table)
	if err != nil {
		t.Fatal(err)
	}
	if rr, err := readClockRun(rowTexts(fig), fig.files[0]); err != nil || !reflect.DeepEqual(rr, r) {
		t.Errorf("the rows' run %+v, %v; the files' %+v", rr, err, r)
	}
	a3 := r.armed3()
	if r.size != "120.9" || Num(r.control.late) != "0.415" || Num(r.stepped.late) != "121.163" ||
		Num(r.stepped.serverView) != "60.021" || Num(r.step2-r.stepped.armed) != "5.205" ||
		Num(r.set2-r.stepped.fired) != "0.048" || len(r.sets) != 3 || Num(r.set3-a3) != "90.545" ||
		Num(r.sets[0].fired-r.set3) != "40.427" || Num(r.sets[1].late) != "85.989" {
		t.Errorf("read %+v", r)
	}
	for _, c := range []struct {
		path string
		edit func(string) string
		want string
	}{
		{timerPack.step(timerStep), func(s string) string {
			return strings.Replace(s, `"late_s":121.163446852`, `"late_s":21.163446852`, 1)
		}, "not its 60 s and 21.163 s late"},
		{timerPack.transcript, func(s string) string {
			return strings.Replace(s, "set to the host's less 120.9 s", "set back 120.9 s", 1)
		}, "0 lines of the step's size"},
		{timerPack.step("T2-set"), func(s string) string {
			return strings.Replace(s, "&& echo set", "&& echo stepped", 1)
		}, "echoes stepped, not set"},
	} {
		if _, _, err := clockStepRows(editFS(t, c.path, c.edit)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s edited: %v, want %q", c.path, err, c.want)
		}
	}
}
