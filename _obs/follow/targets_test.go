package follow

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHandTargetsFromStatus: outside a gate, laugd's /status gives a target for each leased cell, with cell, life,
// block, backend and server_tag and no gate_id, item or arm; a warm, busy or expired cell gives none; no requester,
// whatever its form (a name, a gate's requester, one with a slash and quotes), is a label or in any file; a reset, the
// same id with a new leased_at, gives a new life; 3 failed reads in a row remove every hand target, 2 do not.
func TestHandTargetsFromStatus(t *testing.T) {
	root := fakeRoot(t)
	obs := obsDir(t)
	pool, requests, status := countingLaugd(t)
	addLaugd(t, root, 2001, pool, "/dev/pts/0") // laug pool serve: its stdout is a terminal, no gate
	status.Store("testdata/status/hand.json")
	now := time.Date(2026, 10, 17, 3, 5, 0, 0, time.UTC)
	f := testFollower(t, obs, root, pool, nil, &now)
	targets := filepath.Join(obs, "targets")
	f.poll()
	got := listDir(t, targets)
	want := map[string]string{
		"cell-1dafb33b.json": `[{"targets": ["127.0.0.1:9090"],` + "\n" +
			`  "labels": {"cell": "1dafb33b", "life": "1792206000", "block": "0", "backend": "compose",` + "\n" +
			`             "server_tag": "1.31.0"}}]` + "\n",
		"cell-5e5e5e5e.json": `[{"targets": ["10.254.3.2:9090"],` + "\n" +
			`  "labels": {"cell": "5e5e5e5e", "life": "1792206060", "block": "3", "backend": "firecracker",` + "\n" +
			`             "server_tag": "1.32.1"}}]` + "\n",
		".expires.json": `{"cell-1dafb33b.json":1792213200,"cell-5e5e5e5e.json":1792207260}` + "\n",
	}
	if strings.Join(keys(got), " ") != strings.Join(keys(want), " ") {
		t.Fatalf("targets: %v, want %v", keys(got), keys(want))
	}
	for name, b := range want {
		if got[name] != b {
			t.Errorf("%s:\n%s\nwant\n%s", name, got[name], b)
		}
	}
	all := body(f)
	for _, b := range got {
		all += b
	}
	for _, w := range []string{"gate_id", "item", "arm", "requester", "someone", "x/y", `\"z\"`, "gate-g20261017"} {
		if strings.Contains(all, w) {
			t.Errorf("the targets or the body hold %q", w)
		}
	}
	series := parseBody(t, body(f))
	if v := series[`laug_lease_info{cell="1dafb33b",life="1792206000",block="0",backend="compose",server_tag="1.31.0"}`]; v != 1 {
		t.Errorf("no laug_lease_info for the hand cell: %v", series)
	}
	if requests.Load() == 0 {
		t.Fatal("follow never read /status outside a gate")
	}

	status.Store("testdata/status/reset.json")
	now = now.Add(10 * time.Minute)
	f.poll()
	if b := read(t, filepath.Join(targets, "cell-1dafb33b.json")); !strings.Contains(b, `"life": "1792206600"`) {
		t.Errorf("after a reset: %s, want life 1792206600", b)
	}
	if _, err := os.Stat(filepath.Join(targets, "cell-5e5e5e5e.json")); err == nil {
		t.Errorf("a cell /status no longer lists kept its target")
	}

	status.Store("")
	for i := 1; i <= 3; i++ {
		f.poll()
		_, err := os.Stat(filepath.Join(targets, "cell-1dafb33b.json"))
		if (i < 3) != (err == nil) {
			t.Errorf("after %d failed reads the hand target exists: %v", i, err == nil)
		}
	}
	if v := parseBody(t, body(f))["laug_follow_laugd_errors_total"]; v != 3 {
		t.Errorf("laug_follow_laugd_errors_total %v, want 3", v)
	}
	// No laugd on -pool at all counts as a failed read too, without a request.
	status.Store("testdata/status/hand.json")
	f.poll()
	os.RemoveAll(filepath.Join(root, "proc", "2001"))
	n := requests.Load()
	for i := 0; i < 3; i++ {
		f.poll()
	}
	if requests.Load() != n || len(listDir(t, targets)) != 1 {
		t.Errorf("with no laugd: %d requests, targets %v; want none and .expires.json alone", requests.Load()-n, keys(listDir(t, targets)))
	}
}

// TestFakeLaugdUnaskedWhileGateOpen: while the laugd on -pool is a gate's, a fake gate with no done line, follow
// sends it 0 requests in 20 polls; the same laugd with its stdout on a terminal is asked at every poll; a laugd that
// is a gate's whose host lock is held is asked nothing either.
func TestFakeLaugdUnaskedWhileGateOpen(t *testing.T) {
	dir := gateCopy(t)
	b := read(t, filepath.Join(dir, "timeline.jsonl"))
	write(t, filepath.Join(dir, "timeline.jsonl"), b[:strings.Index(b, `{"step":"wave end"`)]) // the wave still runs
	write(t, filepath.Join(dir, "laugd.log"), "")
	root := fakeRoot(t)
	pool, requests, status := countingLaugd(t)
	status.Store("testdata/status/hand.json")
	addLaugd(t, root, 3001, pool, filepath.Join(dir, "laugd.log"))
	now := time.Unix(1792203000, 0)
	f := testFollower(t, obsDir(t), root, pool, nil, &now)
	for i := 0; i < 20; i++ {
		f.poll()
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("an open gate's laugd got %d requests, want 0", n)
	}
	if !strings.Contains(body(f), `laug_gate_step_active{gate_id="g20261017-020417",step="ended",item="pl"} 1`) {
		t.Errorf("follow did not follow the open gate")
	}

	root2 := fakeRoot(t)
	addLaugd(t, root2, 3002, pool, "/dev/pts/1")
	f2 := testFollower(t, obsDir(t), root2, pool, nil, &now)
	for i := 0; i < 5; i++ {
		f2.poll()
	}
	if n := requests.Load(); n != 5 {
		t.Errorf("a hand laugd got %d requests in 5 polls, want 5", n)
	}

	lock := filepath.Join(t.TempDir(), "laug-gate.lock")
	fd := holdLock(t, lock)
	defer syscall.Close(fd)
	f3 := testFollower(t, obsDir(t), root2, pool, []string{lock}, &now)
	f3.root = "/" // the real /proc/locks, which lists this test's flock
	for i := 0; i < 5; i++ {
		f3.poll()
	}
	if n := requests.Load(); n != 5 {
		t.Errorf("with the host lock held a laugd got %d more requests, want 0", n-5)
	}
}

// TestFollowedAfterExec: gate/run.sh starts laugd as a background setsid, so its pid runs bash, then setsid, then
// laugd, its stdout already on the gate's laugd.log. A gate whose laugd was bash at follow's first poll is followed
// from the second, and that laugd is asked nothing (internal/gateopen's TestLaugdSeenAfterExec holds the scanner).
func TestFollowedAfterExec(t *testing.T) {
	dir := gateCopy(t)
	write(t, filepath.Join(dir, "laugd.log"), "")
	pool, requests, _ := countingLaugd(t)
	laugd := strings.Join([]string{"bin/laug", "pool", "serve", "-pid-file", "none", "-listen", pool, "-min", "0"}, "\x00") + "\x00"
	bash := "bash\x00gate/run.sh\x00"
	root2 := fakeRoot(t)
	addLaugd(t, root2, 6000, pool, filepath.Join(dir, "laugd.log"))
	write(t, filepath.Join(root2, "proc", "6000", "cmdline"), bash)
	now := time.Unix(1792203254+60, 0)
	f := testFollower(t, obsDir(t), root2, pool, nil, &now)
	f.poll()
	if f.gates[gateID] != nil {
		t.Fatalf("a gate followed while its pid runs bash")
	}
	write(t, filepath.Join(root2, "proc", "6000", "cmdline"), laugd)
	f.poll()
	if f.gates[gateID] == nil || !strings.Contains(body(f), `laug_follow_gate_dir_found{gate_id="`+gateID+`"} 1`) {
		t.Errorf("the gate's laugd, bash at the first poll, is not followed at the second")
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d requests to the pool address, want 0", n)
	}
}

// holdLock opens path and takes an exclusive flock on it, as a gate takes the host lock.
func holdLock(t *testing.T, path string) int {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return fd
}

// TestGateRunning: follow -gate-running prints internal/gateopen's answer: exit 0 and its why line for an open gate's
// laugd, exit 1 and nothing once that laugd is gone.
func TestGateRunning(t *testing.T) {
	dir := gateCopy(t)
	write(t, filepath.Join(dir, "laugd.log"), "")
	tl := read(t, filepath.Join(dir, "timeline.jsonl"))
	root := fakeRoot(t)
	// The command line: exit 0 and the why, or exit 1 and nothing.
	var out bytes.Buffer
	write(t, filepath.Join(dir, "timeline.jsonl"), tl[:strings.Index(tl, `{"step":"done"`)])
	addLaugd(t, root, 5003, "127.0.0.1:7700", filepath.Join(dir, "laugd.log"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if code := run(context.Background(), []string{"-gate-running", "-root", root}, &out, &bytes.Buffer{}); code != 0 ||
		out.String() != "gate "+gateID+"'s laugd is on 127.0.0.1:7700 and its timeline has no done line\n" {
		t.Errorf("follow -gate-running, an open gate: exit %d, %q", code, out.String())
	}
	os.RemoveAll(filepath.Join(root, "proc", "5003"))
	out.Reset()
	if code := run(context.Background(), []string{"-gate-running", "-root", root}, &out, &bytes.Buffer{}); code != 1 || out.Len() != 0 {
		t.Errorf("follow -gate-running, no gate: exit %d, %q", code, out.String())
	}
}

// TestStaleTargetGuard: at its first poll follow removes a target file a follow before it left, and its stale
// .expires.json; a gate lease past its expires_at is removed before the release line; .expires.json lists exactly the
// files there are.
func TestStaleTargetGuard(t *testing.T) {
	obs := obsDir(t)
	targets := filepath.Join(obs, "targets")
	write(t, filepath.Join(targets, "cell-deadbeef.json"), `[{"targets": ["127.0.0.1:9090"], "labels": {"cell": "deadbeef"}}]`+"\n")
	write(t, filepath.Join(targets, ".expires.json"), `{"cell-deadbeef.json":1}`+"\n")
	write(t, filepath.Join(targets, "keep.txt"), "not a target file\n")
	dir := gateCopy(t)
	tl := read(t, filepath.Join(dir, "timeline.jsonl"))
	write(t, filepath.Join(dir, "timeline.jsonl"), tl[:strings.Index(tl, `{"step":"ended pl"`)]) // pl still runs
	write(t, filepath.Join(dir, "laugd.log"), "")
	root := fakeRoot(t)
	addLaugd(t, root, 6001, "127.0.0.1:7700", filepath.Join(dir, "laugd.log"))
	now := time.Unix(1792203000, 0)
	f := testFollower(t, obs, root, "127.0.0.1:7700", nil, &now)
	f.poll()
	got := listDir(t, targets)
	if strings.Join(keys(got), " ") != ".expires.json cell-13880449.json cell-7ee6b0fa.json keep.txt omes-g20261017-020417-pl.json" {
		t.Errorf("after the first poll: %v", keys(got))
	}
	if got[".expires.json"] != `{"cell-13880449.json":1792203876,"cell-7ee6b0fa.json":1792203892,"omes-g20261017-020417-pl.json":1792203892}`+"\n" {
		t.Errorf(".expires.json: %s", got[".expires.json"])
	}
	now = time.Unix(1792203880, 0) // steady's lease has expired, pl's has not, and there is no release line
	f.poll()
	got = listDir(t, targets)
	if _, ok := got["cell-13880449.json"]; ok {
		t.Errorf("a lease past its expires_at kept its target")
	}
	if got[".expires.json"] != `{"cell-7ee6b0fa.json":1792203892,"omes-g20261017-020417-pl.json":1792203892}`+"\n" {
		t.Errorf(".expires.json: %s", got[".expires.json"])
	}
	if v := parseBody(t, body(f))["laug_follow_stale_targets"]; v != 0 {
		t.Errorf("laug_follow_stale_targets %v, want 0", v)
	}
}

// TestFollowFlags: -listen and -pool take 127.0.0.1 alone, and one mode at a time.
func TestFollowFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-listen", "0.0.0.0:7720", "-dir", t.TempDir()}, {"-listen", "localhost:7720", "-dir", t.TempDir()},
		{"-listen", ":7720", "-dir", t.TempDir()}, {"-listen", "127.0.0.1:7720"}, {"-gate-running", "-pool", "10.0.0.1:7700"},
		{"-gate-running", "-pool", "localhost:7700"}, {"-gate-running", "-openmetrics", "x"}, {}, {"-gate-running", "x"},
	} {
		var stderr bytes.Buffer
		if code := run(context.Background(), args, &bytes.Buffer{}, &stderr); code != 2 {
			t.Errorf("follow %s: exit %d, want 2; %s", strings.Join(args, " "), code, stderr.String())
		}
	}
	if code := run(context.Background(), []string{"-openmetrics", t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Errorf("-openmetrics on a directory with no timeline: exit %d, want 1", code)
	}
	_ = strconv.Itoa
}
