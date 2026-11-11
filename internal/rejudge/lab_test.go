package rejudge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/tables"
)

// TestLabRuns holds the copy of the lab host runs the tests name (testdata/lab/README.md) to the records they copy:
// each directory there is a gate run whose row of docs/tables/records.jsonl gives its file count and its sha256,
// the sha256 of its files as sha256sum lists them from the checkout's root under gate/results/<id>/, sorted by path
// in bytes, so each copy is that run byte for byte whether or not the run is still in gate/results. The README's
// table names each directory once, and no other.
func TestLabRuns(t *testing.T) {
	checkout := os.DirFS(filepath.Join("..", ".."))
	records, err := tables.Read(checkout, tables.Dir, "records")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]map[string]string{}
	for _, r := range records {
		byPath[r.Key] = map[string]string{"kind": member(r.Value, "kind").String(),
			"files": member(r.Value, "files").String(), "sha256": member(r.Value, "sha256").String()}
	}
	ents, err := os.ReadDir(labRuns)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dirs = append(dirs, e.Name())
		record := tables.GateResults + "/" + e.Name() + "/"
		var lines []string
		err := fs.WalkDir(os.DirFS(labRuns), e.Name(), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(filepath.Join(labRuns, p))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			lines = append(lines, hex.EncodeToString(sum[:])+"  "+tables.GateResults+"/"+p+"\n")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
		sum := sha256.Sum256([]byte(strings.Join(lines, "")))
		got := map[string]string{"kind": `"gate"`, "files": fmt.Sprint(len(lines)),
			"sha256": `"` + hex.EncodeToString(sum[:]) + `"`}
		if want, ok := byPath[record]; !ok || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("testdata/lab/%s: %v; its records row %s: %v", e.Name(), got, record, want)
		}
	}
	readme, err := os.ReadFile(filepath.Join(labRuns, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var named []string
	for _, l := range strings.Split(string(readme), "\n") {
		if id, ok := strings.CutPrefix(l, "| g"); ok {
			named = append(named, "g"+strings.TrimSpace(strings.Split(id, "|")[0]))
		}
	}
	slices.Sort(named)
	if !slices.Equal(named, dirs) || len(dirs) == 0 {
		t.Errorf("testdata/lab/README.md's table names %q; the directories are %q", named, dirs)
	}
}
