package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// sprout deps <file> and sprout dependents <file>: what a file depends on,
// and what depends on it, each with the reason, following the graph in
// codegraph.go for as many hops as asked.

type queryHit struct {
	Path   string `json:"path"`
	Depth  int    `json:"depth"`
	Via    string `json:"via,omitempty"`    // the file it was reached through, past the first hop
	Reason string `json:"reason,omitempty"` // the import, or the names used
	Test   bool   `json:"test,omitempty"`

	id, from FileID // the file, and the one it was reached from
}

type queryResult struct {
	SchemaVersion int        `json:"schemaVersion"`
	Command       string     `json:"command"`
	File          string     `json:"file"`
	Depth         int        `json:"depth"`
	Results       []queryHit `json:"results"`
}

// runQuery runs deps or dependents. root is the project root; "" finds it
// from the file (the nearest .git above it).
// notInGraph says why rel has no place in the dependency graph, and what to
// try instead.
func notInGraph(rel string) string {
	if langOf(rel) == "" {
		return rel + " isn't source code Sprout reads (Go, TypeScript/JavaScript, Python, Rust, Java, Kotlin). For an overview of the project, try sprout tour"
	}
	return rel + " isn't in the dependency graph: it's ignored, vendored or very large"
}

// fileArg resolves the FILE argument of deps, dependents and context to an
// absolute path, or says what's wrong and returns the exit code: 2 for no
// file or a folder, 1 for a file that isn't there. parseArgs gives "." when
// there's no argument, so args tells that apart from a "." typed.
func fileArg(cmd, arg string, args []string, stderr io.Writer) (string, int) {
	if url, ok := remoteURL(arg); ok && arg != "." {
		fmt.Fprintf(stderr, "sprout: %s works on a local checkout; clone it first (git clone %s), then run it inside\n", cmd, url)
		return "", 2
	}
	if arg == "." && !slices.Contains(args, ".") {
		fmt.Fprintf(stderr, "sprout: %s needs a file, e.g. sprout %s main.go\n", cmd, cmd)
		return "", 2
	}
	abs, err := filepath.Abs(arg)
	var info os.FileInfo
	if err == nil {
		info, err = os.Stat(abs)
	}
	switch {
	case os.IsNotExist(err):
		fmt.Fprintf(stderr, "sprout: %s: no such file\n", arg)
		return "", 1
	case err != nil:
		fmt.Fprintln(stderr, "sprout:", err)
		return "", 1
	case info.IsDir():
		fmt.Fprintf(stderr, "sprout: %s needs a file, and %s is a folder (did you mean `sprout %s --entry`?)\n", cmd, arg, arg)
		return "", 2
	}
	return abs, 0
}

func runQuery(cmd string, args []string, root string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sprout "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	depth := fs.Int("depth", 1, "how many hops to follow (-1 for all)")
	asJSON := fs.Bool("json", false, "print JSON")
	noTests := fs.Bool("no-tests", false, "leave test files out")
	arg, err := parseArgs(fs, args)
	if err == flag.ErrHelp {
		fmt.Fprintf(stdout, "Usage: sprout %s <file> [--depth N] [--no-tests] [--json]\n", cmd)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "Run 'sprout %s --help' for usage.\n", cmd)
		return 2
	}
	abs, code := fileArg(cmd, arg, args, stderr)
	if code != 0 {
		return code
	}
	if root == "" {
		root = projectRoot(abs)
	}
	g, err := projectGraph(root, false)
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	rel, _ := filepath.Rel(root, abs)
	rel = filepath.ToSlash(rel)
	id, ok := g.ID(rel)
	if !ok {
		fmt.Fprintln(stderr, "sprout:", notInGraph(rel))
		return 1
	}

	hits := follow(g, []FileID{id}, cmd == "dependents", *depth, *noTests)
	explain(g, hits, cmd == "dependents")
	if *asJSON {
		data, _ := json.Marshal(queryResult{1, cmd, rel, *depth, hits})
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	printHits(stdout, g, cmd, rel, hits)
	return 0
}

// follow walks the graph breadth-first from the start files, so each file
// is reported at its shortest distance, through the first file that
// reached it. Reasons are left for explain, which re-reads files.
func follow(g *Graph, start []FileID, reverse bool, depth int, noTests bool) []queryHit {
	next := g.Deps
	if reverse {
		next = g.Dependents
	}
	seen := map[FileID]bool{}
	for _, id := range start {
		seen[id] = true
	}
	frontier := start
	hits := []queryHit{}
	for d := 1; len(frontier) > 0 && (depth < 0 || d <= depth); d++ {
		var reached []FileID
		for _, p := range frontier {
			for _, c := range next(p) {
				if seen[c] || noTests && g.Files[c].Test {
					continue
				}
				seen[c] = true
				reached = append(reached, c)
				h := queryHit{Path: g.Files[c].Rel, Depth: d, Test: g.Files[c].Test, id: c, from: p}
				if d > 1 {
					h.Via = g.Files[p].Rel
				}
				hits = append(hits, h)
			}
		}
		frontier = reached
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		if a.Test != b.Test {
			return !a.Test
		}
		return a.Path < b.Path
	})
	return hits
}

// explain fills in why each hit is there.
func explain(g *Graph, hits []queryHit, reverse bool) {
	for i := range hits {
		h := &hits[i]
		if reverse {
			h.Reason = g.Why(h.id, h.from)
		} else {
			h.Reason = g.Why(h.from, h.id)
		}
	}
}

func printHits(w io.Writer, g *Graph, cmd, rel string, hits []queryHit) {
	tests := 0
	width := 0
	for _, h := range hits {
		if h.Test {
			tests++
		}
		width = max(width, len(h.Path))
	}
	width = min(width, 60) // a few very long paths shouldn't push every reason off screen
	n := plural(len(hits), "file")
	if tests > 0 {
		n += fmt.Sprintf(" (%s)", plural(tests, "test"))
	}
	switch {
	case len(hits) == 0 && cmd == "deps":
		fmt.Fprintf(w, "%s doesn't depend on other files in the project\n", rel)
		return
	case len(hits) == 0:
		fmt.Fprintf(w, "nothing in the project depends on %s\n", rel)
		return
	case cmd == "deps":
		fmt.Fprintf(w, "%s depends on %s\n\n", rel, n)
	default:
		fmt.Fprintf(w, "%s depend on %s\n\n", n, rel)
	}
	for _, h := range hits {
		var why []string
		if h.Test {
			if runnableTest(g.Files[h.id]) {
				why = append(why, "test")
			} else {
				why = append(why, "test helper")
			}
		}
		if h.Via != "" {
			why = append(why, "via "+h.Via)
		}
		if h.Reason != "" {
			why = append(why, h.Reason)
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, h.Path, strings.Join(why, " · "))
	}
}

// projectRoot is the nearest directory above the file holding .git, so
// imports resolve against the whole repository; outside one, the current
// directory if the file is under it, else the file's own directory.
// gitTop is the nearest folder at or above dir holding .git, or "".
func gitTop(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func projectRoot(file string) string {
	if dir := gitTop(filepath.Dir(file)); dir != "" {
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, file); err == nil && !strings.HasPrefix(rel, "..") {
			return cwd
		}
	}
	return filepath.Dir(file)
}
