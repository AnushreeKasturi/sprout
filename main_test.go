package main

import (
	"bytes"
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
