package poolapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LastLeaseFile is where laug lease keeps the last lease, at the checkout's root. The scripts read it there:
// load/run-omes.sh, faults/run.sh and faults/candidate.sh take it as LEASE_FILE's default, and the Makefile reads its
// address, prometheus, project and server_tag. It is gitignored, as are the temp files beside it, .lease.*.
const LastLeaseFile = ".last-lease.json"

// ErrNoLease is what ReadLast returns for a last-lease file that is missing or empty: no lease, as make release's
// [ -s .last-lease.json ] test read it.
var ErrNoLease = errors.New("no lease")

// WriteLast writes doc, a lease as the pool answered it, to path the way make lease did (Makefile, lease): into a temp
// file beside path, .lease.<random>, then a rename over path. A reader sees the old file or the new one, never a part
// of either, and a write that fails leaves the old file as it was and no temp file. The file is 0600, as mktemp made
// it.
func WriteLast(path string, doc []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".lease.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(doc); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadLast reads the lease in path. A file that is missing or empty is ErrNoLease; one that holds no lease id is an
// error that says so.
func ReadLast(path string) (Lease, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(b) == 0 {
		return Lease{}, ErrNoLease
	}
	if err != nil {
		return Lease{}, err
	}
	var l Lease
	if err := json.Unmarshal(b, &l); err != nil {
		return Lease{}, fmt.Errorf("not a lease: %w", err)
	}
	if l.ID == "" {
		return Lease{}, errors.New("not a lease: no id")
	}
	return l, nil
}

// RemoveLast removes path when it holds lease id, and reports whether it did. A file that is missing, or names
// another lease, stays as it is: release removes the last-lease file only when it released that lease, and reset when
// that lease's reset failed, as faults/candidate.sh does.
func RemoveLast(path, id string) (bool, error) {
	l, err := ReadLast(path)
	if err != nil || l.ID != id {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
