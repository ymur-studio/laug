package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// prctl is Linux's alone, so testRunbookZombie lives in this file, and TestRunbook reaches it through rbZombie.
func init() { rbZombie = testRunbookZombie }

// testRunbookZombie: a block that leaves only a zombie in its group holds. The block's sh forks a sleep, then execs a
// sleep that never waits for it, so the first is a zombie from 0.1 s on; the second ends at 1 s, and bash with it.
// The zombie, an orphan then, goes to the nearest subreaper above it, this test, which reaps it only at the end, as a
// PID 1 that reaps late would. kill(-pgid, 0) answers for it all along: the control holds that, so the test cannot
// pass for want of a zombie.
func testRunbookZombie(t *testing.T) {
	// prctl(PR_SET_CHILD_SUBREAPER), 36 in linux/prctl.h: an orphan below this process comes to it, not to PID 1.
	// The control's parent pid shows it took.
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 36, 1, 0); e != 0 {
		t.Fatalf("prctl PR_SET_CHILD_SUBREAPER: %v", e)
	}
	t.Cleanup(func() { _, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, 36, 0, 0) })
	saved := rbWait
	rbWait = 200 * time.Millisecond
	t.Cleanup(func() { rbWait = saved })
	dir := t.TempDir()
	page := rbPage(t, dir, "4-zombie.md",
		"```sh\necho $$ > group\nsh -c 'sleep 0.1 & echo $! > zombie; exec sleep 1'\n```\n")
	code, out, _ := runApp(t, rbApp(dir), "runbook", "check", page)
	z, group := rbPid(t, dir, "zombie"), rbPid(t, dir, "group")
	t.Cleanup(func() {
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(z, &ws, 0, nil)
	})
	f := procFields(strconv.Itoa(z))
	if len(f) < 3 || f[0] != "Z" || f[1] != strconv.Itoa(os.Getpid()) || f[2] != strconv.Itoa(group) ||
		syscall.Kill(-group, 0) != nil {
		t.Fatalf("the control: pid %d is no zombie of this test's in group %d that kill(-%d, 0) answers for: "+
			"/proc stat fields from the third %q, kill %v", z, group, group, f, syscall.Kill(-group, 0))
	}
	if code != exitDone || !strings.Contains(out, "\nok       "+page+":1 ") ||
		!strings.HasSuffix(out, "\nlaug · runbook check: 1 of 1 blocks held\n") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// mainExitEnv marks the helper TestGroupRuns starts: this test binary again, whose init below ends its main thread
// alone and leaves the others to run.
const mainExitEnv = "LAUG_TEST_MAIN_THREAD_EXIT"

// Package init runs on the main thread, locked to it, so the exit there is the main thread's. SYS_EXIT, exit(2), ends
// one thread, where exit_group(2), as os.Exit calls it, ends them all. Syscall, not RawSyscall, marks the goroutine as
// in a system call first, so the runtime can take its P and run the other goroutine, at GOMAXPROCS=1 too. That
// goroutine ends the helper 30 s on, should no SIGKILL come. A helper that is not on its main thread exits 3, and
// TestGroupRuns's control reads that as a zombie alone.
func init() {
	if os.Getenv(mainExitEnv) != "1" {
		return
	}
	if syscall.Gettid() != os.Getpid() {
		os.Exit(3)
	}
	go func() {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}()
	_, _, _ = syscall.Syscall(syscall.SYS_EXIT, 0, 0, 0)
}

// TestGroupRuns: a process whose main thread alone has exited reads Z in /proc/<pid>/stat, the main thread's state,
// and still runs, so groupRuns answers true for its group. Once SIGKILL has ended its threads, and before the wait
// reaps it, the group holds a zombie alone and groupRuns answers false, though kill(-pgid, 0) still answers for it.
// The controls hold each state, so the test cannot pass for want of one.
func TestGroupRuns(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// -test.run=^$: a helper whose init did not take runs no test.
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), mainExitEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	p := strconv.Itoa(pid)
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	var f []string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if f = procFields(p); len(f) > 0 && f[0] == "Z" {
			break
		}
	}
	ts := taskStates(p)
	if len(f) < 3 || f[0] != "Z" || f[2] != p || ts[p] != "Z" || len(ts) < 2 || syscall.Kill(-pid, 0) != nil {
		t.Fatalf("the control: pid %d is no process in group %d whose main thread alone reads Z: /proc stat fields "+
			"from the third %q, its threads' states %v, kill %v", pid, pid, f, ts, syscall.Kill(-pid, 0))
	}
	if !groupRuns(pid) {
		t.Errorf("groupRuns(%d) = false for a group whose process runs %d threads past its main one: states %v",
			pid, len(ts)-1, ts)
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ts = taskStates(p); len(ts) == 1 {
			break
		}
	}
	if f = procFields(p); len(f) == 0 || f[0] != "Z" || len(ts) != 1 || ts[p] != "Z" || syscall.Kill(-pid, 0) != nil {
		t.Fatalf("the control: pid %d is no zombie of one thread that kill(-%d, 0) answers for: /proc stat fields "+
			"from the third %q, its threads' states %v, kill %v", pid, pid, f, ts, syscall.Kill(-pid, 0))
	}
	if groupRuns(pid) {
		t.Errorf("groupRuns(%d) = true for a group that holds a zombie alone", pid)
	}
	waited = true
	_ = cmd.Wait()
}

// taskStates maps each thread of pid, an entry of /proc/<pid>/task, to the state its stat reads.
func taskStates(pid string) map[string]string {
	ts, _ := os.ReadDir("/proc/" + pid + "/task")
	m := map[string]string{}
	for _, e := range ts {
		if f := procFields(pid + "/task/" + e.Name()); len(f) > 0 {
			m[e.Name()] = f[0]
		}
	}
	return m
}
