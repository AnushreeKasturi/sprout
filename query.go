package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
}

type queryResult struct {
	SchemaVersion int        `json:"schemaVersion"`
	Command       string     `json:"command"`
	File          string     `json:"file"`
	Depth         int        `json:"depth"`
	Results       []queryHit `json:"results"`
}

func runQuery(cmd string, args []string, stdout, stderr io.Writer) int {
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
	if arg == "." {
		fmt.Fprintf(stderr, "sprout: %s needs a file, e.g. sprout %s main.go\n", cmd, cmd)
		return 2
	}

	abs, err := filepath.Abs(arg)
	if err == nil {
		_, err = os.Stat(abs)
	}
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	root := projectRoot(abs)
	tree, err := BuildTree(root, Options{MaxDepth: -1, ShowHidden: true, Stat: true})
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	g := buildGraph(root, tree, true, false)
	rel, _ := filepath.Rel(root, abs)
	rel = filepath.ToSlash(rel)
	id, ok := g.ID(rel)
	if !ok {
		fmt.Fprintf(stderr, "sprout: %s isn't in the dependency graph: Sprout reads Go, TypeScript/JavaScript, Python, Rust, Java and Kotlin, skipping ignored, vendored and very large files\n", rel)
		return 1
	}

	hits := follow(g, id, cmd == "dependents", *depth, *noTests)
	if *asJSON {
		data, _ := json.Marshal(queryResult{1, cmd, rel, *depth, hits})
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	printHits(stdout, cmd, rel, hits)
	return 0
}

// follow walks the graph breadth-first from id, so each file is reported at
// its shortest distance, through the first file that reached it.
func follow(g *Graph, id FileID, reverse bool, depth int, noTests bool) []queryHit {
	next := g.Deps
	if reverse {
		next = g.Dependents
	}
	seen := map[FileID]bool{id: true}
	frontier := []FileID{id}
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
				h := queryHit{Path: g.Files[c].Rel, Depth: d, Test: g.Files[c].Test}
				if d > 1 {
					h.Via = g.Files[p].Rel
				}
				if reverse {
					h.Reason = g.Why(c, p)
				} else {
					h.Reason = g.Why(p, c)
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

func printHits(w io.Writer, cmd, rel string, hits []queryHit) {
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
			why = append(why, "test")
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
func projectRoot(file string) string {
	for dir := filepath.Dir(file); ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, file); err == nil && !strings.HasPrefix(rel, "..") {
			return cwd
		}
	}
	return filepath.Dir(file)
}
