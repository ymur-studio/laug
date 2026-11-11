package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// gitEnv is an environment for the real git, alone with this test: git's directory first on PATH, a HOME and
// XDG_CONFIG_HOME of its own, and no system config, so no setting of the host's reaches a clone.
func gitEnv(t *testing.T) []string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("laug upstream runs git, and git is not on PATH: %v", err)
	}
	home := t.TempDir()
	return []string{"PATH=" + filepath.Dir(git) + ":/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "LANG=C"}
}

// gitDo runs git in dir with env and returns its stdout, trimmed; a failure fails the test.
func gitDo(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=laug", "-c", "user.email=laug", "-c",
		"commit.gpgsign=false"}, args...)...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// gitCommit adds an empty commit to the repository in dir and returns its name.
func gitCommit(t *testing.T, env []string, dir, msg string) string {
	t.Helper()
	gitDo(t, env, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return gitDo(t, env, dir, "rev-parse", "HEAD")
}

// TestUpstream clones a bare repository of this test's, standing in for Omes, as laug upstream clones Omes: no
// network. It holds a submodule, as Omes holds workers/proto/api_upstream, a local repository that git clones for a
// submodule only when protocol.file.allow says so.
func TestUpstream(t *testing.T) {
	env := append(gitEnv(t), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.file.allow", "GIT_CONFIG_VALUE_0=always")
	work := t.TempDir()
	src, proto := filepath.Join(work, "src"), filepath.Join(work, "proto")
	for _, dir := range []string{src, proto} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitDo(t, env, dir, "init", "-q", "-b", "main")
	}
	gitCommit(t, env, proto, "proto")
	gitDo(t, env, src, "submodule", "--quiet", "add", proto, "proto")
	c1, c2 := gitCommit(t, env, src, "one"), gitCommit(t, env, src, "two")
	bare := filepath.Join(work, "omes.git")
	gitDo(t, env, work, "clone", "-q", "--bare", src, bare)
	root := checkout(t)
	omes := filepath.Join(root, ".upstream", "omes")
	up := func(args ...string) (int, string, string) {
		t.Helper()
		return runApp(t, &App{Root: root, Environ: env}, append([]string{"upstream"}, args...)...)
	}
	// at checks .upstream/omes is at want, detached, as git and laug's own reader both say.
	at := func(want string) {
		t.Helper()
		if got := gitDo(t, env, omes, "rev-parse", "HEAD"); got != want {
			t.Errorf("git says .upstream/omes is at %s, want %s", got, want)
		}
		if sha, present, err := omesCommit(root); sha != want || !present || err != nil {
			t.Errorf("omesCommit: %s %t %v, want %s", sha, present, err, want)
		}
		cmd := exec.Command("git", "symbolic-ref", "-q", "HEAD")
		cmd.Dir, cmd.Env = omes, env
		if cmd.Run() == nil {
			t.Errorf(".upstream/omes is on a branch, not detached")
		}
	}

	// Its two lines and no line of git's: the submodule's clone, not quiet, named its absolute path.
	t.Run("a first clone", func(t *testing.T) {
		code, out, errOut := up("-url", bare, "-ref", c1)
		want := []string{"laug · upstream: cloning Omes from " + bare + " into .upstream/omes, then checking out " +
			c1[:7] + "; Ctrl-C stops it", "laug · upstream: ready: Omes " + c1[:7] + " in .upstream/omes; every " +
			"committed result used f0de206"}
		if code != 0 || errOut != "" || !slices.Equal(strings.Split(strings.TrimSuffix(out, "\n"), "\n"), want) {
			t.Errorf("exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", code, out, strings.Join(want, "\n"), errOut)
		}
		at(c1)
		if _, err := os.Stat(filepath.Join(omes, "proto", ".git")); err != nil {
			t.Errorf("the submodule is not checked out: %v", err)
		}
	})

	// The clone holds c2, so no fetch: the origin, moved away, is never asked.
	t.Run("a commit the clone holds", func(t *testing.T) {
		moved := bare + ".moved"
		if err := os.Rename(bare, moved); err != nil {
			t.Fatal(err)
		}
		defer os.Rename(moved, bare)
		code, out, errOut := up("-ref", c2)
		if code != 0 || errOut != "" || firstLine(out) != "laug · upstream: Omes is cloned in .upstream/omes "+
			"already: checking out "+c2[:7]+", fetched from its origin only when the clone lacks it; Ctrl-C stops it" {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
		at(c2)
	})

	// c3 is pushed after the clone, and c4 on it, so c3 is no tip: a fetch by its full name brings it.
	t.Run("a commit the clone lacks", func(t *testing.T) {
		c3 := gitCommit(t, env, src, "three")
		gitCommit(t, env, src, "four")
		gitDo(t, env, src, "push", "-q", bare, "main")
		code, out, errOut := up("-ref", c3, "-url", "https://example.invalid/omes.git")
		if code != 0 || errOut != "" || !strings.Contains(firstLine(out), "; -url is for a first clone and goes unused;") {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
		at(c3)
	})

	t.Run("a commit no one has", func(t *testing.T) {
		before := gitDo(t, env, omes, "rev-parse", "HEAD")
		none := strings.Repeat("0123456789", 4)
		code, _, errOut := up("-ref", none)
		line := "laug · upstream: git fetch --quiet origin " + none + " in .upstream/omes: exit status 128"
		if code != 1 || !slices.Contains(strings.Split(errOut, "\n"), line) {
			t.Errorf("exit %d, want 1 and %q\n%s", code, line, errOut)
		}
		at(before)
	})

	t.Run("refusals", func(t *testing.T) {
		empty := t.TempDir()
		for _, c := range []struct {
			name string
			app  *App
			args []string
			line string
		}{
			{"outside a checkout", &App{Environ: env}, nil, "laug · upstream: not in a laug checkout: Omes goes in its " +
				".upstream/omes, where load and gate run look for it"},
			{"no git", &App{Root: empty, Environ: []string{"PATH=" + empty}}, nil,
				"laug · upstream: no git on PATH to clone Omes with"},
			{"a ref like a flag", &App{Root: empty, Environ: env}, []string{"-ref", "-c"},
				"laug · upstream: -ref -c: a commit, a tag or a branch: letters, digits, ., _, / and -, from a letter or digit"},
			{"a range", &App{Root: empty, Environ: env}, []string{"-ref", "a..b"}, "laug · upstream: -ref a..b: a commit"},
			{"two words", &App{Root: empty, Environ: env}, []string{"-ref", "a b"}, "laug · upstream: -ref 'a b': a commit"},
			{"no url", &App{Root: empty, Environ: env}, []string{"-url", ""}, "laug · upstream: -url '': a repository"},
			{"a url like a flag", &App{Root: empty, Environ: env}, []string{"-url", "--upload-pack=x"},
				"laug · upstream: -url --upload-pack=x: a repository git can clone, not empty and not starting with -"},
			{"an argument", &App{Root: empty, Environ: env}, []string{"omes"},
				`laug · upstream: takes no arguments, got ["omes"]; -ref names the commit`},
		} {
			t.Run(c.name, func(t *testing.T) {
				code, out, errOut := runApp(t, c.app, append([]string{"upstream"}, c.args...)...)
				if code != 2 || out != "" || !strings.HasPrefix(firstLine(errOut), c.line) {
					t.Errorf("exit %d, want 2 and %q\nstdout:\n%s\nstderr:\n%s", code, c.line, out, errOut)
				}
				if ents, _ := os.ReadDir(empty); len(ents) != 0 {
					t.Errorf("a refusal wrote %d entries", len(ents))
				}
			})
		}
	})

	t.Run("the recipe's steps", testUpstreamSteps)
	t.Run("a signal", testUpstreamSignal)
}

// upstreamGitStub is a git that logs each call and its directory, relative to STUB_ROOT, to STUB_LOG and acts out the
// recipe's steps: clone makes the clone's .git; cat-file finds no commit; rev-parse names STUB_HEAD. A clone with
// STUB_SLOW set waits for a signal instead, and logs the TERM it gets.
const upstreamGitStub = `#!/bin/sh
printf '%s\t%s\n' "${PWD#"$STUB_ROOT"}" "$*" >> "$STUB_LOG"
case "$1" in
clone)
  if [ -n "$STUB_SLOW" ]; then
    sleep 30 >/dev/null 2>&1 & p=$!
    trap 'kill $p; echo TERM >> "$STUB_LOG"; exit 143' TERM
    wait $p
  fi
  mkdir -p "$5/.git" ;;
cat-file) exit 1 ;;
rev-parse) echo "$STUB_HEAD" ;;
esac
`

// realCheckout is a checkout whose path has no symbolic link in it, so the stub's PWD starts with it.
func realCheckout(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(checkout(t))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// testUpstreamSteps: bare, upstream runs the recipe's git commands in its order, with Omes's URL
// and the commit every result used, in the checkout and in .upstream/omes; a stub git stands in, so nothing is
// fetched.
func testUpstreamSteps(t *testing.T) {
	root, bin := realCheckout(t), t.TempDir()
	log := filepath.Join(bin, "log")
	writeExec(t, filepath.Join(bin, "git"), upstreamGitStub)
	environ := []string{"PATH=" + bin + ":/usr/bin:/bin", "STUB_ROOT=" + root, "STUB_LOG=" + log, "STUB_HEAD=" + omesRef}
	code, out, errOut := runApp(t, &App{Root: root, Environ: environ}, "upstream")
	b, _ := os.ReadFile(log)
	want := "\tclone --quiet --no-checkout https://github.com/temporalio/omes.git .upstream/omes\n" +
		"/.upstream/omes\tcat-file -e " + omesRef + "^{commit}\n" +
		"/.upstream/omes\tfetch --quiet origin " + omesRef + "\n" +
		"/.upstream/omes\tcheckout --quiet --detach " + omesRef + "\n" +
		"/.upstream/omes\tsubmodule update --quiet --init --recursive\n" +
		"/.upstream/omes\trev-parse HEAD\n"
	if string(b) != want {
		t.Errorf("git ran:\n%s\nwant:\n%s", b, want)
	}
	if code != 0 || errOut != "" || !strings.HasSuffix(out, "\nlaug · upstream: ready: Omes f0de206 in .upstream/omes, "+
		"the commit every committed result used\n") || !strings.HasPrefix(out, "laug · upstream: cloning Omes from "+
		"https://github.com/temporalio/omes.git into .upstream/omes, then checking out f0de206; Ctrl-C stops it\n") {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

// testUpstreamSignal: a signal during a git step stops git with TERM, at once, and upstream says which step it stopped,
// exit 1.
func testUpstreamSignal(t *testing.T) {
	root, bin := realCheckout(t), t.TempDir()
	log := filepath.Join(bin, "log")
	writeExec(t, filepath.Join(bin, "git"), upstreamGitStub)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	var stdout, stderr strings.Builder
	start := time.Now()
	environ := []string{"PATH=" + bin + ":/usr/bin:/bin", "STUB_ROOT=" + root, "STUB_LOG=" + log, "STUB_SLOW=1"}
	code := run(ctx, &App{Root: root, Environ: environ, Stdout: &stdout, Stderr: &stderr}, []string{"upstream"})
	took := time.Since(start)
	b, _ := os.ReadFile(log)
	line := "laug · upstream: a signal stopped git clone --quiet --no-checkout https://github.com/temporalio/omes.git " +
		".upstream/omes; laug upstream again finishes it"
	if code != 1 || firstLine(stderr.String()) != line || took > 5*time.Second || !strings.HasSuffix(string(b), "TERM\n") {
		t.Errorf("exit %d in %s\nstderr:\n%s\ngit's log:\n%s", code, took, stderr.String(), b)
	}
}

// TestGitHead holds omesCommit's reader to git rev-parse HEAD: a detached HEAD, a branch in refs/, a branch in
// packed-refs, a .git file that names the git directory, and a linked worktree, whose branches are in the common
// directory.
func TestGitHead(t *testing.T) {
	env := gitEnv(t)
	work := t.TempDir()
	repo := filepath.Join(work, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDo(t, env, repo, "init", "-q", "-b", "main")
	c1, c2 := gitCommit(t, env, repo, "one"), gitCommit(t, env, repo, "two")
	check := func(name, dir, want string) {
		t.Helper()
		if git := gitDo(t, env, dir, "rev-parse", "HEAD"); git != want {
			t.Fatalf("%s: git says %s, want %s", name, git, want)
		}
		if sha, present, err := gitHead(dir); sha != want || !present || err != nil {
			t.Errorf("%s: gitHead %q %t %v, want %s", name, sha, present, err, want)
		}
	}
	check("a branch in refs/", repo, c2)
	gitDo(t, env, repo, "pack-refs", "--all")
	if _, err := os.Stat(filepath.Join(repo, ".git", "refs", "heads", "main")); err == nil {
		t.Fatal("pack-refs left refs/heads/main")
	}
	check("a branch in packed-refs", repo, c2)
	gitDo(t, env, repo, "checkout", "-q", "--detach", c1)
	check("a detached HEAD", repo, c1)

	sep := filepath.Join(work, "sep")
	if err := os.Mkdir(sep, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDo(t, env, sep, "init", "-q", "-b", "main", "--separate-git-dir", filepath.Join(work, "sep.git"))
	check("a .git file", sep, gitCommit(t, env, sep, "sep"))

	gitDo(t, env, repo, "worktree", "add", "-q", "-b", "side", filepath.Join(work, "wt"), c1)
	check("a linked worktree", filepath.Join(work, "wt"), c1)

	if sha, present, err := gitHead(filepath.Join(work, "none")); sha != "" || present || err != nil {
		t.Errorf("no .git: %q %t %v", sha, present, err)
	}
	writeFile(t, filepath.Join(work, "bad", ".git", "HEAD"), "not a commit\n")
	if _, present, err := gitHead(filepath.Join(work, "bad")); !present || err == nil {
		t.Errorf("a HEAD that holds neither: present %t, err %v", present, err)
	}
}
