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
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				fmt.Fprint(stdout, "Usage: sprout impact [FILE...] [--staged | --diff REV | --commit REV] [--depth N] [--all] [--json]\n\nWith no files, the uncommitted changes, untracked files included.\n")
				return 0
			}
			fmt.Fprintln(stderr, "Run 'sprout impact --help' for usage.")
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		args = fs.Args()[1:]
	}
	modes := 0
	for _, on := range []bool{*staged, *diff != "", *commit != "", len(files) > 0} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		fmt.Fprintln(stderr, "sprout: impact takes files, --staged, --diff or --commit, one at a time")
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	start := cwd
	if len(files) > 0 {
		if start, err = filepath.Abs(files[0]); err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 1
		}
	}
	if root == "" {
		root = projectRoot(start)
	}

	// The changed paths, relative to root.
	var changed, deleted []string
	res := impactResult{SchemaVersion: 1, Command: "impact"}
	if len(files) > 0 {
		res.Source = "files"
		for _, f := range files {
			abs, err := filepath.Abs(f)
			if err == nil {
				_, err = os.Stat(abs)
			}
			if err != nil {
				fmt.Fprintf(stderr, "sprout: %s: no such file\n", f)
				return 1
			}
			rel, _ := filepath.Rel(root, abs)
			changed = append(changed, filepath.ToSlash(rel))
		}
	} else {
		repo, err := openRepo(root)
		if err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 1
		}
		var codes map[string]string
		switch {
		case *staged:
			res.Source = "staged changes"
			codes, err = repo.staged()
		case *diff != "":
			res.Source = "diff " + *diff
			codes, err = repo.changedIn(*diff)
		case *commit != "":
			res.Source = "commit " + *commit
			if strings.HasPrefix(*commit, "-") {
				err = fmt.Errorf("invalid revision %q", *commit)
			} else {
				codes, err = repo.changedIn(*commit + "^!")
			}
		default:
			res.Source = "uncommitted changes"
			codes, err = repo.status()
		}
		if err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 1
		}
		for rel, code := range codes {
			if code == "D" {
				deleted = append(deleted, rel)
			} else {
				changed = append(changed, rel)
			}
		}
	}
	sort.Strings(changed)
	sort.Strings(deleted)

	g, err := projectGraph(root, false)
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
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
	res.Affected = follow(g, ids, true, *depth, false)

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

	if *asJSON {
		explain(g, res.Affected, true)
		data, _ := json.Marshal(res)
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	printImpact(stdout, g, res, *all)
	return 0
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
		fmt.Fprintf(w, "  %s\n", c)
	}

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
		fmt.Fprintf(w, "  %-*s  %s\n", width, h.Path, why)
	}
	if rest := len(code) - len(shown); rest > 0 {
		fmt.Fprintf(w, "  … and %s through them (--all lists them)\n", plural(rest, "more file"))
	}

	fmt.Fprintf(w, "\nTests: %s", plural(len(res.Tests), "file"))
	if len(res.GoPackages) > 0 {
		fmt.Fprintf(w, ", %s", plural(len(res.GoPackages), "Go package"))
	}
	fmt.Fprintln(w)
	if len(res.GoPackages) > 0 {
		pkgs := res.GoPackages
		more := ""
		if len(pkgs) > 12 && !all {
			more = fmt.Sprintf(" … +%d more (--all)", len(pkgs)-12)
			pkgs = pkgs[:12]
		}
		fmt.Fprintf(w, "  go test %s%s\n", strings.Join(pkgs, " "), more)
	}
	var other []string
	for _, t := range res.Tests {
		if !strings.HasSuffix(t, "_test.go") {
			other = append(other, t)
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

	if len(res.Untracked) > 0 {
		fmt.Fprintf(w, "\nNot traced (not source code Sprout reads): %s\n", strings.Join(capList(res.Untracked, 6), ", "))
	}
	if len(res.Deleted) > 0 {
		fmt.Fprintf(w, "Deleted: %s (what imported them can't be traced from the files left)\n", strings.Join(capList(res.Deleted, 6), ", "))
	}
}
