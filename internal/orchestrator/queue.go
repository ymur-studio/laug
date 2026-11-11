package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
)

// RunDir is where a checkout keeps its orchestrator state, relative to the checkout's root. It is gitignored, so
// the server's db file and the queue file never make a gate's tree dirty.
const RunDir = "orchestrator/run"

var queueRE = regexp.MustCompile(`^laug-gate-[0-9a-f]{8}$`)

// Queue returns this checkout's task queue, laug-gate-<8 hex>, kept in <runDir>/queue. With create, the first
// caller makes the file; it never changes after that. One queue per checkout keeps a worker in one worktree from
// running a gate started in another. laug starts the line that makes the file.
func Queue(runDir string, create bool, laug string) (string, error) {
	path := filepath.Join(runDir, "queue")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && create {
		err = makeQueue(runDir, path)
		if err == nil {
			b, err = os.ReadFile(path)
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s does not exist yet: %s orchestrator worker makes it", path, laug)
	}
	if err != nil {
		return "", err
	}
	q := strings.TrimSpace(string(b))
	if !queueRE.MatchString(q) {
		return "", fmt.Errorf("%s holds %q, not laug-gate-<8 hex>; it is never rewritten, so remove it by hand", path, q)
	}
	return q, nil
}

// makeQueue writes a temp file and links it into place, so two first callers agree on one name and neither reads
// a half-written file.
func makeQueue(runDir, path string) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(runDir, ".queue-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := fmt.Fprintf(tmp, "laug-gate-%s\n", hex.EncodeToString(r[:])); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// WorkerIdentity is laug-worker@<commit>: the first 7 hex digits of the commit the binary was built from, plus "-dirty"
// when the tree had changes. WorkerMain takes them from laug's -commit, the stamp make build passes bin/laug through
// -ldflags -X, because go build stamps the commit of the first parent directory that holds a .git directory
// (go1.26.0 cmd/go/internal/vcs/vcs.go:582-640, and :218 for the directory). A linked worktree's .git is a file, so a
// worktree outside any checkout gets no stamp, and one nested inside a checkout gets that checkout's commit (go1.26.0,
// both in docs/tables/records.jsonl#cells/results/2026-10-15-gate6-step1.txt section 3). With commit empty it reads the
// binary's own VCS stamp, which go build writes in the main checkout and go run and go test do not; with neither it
// is laug-worker@unknown. The SDK's default identity is <pid>@<host>@, which would put the host name in every history
// (SDK v1.49.0 getWorkerIdentity).
func WorkerIdentity(commit, modified string) string {
	if commit != "" {
		return identityFrom([]debug.BuildSetting{{Key: "vcs.revision", Value: commit}, {Key: "vcs.modified", Value: modified}})
	}
	var settings []debug.BuildSetting
	if bi, ok := debug.ReadBuildInfo(); ok {
		settings = bi.Settings
	}
	return identityFrom(settings)
}

func identityFrom(settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) < 7 || strings.Trim(rev, "0123456789abcdef") != "" {
		return "laug-worker@unknown"
	}
	id := "laug-worker@" + rev[:7]
	if dirty {
		id += "-dirty"
	}
	return id
}
