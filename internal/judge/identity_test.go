package judge

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// TestIdentityCoversDeps fails when the identity could miss code a verdict depends on: when this package links a
// package of this module other than itself and internal/jsonx, whose sources it would not cover; or when a .go
// file sits in a directory below either, which `*.go` does not embed, or its name starts with . or _, which the
// go command does not build. Every name must also print bare in a sha256sum line.
func TestIdentityCoversDeps(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go list needs the go command: %v", err)
	}
	mod, err := exec.Command(gobin, "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	root := strings.TrimSpace(string(mod))
	out, err := exec.Command(gobin, "list", "-deps", "-f", "{{.ImportPath}} {{.Standard}}", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	own := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, std, _ := strings.Cut(line, " ")
		if std == "true" {
			continue
		}
		own = append(own, path)
		if path != root+"/internal/judge" && path != root+"/internal/jsonx" {
			t.Errorf("internal/judge links %s, which the identity does not cover", path)
		}
	}
	if !slices.Contains(own, root+"/internal/jsonx") {
		t.Errorf("go list -deps: internal/jsonx is not among %v", own)
	}
	plain := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	for _, tr := range trees {
		top := filepath.Join("..", "..", filepath.FromSlash(tr.dir))
		err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(top, path)
			name := d.Name()
			switch {
			case !strings.HasSuffix(name, ".go"):
			case filepath.Dir(rel) != ".":
				t.Errorf("%s/%s: a .go file below %s, which the identity does not cover", tr.dir, filepath.ToSlash(rel),
					tr.dir)
			case !plain.MatchString(name):
				t.Errorf("%s/%s: a .go file the go command skips, or whose name sha256sum would escape", tr.dir, name)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestIdentityMatchesDisk holds the embedded sources to the files on disk, as SourcesIn reads them from the
// repository root: every .go file directly in internal/jsonx and internal/judge that is not a test, and
// internal/judge/versions.txt, with the same bytes. With sha256sum present, Sum is the sha256 of what sha256sum prints
// for those paths, from the repository root, in byte order. SourcesIn also finds a file the binary lacks, and reads a
// changed one as changed; a test added or changed leaves the sum as it was.
func TestIdentityMatchesDisk(t *testing.T) {
	disk, err := SourcesIn(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range disk {
		if s.SHA256 != fileSHA256(t, filepath.Join("..", "..", filepath.FromSlash(s.Path))) {
			t.Errorf("SourcesIn: %s has sha256 %s, and the file another", s.Path, s.SHA256)
		}
	}
	srcs := Sources()
	if !slices.Equal(srcs, disk) {
		t.Fatalf("embedded:\n%v\non disk:\n%v", srcs, disk)
	}
	if !slices.ContainsFunc(srcs, func(s Source) bool { return s.Path == "internal/judge/versions.txt" }) {
		t.Error("versions.txt is not among the sources")
	}
	for _, s := range srcs {
		if strings.HasSuffix(s.Path, "_test.go") {
			t.Errorf("%s: a test among the sources", s.Path)
		}
	}
	if !slices.ContainsFunc(srcs, func(s Source) bool { return s.Path == "internal/judge/identity.go" }) {
		t.Error("identity.go is not among the sources")
	}
	id := Self()
	if id.SHA256 != Sum(srcs) || id.Version != Current || id.Go != runtime.Version() {
		t.Errorf("Self() = %+v", id)
	}
	sum, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Logf("not checked against sha256sum: %v", err)
		return
	}
	paths := make([]string, len(srcs))
	for i, s := range srcs {
		paths[i] = s.Path
	}
	cmd := exec.Command(sum, paths...)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sha256sum: %v", err)
	}
	if got := sha256Hex(out); got != id.SHA256 {
		t.Errorf("sha256sum's lines hash to %s, Self() is %s", got, id.SHA256)
	}
	t.Logf("%d sources, sha256 %s", len(srcs), id.SHA256)

	// A checkout that holds the same files, and one that holds a file more or a file changed. What the identity does
	// not cover, a file below the two directories, one that is not .go or a test, changes nothing.
	made := fstest.MapFS{}
	for _, s := range disk {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(s.Path)))
		if err != nil {
			t.Fatal(err)
		}
		made[s.Path] = &fstest.MapFile{Data: data}
	}
	made["internal/judge/testdata/x.go"] = &fstest.MapFile{Data: []byte("package x\n")}
	made["internal/jsonx/notes.txt"] = &fstest.MapFile{Data: []byte("x")}
	made["internal/judge/identity_test.go"] = &fstest.MapFile{Data: []byte("package judge\n")}
	made["internal/jsonx/zz_test.go"] = &fstest.MapFile{Data: []byte("package jsonx\n")}
	sumOf := func(fsys fs.FS) string {
		t.Helper()
		srcs, err := SourcesIn(fsys)
		if err != nil {
			t.Fatal(err)
		}
		return Sum(srcs)
	}
	if got := sumOf(made); got != id.SHA256 {
		t.Errorf("a copy of the sources: %s, want Self's %s", got, id.SHA256)
	}
	made["internal/judge/zz.go"] = &fstest.MapFile{Data: []byte("package judge\n")}
	if got := sumOf(made); got == id.SHA256 {
		t.Error("a .go file more: the sum is still Self's")
	}
	delete(made, "internal/judge/zz.go")
	made["internal/jsonx/value.go"] = &fstest.MapFile{Data: append(made["internal/jsonx/value.go"].Data, '\n')}
	if got := sumOf(made); got == id.SHA256 {
		t.Error("value.go changed: the sum is still Self's")
	}
	if _, err := SourcesIn(fstest.MapFS{"internal/judge/versions.txt": {Data: []byte("x")}}); err == nil {
		t.Error("a checkout with no internal/jsonx: no error")
	}
}

// TestChangedVsBase builds a repository of its own, commit by commit, and asks of each commit whether the judge this
// test binary embeds differs from it.
func TestChangedVsBase(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git on PATH: %v", err)
	}
	repo := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + repo, "LC_ALL=C", "TZ=UTC", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_AUTHOR_NAME=judge test", "GIT_AUTHOR_EMAIL=judge-test@example.invalid",
		"GIT_COMMITTER_NAME=judge test", "GIT_COMMITTER_EMAIL=judge-test@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z"}
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir, cmd.Env = repo, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(p, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(repo, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) string {
		t.Helper()
		gitRun("add", "-A")
		gitRun("commit", "-q", "--allow-empty", "--no-verify", "-m", msg)
		return gitRun("rev-parse", "HEAD")
	}
	gitRun("init", "-q")
	write("README.md", "a repository with no judge\n", 0o644)
	none := commit("none")
	for _, tr := range trees {
		entries, err := fs.ReadDir(tr.fsys, ".")
		if err != nil {
			t.Fatal(err)
		}
		for i, e := range entries {
			data, err := fs.ReadFile(tr.fsys, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o644)
			if i == 0 {
				mode = 0o755 // a file mode is not in the identity
			}
			write(tr.dir+"/"+e.Name(), string(data), mode)
		}
	}
	same := commit("the judge as embedded")
	write("internal/judge/testdata/v9/x.go", "package x\n", 0o644)
	write("internal/judge/sub/y.go", "package y\n", 0o644)
	write("internal/jsonx/README.md", "not covered\n", 0o644)
	write("internal/judge/notes.txt", "not covered\n", 0o644)
	write("internal/judge/identity_test.go", "package judge\n", 0o644)
	write("internal/jsonx/zz_test.go", "package jsonx\n", 0o644)
	uncovered := commit("files the identity does not cover, two tests among them")
	write("internal/judge/extra.go", "package judge\n", 0o644)
	extra := commit("one more .go file")
	gitRun("rm", "-q", "internal/judge/extra.go", "internal/judge/versions.txt")
	noVersions := commit("no versions.txt")
	gitRun("checkout", "-q", same, "--", "internal/judge/versions.txt")
	value, err := os.ReadFile(filepath.Join(repo, "internal", "jsonx", "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	write("internal/jsonx/value.go", string(value)+"\n", 0o644)
	edited := commit("a byte more in jsonx")
	write("internal/jsonx/value.go", string(value), 0o644)
	if err := os.Symlink("value.go", filepath.Join(repo, "internal", "jsonx", "link.go")); err != nil {
		t.Fatal(err)
	}
	link := commit("a symlink among the sources")

	ctx := context.Background()
	for _, c := range []struct {
		name, base string
		want       bool
	}{
		{"no judge at all", none, true},
		{"the judge as embedded", same, false},
		{"by a ref", "HEAD~5", false},
		{"files the identity does not cover, two tests among them", uncovered, false},
		{"one more .go file", extra, true},
		{"no versions.txt", noVersions, true},
		{"a byte more", edited, true},
		{"a symlink among the sources", link, true},
	} {
		got, err := ChangedVsBase(ctx, git, repo, c.base, env)
		if err != nil || got != c.want {
			t.Errorf("%s: ChangedVsBase = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	for _, base := range []string{"", "-x", "nope", strings.Repeat("0", 40)} {
		if got, err := ChangedVsBase(ctx, git, repo, base, env); err == nil {
			t.Errorf("base %q: %v and no error", base, got)
		}
	}
}
