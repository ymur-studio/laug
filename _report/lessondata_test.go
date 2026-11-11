package report

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

// TestReadBins: boot 6's first kill-history raun reads with its failed starts a second, and readBins refuses bins
// that name another run or remove_at, leave a failure unplaced, lose a failure or move one across the restart, or
// hold a part of a second, a second out of order or an empty second.
func TestReadBins(t *testing.T) {
	needRecords(t, killHistoryBins[0])
	read := func(fsys fs.FS) (khRaun, error) {
		r, err := readKH(fsys, killHistoryRauns[0])
		if err != nil {
			t.Fatal(err)
		}
		return r, readBins(fsys, killHistoryBins[0], &r)
	}
	r, err := read(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.bins) != 43 || r.bins[0] != (khBin{-110, 12}) || r.last != 5 || r.failed[2]+r.failed[3] != 50 {
		t.Errorf("run 1: %d bins from %+v, the last failure at +%s s, %s from the restart on", len(r.bins), r.bins[0],
			Num(r.last), Num(r.failed[2]+r.failed[3]))
	}
	last := "{\n      \"s\": 5,\n      \"n\": 1\n    }\n  ]"
	for _, c := range []struct{ name, old, new, want string }{
		{"another run", `"run_id": "raun-lab6-kill-history-1"`, `"run_id": "raun-lab6-kill-history-2"`,
			`run_id "raun-lab6-kill-history-2", not raun-lab6-kill-history-1`},
		{"another remove_at", `"remove_at": 1793076334`, `"remove_at": 1793076335`,
			"remove_at 1793076335 and 0 unplaced, not 1793076334 and 0"},
		{"a failure unplaced", "\"failed_in_log\": 595,\n  \"unplaced\": 0", "\"failed_in_log\": 595,\n  \"unplaced\": 1",
			"remove_at 1793076334 and 1 unplaced"},
		{"a bin dropped from the hold", "[\n    {\n      \"s\": -110,\n      \"n\": 12\n    },", "[",
			"583 failed a second in all and 50 from the restart on, not 595 and the windows' 50"},
		{"a bin moved before the restart", "\"s\": 0,\n      \"n\": 12", "\"s\": -1,\n      \"n\": 12",
			"595 failed a second in all and 38 from the restart on, not 595 and the windows' 50"},
		{"a part of a second", last, strings.Replace(last, `"s": 5,`, `"s": 5.5,`, 1),
			"1 failed at 5.5 s, is not a count in its own later second"},
		{"a second out of order", "\"s\": 2,\n      \"n\": 17", "\"s\": 1,\n      \"n\": 17",
			"17 failed at 1 s, is not a count in its own later second"},
		{"an empty second", last, strings.Replace(last, `"n": 1`, `"n": 0`, 1),
			"0 failed at 5 s, is not a count in its own later second"},
	} {
		fsys := editFS(t, killHistoryBins[0], func(s string) string {
			if strings.Count(s, c.old) != 1 {
				t.Fatalf("%s: %q is not in %s once", c.name, c.old, killHistoryBins[0])
			}
			return strings.Replace(s, c.old, c.new, 1)
		})
		if _, err := read(fsys); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
