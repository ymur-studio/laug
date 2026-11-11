package poolapi_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ymur-studio/laug/internal/poolapi"
)

const (
	docA = `{"id":"0a0b0c0d","address":"fake-0a0b0c0d:7233","block":0,"state":"leased","requester":"laug"}` + "\n"
	docB = `{"id":"1a1b1c1d","address":"fake-1a1b1c1d:7243","block":1,"state":"leased","requester":"laug"}` + "\n"
)

// leftovers lists the entries of dir but the ones named.
func leftovers(t *testing.T, dir string, keep ...string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if !strings.Contains(" "+strings.Join(keep, " ")+" ", " "+e.Name()+" ") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestWriteLast: the file holds the pool's answer byte for byte, 0600, with no temp file left beside it; a second
// write replaces it whole; a write that cannot land leaves no temp file and what was there as it was.
func TestWriteLast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, poolapi.LastLeaseFile)
	for _, doc := range []string{docA, docB} {
		if err := poolapi.WriteLast(path, []byte(doc)); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		fi, _ := os.Stat(path)
		if err != nil || string(b) != doc || fi.Mode().Perm() != 0o600 {
			t.Errorf("after a write: %q, %v, mode %v", b, err, fi.Mode())
		}
		if left := leftovers(t, dir, poolapi.LastLeaseFile); left != nil {
			t.Errorf("left beside it: %q", left)
		}
	}

	// The rename cannot land on a directory: the write fails, the directory and what it holds stay, no temp is left.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := poolapi.WriteLast(blocked, []byte(docA)); err == nil {
		t.Error("a write over a directory did not fail")
	}
	if fi, err := os.Stat(filepath.Join(blocked, "inside")); err != nil || !fi.IsDir() {
		t.Errorf("the directory changed: %v", err)
	}
	if left := leftovers(t, dir, poolapi.LastLeaseFile, "blocked"); left != nil {
		t.Errorf("a failed write left %q", left)
	}
	if b, _ := os.ReadFile(path); string(b) != docB {
		t.Errorf("a failed write elsewhere changed the file: %q", b)
	}
	if err := poolapi.WriteLast(filepath.Join(dir, "no-such-dir", poolapi.LastLeaseFile), []byte(docA)); err == nil {
		t.Error("a write into a missing directory did not fail")
	}
}

// TestWriteLastAtomic: while one goroutine writes two leases in turn, 200 times, a reader never sees anything but one
// of the two whole.
func TestWriteLastAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, poolapi.LastLeaseFile)
	if err := poolapi.WriteLast(path, []byte(docA)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := range 200 {
			doc := docA
			if i%2 == 0 {
				doc = docB
			}
			if err := poolapi.WriteLast(path, []byte(doc)); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	reads, bad := 0, ""
	for stop := false; !stop; reads++ {
		select {
		case <-done:
			stop = true
		default:
		}
		b, err := os.ReadFile(path)
		if (err != nil || string(b) != docA && string(b) != docB) && bad == "" {
			bad = fmt.Sprintf("read %d: %q, %v", reads, b, err)
		}
	}
	wg.Wait()
	if bad != "" {
		t.Errorf("a reader saw a part of a write: %s", bad)
	}
	if left := leftovers(t, dir, poolapi.LastLeaseFile); left != nil {
		t.Errorf("left beside it: %q", left)
	}
}

// TestReadLast: a missing or empty file is ErrNoLease, as make release's [ -s ] read it; one that is not JSON or
// names no id is another error; a lease reads back. RemoveLast removes the file only when it names that id.
func TestReadLast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, poolapi.LastLeaseFile)
	if _, err := poolapi.ReadLast(path); !errors.Is(err, poolapi.ErrNoLease) {
		t.Errorf("missing: %v", err)
	}
	for content, want := range map[string]string{"": "no lease", "{": "not a lease: ", `{"address":"x"}`: "not a lease: no id"} {
		os.WriteFile(path, []byte(content), 0o600)
		if _, err := poolapi.ReadLast(path); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v, want %q", content, err, want)
		}
	}
	os.WriteFile(path, []byte(docA), 0o600)
	if l, err := poolapi.ReadLast(path); err != nil || l.ID != "0a0b0c0d" || l.Address != "fake-0a0b0c0d:7233" {
		t.Errorf("a lease: %+v, %v", l, err)
	}

	if removed, err := poolapi.RemoveLast(path, "1a1b1c1d"); removed || err != nil {
		t.Errorf("another id: removed %t, %v", removed, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("another id removed the file: %v", err)
	}
	if removed, err := poolapi.RemoveLast(path, "0a0b0c0d"); !removed || err != nil {
		t.Errorf("its id: removed %t, %v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("its id left the file: %v", err)
	}
	if removed, err := poolapi.RemoveLast(path, "0a0b0c0d"); removed || err != nil {
		t.Errorf("no file: removed %t, %v", removed, err)
	}
}
