package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// chdir moves the test into dir until it ends.
func chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
}

// impactRepo is queryRepo (c -> b -> a, with b_test) as a git repository
// with everything committed.
func impactRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := queryRepo(t)
	os.RemoveAll(filepath.Join(dir, ".git"))
	git(t, dir, "init", "-q")
	write(t, dir, "README.md", "# ex\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestImpactOfFiles(t *testing.T) {
	dir := impactRepo(t)
	chdir(t, dir)
	out, errOut, code := runCLI(t, "impact", "a/a.go")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"Impact of 1 file\n", "Affected: 2 files, 1 directly", "b/b.go  uses a.Hello",
		"… and 1 more file through them", "Tests: 1 file, 1 Go package", "go test ./b"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out, _, _ = runCLI(t, "impact", "a/a.go", "--all")
	if !strings.Contains(out, "c/c.go  via b/b.go · uses b.B") {
		t.Errorf("--all should list indirect files with their path:\n%s", out)
	}
}

func TestImpactFromGit(t *testing.T) {
	dir := impactRepo(t)
	chdir(t, dir)

	// Nothing uncommitted: suggest the branch the repository actually uses.
	for _, branch := range []string{"master", "main"} {
		git(t, dir, "branch", "-M", branch)
		out, _, _ := runCLI(t, "impact")
		if !strings.Contains(out, "Nothing changed (uncommitted changes)") || !strings.Contains(out, "sprout impact --diff "+branch+"...HEAD") {
			t.Errorf("clean tree on %s:\n%s", branch, out)
		}
	}
	var out string

	write(t, dir, "web/x.ts", "export const x = 2;\n")
	write(t, dir, "README.md", "# changed\n")
	out, _, _ = runCLI(t, "impact")
	for _, want := range []string{"(uncommitted changes)", "web/x.ts", "web/y.ts  imports ./x.js", "Not traced (not source code Sprout reads): README.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	git(t, dir, "add", "web/x.ts")
	out, _, _ = runCLI(t, "impact", "--staged")
	if !strings.Contains(out, "Impact of 1 changed file (staged changes)") || strings.Contains(out, "README") {
		t.Errorf("--staged:\n%s", out)
	}

	git(t, dir, "commit", "-q", "-m", "x")
	out, _, _ = runCLI(t, "impact", "--commit", "HEAD", "--json")
	var r impactResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.Source != "commit HEAD" || len(r.Changed) != 1 || r.Changed[0] != "web/x.ts" || len(r.Affected) != 1 || r.Affected[0].Reason != "imports ./x.js" {
		t.Errorf("--commit HEAD --json: %s", out)
	}

	out, _, _ = runCLI(t, "impact", "--diff", "HEAD~1...HEAD")
	if !strings.Contains(out, "(diff HEAD~1...HEAD)") || !strings.Contains(out, "web/y.ts") {
		t.Errorf("--diff:\n%s", out)
	}
}

func TestImpactErrors(t *testing.T) {
	dir := impactRepo(t)
	chdir(t, dir)
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"impact", "--staged", "a/a.go"}, 2, "one at a time"},
		{[]string{"impact", "--commit", "-x"}, 1, `invalid revision "-x"`},
		{[]string{"impact", "missing.go"}, 1, "missing.go: no such file"},
	} {
		_, errOut, code := runCLI(t, c.args...)
		if code != c.code || !strings.Contains(errOut, c.msg) {
			t.Errorf("%v: exit %d, stderr %q; want exit %d containing %q", c.args, code, errOut, c.code, c.msg)
		}
	}
}

// Python helpers among the tests are followed but not listed as tests to run.
func TestImpactListsRunnableTests(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "app/__init__.py", "")
	write(t, dir, "app/crud.py", "def create(): pass\n")
	write(t, dir, "tests/conftest.py", "from app import crud\n")
	write(t, dir, "tests/utils/user.py", "from app import crud\n")
	write(t, dir, "tests/test_api.py", "from tests.utils import user\n")
	os.Mkdir(filepath.Join(dir, ".git"), 0o750)
	out, _, code := runCLI(t, "impact", filepath.Join(dir, "app/crud.py"), "--json")
	var r impactResult
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("exit %d: %s", code, out)
	}
	if strings.Join(r.Tests, ",") != "tests/test_api.py" {
		t.Errorf("tests = %v, want only tests/test_api.py (reached through the helper)", r.Tests)
	}
}

// Python and JS/TS tests get their project's runner, run from the project's
// folder; tests with no recognisable runner are just listed.
func TestImpactTestCommands(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "backend/pyproject.toml", "[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\n")
	write(t, dir, "backend/app/crud.py", "def create(): pass\n")
	write(t, dir, "backend/tests/test_crud.py", "from app.crud import create\n")
	write(t, dir, "web/package.json", `{"devDependencies": {"vitest": "^3"}}`)
	write(t, dir, "web/src/util.ts", "export const x = 1;\n")
	write(t, dir, "web/src/util.test.ts", "import { x } from './util';\n")
	write(t, dir, "plain/package.json", `{"name": "plain"}`)
	write(t, dir, "plain/lib.js", "module.exports = 1;\n")
	write(t, dir, "plain/lib.test.js", "require('./lib');\n")
	chdir(t, dir)

	out, errOut, code := runCLI(t, "impact", "backend/app/crud.py", "web/src/util.ts", "plain/lib.js", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var res impactResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	want := []testCmd{
		{Dir: "backend", Argv: []string{"python", "-m", "pytest", "tests/test_crud.py"}},
		{Dir: "web", Argv: []string{"npx", "vitest", "run", "src/util.test.ts"}},
	}
	if !reflect.DeepEqual(res.TestCommands, want) {
		t.Fatalf("test commands: %+v", res.TestCommands)
	}

	text, _, _ := runCLI(t, "impact", "backend/app/crud.py", "web/src/util.ts", "plain/lib.js")
	for _, line := range []string{
		"  in backend/:\n    python -m pytest tests/test_crud.py",
		"  in web/:\n    npx vitest run src/util.test.ts",
		"  plain/lib.test.js", // no runner: listed, not dropped
	} {
		if !strings.Contains(text, line+"\n") {
			t.Errorf("missing %q in:\n%s", line, text)
		}
	}
}

// Test paths come from the repository. A command line is only offered when
// every path is safe unquoted in any shell; otherwise the tests are listed.
func TestCommandLineSafety(t *testing.T) {
	safe := testCmd{Dir: "backend", Argv: []string{"python", "-m", "pytest", "tests/test_a.py"}, runner: 3}
	if lines, ok := commandLine(safe, false); !ok || !reflect.DeepEqual(lines, []string{"in backend/:", "  python -m pytest tests/test_a.py"}) {
		t.Errorf("safe command: %q %v", lines, ok)
	}
	// Past 12 files the note goes on its own line: the command stays valid.
	many := testCmd{Dir: ".", Argv: []string{"npx", "jest"}, runner: 2}
	for i := 0; i < 15; i++ {
		many.Argv = append(many.Argv, fmt.Sprintf("t%d.test.js", i))
	}
	if lines, _ := commandLine(many, false); len(lines) != 2 || strings.Count(lines[0], ".test.js") != 12 || lines[1] != "(runs 12 of 15; --all prints the full command)" {
		t.Errorf("truncated command: %q", lines)
	}
	if lines, _ := commandLine(many, true); len(lines) != 1 || strings.Count(lines[0], ".test.js") != 15 {
		t.Errorf("--all command: %q", lines)
	}
	for _, bad := range []testCmd{
		{Dir: ".", Argv: []string{"python", "-m", "pytest", "tests/it's $(x).py"}, runner: 3},
		{Dir: ".", Argv: []string{"npx", "jest", "a&calc.test.js"}, runner: 2}, // cmd.exe runs calc
		{Dir: "my app", Argv: []string{"npx", "jest", "a.test.js"}, runner: 2},
		{Dir: ".", Argv: []string{"npx", "jest", "--config=evil.js"}, runner: 2}, // read as an option
	} {
		if lines, ok := commandLine(bad, false); ok {
			t.Errorf("unsafe command offered: %s", lines)
		}
	}

	dir := t.TempDir()
	write(t, dir, "pyproject.toml", "[tool.pytest.ini_options]\n")
	write(t, dir, "app.py", "def f(): pass\n")
	write(t, dir, "tests/test_a&b.py", "import app\n")
	chdir(t, dir)
	out, _, _ := runCLI(t, "impact", "app.py")
	if strings.Contains(out, "pytest") || !strings.Contains(out, "  tests/test_a&b.py\n") {
		t.Errorf("an unsafe test path should be listed, not put in a command:\n%s", out)
	}
}
