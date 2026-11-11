package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// WorkerLock is the file this checkout's worker holds a write lock on, in RunDir. The lock is a POSIX record lock
// (fcntl F_SETLK): the kernel drops it when the worker exits, by SIGKILL too, and F_GETLK names the process that
// holds it. So a held lock always means a live worker, and a stale file never refuses a new one.
//
// Once it holds the lock, the worker writes one line into the file, "<pid> <identity>", so the starter can ask which
// build holds it. The server's poller list cannot say: it lists a stopped worker for about 5 min after its last poll
// (docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt section 14), so another commit's
// worker held the lock beside HEAD's stale poller and the starter let the gate start
// (docs/tables/records.jsonl#cells/results/2026-10-16-gate6-step6.txt, section 2.3, item 5, another commit's worker).
const WorkerLock = "worker.lock"

// LockWorker takes this checkout's worker lock before the worker dials, or says which pid holds it. Two workers
// started at once cannot both pass: the kernel gives the lock to one. Once the lock is its own, it truncates the file
// and writes "<pid> <identity>\n"; a worker refused leaves the holder's line alone. The caller keeps the returned file
// open for its whole life and never opens the lock file again: closing any descriptor of a file drops the process's
// record locks on it.
func LockWorker(runDir, identity string) (*os.File, error) {
	if identity == "" || strings.ContainsAny(identity, " \t\r\n") {
		return nil, fmt.Errorf("worker identity %q: one word, as WorkerIdentity makes it", identity)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(runDir, WorkerLock)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk); err != nil {
		f.Close()
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES) {
			pid, _ := WorkerPid(runDir)
			return nil, fmt.Errorf("a worker for this checkout already runs, pid %d (it holds the lock on %s)", pid, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("truncate %s: %w", path, err)
	}
	if _, err := f.WriteAt([]byte(fmt.Sprintf("%d %s\n", os.Getpid(), identity)), 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("write the identity into %s: %w", path, err)
	}
	return f, nil
}

// Holder is the process that holds this checkout's worker lock: its pid, from the kernel, and the identity it wrote
// into the lock file. Identity is "" when the file's line names another pid, or none: a worker built before the lock
// held an identity writes nothing, so the line left is the worker's before it; and a worker writes its line just after
// it takes the lock.
type Holder struct {
	Pid      int
	Identity string
}

// WorkerHolder returns the process that holds this checkout's worker lock, or a zero Holder when no process does,
// whatever the file holds. It asks the kernel (F_GETLK) and takes nothing, so a worker starting at the same moment is
// not refused. The worker itself never calls it: F_GETLK does not report the caller's own lock, and closing the
// descriptor would drop it.
func WorkerHolder(runDir string) (Holder, error) {
	f, err := os.Open(filepath.Join(runDir, WorkerLock))
	if errors.Is(err, fs.ErrNotExist) {
		return Holder{}, nil
	}
	if err != nil {
		return Holder{}, err
	}
	defer f.Close()
	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		return Holder{}, fmt.Errorf("F_GETLK %s: %w", f.Name(), err)
	}
	if lk.Type == syscall.F_UNLCK {
		return Holder{}, nil
	}
	h := Holder{Pid: int(lk.Pid)}
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return h, fmt.Errorf("read %s: %w", f.Name(), err)
	}
	pid, id, ok := strings.Cut(strings.TrimSuffix(string(b), "\n"), " ")
	if ok && pid == strconv.Itoa(h.Pid) && id != "" && !strings.ContainsAny(id, " \t\r\n") {
		h.Identity = id
	}
	return h, nil
}

// WorkerPid returns the pid of the process that holds this checkout's worker lock, or 0 when no process does: the
// probe samples it.
func WorkerPid(runDir string) (int, error) {
	h, err := WorkerHolder(runDir)
	return h.Pid, err
}
