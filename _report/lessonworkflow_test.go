package report

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// editFS is the checkout with the file at p read through edit, which must change it.
func editFS(t *testing.T, p string, edit func(string) string) fs.FS {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	edited := edit(string(data))
	if edited == string(data) {
		t.Fatalf("the edit left %s as it was", p)
	}
	return swapFS{os.DirFS(root), p, []byte(edited)}
}

// TestBoot4Host: the gates rows of boot 4's workflow gates must place the batch on the lab host, and each of boot 4's
// figures, which prints its words for the host, refuses a batch one of them places on another.
func TestBoot4Host(t *testing.T) {
	if _, host, err := boot4Tables(os.DirFS(root), adoptedTable); err != nil || host != "the lab host, an m8i.8xlarge" {
		t.Fatalf("%q, %v", host, err)
	}
	g2 := tableLine(t, "gates", adoptedGates[0].gate)
	fsys := editFS(t, tablesPath+"/gates.jsonl", func(s string) string {
		return strings.Replace(s, g2, strings.Replace(g2, `"host":{"type":"m8i.8xlarge","vcpu":32`,
			`"host":{"type":"m8i.2xlarge","vcpu":8`, 1), 1)
	})
	_, adoptedErr := adopted(fsys)
	_, staleErr := staleness(fsys)
	_, _, panelErr := cancelsPanel(fsys, "")
	for name, err := range map[string]error{"adopted": adoptedErr, "staleness": staleErr, "cancels": panelErr} {
		if err == nil || !strings.Contains(err.Error(), "not the lab host") {
			t.Errorf("%s with G2 on another host: %v", name, err)
		}
	}
}

// TestReadAdopted: G4 reads its KILL, the next worker and the attempts as its files record them, and is refused when
// its gate did not run through workflow Gate, the reading's wave is not the timeline's, or the next worker replayed
// another gate.
func TestReadAdopted(t *testing.T) {
	needRecords(t, "gate/results/"+adoptedGates[1].gate+"/gate.json")
	g4 := adoptedGates[1]
	r, err := readAdopted(os.DirFS(root), g4)
	if err != nil {
		t.Fatal(err)
	}
	if Num(r.killed-r.wave) != "60.062" || Num(r.started-r.killed) != "30.114" || Num(r.post-r.start) != "612.832" ||
		Num(r.waveEnd-r.wave) != "361.434" || Num(maxGap(r.polls)) != "5.146" || len(r.polls) != 71 ||
		len(r.ended) != 3 || r.attempt != 2 || r.ones != 15 || r.verdict != "ADVANCE" || r.server != "1.31.0" ||
		!slices.Equal(r.over, []string{"await-steady", "await-incumbent", "wave", "await-pl"}) {
		t.Errorf("read %+v", r)
	}
	g2, err := readAdopted(os.DirFS(root), adoptedGates[0])
	if err != nil || g2.killed != 0 || g2.over != nil || len(g2.files) != 4 {
		t.Errorf("G2: %+v, %v", g2, err)
	}
	// The figure reads each gate from its rows as -tables read it from its files.
	ts, err := readFigTables(os.DirFS(root), "gates", adoptedTable, boot4ReadingsTable)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range adoptedGates {
		f, err := readAdopted(os.DirFS(root), g)
		if err != nil {
			t.Fatal(err)
		}
		r, err := rowsAdopted(ts[0], ts[1], ts[2], g)
		f.files = nil
		if err != nil || !reflect.DeepEqual(r, f) {
			t.Errorf("%s: the rows' %+v, %v; the files' %+v", g.step, r, err, f)
		}
	}
	dir := "gate/results/" + g4.gate + "/"
	for _, c := range []struct {
		path string
		edit func(string) string
		want string
	}{
		{dir + "gate.json", func(s string) string {
			return strings.Replace(s, `"driver": "step.sh"`, `"driver": "run.sh"`, 1)
		}, "not workflow gate-" + g4.gate},
		{boot4Readings, func(s string) string {
			return strings.Replace(s, `"wave_at": 1792835717.285`, `"wave_at": 1792835718.285`, 1)
		}, "is not " + dir + "timeline.jsonl's wave mark"},
		{g4.worker, func(s string) string {
			return strings.Replace(s, "gate-"+g4.gate, "gate-g20261024-092853", 1)
		}, "want one line replaying 1 open gate"},
	} {
		if _, err := readAdopted(editFS(t, c.path, c.edit), g4); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s edited: %v, want %q", c.path, err, c.want)
		}
	}
}

// TestReadAttempts keeps the activities on an attempt over 1 and counts the others, and refuses activities on two
// attempts over 1 or an entry with no attempt.
func TestReadAttempts(t *testing.T) {
	read := func(data string) ([]string, int, int, error) {
		return readAttempts(fstest.MapFS{"a.json": {Data: []byte(data)}}, "a.json")
	}
	over, attempt, ones, err := read(`[{"activity":"open","attempt":1},{"activity":"wave","attempt":2},` +
		`{"activity":"post","attempt":1}]`)
	if err != nil || !slices.Equal(over, []string{"wave"}) || attempt != 2 || ones != 2 {
		t.Errorf("%v %d %d %v", over, attempt, ones, err)
	}
	for _, c := range []struct{ data, want string }{
		{`[{"activity":"wave","attempt":2},{"activity":"judge","attempt":3}]`, "attempts 2 and 3"},
		{`[{"activity":"wave"}]`, "entry 1 is no activity"},
		{`[{"activity":"wave","attempt":1.5}]`, "entry 1 is no activity"},
		{`[]`, "no activity"},
	} {
		if _, _, _, err := read(c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.data, err, c.want)
		}
	}
}

// TestReadStale reads make obs-test's lines in their own text, and refuses a step file missing one of them or whose
// times are out of their order.
func TestReadStale(t *testing.T) {
	needRecords(t, boot4Pack+"steps/F12.txt")
	r, err := readStale(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	if r.out != "0.0" || r.back != "245.0" || r.ctlOut != "340.0" || r.cellOut != "2.5" || r.storeOut != "5.0" ||
		r.lagOut != "2.5" || r.reads != "235" ||
		r.readsTo != "240.6" || Num(r.backAt) != "245" || r.storeBack != "5.0" || r.ctlCell != "3.0" ||
		r.ctlReads != "297" || r.ctlTo != "296.7" || r.ctlFlip != "297.7" || r.inStore != "340.82" ||
		r.control != "313.25" {
		t.Errorf("read %+v", r)
	}
	for _, c := range []struct {
		edit func(string) string
		want string
	}{
		{func(s string) string {
			return strings.Replace(s, "--- PASS: TestFakeCellStalenessControl", "--- FAIL: x", 1)
		}, "want a PASS line"},
		{func(s string) string {
			return strings.Replace(s, "first read 512 at +297.7 s", "first read 512 at +1.0 s", 1)
		}, "out of their order"},
		{func(s string) string { return strings.Replace(s, "  negative control: ", "  control: ", 1) },
			"0 lines of the negative control"},
	} {
		if _, err := readStale(editFS(t, obsTestSteps, c.edit)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
	// The figure states the control's config from the test's own source, and refuses a test that no longer sets it.
	fsys := editFS(t, fakeCellTest, func(s string) string { return strings.ReplaceAll(s, stalenessOff, "x") })
	if _, err := staleness(fsys); err == nil || !strings.Contains(err.Error(), "does not set") {
		t.Errorf("a control that does not set %q: %v", stalenessOff, err)
	}
}

// TestReadCancel reads each cancel's events and timeline, F5's stop path scheduled after the cancellations and F7's
// before them, and refuses events that do not run 1 to n or hold a second cancel request.
func TestReadCancel(t *testing.T) {
	needRecords(t, boot4Pack+"extra/f5-events.json")
	f5, f7 := boot4Cancels[0], boot4Cancels[1]
	r, err := readCancel(os.DirFS(root), f5.step, f5.gate, "true")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.actReq) != 4 || len(r.actDone) != 4 || len(r.stops) != 4 || r.stopFirst || Num(r.stops[0][0]) != "0.248" ||
		span(r.actDone) != "0.112 to 0.205" || Num(r.term) != "0.347" || span(r.ended) != "0.863 to 0.885" ||
		Num(r.closed) != "1.641" || Num(r.sinceWave) != "30.048" {
		t.Errorf("F5: %+v", r)
	}
	r, err = readCancel(os.DirFS(root), f7.step, f7.gate, "false")
	if err != nil || !r.stopFirst || Num(r.stops[0][0]) != "0.046" || Num(r.closed) != "1.694" {
		t.Errorf("F7: %+v, %v", r, err)
	}
	ev := boot4Pack + "extra/f5-events.json"
	for _, c := range []struct {
		edit func(string) string
		want string
	}{
		{func(s string) string { return strings.Replace(s, `{"id":"5",`, `{"id":"50",`, 1) }, "not event 5"},
		{func(s string) string {
			return strings.Replace(s, `"id":"88","type":"EVENT_TYPE_WORKFLOW_TASK_SCHEDULED"`,
				`"id":"88","type":"EVENT_TYPE_WORKFLOW_EXECUTION_CANCEL_REQUESTED"`, 1)
		}, "two cancel requests"},
	} {
		if _, err := readCancel(editFS(t, ev, c.edit), f5.step, f5.gate, "true"); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
	// -tables takes each cancel's mode from the transcript's heading of its step, as the panel takes it from the
	// heading's row.
	fsys := editFS(t, boot4Transcript, func(s string) string {
		return strings.Replace(s, "-- F7: gate cancel, WaitForCancellation false", "-- F7: gate cancel", 1)
	})
	if _, _, err := cancelRows(fsys); err == nil || !strings.Contains(err.Error(), "no heading of F7") {
		t.Errorf("no heading of F7: %v", err)
	}
	heading := tableLine(t, cancelTable, "2026-10-24-boot4.txt/0479")
	fsys = editFS(t, tablesPath+"/"+cancelTable+".jsonl", func(s string) string {
		return strings.Replace(s, heading, "", 1)
	})
	if _, _, err := cancelsPanel(fsys, ""); err == nil || !strings.Contains(err.Error(), "no heading of F7") {
		t.Errorf("no row of F7's heading: %v", err)
	}
}
