package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestFlagsAfterPath(t *testing.T) {
	dir := setupTestDir(t)

	before, _, _ := runCLI(t, "--depth", "1", dir)
	after, _, code := runCLI(t, dir, "--depth", "1")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if before != after {
		t.Errorf("flag position changed output:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(after, "main.go") {
		t.Error("--depth 1 after the path was ignored")
	}
}

func TestTooManyArgs(t *testing.T) {
	if _, _, code := runCLI(t, ".", "extra"); code != 2 {
		t.Errorf("expected exit 2 for extra positional args, got %d", code)
	}
}

func TestVersion(t *testing.T) {
	out, _, code := runCLI(t, "--version")
	if code != 0 || !strings.HasPrefix(out, "sprout ") {
		t.Errorf("unexpected --version output %q (exit %d)", out, code)
	}
}

func TestHelp(t *testing.T) {
	out, _, code := runCLI(t, "--help")
	if code != 0 || !strings.Contains(out, "\nUsage\n") || strings.Contains(out, "\x1b[") {
		t.Errorf("--help: exit %d, output %q", code, out)
	}
	_, errOut, code := runCLI(t, "--nope")
	if code != 2 || !strings.Contains(errOut, "sprout --help") {
		t.Errorf("unknown flag: exit %d, stderr %q", code, errOut)
	}
}

func TestFlagLimits(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")
	for _, args := range [][]string{{"--depth", "-3"}, {"--ai", "--budget", "0"}} {
		if _, errOut, code := runCLI(t, append([]string{dir, "--no-config"}, args...)...); code != 2 || errOut == "" {
			t.Errorf("%v: exit %d, %q", args, code, errOut)
		}
	}
	if _, errOut, code := runCLI(t, dir, "--no-config", "--ai", "--budget", "5"); code != 0 || !strings.Contains(errOut, "over --budget 5") {
		t.Errorf("a map over budget should say so: exit %d, %q", code, errOut)
	}
}

func TestSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "real.md", "x")
	if err := os.Symlink("real.md", filepath.Join(dir, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if out, _, _ := runCLI(t, dir, "--no-config"); !strings.Contains(out, "link.md -> real.md") {
		t.Errorf("symlink target missing:\n%s", out)
	}
}
