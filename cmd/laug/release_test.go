package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelease gives leases back to internal/pool and keeps the last-lease file honest: a release of another lease
// leaves the file and says whose it is; a release of the lease the file names removes it, by -last or by id; a
// refused release leaves the file and says it still names that lease. -json prints the pool's answer alone. Bare, it
// prints the last lease's id and the line to type, -file's included, on stderr with exit 2. pool status shows the
// cells gone.
func TestRelease(t *testing.T) {
	addr, _ := leasePool(t)
	root := checkout(t)
	path := filepath.Join(root, ".last-lease.json")
	first, _, _ := leaseIn(t, root, addr)
	second, until, raw := leaseIn(t, root, addr)

	code, out, errOut := runApp(t, &App{Root: root}, "release", "-pool", addr)
	if code != 2 || out != "" || !strings.HasPrefix(errOut, "laug · release: names no lease: give it a lease id, or -last\n"+
		"the last lease, in .last-lease.json, is "+second+", for laug until "+until+"; to release it:\n"+
		"  laug release "+second+" -pool "+addr+"\nusage: laug release <id> | -last ") {
		t.Errorf("bare: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	code, out, errOut = runApp(t, &App{Root: root}, "release", first, "-pool", addr)
	if code != 0 || errOut != "" || out != "laug · release: "+first+" given back, its cell destroyed; .last-lease.json "+
		"names "+second+", which stays\n" {
		t.Errorf("release of another lease: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(path); string(b) != raw {
		t.Errorf("a release of another lease changed .last-lease.json:\n%s", b)
	}

	code, out, errOut = runApp(t, &App{Root: root}, "release", "-last", "-json", "-pool", addr)
	if code != 0 || errOut != "" || out != `{"released":"`+second+`"}`+"\n" {
		t.Errorf("release -last -json: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("release -last left .last-lease.json: %v", err)
	}
	code, out, _ = runArgs(t, "pool", "status", "-pool", addr)
	if code != 0 || !strings.Contains(firstLine(out), ", 0 leased, ") {
		t.Errorf("pool status after both releases: exit %d\n%s", code, out)
	}

	// A refused release: the file names that lease, and stays.
	os.WriteFile(path, []byte(raw), 0o600)
	code, out, errOut = runApp(t, &App{Root: root}, "release", "-last", "-pool", addr)
	if code != 1 || out != "" || errOut != "laug · release: the pool on "+addr+" refused "+second+": cell not found "+
		"(404 Not Found); .last-lease.json still names it\n" {
		t.Errorf("a refused release: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if b, _ := os.ReadFile(path); string(b) != raw {
		t.Errorf("a refused release changed .last-lease.json:\n%s", b)
	}

	// By id, in text, with a -file of its own: the line to type keeps -file, and the release removes that file.
	if code, out, errOut := runApp(t, &App{Root: root}, "lease", "-pool", addr, "-file", "run 2.json"); code != 0 {
		t.Fatalf("lease -file: exit %d\n%s%s", code, out, errOut)
	}
	doc, _ := leaseDoc(t, filepath.Join(root, "run 2.json"))
	id := doc["id"].(string)
	_, _, errOut = runApp(t, &App{Root: root}, "release", "-pool", addr, "-file", "run 2.json")
	if !strings.Contains(errOut, "\n  laug release "+id+" -pool "+addr+" -file 'run 2.json'\n") {
		t.Errorf("bare with -file: no line to type with -file:\n%s", errOut)
	}
	code, out, errOut = runApp(t, &App{Root: root}, "release", id, "-pool", addr, "-file", "run 2.json")
	if code != 0 || errOut != "" || out != "laug · release: "+id+" given back, its cell destroyed; run 2.json removed\n" {
		t.Errorf("release <id> -file: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "run 2.json")); !os.IsNotExist(err) {
		t.Errorf("release <id> -file left the file: %v", err)
	}
	code, out, errOut = runApp(t, &App{Root: root}, "release", "-pool", "[::]:7700", id)
	if code != 2 || out != "" || !strings.HasPrefix(errOut, `laug · release: -pool [::]:7700: host "::" is not a loopback`) {
		t.Errorf("release -pool [::]:7700: exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}
