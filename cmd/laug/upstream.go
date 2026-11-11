package main

// laug upstream: Omes cloned into .upstream/omes at the commit every committed result used. And omesCommit, the commit
// that clone is at, which version and deps -lab read.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// refRE is what -ref takes: a commit, a tag or a branch, one word git cannot read as an option.
var refRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func runUpstream(ctx context.Context, a *App, args []string) int {
	f := a.newFlags("upstream", "[-ref <commit>] [-url <repository>]")
	ref := f.String("ref", omesRef, "the Omes `commit` to check out, the one every committed result used by default; a "+
		"tag or a branch works too")
	url := f.String("url", omesURL, "the `repository` to clone Omes from when .upstream/omes holds no clone yet")
	pos, code, ok := f.parse(args, a.Stdout, a.Stderr)
	if !ok {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(f, "takes no arguments, got %q; -ref names the commit", pos)
	}
	if !refRE.MatchString(*ref) || strings.Contains(*ref, "..") {
		return a.refused("upstream", fmt.Errorf("-ref %s: a commit, a tag or a branch: letters, digits, ., _, / and -, "+
			"from a letter or digit", shellWord(*ref)))
	}
	if *url == "" || strings.HasPrefix(*url, "-") {
		return a.refused("upstream", fmt.Errorf("-url %s: a repository git can clone, not empty and not starting "+
			"with -", shellWord(*url)))
	}
	if a.Root == "" {
		return a.refused("upstream", errors.New("not in a laug checkout: Omes goes in its .upstream/omes, where load "+
			"and gate run look for it"))
	}
	git := a.lookPath("git")
	if git == "" {
		return a.refused("upstream", errors.New("no git on PATH to clone Omes with"))
	}

	dir := filepath.Join(a.Root, omesDir)
	// gitIn runs git in dir with the App's environment. out and errOut nil discard. A signal stops it with SIGTERM, as
	// Ctrl-C at a terminal would: git clone then removes what it had cloned.
	gitIn := func(dir string, out, errOut io.Writer, args ...string) error {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, firstWins(a.Environ), out, errOut
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 10 * time.Second
		return cmd.Run()
	}
	// fail says which git step failed, or that a signal stopped it, and returns 1. Every step but the clone runs in
	// .upstream/omes.
	fail := func(err error, args ...string) int {
		what := "git " + strings.Join(args, " ")
		if args[0] != "clone" {
			what += " in " + omesDir
		}
		if ctx.Err() != nil {
			fmt.Fprintf(a.Stderr, "laug · upstream: a signal stopped %s; %s upstream again finishes it\n", what, a.self())
		} else {
			fmt.Fprintf(a.Stderr, "laug · upstream: %s: %v\n", what, a.errShown(err))
		}
		return exitFailed
	}

	want := short(*ref)
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
		unused := ""
		if given(f, "url") {
			unused = "; -url is for a first clone and goes unused"
		}
		fmt.Fprintf(a.Stdout, "laug · upstream: Omes is cloned in %s already: checking out %s, fetched from its "+
			"origin only when the clone lacks it%s; Ctrl-C stops it\n", omesDir, want, unused)
	} else {
		fmt.Fprintf(a.Stdout, "laug · upstream: cloning Omes from %s into %s, then checking out %s; Ctrl-C stops it\n",
			*url, omesDir, want)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			fmt.Fprintf(a.Stderr, "laug · upstream: %v\n", a.errShown(err))
			return exitFailed
		}
		clone := []string{"clone", "--quiet", "--no-checkout", *url, omesDir}
		if err := gitIn(a.Root, a.Stdout, a.Stderr, clone...); err != nil {
			return fail(err, clone...)
		}
	}
	if gitIn(dir, nil, nil, "cat-file", "-e", *ref+"^{commit}") != nil {
		fetch := []string{"fetch", "--quiet", "origin", *ref}
		if err := gitIn(dir, a.Stdout, a.Stderr, fetch...); err != nil {
			return fail(err, fetch...)
		}
	}
	// Both run --quiet, as the clone and the fetch do, so git prints only its errors: without it, the submodule's
	// clone printed "Cloning into '<its absolute path>'...", a home path on most hosts.
	for _, step := range [][]string{{"checkout", "--quiet", "--detach", *ref}, {"submodule", "update", "--quiet",
		"--init", "--recursive"}} {
		if err := gitIn(dir, a.Stdout, a.Stderr, step...); err != nil {
			return fail(err, step...)
		}
	}
	var head strings.Builder
	if err := gitIn(dir, &head, a.Stderr, "rev-parse", "HEAD"); err != nil {
		return fail(err, "rev-parse", "HEAD")
	}
	at := short(strings.TrimSpace(head.String()))
	if strings.TrimSpace(head.String()) == omesRef {
		fmt.Fprintf(a.Stdout, "laug · upstream: ready: Omes %s in %s, the commit every committed result used\n", at,
			omesDir)
	} else {
		fmt.Fprintf(a.Stdout, "laug · upstream: ready: Omes %s in %s; every committed result used %s\n", at, omesDir,
			short(omesRef))
	}
	return exitDone
}

// shaRE is a commit's object name: SHA-1, or SHA-256 in a repository that uses it.
var shaRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// omesCommit is the commit the checkout's Omes clone, .upstream/omes, is at, read as gitHead reads it. present is
// false when root is empty or holds no clone.
func omesCommit(root string) (sha string, present bool, err error) {
	if root == "" {
		return "", false, nil
	}
	return gitHead(filepath.Join(root, omesDir))
}

// gitHead reads the commit worktree's HEAD names from its .git, with no git: a detached HEAD holds the commit, as
// laug upstream leaves it, and a branch's commit is in refs/ or packed-refs. A .git that is a file, as a submodule's
// is, names the git directory; a linked worktree's branches are in its common directory. present is false when
// worktree has no .git.
func gitHead(worktree string) (sha string, present bool, err error) {
	gitDir := filepath.Join(worktree, ".git")
	fi, err := os.Stat(gitDir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(gitDir)
		if err != nil {
			return "", true, err
		}
		p, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			return "", true, errors.New(".git is neither a directory nor a gitdir: file")
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(worktree, p)
		}
		gitDir = p
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", true, err
	}
	head := strings.TrimSpace(string(b))
	ref, symbolic := strings.CutPrefix(head, "ref: ")
	if !symbolic {
		if shaRE.MatchString(head) {
			return head, true, nil
		}
		return "", true, fmt.Errorf("HEAD holds %q, neither a commit nor a ref", head)
	}
	common := gitDir
	if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	}
	if b, err := os.ReadFile(filepath.Join(common, filepath.FromSlash(ref))); err == nil {
		if s := strings.TrimSpace(string(b)); shaRE.MatchString(s) {
			return s, true, nil
		}
	}
	if pf, err := os.Open(filepath.Join(common, "packed-refs")); err == nil {
		defer pf.Close()
		sc := bufio.NewScanner(pf)
		for sc.Scan() {
			if s, name, ok := strings.Cut(sc.Text(), " "); ok && name == ref && shaRE.MatchString(s) {
				return s, true, nil
			}
		}
	}
	return "", true, fmt.Errorf("HEAD names %s, which neither refs/ nor packed-refs holds", ref)
}
