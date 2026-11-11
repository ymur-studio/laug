package gateopen

// The tests of gateopen. A gate's laugd is a fake proc tree in a temp dir, and a lock is a real flock on a file in a
// temp dir: no test touches a host lock path or a laugd. testdata holds a gate directory's gate.json and
// timeline.jsonl (testdata/README.md).

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const gateID = "g20261017-020417"

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// gateCopy copies testdata's gate into <tmp>/results/<gate id> and returns that directory.
func gateCopy(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "results", gateID)
	for _, name := range []string{"gate.json", "timeline.jsonl"} {
		write(t, filepath.Join(dir, name), read(t, filepath.Join("testdata", gateID, name)))
	}
	return dir
}

// fakeRoot is a proc tree with an empty locks file and no process.
func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "proc", "locks"), "")
	return root
}

// addLaugd puts a process in the fake tree that runs laugd on addr with its stdout on out, as gate/run.sh starts it.
func addLaugd(t *testing.T, root string, pid int, addr, out string) {
	t.Helper()
	d := filepath.Join(root, "proc", strconv.Itoa(pid))
	write(t, filepath.Join(d, "cmdline"), strings.Join([]string{"bin/laug", "pool", "serve", "-pid-file", "none", "-listen", addr,
		"-min", "0", "-max", "2", "-quota", "2", "-max-lease", "20m0s", "-server-tag", "1.31.0", "-backend", "compose"},
		"\x00")+"\x00")
	if err := os.MkdirAll(filepath.Join(d, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(d, "fd", "1")); err != nil {
		t.Fatal(err)
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

// TestLockPaths: the three places a gate takes its host lock, each once.
func TestLockPaths(t *testing.T) {
	for _, c := range []struct {
		xdg  string
		want string
	}{
		{"/run/user/1000/", "/run/user/1000/laug-gate.lock /tmp/laug-gate.lock"},
		{"/x", "/x/laug-gate.lock /run/user/1000/laug-gate.lock /tmp/laug-gate.lock"},
		{"", "/tmp/laug-gate.lock /run/user/1000/laug-gate.lock"},
	} {
		if got := strings.Join(LockPaths(c.xdg, 1000), " "); got != c.want {
			t.Errorf("XDG_RUNTIME_DIR %q: %s, want %s", c.xdg, got, c.want)
		}
	}
}

// TestGateOpen: GateOpen answers yes for each of three lock paths while a flock is held on it, taken in this
// process or by flock -n on an inherited descriptor whose process then exits (gate/run.sh's form), and for an open
// gate's laugd; no with every lock free, for a laugd whose gate has its done line, a laugd that is no gate's, a laugd
// on another port and no laugd. Nothing it does takes a lock: a flock -n by another process while it reads succeeds.
func TestGateOpen(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	var locks []string
	for _, d := range dirs {
		locks = append(locks, filepath.Join(d, "laug-gate.lock"))
	}
	none := "127.0.0.1:1" // no laugd listens here, so the real /proc gives no gate
	if why, open := GateOpen("/", none, locks, ""); open {
		t.Fatalf("no lock file yet: open, %s", why)
	}
	for i, l := range locks {
		fd := holdLock(t, l)
		why, open := GateOpen("/", none, locks, dirs[0])
		want := l + " is held"
		if i == 0 {
			want = "laug-gate.lock in XDG_RUNTIME_DIR is held"
		}
		if !open || why != want {
			t.Errorf("lock %d held in this process: open %v, %q; want %q", i, open, why, want)
		}
		syscall.Close(fd)
		if why, open := GateOpen("/", none, locks, dirs[0]); open {
			t.Errorf("lock %d released: still open, %s", i, why)
		}
	}
	if _, err := exec.LookPath("flock"); err == nil {
		for _, l := range locks {
			// The holder says held once its flock -n took the lock; a probe by flock(1) here would take the lock
			// itself and could make the holder's refuse, the race this probe must never cause.
			ctx, cancel := context.WithCancel(context.Background())
			cmd := exec.CommandContext(ctx, "bash", "-c", `exec 8>>"$1"; flock -n 8 || exit 3; echo held; exec sleep 30`, "holder", l)
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line := make([]byte, 5)
			if _, err := io.ReadFull(stdout, line); err != nil || string(line) != "held\n" {
				cancel()
				t.Fatalf("the holder did not take %s: %q %v", l, line, err)
			}
			if why, open := GateOpen("/", none, locks, ""); !open || why != l+" is held" {
				t.Errorf("flock -n 8 on %s, its process gone: open %v, %q", l, open, why)
			}
			cancel()
			cmd.Wait()
		}
	} else {
		t.Log("no flock binary: the inherited-descriptor form was not run")
	}
	// While GateOpen reads in a loop, a gate's own flock -n never fails.
	free := locks[1]
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				GateOpen("/", none, locks, "")
			}
		}
	}()
	refused := 0
	for i := 0; i < 300; i++ {
		fd, err := syscall.Open(free, syscall.O_WRONLY|syscall.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			refused++
		}
		syscall.Close(fd)
	}
	close(stop)
	if refused != 0 {
		t.Errorf("beside a loop of GateOpen, %d of 300 flock -n were refused, want 0", refused)
	}

	dir := gateCopy(t)
	write(t, filepath.Join(dir, "laugd.log"), "")
	tl := read(t, filepath.Join(dir, "timeline.jsonl"))
	root := fakeRoot(t)
	addLaugd(t, root, 5001, "127.0.0.1:7700", filepath.Join(dir, "laugd.log"))
	write(t, filepath.Join(dir, "timeline.jsonl"), tl[:strings.Index(tl, `{"step":"done"`)])
	if why, open := GateOpen(root, "127.0.0.1:7700", nil, ""); !open || why != "gate "+gateID+"'s laugd is on 127.0.0.1:7700 and its timeline has no done line" {
		t.Errorf("an open gate's laugd: open %v, %q", open, why)
	}
	if _, open := GateOpen(root, "127.0.0.1:7701", nil, ""); open {
		t.Errorf("the gate's laugd is on 7700, and -pool 7701 reads open")
	}
	write(t, filepath.Join(dir, "timeline.jsonl"), tl)
	if why, open := GateOpen(root, "127.0.0.1:7700", nil, ""); open {
		t.Errorf("a gate with its done line: open, %s", why)
	}
	os.RemoveAll(filepath.Join(root, "proc", "5001"))
	addLaugd(t, root, 5002, "127.0.0.1:7700", "/dev/pts/0")
	if why, open := GateOpen(root, "127.0.0.1:7700", nil, ""); open {
		t.Errorf("a laugd that is no gate's: open, %s", why)
	}
	// A proc tree with no locks file: a lock file that exists may be held, so a gate is open.
	if why, open := GateOpen(t.TempDir(), none, locks, ""); !open || why != locks[0]+" may be held, since /proc/locks cannot be read" {
		t.Errorf("an unreadable /proc/locks: open %v, %q", open, why)
	}
}

// TestLaugdSeenAfterExec: gate/run.sh starts laugd as a background setsid, so its pid runs bash, then setsid, then
// laugd, its stdout already on the gate's laugd.log. A poll that sees bash, or an empty command line, does not keep
// the pid from being the gate's laugd at the next poll; a pid seen at two polls keeps the second verdict.
func TestLaugdSeenAfterExec(t *testing.T) {
	dir := gateCopy(t)
	write(t, filepath.Join(dir, "laugd.log"), "")
	root := fakeRoot(t)
	pool := "127.0.0.1:7700"
	laugd := strings.Join([]string{"bin/laug", "pool", "serve", "-pid-file", "none", "-listen", pool, "-min", "0"}, "\x00") + "\x00"
	bash := "bash\x00gate/run.sh\x00"
	cmdline := func(pid int, s string) { write(t, filepath.Join(root, "proc", strconv.Itoa(pid), "cmdline"), s) }
	for pid, cmd := range map[int]string{5000: bash, 5001: "", 5002: bash} {
		addLaugd(t, root, pid, pool, filepath.Join(dir, "laugd.log"))
		cmdline(pid, cmd)
	}
	sc := &Scanner{Root: root, Pool: pool}
	pids := func() (out []string) {
		for _, l := range sc.Laugds() {
			if l.ID != gateID {
				t.Errorf("pid %d: gate %q, want %s", l.PID, l.ID, gateID)
			}
			out = append(out, strconv.Itoa(l.PID))
		}
		sort.Strings(out)
		return out
	}
	if got := pids(); len(got) != 0 {
		t.Errorf("the first poll, bash and an empty command line: laugd pids %v, want none", got)
	}
	cmdline(5000, laugd)
	cmdline(5001, laugd)
	if got := strings.Join(pids(), " "); got != "5000 5001" {
		t.Errorf("the second poll, 5000 and 5001 now laugd, 5002 still bash: laugd pids %q, want 5000 5001", got)
	}
	cmdline(5002, laugd)
	if got := strings.Join(pids(), " "); got != "5000 5001" {
		t.Errorf("the third poll: laugd pids %q, want 5000 5001, the second poll's verdicts kept", got)
	}
}

// TestLaugdOn: the pool is found by its command line, laug pool serve, as a person starts it and as the gate drivers
// start theirs with -pid-file none, by -listen in any of its four spellings, or by no -listen when the address is the
// default it takes; never another program, another address, a pool serve with its -listen elsewhere, or a process
// named laugd: the pool/laugd link is retired, and no laug runs under that name.
func TestLaugdOn(t *testing.T) {
	const other = "127.0.0.1:17790"
	for _, c := range []struct {
		argv string // the command line, split on spaces
		addr string
		want bool
	}{
		{"bin/laug pool serve -pid-file none -listen 127.0.0.1:7700 -min 0", "127.0.0.1:7700", true},
		{"/x/bin/laug pool serve --listen 127.0.0.1:7700", "127.0.0.1:7700", true},
		{"laug pool serve -min 0 -listen=" + other, other, true},
		{"laug pool serve --listen=" + other, other, true},
		{"laug pool serve -pid-file none -listen " + other, "127.0.0.1:7700", false},
		{"laug pool serve -pid-file none -min 0", "127.0.0.1:7700", true},
		{"laug pool serve -pid-file none -min 0", other, false},
		{"bin/laug pool serve -listen " + other + " -backend ../cells/backend-compose.sh", other, true},
		{"../bin/laug pool serve -min 3", "127.0.0.1:7700", true},
		{"laug pool serve -listen=" + other, "127.0.0.1:7700", false},
		{"laug pool serve", other, false},
		{"laug pool status -pool 127.0.0.1:7700", "127.0.0.1:7700", false},
		{"laug pool", "127.0.0.1:7700", false},
		{"go run ./cmd/laug pool serve -listen 127.0.0.1:7700", "127.0.0.1:7700", false},
		{"bash -c laug pool serve -listen 127.0.0.1:7700", "127.0.0.1:7700", false},
		{"pool/laugd -listen 127.0.0.1:7700", "127.0.0.1:7700", false},
		{"laugd -min 0", "127.0.0.1:7700", false},
		{"laugx pool serve -listen 127.0.0.1:7700", "127.0.0.1:7700", false},
		{"", "127.0.0.1:7700", false},
	} {
		var argv []string
		if c.argv != "" {
			argv = strings.Split(c.argv, " ")
		}
		if got := laugdOn(argv, c.addr); got != c.want {
			t.Errorf("laugdOn(%q, %s) = %t, want %t", c.argv, c.addr, got, c.want)
		}
	}
}
