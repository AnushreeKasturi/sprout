package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// queryRepo is a small repository: c uses b, b uses a, and b has a test.
func queryRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "go.mod", "module ex\n\ngo 1.22\n")
	write(t, dir, "a/a.go", "package a\n\nfunc Hello() string { return \"hi\" }\n\ntype Unused struct{}\n")
	write(t, dir, "b/b.go", "package b\n\nimport \"ex/a\"\n\nfunc B() string { return a.Hello() }\n")
	write(t, dir, "b/b_test.go", "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) { _ = B() }\n")
	write(t, dir, "c/c.go", "package c\n\nimport \"ex/b\"\n\nfunc C() string { return b.B() }\n")
	write(t, dir, "web/x.ts", "export const x = 1;\n")
	write(t, dir, "web/y.ts", "import {x} from './x.js';\nexport const y = x + 1;\n")
	return dir
}

func TestDeps(t *testing.T) {
	dir := queryRepo(t)
	out, errOut, code := runCLI(t, "deps", filepath.Join(dir, "b/b.go"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "b/b.go depends on 1 file") || !strings.Contains(out, "a/a.go  uses a.Hello") {
		t.Errorf("deps b/b.go:\n%s", out)
	}
	out, _, _ = runCLI(t, "deps", filepath.Join(dir, "web/y.ts"))
	if !strings.Contains(out, "web/x.ts  imports ./x.js") {
		t.Errorf("deps web/y.ts:\n%s", out)
	}
	out, _, _ = runCLI(t, "deps", filepath.Join(dir, "a/a.go"))
	if !strings.Contains(out, "doesn't depend on other files") {
		t.Errorf("deps a/a.go:\n%s", out)
	}
}

func TestDependents(t *testing.T) {
	dir := queryRepo(t)
	a := filepath.Join(dir, "a/a.go")

	out, _, _ := runCLI(t, "dependents", a)
	if !strings.Contains(out, "1 file depend on a/a.go") || strings.Contains(out, "c/c.go") {
		t.Errorf("one hop should stop at b:\n%s", out)
	}

	out, _, _ = runCLI(t, "dependents", a, "--depth", "2")
	for _, want := range []string{"3 files (1 test) depend on a/a.go", "b/b.go       uses a.Hello", "c/c.go       via b/b.go · uses b.B", "b/b_test.go  test · via b/b.go · uses B"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Non-tests come before tests at the same depth.
	if strings.Index(out, "c/c.go") > strings.Index(out, "b/b_test.go") {
		t.Errorf("tests should be listed last:\n%s", out)
	}

	out, _, _ = runCLI(t, "dependents", a, "--depth", "-1", "--no-tests")
	if strings.Contains(out, "b_test.go") || !strings.Contains(out, "c/c.go") {
		t.Errorf("--no-tests with unlimited depth:\n%s", out)
	}
}

func TestQueryJSON(t *testing.T) {
	dir := queryRepo(t)
	out, _, code := runCLI(t, "dependents", filepath.Join(dir, "web/x.ts"), "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var r queryResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.SchemaVersion != 1 || r.Command != "dependents" || r.File != "web/x.ts" || len(r.Results) != 1 || r.Results[0].Path != "web/y.ts" || r.Results[0].Reason != "imports ./x.js" {
		t.Errorf("unexpected JSON: %s", out)
	}
	out, _, _ = runCLI(t, "deps", filepath.Join(dir, "a/a.go"), "--json")
	if !strings.Contains(out, `"results":[]`) {
		t.Errorf("no results should be an empty list, not null: %s", out)
	}
}

func TestQueryErrors(t *testing.T) {
	dir := queryRepo(t)
	write(t, dir, "notes.txt", "hello\n")
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"deps"}, 2, "needs a file"},
		{[]string{"deps", filepath.Join(dir, "missing.go")}, 1, "no such file"},
		{[]string{"dependents", filepath.Join(dir, "notes.txt")}, 1, "isn't in the dependency graph"},
		{[]string{"deps", filepath.Join(dir, "a/a.go"), "--bogus"}, 2, ""},
	} {
		_, errOut, code := runCLI(t, c.args...)
		if code != c.code || !strings.Contains(errOut, c.msg) {
			t.Errorf("%v: exit %d, stderr %q; want exit %d containing %q", c.args, code, errOut, c.code, c.msg)
		}
	}
}

// Outside a git repository, the root is the current directory.
func TestQueryRootWithoutGit(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.ts", "export const x = 1;\n")
	write(t, dir, "sub/y.ts", "import {x} from '../x.js';\n")
	chdir(t, dir)
	out, _, code := runCLI(t, "dependents", "x.ts")
	if code != 0 || !strings.Contains(out, "sub/y.ts  imports ../x.js") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}
