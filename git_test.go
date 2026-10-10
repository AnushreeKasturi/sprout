package main

import (
	"os"
	"strings"
	"testing"
)

func TestGitStatus(t *testing.T) {
	dir := setupGitRepo(t)
	write(t, dir, "lib/gone.go", "x")
	write(t, dir, ".github/ci.yml", "x")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")

	write(t, dir, "src/main.go", "package main // edited")
	write(t, dir, "new.txt", "x")
	write(t, dir, ".github/ci.yml", "edited")
	if err := os.Remove(dir + "/lib/gone.go"); err != nil {
		t.Fatal(err)
	}

	out, errOut, code := runCLI(t, dir, "--git")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"main.go  M", "new.txt  ?", "gone.go  D", "src/  (1 changed)", " on "} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".github") {
		t.Errorf("a change inside a hidden dir must not surface it:\n%s", out)
	}
}

func TestGitStatusFromSubdir(t *testing.T) {
	dir := setupGitRepo(t)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	write(t, dir, "src/main.go", "edited")

	out, _, code := runCLI(t, dir+"/src", "--git")
	if code != 0 || !strings.Contains(out, "main.go  M") {
		t.Errorf("repo-relative paths weren't mapped to the subdir root:\n%s", out)
	}
}

// --git has nothing to mark outside a repository, so it shows the tree and
// says why; --diff can't do without one.
func TestGitOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "x")
	out, errOut, code := runCLI(t, dir, "--git", "--no-config")
	if code != 0 || !strings.Contains(out, "a.txt") || !strings.Contains(errOut, "showing the tree without --git") {
		t.Errorf("--git outside a repo: exit %d\n%s\n%s", code, out, errOut)
	}
	if _, _, code := runCLI(t, dir, "--diff", "main", "--no-config"); code != 1 {
		t.Errorf("--diff outside a repo: exit %d, want 1", code)
	}
}

func TestDiffTree(t *testing.T) {
	dir := setupGitRepo(t)
	write(t, dir, "api/old.go", "a\nb\n")
	write(t, dir, "api/keep.go", "a\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	git(t, dir, "tag", "base")

	write(t, dir, "api/keep.go", "a\nb\nc\n")
	git(t, dir, "mv", "api/old.go", "api/renamed.go")
	write(t, dir, "web/app.js", "x\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "change")

	out, errOut, code := runCLI(t, dir, "--diff", "base..HEAD")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"keep.go  M  +2 -0", "renamed.go  R", "app.js  A  +1 -0", "3 files changed, +3 -0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README") || strings.Contains(out, "old.go") {
		t.Errorf("unchanged or renamed-away paths leaked:\n%s", out)
	}

	out, _, _ = runCLI(t, dir, "--diff", "base..HEAD", "-L", "1")
	if !strings.Contains(out, "api/  (2 changed)  +2 -0") || strings.Contains(out, "keep.go") {
		t.Errorf("--depth should collapse subtrees into totals:\n%s", out)
	}
}

func TestDiffRejectsOptionInjection(t *testing.T) {
	dir := setupGitRepo(t)
	_, errOut, code := runCLI(t, dir, "--diff=--output=/tmp/pwned")
	if code != 1 || !strings.Contains(errOut, "invalid revision") {
		t.Errorf("expected rejection, got exit %d: %s", code, errOut)
	}
}
