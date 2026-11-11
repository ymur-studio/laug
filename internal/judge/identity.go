package judge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// source is this package's .go files, its tests among them as *.go takes them, and versions.txt. With jsonx.Source it
// holds all the code a verdict depends on: TestIdentityCoversDeps fails when this package imports another of the
// module's. The identity takes the files that are not tests (covered).
//
//go:embed *.go versions.txt
var source embed.FS

// The two directories of the identity, from the repository root, and the files each embeds.
var trees = []struct {
	dir  string
	fsys embed.FS
}{{"internal/jsonx", jsonx.Source}, {"internal/judge", source}}

// A Source is one file of the identity: its path from the repository root and the sha256 of its bytes.
type Source struct {
	Path   string
	SHA256 string
}

// An Identity is what a gate run records of the judge that judged it.
type Identity struct {
	Version string // the version this judge gives a new run: Current
	SHA256  string // the sha256 of its sources' sha256sum lines (Sum)
	Go      string // the Go release that built it
}

// Self is this binary's judge: Current, the sha256 of the sources it embeds, and runtime.Version().
func Self() Identity {
	return Identity{Version: Current, SHA256: self(), Go: runtime.Version()}
}

var self = sync.OnceValue(func() string { return Sum(Sources()) })

// Sources is the files the identity covers, as this binary embeds them: every .go file directly in internal/judge
// and internal/jsonx that is not a test, and internal/judge/versions.txt, in the byte order of their paths. A test
// pins what the judge does and is no part of it, so a change to one leaves the identity as it was.
func Sources() []Source {
	var srcs []Source
	for _, t := range trees {
		entries, err := fs.ReadDir(t.fsys, ".")
		if err != nil {
			panic(err) // an embed.FS lists its own root
		}
		for _, e := range entries {
			if !covered(t.dir + "/" + e.Name()) {
				continue
			}
			data, err := fs.ReadFile(t.fsys, e.Name())
			if err != nil {
				panic(err)
			}
			srcs = append(srcs, Source{Path: t.dir + "/" + e.Name(), SHA256: sha256Hex(data)})
		}
	}
	slices.SortFunc(srcs, func(a, b Source) int { return strings.Compare(a.Path, b.Path) })
	return srcs
}

// SourcesIn is the files the identity covers as a checkout holds them: root is the checkout's root directory, and
// each Source is a .go file directly in internal/judge or internal/jsonx that is not a test, or
// internal/judge/versions.txt, with the sha256 of its bytes there, in the byte order of the paths. A binary built
// from that checkout embeds the same list (TestIdentityMatchesDisk), so Sum(SourcesIn(root)) is Self's sha256 exactly
// when the binary holds the judge the checkout holds. An error is a directory or a file that cannot be read.
func SourcesIn(root fs.FS) ([]Source, error) {
	var srcs []Source
	for _, t := range trees {
		entries, err := fs.ReadDir(root, t.dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			p := t.dir + "/" + e.Name()
			if e.IsDir() || !covered(p) {
				continue
			}
			data, err := fs.ReadFile(root, p)
			if err != nil {
				return nil, err
			}
			srcs = append(srcs, Source{Path: p, SHA256: sha256Hex(data)})
		}
	}
	slices.SortFunc(srcs, func(a, b Source) int { return strings.Compare(a.Path, b.Path) })
	return srcs, nil
}

// Sum is the identity's sha256 over srcs: the sha256 of sha256sum's lines for them, "<sha256>  <path>", in the byte
// order of the paths. In a checkout whose files are the ones embedded, it is:
//
//	git ls-files -z ':(glob)internal/judge/*.go' internal/judge/versions.txt ':(glob)internal/jsonx/*.go' \
//	  ':(glob,exclude)internal/*/*_test.go' | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum
func Sum(srcs []Source) string {
	srcs = slices.Clone(srcs)
	slices.SortFunc(srcs, func(a, b Source) int { return strings.Compare(a.Path, b.Path) })
	var lines bytes.Buffer
	for _, s := range srcs {
		fmt.Fprintf(&lines, "%s  %s\n", s.SHA256, s.Path)
	}
	return sha256Hex(lines.Bytes())
}

// covered says the path p, from the repository root, is one the identity covers: a .go file directly in
// internal/judge or internal/jsonx whose name does not end in _test.go, as the embed patterns take .go files less the
// tests, or internal/judge/versions.txt.
func covered(p string) bool {
	if p == "internal/judge/versions.txt" {
		return true
	}
	for _, t := range trees {
		if name, ok := strings.CutPrefix(p, t.dir+"/"); ok && !strings.Contains(name, "/") &&
			strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}

// ChangedVsBase says whether the judge this binary embeds differs from the one at base, a commit of the checkout at
// root: true when the identity of base's files is not Self's, which holds when base has none of them. It reads base
// with git (the command git, run in root with env): ls-tree for the covered paths, cat-file for their bytes. A gate's
// other inputs compare their sha256 with base's blob the same way (the inputs loop in gate/run.sh's preflight). Built
// from HEAD with the two directories clean, it answers as `git diff --quiet <base> -- internal/judge internal/jsonx`
// does, but for a change to a test, under a testdata/ directory or to another file the identity does not cover, which
// it does not count.
func ChangedVsBase(ctx context.Context, git, root, base string, env []string) (bool, error) {
	srcs, err := sourcesAt(ctx, git, root, base, env)
	if err != nil {
		return false, err
	}
	return srcs == nil || Sum(srcs) != self(), nil
}

// sourcesAt is the covered files at commit base, or nil, with no error, when one of them is not a regular file
// there, which no embed holds.
func sourcesAt(ctx context.Context, git, root, base string, env []string) ([]Source, error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("base %q: not a commit name", base)
	}
	run := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir, cmd.Env, cmd.Stdin = root, env, bytes.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	commit, err := run(nil, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("base %s is no commit here: %w", base, err)
	}
	tree, err := run(nil, "ls-tree", "-r", "-z", "--full-tree", strings.TrimSpace(string(commit)), "--",
		"internal/judge", "internal/jsonx")
	if err != nil {
		return nil, err
	}
	var paths, oids []string
	for _, rec := range strings.Split(strings.TrimSuffix(string(tree), "\x00"), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok || !covered(p) {
			continue
		}
		f := strings.Fields(meta) // <mode> <type> <oid>
		if len(f) != 3 {
			return nil, fmt.Errorf("git ls-tree: %q", rec)
		}
		if f[1] != "blob" || f[0] != "100644" && f[0] != "100755" {
			return nil, nil // a symlink or a submodule: never an embedded file
		}
		paths, oids = append(paths, p), append(oids, f[2])
	}
	if len(oids) == 0 {
		return []Source{}, nil
	}
	out, err := run([]byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	srcs := make([]Source, len(oids))
	for i, oid := range oids {
		header, rest, ok := bytes.Cut(out, []byte("\n"))
		f := strings.Fields(string(header)) // <oid> blob <size>
		if !ok || len(f) != 3 || f[0] != oid || f[1] != "blob" {
			return nil, fmt.Errorf("git cat-file --batch: %q for %s", header, oid)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 || len(rest) < size+1 || rest[size] != '\n' {
			return nil, fmt.Errorf("git cat-file --batch: %q for %s", header, oid)
		}
		srcs[i] = Source{Path: paths[i], SHA256: sha256Hex(rest[:size])}
		out = rest[size+1:]
	}
	return srcs, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
