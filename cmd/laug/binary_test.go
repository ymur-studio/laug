package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	builtOnce sync.Once
	built     string
	buildErr  error
)

// laugBinary builds this package once into a temp dir, as make build builds bin/laug, and returns the dir. TestMain
// removes it.
func laugBinary(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		dir, err := os.MkdirTemp("", "laug-binary-")
		if err != nil {
			buildErr = err
			return
		}
		built = dir
		out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "laug"), ".").CombinedOutput()
		if err != nil {
			buildErr = errors.New("go build: " + err.Error() + "\n" + string(out))
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return built
}

// started is a process of the built binary whose first stdout line has been read.
type started struct {
	cmd    *exec.Cmd
	first  string
	stderr *syncBuffer
}

func startBinary(t *testing.T, dir, name string, args ...string) *started {
	t.Helper()
	cmd := exec.Command(filepath.Join(laugBinary(t), name), args...)
	cmd.Dir = dir
	stdout := &syncBuffer{}
	s := &started{cmd: cmd, stderr: &syncBuffer{}}
	cmd.Stdout, cmd.Stderr = stdout, s.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(stdout.String(), "\n"); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s %q: no first line within 5 s\nstderr:\n%s", name, args, s.stderr.String())
		}
	}
	s.first = firstLine(stdout.String())
	return s
}

// signal sends sig and returns the exit code and how long the process took to exit.
func (s *started) signal(t *testing.T, sig syscall.Signal) (int, time.Duration) {
	t.Helper()
	start := time.Now()
	if err := s.cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, time.Since(start)
	case <-time.After(5 * time.Second):
		t.Fatalf("no exit within 5 s of %s", sig)
	}
	return 0, 0
}

// TestBinary runs the built binary. laug pool serve keeps its pid file while it runs and stops on SIGTERM within 2 s,
// exit 0, the file gone. Started by a name make build once linked to it, laugd, cellstat, follow or healthcheck, it is
// laug all the same: the name runs nothing of its own, so a laugd flag is an unknown command, exit 2, its usage on
// stderr.
func TestBinary(t *testing.T) {
	bin := laugBinary(t)
	root := checkout(t)
	work := filepath.Join(root, "sub")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}

	s := startBinary(t, work, "laug", "pool", "serve", "-listen", "127.0.0.1:0", "-min", "1")
	pid := strconv.Itoa(s.cmd.Process.Pid)
	if !strings.HasPrefix(s.first, "laug · pool serve: listening on 127.0.0.1:") ||
		!strings.HasSuffix(s.first, "; pid "+pid+" in cells/run/pool.pid; Ctrl-C or SIGTERM stops it") {
		t.Errorf("laug pool serve's first line: %q", s.first)
	}
	pidPath := filepath.Join(root, "cells", "run", "pool.pid")
	if b, err := os.ReadFile(pidPath); err != nil || string(b) != pid+"\n" {
		t.Errorf("cells/run/pool.pid under the checkout above the working directory: %q, %v; want %s", b, err, pid)
	}
	if code, took := s.signal(t, syscall.SIGTERM); code != 0 || took >= 2*time.Second {
		t.Errorf("laug pool serve after SIGTERM: exit %d after %s, want 0 under 2 s\nstderr:\n%s", code, took, s.stderr.String())
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("cells/run/pool.pid is still there after SIGTERM: %v", err)
	}

	links := t.TempDir()
	for _, name := range []string{"laugd", "cellstat", "follow", "healthcheck"} {
		link := filepath.Join(links, name)
		if err := os.Symlink(filepath.Join(bin, "laug"), link); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(link, "-listen", "127.0.0.1:0")
		cmd.Dir = work
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 || len(out) != 0 ||
			firstLine(stderr.String()) != `laug · unknown command "-listen"` {
			t.Errorf("started as %s: %v, stdout %q, stderr %q; want exit 2 and laug's unknown command", name, err, out,
				stderr.String())
		}
	}
}
