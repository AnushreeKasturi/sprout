package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}

	must(os.MkdirAll(filepath.Join(dir, "src"), 0750))
	must(os.MkdirAll(filepath.Join(dir, "node_modules"), 0750))
	must(os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0644))
	must(os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	must(os.WriteFile(filepath.Join(dir, ".hidden"), []byte("secret"), 0644))

	return dir
}

func childNames(node *Node) map[string]bool {
	names := map[string]bool{}
	for _, c := range node.Children {
		names[c.Name] = true
	}
	return names
}

func TestBuildTreeBasic(t *testing.T) {
	dir := setupTestDir(t)

	tree, err := BuildTree(dir, Options{MaxDepth: -1})
	if err != nil {
		t.Fatalf("BuildTree failed: %v", err)
	}
	root := tree.Root

	names := childNames(root)
	if !names["src"] || !names["README.md"] {
		t.Errorf("expected src and README.md in tree, got %v", names)
	}
	if names[".hidden"] {
		t.Error("hidden file should not appear by default")
	}
}

func TestBuildTreeShowHidden(t *testing.T) {
	dir := setupTestDir(t)

	tree, err := BuildTree(dir, Options{ShowHidden: true, MaxDepth: -1})
	if err != nil {
		t.Fatalf("BuildTree failed: %v", err)
	}
	root := tree.Root

	if !childNames(root)[".hidden"] {
		t.Error("expected .hidden to appear when ShowHidden is true")
	}
}

func TestBuildTreeDepthLimit(t *testing.T) {
	dir := setupTestDir(t)

	tree, err := BuildTree(dir, Options{MaxDepth: 1})
	if err != nil {
		t.Fatalf("BuildTree failed: %v", err)
	}
	root := tree.Root

	for _, c := range root.Children {
		if c.Name == "src" && len(c.Children) != 0 {
			t.Error("expected src to have no children when MaxDepth is 1")
		}
	}
}

func TestBuildTreeIgnore(t *testing.T) {
	dir := setupTestDir(t)

	tree, err := BuildTree(dir, Options{MaxDepth: -1, NoIgnore: true, Ignore: []string{"node_modules"}})
	if err != nil {
		t.Fatalf("BuildTree failed: %v", err)
	}

	if childNames(tree.Root)["node_modules"] {
		t.Error("node_modules should have been ignored")
	}
}

func TestBuildTreeNonExistentPath(t *testing.T) {
	_, err := BuildTree(filepath.Join(t.TempDir(), "does-not-exist"), Options{})
	if err == nil {
		t.Error("expected an error for a non-existent path")
	}
}

func TestUnreadableDirDoesNotAbortWalk(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod-based permission test needs a non-root Unix user")
	}
	dir := setupTestDir(t)
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	T.Cleanup(func() { os.Chmod(locked, 0o600) })

	tree, err := BuildTree(dir, Options{MaxDepth: -1})
	if err != nil {
		t.Fatalf("walk aborted: %v", err)
	}
	root := tree.Root
	var found *Node
	for _, c := range root.Children {
		if c.Name == "locked" {
			found = c
		}
	}
	if found == nil || found.Err == nil {
		t.Error("expected locked/ in the tree with its read error recorded")
	}
	if !childNames(root)["src"] {
		t.Error("siblings of the unreadable dir should still be listed")
	}
}

func TestDefaultIgnoresOutsideGit(t *testing.T) {
	dir := setupTestDir(t)

	tree, err := BuildTree(dir, Options{MaxDepth: -1})
	if err != nil {
		t.Fatal(err)
	}
	if childNames(tree.Root)["node_modules"] {
		t.Error("node_modules should be hidden by default outside git")
	}
	if tree.Skipped != 2 { // node_modules + .hidden
		t.Errorf("Skipped = %d, want 2", tree.Skipped)
	}

	tree, _ = BuildTree(dir, Options{MaxDepth: -1, NoIgnore: true, ShowHidden: true})
	if names := childNames(tree.Root); !names["node_modules"] || !names[".hidden"] {
		t.Errorf("--all should show everything, got %v", names)
	}
}

func TestGitignoreRespected(t *testing.T) {
	dir := setupGitRepo(t)
	write(t, dir, ".gitignore", "secrets/\n*.log\n")
	write(t, dir, "secrets/key.pem", "x")
	write(t, dir, "debug.log", "x")
	write(t, dir, "bin/run.sh", "x")  // on the non-git default list? no — and git should show it
	write(t, dir, "dist/app.js", "x") // not gitignored here, so it must show

	tree, err := BuildTree(dir, Options{MaxDepth: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !tree.GitAware {
		t.Fatal("expected git-aware walk")
	}
	names := childNames(tree.Root)
	if names["secrets"] || names["debug.log"] {
		t.Errorf("gitignored entries leaked: %v", names)
	}
	if !names["dist"] || !names["bin"] || !names["src"] {
		t.Errorf("in git, only .gitignore decides; got %v", names)
	}
}

// setupGitRepo is setupTestDir inside a fresh git repo.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := setupTestDir(t)
	git(t, dir, "init", "-q")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFSPath(t *testing.T) {
	dir := setupTestDir(t)
	tree, err := BuildTree(dir, Options{MaxDepth: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.FSPath(tree.Root); got != dir {
		t.Errorf("root FSPath = %q, want %q", got, dir)
	}
	var src *Node
	for _, c := range tree.Root.Children {
		if c.Name == "src" {
			src = c
		}
	}
	main := src.Children[0]
	if got, want := tree.FSPath(main), filepath.Join(dir, "src", "main.go"); got != want {
		t.Errorf("FSPath = %q, want %q", got, want)
	}
	if _, err := os.Stat(tree.FSPath(main)); err != nil {
		t.Errorf("FSPath doesn't point at the file: %v", err)
	}
	// A deleted file inserted from git status isn't on disk.
	gone := insertPath(tree, "src/gone.go")
	if got := tree.FSPath(gone); got != "" {
		t.Errorf("inserted node FSPath = %q, want empty", got)
	}
	// Nor is anything in a --diff tree.
	d := diffTree("x", map[string]*Node{"a/b.go": {Status: "M"}}, -1)
	if got := d.FSPath(d.Root.Children[0].Children[0]); got != "" {
		t.Errorf("diff tree FSPath = %q, want empty", got)
	}
}
