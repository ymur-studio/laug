// Package gateopen answers whether a gate runs on this host, and why, read from the gate locks and the laugd
// processes and never by taking a lock or asking a laugd anything. A gate is open when a host lock is held
// (gate/lib.sh, GATE_HOST_LOCK), or when the laugd on the pool's address is a gate's and that gate's timeline has
// no done line. A gate's laugd is found by its stdout: gate/run.sh and gate/step.sh start it with its stdout on
// <gate dir>/laugd.log, so a readlink of its fd 1 names the gate's directory wherever the checkout is. laug pool
// down asks it before it removes cells, and bin/obs follow -gate-running prints its answer.
package gateopen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/ymur-studio/laug/internal/pool"
)

var gateIDRe = regexp.MustCompile(`^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)

// IsGateID says whether id has a gate id's form: g, the date, -, the time, and an optional -<n>.
func IsGateID(id string) bool { return gateIDRe.MatchString(id) }

// Laugd is a process that runs laugd on the pool's address, and the gate directory its stdout names, if any.
type Laugd struct {
	PID     int
	Dir, ID string // empty: a laugd that is no gate's (laug pool serve, or a root laugd whose fd cannot be read)
}

// laugdOn says whether argv runs the pool listening on addr: laug pool serve, argv[0]'s base name laug and then pool
// and serve, as a person starts it and gate/run.sh and gate/step.sh start a gate's (bin/laug pool serve -pid-file
// none). The arguments after that hold -listen addr (or --listen, or either with =), or no -listen at all when addr is
// pool.DefaultListen, the address it takes without one. go run's parent, argv[0] go, is not the pool.
func laugdOn(argv []string, addr string) bool {
	if len(argv) < 3 || filepath.Base(argv[0]) != "laug" || argv[1] != "pool" || argv[2] != "serve" {
		return false
	}
	args := argv[3:]
	listen := false
	for i, a := range args {
		switch {
		case (a == "-listen" || a == "--listen") && i+1 < len(args) && args[i+1] == addr:
			return true
		case a == "-listen="+addr || a == "--listen="+addr:
			return true
		case a == "-listen" || a == "--listen" || strings.HasPrefix(a, "-listen=") || strings.HasPrefix(a, "--listen="):
			listen = true
		}
	}
	return !listen && addr == pool.DefaultListen
}

func readCmdline(root string, pid int) []string {
	b, err := os.ReadFile(filepath.Join(root, "proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// Scanner finds laugd on Pool in the proc tree under Root, "/" on a host. A pid's command line is read at the first
// two polls that see it, and the second verdict kept until the pid leaves the listing, as cellstat keeps Omes's:
// gate/run.sh and gate/step.sh start laugd as a background setsid, so one pid is bash, then setsid, then laugd, and
// a poll in between sees bash or setsid, or the empty command line between an exec's new comm and its new argv
// (fs/exec.c; source only). In 20 background setsid starts each pid ran its program by its parent's first
// read, 1.2 to 1.8 ms after the fork (seen on the dev host), so the second poll, 2 s on, sees laugd. A
// process that execs laugd later than one poll after it was first seen is missed; neither driver starts one so.
type Scanner struct {
	Root, Pool string
	seen       map[int]verdict
}

// verdict is whether a pid runs laugd on Pool; final once it came from the second poll that saw the pid.
type verdict struct{ on, final bool }

// Laugds lists the processes that run laugd on Pool now, each with the gate its stdout names, if any.
func (s *Scanner) Laugds() []Laugd {
	ents, err := os.ReadDir(filepath.Join(s.Root, "proc"))
	if err != nil {
		return nil
	}
	if s.seen == nil {
		s.seen = map[int]verdict{}
	}
	var out []Laugd
	live := map[int]bool{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		live[pid] = true
		v, ok := s.seen[pid]
		if !v.final {
			v = verdict{on: laugdOn(readCmdline(s.Root, pid), s.Pool), final: ok}
			s.seen[pid] = v
		}
		if !v.on {
			continue
		}
		lp := Laugd{PID: pid}
		// A same-user process's fd links read without ptrace rights under Yama scope 1 (seen on the dev host and the
		// lab host). A root laugd's do not: it has no gate here, and its leases are read from /status.
		if t, err := os.Readlink(filepath.Join(s.Root, "proc", strconv.Itoa(pid), "fd", "1")); err == nil {
			lp.Dir, lp.ID, _ = gateDirOf(t)
		}
		out = append(out, lp)
	}
	for pid := range s.seen {
		if !live[pid] {
			delete(s.seen, pid)
		}
	}
	return out
}

// gateDirOf takes an fd link and gives the gate directory when the link is <dir>/laugd.log, dir's name is a gate id
// and dir holds gate.json, which the drivers write before they start laugd. A deleted file is no gate.
func gateDirOf(target string) (dir, id string, ok bool) {
	if !filepath.IsAbs(target) || strings.HasSuffix(target, " (deleted)") || filepath.Base(target) != "laugd.log" {
		return "", "", false
	}
	dir = filepath.Dir(target)
	id = filepath.Base(dir)
	if !gateIDRe.MatchString(id) {
		return "", "", false
	}
	if fi, err := os.Stat(filepath.Join(dir, "gate.json")); err != nil || !fi.Mode().IsRegular() {
		return "", "", false
	}
	return dir, id, true
}

// LockPaths is the host lock in every place a gate may take it (gate/lib.sh, GATE_HOST_LOCK): XDG_RUNTIME_DIR's,
// /run/user/<uid>'s whether XDG_RUNTIME_DIR is set or not, and /tmp's, each once, since a shell and a batch can differ
// in XDG_RUNTIME_DIR and a batch may have none.
func LockPaths(xdg string, uid int) []string {
	var out []string
	seen := map[string]bool{}
	dir := xdg
	if dir == "" {
		dir = "/tmp"
	}
	for _, d := range []string{dir, "/run/user/" + strconv.Itoa(uid), "/tmp"} {
		p := filepath.Join(d, "laug-gate.lock")
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// lockKey is a file's device and inode as /proc/locks writes them, <major>:<minor>:<inode>, major and minor in hex:
// glibc's major() and minor() of st_dev.
func lockKey(path string) (string, bool) {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return "", false
	}
	dev := uint64(st.Dev)
	major := ((dev >> 8) & 0xfff) | ((dev >> 32) &^ 0xfff)
	minor := (dev & 0xff) | ((dev >> 12) &^ 0xff)
	return fmt.Sprintf("%02x:%02x:%d", major, minor, st.Ino), true
}

// flockKeys gives the <major>:<minor>:<inode> of each FLOCK in text, a /proc/locks or an fdinfo; a line that starts
// with -> is a waiter, not a holder.
func flockKeys(text string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "lock:" {
			f = f[1:]
		}
		if len(f) < 2 || f[1] != "FLOCK" {
			continue
		}
		for _, x := range f[2:] {
			if strings.Count(x, ":") == 2 {
				out[x] = true
				break
			}
		}
	}
	return out
}

// HeldLock says which of files a FLOCK is held on, read and never taken: a probe that took the lock, even shared and
// for a moment, made a gate's own flock -n refuse (docs/tables/records.jsonl#cells/results/2026-10-17-obs-offline.txt,
// section 2). A file counts as held when <root>/proc/locks lists a FLOCK on its device and inode, or the fdinfo of an
// open descriptor of it does: gate/run.sh takes its lock with flock -n 8, whose process exits at once, and /proc/locks
// then leaves the lock out while the holder's fdinfo lists it (seen on the dev host). Only a descriptor
// whose link names one of files has its fdinfo read, so a pass costs a readlink a descriptor, not a read. A holder of
// another user is seen in /proc/locks only; a /proc/locks that cannot be read counts as held. A file that does not
// exist is not made.
func HeldLock(root string, files []string) (why string, held bool) {
	keys := map[string]string{}
	for _, f := range files {
		if k, ok := lockKey(f); ok {
			keys[k] = f
		}
	}
	if len(keys) == 0 {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(root, "proc", "locks"))
	if err != nil {
		for _, f := range files {
			if _, ok := lockKey(f); ok {
				return f + " may be held, since /proc/locks cannot be read", true
			}
		}
	}
	for k := range flockKeys(string(b)) {
		if f, ok := keys[k]; ok {
			return f + " is held", true
		}
	}
	names := map[string]bool{}
	for _, f := range keys {
		names[f] = true
		if r, err := filepath.EvalSymlinks(f); err == nil {
			names[r] = true // an fd link names the file's real path
		}
	}
	ents, _ := os.ReadDir(filepath.Join(root, "proc"))
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		fdDir := filepath.Join(root, "proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			t, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !names[t] {
				continue
			}
			info, err := os.ReadFile(filepath.Join(root, "proc", e.Name(), "fdinfo", fd.Name()))
			if err != nil {
				continue
			}
			for k := range flockKeys(string(info)) {
				if f, ok := keys[k]; ok {
					return f + " is held", true
				}
			}
		}
	}
	return "", false
}

// lockName is how a lock is named on stdout and in logs: XDG_RUNTIME_DIR's by that name.
func lockName(why, xdg string) string {
	if xdg != "" {
		if p := filepath.Join(xdg, "laug-gate.lock"); strings.HasPrefix(why, p+" ") {
			return "laug-gate.lock in XDG_RUNTIME_DIR" + strings.TrimPrefix(why, p)
		}
	}
	return why
}

// timelineDone reports a gate dir's done line: its wall clock, and whether there is one. Only whole lines count.
func timelineDone(dir string) (float64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "timeline.jsonl"))
	if err != nil {
		return 0, false
	}
	for _, s := range wholeLines(b) {
		var l struct {
			Step string   `json:"step"`
			Wall *float64 `json:"wall"`
		}
		if json.Unmarshal(s, &l) == nil && l.Step == "done" && l.Wall != nil {
			return *l.Wall, true
		}
	}
	return 0, false
}

// GateOpen asks whether a gate is open, as laug pool down does before it removes cells and the stack does before it
// starts, stops, purges or tests anything: a gate is open when one of locks is held, read in the proc tree under root,
// or the laugd on pool is a gate's and that gate's timeline has no done line. xdg is XDG_RUNTIME_DIR, by which name
// its lock is given. why names the lock, or the gate by its id: never a gate directory's path.
func GateOpen(root, pool string, locks []string, xdg string) (why string, open bool) {
	if w, held := HeldLock(root, locks); held {
		return lockName(w, xdg), true
	}
	sc := Scanner{Root: root, Pool: pool}
	for _, l := range sc.Laugds() {
		if l.Dir == "" {
			continue
		}
		if _, done := timelineDone(l.Dir); !done {
			return "gate " + l.ID + "'s laugd is on " + pool + " and its timeline has no done line", true
		}
	}
	return "", false
}

// wholeLines splits b at newlines and drops what follows the last one: a line still being written.
func wholeLines(b []byte) [][]byte {
	var out [][]byte
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return out
		}
		if line := bytes.TrimSpace(b[:i]); len(line) > 0 {
			out = append(out, line)
		}
		b = b[i+1:]
	}
}
