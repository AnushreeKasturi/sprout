package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func tourFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "greetings")
	err := filepath.WalkDir("testdata/tour", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel("testdata/tour", path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, dir, rel, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func tourJSON(t *testing.T, dir string, args ...string) (tourResult, string) {
	t.Helper()
	out, errOut, code := runCLI(t, append([]string{"tour", dir, "--json"}, args...)...)
	if code != 0 || errOut != "" {
		t.Fatalf("tour: exit %d: %s", code, errOut)
	}
	var r tourResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	return r, out
}

func TestTourDeterministic(t *testing.T) {
	dir := tourFixture(t)
	_, first := tourJSON(t, dir)
	_, second := tourJSON(t, dir)
	if first != second || strings.Contains(first, dir) {
		t.Fatal("JSON must be deterministic and must not expose the root path")
	}
	before, _, code := runCLI(t, "tour", "--json", dir)
	if code != 0 || before != first {
		t.Fatal("flags before/after the path must be equivalent")
	}
}

func TestTourFixtureEvidence(t *testing.T) {
	r, _ := tourJSON(t, tourFixture(t))
	if r.SchemaVersion != 1 || r.Command != "tour" || r.Project != "greetings" || r.Files != 6 || r.Directories != 5 {
		t.Fatalf("metadata: %+v", r)
	}
	if r.Purpose == nil || r.Purpose.Path != "README.md" || r.Purpose.Text != "A small command that prints a greeting." {
		t.Fatalf("purpose: %+v", r.Purpose)
	}
	if !reflect.DeepEqual(r.Projects, []ProjectInfo{{"go.mod", "Go", "go modules"}}) || r.Languages["Go"] != 3 {
		t.Fatalf("ecosystems/languages: %+v / %v", r.Projects, r.Languages)
	}
}

func TestTourReadingPlan(t *testing.T) {
	dir := tourFixture(t)
	r, _ := tourJSON(t, dir)
	want := []tourStep{{"README.md", "project overview (README)"}, {"cmd/app/main.go", "likely entry point (filename convention)"}, {"internal/greeting/greeting.go", "used by 1 file"}}
	if !reflect.DeepEqual(r.ReadingOrder, want) {
		t.Fatalf("reading order: %+v", r.ReadingOrder)
	}
	if !reflect.DeepEqual(r.NextCommands[0], []string{"sprout", "context", "./cmd/app/main.go"}) {
		t.Fatalf("next: %v", r.NextCommands)
	}
	out, _, code := runCLI(t, "tour", dir)
	for _, want := range []string{"Tour of greetings", "Purpose (README.md excerpt)", "Layout", "Start here:", "likely entry point", "used by 1 file", "sprout context './cmd/app/main.go'", "not runtime behavior"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("text missing %q: %s", want, out)
		}
	}
	// The underlying reading sequence is unchanged for existing --entry users.
	entry, _, code := runCLI(t, dir, "--entry", "--no-config")
	if code != 0 || !strings.Contains(entry, "Reading order for greetings") || strings.Contains(entry, "likely entry point") {
		t.Fatalf("--entry contract changed: %s", entry)
	}
}

func TestTourDegradesWithoutEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"empty", nil},
		{"unsupported", map[string]string{"notes.xyz": "unrecognized source"}},
		{"readme without prose", map[string]string{"README.md": "# Heading\n\n![badge](x)\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, text := range tc.files {
				write(t, dir, rel, text)
			}
			t.Setenv("PATH", t.TempDir()) // no git or language runtimes required
			r, raw := tourJSON(t, dir)
			if r.Purpose != nil || len(r.Projects) != 0 || strings.Contains(raw, `"readingOrder":null`) || !strings.Contains(raw, "Git ignore rules are unavailable") {
				t.Fatalf("fallback: %s", raw)
			}
			out, _, code := runCLI(t, "tour", dir)
			if code != 0 || !strings.Contains(out, "no readable README prose") || !strings.Contains(out, "sprout -L 2") {
				t.Fatalf("fallback text: %s", out)
			}
		})
	}
}

func TestTourLimitsAndIgnoredFiles(t *testing.T) {
	dir := tourFixture(t)
	write(t, dir, ".sproutignore", "README.md\ngo.mod\nignored/\n")
	write(t, dir, "ignored/main.py", "secret")
	write(t, dir, ".github/workflows/ci.yml", "on: push")
	for i := 0; i < 20; i++ {
		write(t, dir, fmt.Sprintf("service%02d/main.py", i), "print('never executed')")
	}
	r, raw := tourJSON(t, dir, "--limit", "2")
	if r.Purpose != nil || len(r.Projects) != 0 || r.Skipped < 3 || strings.Contains(raw, "ignored/main.py") {
		t.Fatalf("ignored evidence leaked: %s", raw)
	}
	if len(r.ReadingOrder) != 2 || len(r.Layout) != 2 || r.OmittedReading == 0 || r.OmittedLayout != 21 {
		t.Fatalf("limits: %+v", r)
	}
	out, _, _ := runCLI(t, "tour", dir, "--limit", "2")
	if !strings.Contains(out, "omitted") || !strings.Contains(out, "raise --limit") {
		t.Fatalf("silent truncation: %s", out)
	}
}

func TestTourSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "secret", "PRIVATE_SENTINEL")
	for _, name := range []string{"README.md", "go.mod", "main.go", "linked-dir"} {
		target := filepath.Join(outside, "secret")
		if name == "linked-dir" {
			target = outside
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	r, raw := tourJSON(t, dir)
	if r.Files != 0 || r.Skipped != 4 || r.Purpose != nil || len(r.ReadingOrder) != 0 || strings.Contains(raw, "PRIVATE_SENTINEL") {
		t.Fatalf("symlink input: %s", raw)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, ".sproutignore")); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := runCLI(t, "tour", dir)
	if code != 1 || !strings.Contains(errOut, "regular file") {
		t.Fatalf("unsafe ignore file: %d %s", code, errOut)
	}
}

func TestTourUsage(t *testing.T) {
	dir := tourFixture(t)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0}, {[]string{"--nope"}, 2},
		{[]string{"--limit", "0"}, 2}, {[]string{"--limit", "51"}, 2},
		{[]string{"--limit", "x"}, 2}, {[]string{dir, "extra"}, 2},
		{[]string{filepath.Join(dir, "missing")}, 1},
		{[]string{filepath.Join(dir, "README.md")}, 1},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, _, code := runCLI(t, append([]string{"tour"}, tc.args...)...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d", code, tc.code)
			}
		})
	}
	chdir(t, dir)
	write(t, dir, ".sproutrc", "--not-a-valid-flag")
	_, errOut, code := runCLI(t, "tour")
	if code != 0 {
		t.Fatalf("default directory/config independence: %s", errOut)
	}
	write(t, dir, "tour/file.txt", "")
	out, _, code := runCLI(t, "./tour", "--no-config")
	if code != 0 || !strings.Contains(out, "file.txt") {
		t.Fatal("a folder named tour must remain accessible as ./tour")
	}
}

func TestTourPresentationSafety(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "README.txt", "# Heading\nVisit https://user:password@example.com/docs?token=secret to learn more.\n")
	r, raw := tourJSON(t, dir)
	if r.Purpose == nil || strings.Contains(raw, "password") || strings.Contains(raw, "secret") || r.ReadingOrder[0].Path != "README.txt" {
		t.Fatalf("unsafe excerpt: %s", raw)
	}
	if got := printable("hello\x1b[31m\n\u202eworld"); strings.ContainsAny(got, "\x1b\n\u202e") {
		t.Fatalf("terminal control characters: %q", got)
	}
	if runtime.GOOS != "windows" {
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("needs bash")
		}
		path := "./a'$(echo injected)/main.go"
		r.NextCommands = [][]string{{"sprout", "context", path}}
		var out bytes.Buffer
		if err := printTour(&out, r); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, "  sprout context ") {
				cmd := exec.Command(bash, "-c", "sprout() { printf '%s' \"$2\"; }; "+line)
				got, err := cmd.CombinedOutput()
				if err != nil || string(got) != path {
					t.Fatalf("unsafe command: %q: %v %s", line, err, got)
				}
				return
			}
		}
		t.Fatal("context suggestion missing")
	}
}

func TestTourRespectsGitIgnore(t *testing.T) {
	dir := setupGitRepo(t)
	write(t, dir, ".gitignore", "README.md\npackage.json\nprivate/\n")
	write(t, dir, "README.md", "PRIVATE_SENTINEL")
	write(t, dir, "package.json", "{}")
	write(t, dir, "private/main.py", "print('private')")
	r, raw := tourJSON(t, dir)
	if r.Purpose != nil || len(r.Projects) != 0 || strings.Contains(raw, "PRIVATE_SENTINEL") || strings.Contains(raw, "private/main.py") {
		t.Fatalf("gitignored evidence leaked: %s", raw)
	}
}

// A monorepo app's tsconfig often extends a base config above the app. That
// works inside the repository, but a project can't reach files past it.
func TestGraphConfigInheritance(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "project")
	write(t, parent, "base.json", `{"compilerOptions":{"paths":{"alias":["lib.ts"]}}}`)
	write(t, dir, "main.ts", `import { value } from 'alias';`)
	write(t, dir, "lib.ts", `export const value = 1;`)
	graphDeps := func(extends string) []string {
		write(t, dir, "tsconfig.json", `{"extends":"`+extends+`","compilerOptions":{"baseUrl":"."}}`)
		tree, err := BuildTree(dir, Options{MaxDepth: -1, Stat: true})
		if err != nil {
			t.Fatal(err)
		}
		return deps(t, buildGraph(dir, tree, false, false), "main.ts")
	}
	symlinks := os.Symlink(filepath.Join(parent, "base.json"), filepath.Join(dir, "linked.json")) == nil

	// No repository: the project folder is the limit, links included.
	if got := graphDeps("../base.json"); len(got) != 0 {
		t.Fatalf("config outside the folder was read: %v", got)
	}
	if got := graphDeps("./linked.json"); symlinks && len(got) != 0 {
		t.Fatalf("linked config outside the folder was read: %v", got)
	}
	if got := graphDeps("../"); len(got) != 0 {
		t.Fatalf("a directory was read as config: %v", got)
	}

	// The folder is an app inside a repository: its base config is read.
	if err := os.Mkdir(filepath.Join(parent, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := graphDeps("../base.json"); !reflect.DeepEqual(got, []string{"lib.ts"}) {
		t.Fatalf("inherited paths ignored: %v", got)
	}
}

func TestReadmeProse(t *testing.T) {
	for in, want := range map[string]string{
		"# Sprout\n\n**Map your codebase.**\n":                                    "Map your codebase.",
		"Anyhow&ensp;¯\\\\_(ツ)\\_/¯\n=====\n\nProvides [anyhow::Error][Error].\n": "Provides anyhow::Error.",
		"[<img src=x>](y)\n- ⚡ [**FastAPI**](https://x) for the `API`.\n":         "⚡ FastAPI for the API.",
		"---\n\nText &amp; more\n":                                                "Text & more",
		// bubbletea: the sentence wraps onto the next line
		"The fun way to build apps. A Go framework\nbased on The Elm Architecture.\n\nMore.\n": "The fun way to build apps. A Go framework based on The Elm Architecture.",
		// gleam: a licence header in an HTML comment
		"<!--\n  SPDX-License-Identifier: Apache-2.0\n-->\n\nGleam is friendly.\n": "Gleam is friendly.",
		// list items stand alone
		"- First item\n- Second item\n": "First item",
		// long text ends at a sentence, not mid-word
		strings.Repeat("Word ", 30) + "end. " + strings.Repeat("More ", 30) + "\n": strings.TrimSpace(strings.Repeat("Word ", 30)) + " end.",
	} {
		if got := readmeProse(strings.NewReader(in)); got != want {
			t.Errorf("readmeProse(%q) = %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	write(t, dir, "Readme.md", "Hello.")
	if got := readmeFile(dir); got != "Readme.md" || readmeSummary(dir) != "Hello." {
		t.Fatalf("readmeFile = %q", got)
	}
}

func TestTourHelpAndCompletions(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--man"}, {"--completion", "bash"}, {"--completion", "zsh"}, {"--completion", "fish"}, {"--completion", "powershell"}} {
		out, errOut, code := runCLI(t, args...)
		if code != 0 || !strings.Contains(out, "tour") {
			t.Errorf("%v missing tour: exit %d %s", args, code, errOut)
		}
	}
}

func TestTourDoesNotRunFSMonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX hook script")
	}
	dir := setupGitRepo(t)
	marker := filepath.Join(t.TempDir(), "executed")
	t.Setenv("SPROUT_TEST_MONITOR", marker)
	write(t, dir, "monitor.sh", "#!/bin/sh\nprintf ran > \"$SPROUT_TEST_MONITOR\"\n")
	git(t, dir, "config", "core.fsmonitor", "sh ./monitor.sh")
	tourJSON(t, dir)
	runCLI(t, dir, "--git", "--no-config")
	chdir(t, dir)
	runCLI(t, "impact")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("analysis executed a repository hook: %v", err)
	}
	// Positive control: ordinary Git invokes this fixture hook, proving
	// the assertion above depends on Sprout disabling it.
	git(t, dir, "ls-files")
	if data, err := os.ReadFile(marker); err != nil || string(data) != "ran" {
		t.Fatalf("fsmonitor fixture did not run in the control: %q, %v", data, err)
	}
}

func TestTourCycles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.ts", "import { helper } from './helper';\nexport const start = helper;\n")
	write(t, dir, "helper.ts", "import { start } from './main';\nexport const helper = start;\n")
	r, _ := tourJSON(t, dir)
	if len(r.ReadingOrder) != 2 || r.ReadingOrder[0].Path != "main.ts" || r.ReadingOrder[1].Path != "helper.ts" {
		t.Fatalf("cyclic graph must produce a deduplicated reading sequence: %v", r.ReadingOrder)
	}
}

func BenchmarkTour(b *testing.B) {
	dir := os.Getenv("SPROUT_BENCH_DIR")
	if dir == "" {
		dir = "testdata/tour"
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out, errOut bytes.Buffer
		if code := runTour([]string{dir, "--json"}, &out, &errOut); code != 0 {
			b.Fatalf("exit %d: %s", code, errOut.String())
		}
	}
}

// A library's tutorials have main.go files too; they aren't where to start.
func TestTourSkipsTutorials(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/lib\n")
	write(t, dir, "lib.go", "package lib\nfunc Run() {}\n")
	write(t, dir, "tutorials/basics/main.go", "package main\nimport \"example.com/lib\"\nfunc main() { lib.Run() }\n")
	r, _ := tourJSON(t, dir)
	for _, s := range r.ReadingOrder {
		if strings.HasPrefix(s.Path, "tutorials/") {
			t.Fatalf("tutorial ranked: %+v", r.ReadingOrder)
		}
	}
}
