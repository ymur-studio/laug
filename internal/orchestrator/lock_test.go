package orchestrator

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// A record lock is per process, and F_GETLK does not report the caller's own, so a child process holds the lock:
// this test binary run again with LAUG_HOLD_LOCK set. It takes the lock with LockWorker as LAUG_HOLD_ID, or, with
// LAUG_HOLD_ID empty, by fcntl alone and writes nothing, as a worker built before the lock held an identity did.
func TestMain(m *testing.M) {
	if dir := os.Getenv("LAUG_HOLD_LOCK"); dir != "" {
		var f *os.File
		var err error
		if id := os.Getenv("LAUG_HOLD_ID"); id != "" {
			f, err = LockWorker(dir, id)
		} else if f, err = os.OpenFile(filepath.Join(dir, WorkerLock), os.O_RDWR|os.O_CREATE, 0o644); err == nil {
			err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart})
		}
		if err != nil {
			os.Stdout.WriteString("refused: " + err.Error() + "\n")
			os.Exit(3)
		}
		os.Stdout.WriteString("locked\n")
		bufio.NewReader(os.Stdin).ReadString('\n') // hold it until the parent closes stdin
		f.Close()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// holder starts a child that takes the lock in dir as id ("" for no identity), and returns it once it has answered.
func holder(t *testing.T, dir, id string) (*exec.Cmd, func(), string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "LAUG_HOLD_LOCK="+dir, "LAUG_HOLD_ID="+id)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(out).ReadString('\n')
	return cmd, func() { in.Close() }, strings.TrimSpace(line)
}

func TestWorkerLock(t *testing.T) {
	dir := t.TempDir()
	if pid, err := WorkerPid(dir); pid != 0 || err != nil {
		t.Fatalf("WorkerPid with no lock file = %d, %v; want 0", pid, err)
	}

	a, releaseA, line := holder(t, dir, "laug-worker@abc1234")
	if line != "locked" {
		t.Fatalf("first worker: %q", line)
	}
	if pid, err := WorkerPid(dir); pid != a.Process.Pid || err != nil {
		t.Fatalf("WorkerPid = %d, %v; want the holder, %d", pid, err, a.Process.Pid)
	}
	b, releaseB, line := holder(t, dir, "laug-worker@abc1234")
	releaseB()
	b.Wait()
	if !strings.Contains(line, "already runs") || !strings.Contains(line, "pid "+strconv.Itoa(a.Process.Pid)) {
		t.Fatalf("second worker was not refused with the first one's pid: %q", line)
	}

	// The first exits normally: the lock is free, though the file stays.
	releaseA()
	a.Wait()
	if pid, err := WorkerPid(dir); pid != 0 || err != nil {
		t.Fatalf("WorkerPid after the holder exited = %d, %v; want 0", pid, err)
	}

	// A worker killed with SIGKILL runs no defer; the kernel still drops its lock.
	c, releaseC, line := holder(t, dir, "laug-worker@abc1234")
	defer releaseC()
	if line != "locked" {
		t.Fatalf("a worker after a clean exit was refused: %q", line)
	}
	c.Process.Signal(syscall.SIGKILL)
	c.Wait()
	if pid, err := WorkerPid(dir); pid != 0 || err != nil {
		t.Fatalf("WorkerPid after SIGKILL = %d, %v; want 0", pid, err)
	}
	f, err := LockWorker(dir, "laug-worker@abc1234")
	if err != nil {
		t.Fatalf("LockWorker after SIGKILL: %v", err)
	}
	f.Close()
}

// The holder's line: written once the lock is taken, read back while it is held, left alone by a worker refused, and
// read as no identity when no process holds the lock or the line names another pid.
func TestWorkerLockIdentity(t *testing.T) {
	dir := t.TempDir()
	line := func() string {
		b, err := os.ReadFile(filepath.Join(dir, WorkerLock))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if h, err := WorkerHolder(dir); h != (Holder{}) || err != nil {
		t.Fatalf("WorkerHolder with no lock file = %+v, %v; want none", h, err)
	}
	for _, bad := range []string{"", "laug-worker@abc1234 x", "laug-worker@abc1234\n"} {
		if f, err := LockWorker(dir, bad); err == nil {
			f.Close()
			t.Errorf("LockWorker took the lock as %q", bad)
		}
	}

	a, releaseA, ans := holder(t, dir, "laug-worker@abc1234-dirty")
	if ans != "locked" {
		t.Fatalf("first worker: %q", ans)
	}
	want := Holder{Pid: a.Process.Pid, Identity: "laug-worker@abc1234-dirty"}
	if h, err := WorkerHolder(dir); h != want || err != nil {
		t.Fatalf("WorkerHolder = %+v, %v; want %+v", h, err, want)
	}
	aLine := strconv.Itoa(a.Process.Pid) + " laug-worker@abc1234-dirty\n"
	if got := line(); got != aLine {
		t.Fatalf("the lock file holds %q; want %q", got, aLine)
	}
	b, releaseB, ans := holder(t, dir, "laug-worker@0000000")
	releaseB()
	b.Wait()
	if !strings.HasPrefix(ans, "refused: ") {
		t.Fatalf("second worker: %q", ans)
	}
	if h, err := WorkerHolder(dir); h != want || err != nil || line() != aLine {
		t.Fatalf("after a worker was refused: %+v, %v, the file %q; want %+v, %q", h, err, line(), want, aLine)
	}

	// The holder exits: the file keeps its line, and no process holds the lock, so there is no identity.
	releaseA()
	a.Wait()
	if h, err := WorkerHolder(dir); h != (Holder{}) || err != nil || line() != aLine {
		t.Fatalf("after the holder exited: %+v, %v, the file %q; want none, the line kept", h, err, line())
	}

	// A holder that writes no line (a build before the identity) leaves the last worker's line, which names its pid.
	c, releaseC, ans := holder(t, dir, "")
	defer func() { releaseC(); c.Wait() }()
	if ans != "locked" {
		t.Fatalf("a holder with no identity: %q", ans)
	}
	if h, err := WorkerHolder(dir); h != (Holder{Pid: c.Process.Pid}) || err != nil {
		t.Fatalf("WorkerHolder over another pid's line = %+v, %v; want pid %d and no identity", h, err, c.Process.Pid)
	}
	if pid, err := WorkerPid(dir); pid != c.Process.Pid || err != nil {
		t.Fatalf("WorkerPid = %d, %v; want %d", pid, err, c.Process.Pid)
	}
}
