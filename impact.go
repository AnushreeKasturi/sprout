package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// sprout impact: what a change could affect, and which tests cover it.
// The change is the files named, or what git says changed: uncommitted
// work (the default), the staged files, a revision range, or one commit.

type impactResult struct {
	SchemaVersion int        `json:"schemaVersion"`
	Command       string     `json:"command"`
	Source        string     `json:"source"`
	Changed       []string   `json:"changed"`
	Deleted       []string   `json:"deleted,omitempty"`
	Untracked     []string   `json:"notInGraph,omitempty"` // changed files that aren't source code Sprout reads
	Affected      []queryHit `json:"affected"`
	Tests         []string   `json:"tests"`
	GoPackages    []string   `json:"goTestPackages,omitempty"`
	TestCommands  []testCmd  `json:"testCommands,omitempty"` // Python and JS/TS tests, by project

	base string // with nothing uncommitted: the branch to suggest --diff against
}

// testCmd runs some of a change's tests: argv, run from Dir (relative to
// the root, "." for the root itself).
type testCmd struct {
	Dir    string   `json:"dir"`
	Argv   []string `json:"argv"`
	runner int      // Argv[:runner] is the runner, the rest test files
}

// runImpact runs impact. root is the project root; "" finds it from the
// first file, or the current directory.
func runImpact(args []string, root string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sprout impact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	staged := fs.Bool("staged", false, "the staged changes")
	diff := fs.String("diff", "", "the changes in a revision range, e.g. main...HEAD")
	commit := fs.String("commit", "", "the changes in one commit, e.g. HEAD")
	depth := fs.Int("depth", -1, "how many hops of dependents to follow (-1 for all)")
	all := fs.Bool("all", false, "list every affected file, not just direct dependents")
	asJSON := fs.Bool("json", false, "print JSON")
	files, err := parseFiles(fs, args)
	if err == flag.ErrHelp {
		fmt.Fprint(stdout, "Usage: sprout impact [FILE...] [--staged | --diff REV | --commit REV] [--depth N] [--all] [--json]\n\nWith no files, the uncommitted changes, untracked files included.\n")
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "Run 'sprout impact --help' for usage.")
		return 2
	}
	if count(*staged, *diff != "", *commit != "", len(files) > 0) > 1 {
		fmt.Fprintln(stderr, "sprout: impact takes files, --staged, --diff or --commit, one at a time")
		return 2
	}

	if root == "" {
		if root, err = impactRoot(files); err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 1
		}
	}

	// The changed paths, relative to root.
	res := impactResult{SchemaVersion: 1, Command: "impact", Source: "files"}
	var changed, deleted []string
	if len(files) > 0 {
		changed, err = namedChanges(root, files)
	} else {
		res.Source, changed, deleted, err = gitChanges(root, *staged, *diff, *commit)
	}
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	g, err := projectGraph(root, false)
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	if len(files) == 0 && len(changed)+len(deleted) == 0 {
		if repo, err := openRepo(root); err == nil {
			res.base = repo.defaultBranch()
		}
	}
	traceImpact(g, &res, root, changed, deleted, *depth)

	if *asJSON {
		explain(g, res.Affected, true)
		data, _ := json.Marshal(res)
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	printImpact(stdout, g, res, *all)
	return 0
}

// count is how many of on are true.
func count(on ...bool) int {
	n := 0
	for _, b := range on {
		if b {
			n++
		}
	}
	return n
}

// impactRoot is the project the change belongs to: around the first file
// named, else around the current folder.
func impactRoot(files []string) (string, error) {
	start, err := os.Getwd()
	if err == nil && len(files) > 0 {
		start, err = filepath.Abs(files[0])
	}
	if err != nil {
		return "", err
	}
	return projectRoot(start), nil
}

// parseFiles parses fs from args, with flags before, between or after any
// number of files.
func parseFiles(fs *flag.FlagSet, args []string) ([]string, error) {
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return files, nil
		}
		files = append(files, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// namedChanges is files as paths relative to root; each must exist.
func namedChanges(root string, files []string) ([]string, error) {
	var changed []string
	for _, f := range files {
		if url, ok := remoteURL(f); ok {
			return nil, fmt.Errorf("impact works on a local checkout; clone it first (git clone %s), then run sprout impact inside", url)
		}
		abs, err := filepath.Abs(f)
		if err == nil {
			_, err = os.Stat(abs)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: no such file", f)
		}
		rel, _ := filepath.Rel(root, abs)
		changed = append(changed, filepath.ToSlash(rel))
	}
	sort.Strings(changed)
	return changed, nil
}

// gitChanges is what changed according to git: the staged changes, a
// revision range, one commit, or by default everything uncommitted.
func gitChanges(root string, staged bool, diff, commit string) (source string, changed, deleted []string, err error) {
	repo, err := openRepo(root)
	if err != nil {
		return "", nil, nil, err
	}
	var codes map[string]string
	switch {
	case staged:
		source = "staged changes"
		codes, err = repo.staged()
	case diff != "":
		source = "diff " + diff
		codes, err = repo.changedIn(diff)
	case commit != "":
		source = "commit " + commit
		if strings.HasPrefix(commit, "-") {
			err = fmt.Errorf("invalid revision %q", commit)
		} else {
			codes, err = repo.changedIn(commit + "^!")
		}
	default:
		source = "uncommitted changes"
		codes, err = repo.status()
	}
	if err != nil {
		return "", nil, nil, err
	}
	for rel, code := range codes {
		if code == "D" {
			deleted = append(deleted, rel)
		} else {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	sort.Strings(deleted)
	return source, changed, deleted, nil
}

// traceImpact fills res from the changed files: what's in the graph, what
// depends on it, and the tests that reach it, with commands to run them.
func traceImpact(g *Graph, res *impactResult, root string, changed, deleted []string, depth int) {
	var ids []FileID
	res.Changed, res.Deleted = []string{}, deleted
	for _, rel := range changed {
		if id, ok := g.ID(rel); ok {
			ids = append(ids, id)
			res.Changed = append(res.Changed, rel)
		} else {
			res.Untracked = append(res.Untracked, rel)
		}
	}
	res.Affected = follow(g, ids, true, depth, false)

	// Tests: the changed tests themselves, and every test that reaches a change.
	tests := map[string]bool{}
	for _, id := range ids {
		if runnableTest(g.Files[id]) {
			tests[g.Files[id].Rel] = true
		}
	}
	for _, h := range res.Affected {
		if h.Test && runnableTest(g.Files[h.id]) {
			tests[h.Path] = true
		}
	}
	res.Tests = sortedKeys(tests)
	res.GoPackages = goTestPackages(res.Tests)
	res.TestCommands = testCommands(root, res.Tests)
}

// runnableTest reports whether a test file is one you run, rather than a
// helper living among the tests: in Python, conftest.py and tests/utils/
// are followed, so tests reached through them are found, but only files
// named test_*.py or *_test.py are tests to run.
func runnableTest(f GraphFile) bool {
	if !f.Test {
		return false
	}
	if base := path.Base(f.Rel); strings.HasSuffix(base, ".py") {
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
	}
	return true
}

var pyProjectFiles = []string{"pyproject.toml", "setup.cfg", "tox.ini", "pytest.ini", "setup.py"}

// testCommands groups Python and JS/TS tests by the project they belong to,
// the nearest folder with a Python project file or package.json, and builds
// that project's test command when its runner is recognisable: pytest,
// vitest or jest. Tests with no recognisable runner aren't given one.
func testCommands(root string, tests []string) []testCmd {
	runners := map[string][]string{} // "lang dir" -> runner, read once
	var cmds []testCmd
	index := map[string]int{} // "dir runner" -> its command in cmds
	for _, t := range tests {
		lang := langOf(path.Base(t))
		if lang != "py" && lang != "js" {
			continue
		}
		names := pyProjectFiles
		if lang == "js" {
			names = []string{"package.json"}
		}
		dir := nearestWith(root, path.Dir(t), names)
		key := lang + " " + dir
		runner, ok := runners[key]
		if !ok {
			runner = testRunner(root, dir, lang)
			runners[key] = runner
		}
		if runner == nil {
			continue
		}
		k := dir + " " + strings.Join(runner, " ")
		if _, ok := index[k]; !ok {
			index[k] = len(cmds)
			cmds = append(cmds, testCmd{dir, append([]string{}, runner...), len(runner)})
		}
		rel := t
		if dir != "." {
			rel = strings.TrimPrefix(t, dir+"/")
		}
		cmds[index[k]].Argv = append(cmds[index[k]].Argv, rel)
	}
	return cmds
}

// nearestWith is the closest folder from dir up to the root holding one of
// names, or "" if none does.
func nearestWith(root, dir string, names []string) string {
	for {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), n)); err == nil {
				return dir
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return ""
		}
		dir = path.Dir(dir)
	}
}

// testRunner is the test command of the project in dir, or nil: pytest when
// its project files or a conftest.py mention it, vitest or jest when
// package.json depends on them (or ava, or mocha).
func testRunner(root, dir, lang string) []string {
	if dir == "" {
		return nil
	}
	read := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), name))
		return string(data)
	}
	if lang == "py" {
		for _, n := range append([]string{"conftest.py"}, pyProjectFiles...) {
			if text := read(n); n == "conftest.py" && text != "" || strings.Contains(text, "pytest") {
				return []string{"python", "-m", "pytest"}
			}
		}
		return nil
	}
	var pkg struct{ Dependencies, DevDependencies map[string]any }
	if json.Unmarshal([]byte(read("package.json")), &pkg) != nil {
		return nil
	}
	has := func(name string) bool { return pkg.Dependencies[name] != nil || pkg.DevDependencies[name] != nil }
	switch {
	case has("vitest"):
		return []string{"npx", "vitest", "run"}
	case has("jest"):
		return []string{"npx", "jest"}
	case has("ava"):
		return []string{"npx", "ava"}
	case has("mocha"):
		return []string{"npx", "mocha"}
	}
	return nil
}

// projectGraph builds the dependency graph of root, tests included; with
// symbols, each file keeps its declarations' signatures.
func projectGraph(root string, symbols bool) (*Graph, error) {
	tree, err := BuildTree(root, Options{MaxDepth: -1, ShowHidden: true, Stat: true})
	if err != nil {
		return nil, err
	}
	return buildGraph(root, tree, true, symbols), nil
}

func goTestPackages(tests []string) []string {
	pkgs := map[string]bool{}
	for _, t := range tests {
		if strings.HasSuffix(t, "_test.go") {
			if dir := path.Dir(t); dir == "." {
				pkgs["."] = true
			} else {
				pkgs["./"+dir] = true
			}
		}
	}
	return sortedKeys(pkgs)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func printImpact(w io.Writer, g *Graph, res impactResult, all bool) {
	if len(res.Changed) == 0 {
		switch {
		case len(res.Untracked)+len(res.Deleted) > 0:
			fmt.Fprintf(w, "No source files Sprout reads changed (%s): nothing to trace\n", res.Source)
		default:
			fmt.Fprintf(w, "Nothing changed (%s)\n", res.Source)
		}
		if res.Source == "uncommitted changes" && res.base != "" {
			fmt.Fprintf(w, "For this branch's changes: sprout impact --diff %s...HEAD. For the last commit: --commit HEAD\n", res.base)
		} else if res.Source == "uncommitted changes" {
			fmt.Fprintln(w, "For the last commit: sprout impact --commit HEAD")
		}
		return
	}
	var code []queryHit
	direct := 0
	for _, h := range res.Affected {
		if h.Test {
			continue
		}
		code = append(code, h)
		if h.Depth == 1 {
			direct++
		}
	}
	if res.Source == "files" {
		fmt.Fprintf(w, "Impact of %s\n\n", plural(len(res.Changed), "file"))
	} else {
		fmt.Fprintf(w, "Impact of %s (%s)\n\n", plural(len(res.Changed), "changed file"), res.Source)
	}
	for _, c := range res.Changed {
		fmt.Fprintf(w, "  %s\n", printable(c))
	}
	printAffected(w, g, code, direct, all)
	printImpactTests(w, res, all)
	if len(res.Untracked) > 0 {
		fmt.Fprintf(w, "\nNot traced (not source code Sprout reads): %s\n", printable(strings.Join(capList(res.Untracked, 6), ", ")))
	}
	if len(res.Deleted) > 0 {
		fmt.Fprintf(w, "Deleted: %s (what imported them can't be traced from the files left)\n", printable(strings.Join(capList(res.Deleted, 6), ", ")))
	}
}

// printAffected lists the code a change reaches: the direct dependents with
// why, the rest counted unless all.
func printAffected(w io.Writer, g *Graph, code []queryHit, direct int, all bool) {
	fmt.Fprintf(w, "\nAffected: %s", plural(len(code), "file"))
	if len(code) > 0 {
		fmt.Fprintf(w, ", %d directly", direct)
	}
	fmt.Fprintln(w)
	shown := code
	if !all {
		shown = code[:direct]
	}
	explain(g, shown, true)
	width := 0
	for _, h := range shown {
		width = max(width, len(h.Path))
	}
	width = min(width, 60)
	for _, h := range shown {
		why := h.Reason
		if h.Via != "" {
			why = strings.TrimSuffix("via "+h.Via+" · "+why, " · ")
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, printable(h.Path), printable(why))
	}
	if rest := len(code) - len(shown); rest > 0 {
		fmt.Fprintf(w, "  … and %s through them (--all lists them)\n", plural(rest, "more file"))
	}
}

// commandLine is c ready to paste, with at most 12 files unless all; any
// note about the rest goes on its own line, so the command stays valid.
// The folder to run it from goes on a line before it: "cd dir &&" isn't
// valid in Windows PowerShell 5.1. ok is false when a path isn't shellSafe;
// the tests are then listed by name, and --json still has the argv.
func commandLine(c testCmd, all bool) (lines []string, ok bool) {
	for _, a := range append([]string{c.Dir}, c.Argv[c.runner:]...) {
		if !shellSafe(a) {
			return nil, false
		}
	}
	runner, files := c.Argv[:c.runner], c.Argv[c.runner:]
	total := len(files)
	if total > 12 && !all {
		files = files[:12]
	}
	indent := ""
	if c.Dir != "." {
		lines = append(lines, "in "+c.Dir+"/:")
		indent = "  "
	}
	lines = append(lines, indent+strings.Join(append(append([]string{}, runner...), files...), " "))
	if len(files) < total {
		lines = append(lines, fmt.Sprintf("%s(runs %d of %d; --all prints the full command)", indent, len(files), total))
	}
	return lines, true
}

// printCommand writes lines from commandLine, indented under Tests.
func printCommand(w io.Writer, lines []string) {
	for _, l := range lines {
		fmt.Fprintf(w, "  %s\n", l)
	}
}

// printImpactTests lists the tests that reach a change: a go test command
// for Go packages, pytest, vitest or jest commands where the project uses
// them, and the other test files by name.
func printImpactTests(w io.Writer, res impactResult, all bool) {
	fmt.Fprintf(w, "\nTests: %s", plural(len(res.Tests), "file"))
	if len(res.GoPackages) > 0 {
		fmt.Fprintf(w, ", %s", plural(len(res.GoPackages), "Go package"))
	}
	fmt.Fprintln(w)
	covered := map[string]bool{}
	if lines, ok := commandLine(testCmd{".", append([]string{"go", "test"}, res.GoPackages...), 2}, all); ok && len(res.GoPackages) > 0 {
		printCommand(w, lines)
		for _, t := range res.Tests {
			covered[t] = strings.HasSuffix(t, "_test.go")
		}
	}
	for _, c := range res.TestCommands {
		if lines, ok := commandLine(c, all); ok {
			printCommand(w, lines)
			for _, f := range c.Argv[c.runner:] {
				covered[path.Join(c.Dir, f)] = true
			}
		}
	}
	var other []string
	for _, t := range res.Tests {
		if !covered[t] {
			other = append(other, printable(t))
		}
	}
	if len(other) > 12 && !all {
		other = append(other[:12], fmt.Sprintf("… +%d more (--all)", len(other)-12))
	}
	for _, t := range other {
		fmt.Fprintf(w, "  %s\n", t)
	}
	if len(res.Tests) == 0 {
		fmt.Fprintln(w, "  none found: no test imports the changed files, directly or through others")
	}
}
