package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sprout context FILE: what to know before editing a file, fitted to a
// token budget. What it declares; what it depends on, with only the
// signatures it uses from each; who uses it and why; and the tests that
// reach it.

func runContext(args []string, root string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sprout context", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	budget := fs.Int("budget", 1500, "approximate token budget")
	arg, err := parseArgs(fs, args)
	if err == flag.ErrHelp {
		fmt.Fprint(stdout, "Usage: sprout context <file> [--budget N]\n")
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "Run 'sprout context --help' for usage.")
		return 2
	}
	if arg == "." {
		fmt.Fprintln(stderr, "sprout: context needs a file, e.g. sprout context main.go")
		return 2
	}
	abs, err := filepath.Abs(arg)
	if err == nil {
		_, err = os.Stat(abs)
	}
	if err != nil {
		fmt.Fprintf(stderr, "sprout: %s: no such file\n", arg)
		return 1
	}
	if root == "" {
		root = projectRoot(abs)
	}
	tree, err := BuildTree(root, Options{MaxDepth: -1, ShowHidden: true, Stat: true})
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	g := buildGraph(root, tree, true, true)
	rel, _ := filepath.Rel(root, abs)
	rel = filepath.ToSlash(rel)
	id, ok := g.ID(rel)
	if !ok {
		fmt.Fprintf(stderr, "sprout: %s isn't in the dependency graph: Sprout reads Go, TypeScript/JavaScript, Python, Rust, Java and Kotlin, skipping ignored, vendored and very large files\n", rel)
		return 1
	}
	src, _ := os.ReadFile(abs)
	fmt.Fprint(stdout, fileContext(g, id, string(src), *budget))
	return 0
}

// fileContext renders the context of one file, spending about budget tokens.
func fileContext(g *Graph, id FileID, src string, budget int) string {
	f := g.Files[id]
	deps := follow(g, []FileID{id}, false, 1, false)
	users := follow(g, []FileID{id}, true, 1, false)
	reach := follow(g, []FileID{id}, true, -1, false)
	explain(g, deps, false)
	explain(g, users, true)
	var tests []string
	affected := 0
	for _, h := range reach {
		if h.Test {
			tests = append(tests, h.Path)
		} else {
			affected++
		}
	}
	// tests is nearest first: the ones that import the file come before
	// the ones that reach it through others.
	var code []queryHit
	for _, h := range users {
		if !h.Test {
			code = append(code, h)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", f.Rel)
	summary := []string{plural(len(deps), "dependency"), plural(len(code), "direct user")}
	if affected > len(code) {
		summary = append(summary, fmt.Sprintf("%d affected through them", affected))
	}
	summary = append(summary, plural(len(tests), "test")+" reach it")
	fmt.Fprintf(&b, "%s\n", strings.Join(summary, " · "))
	used := estimateTokens(b.String())

	// Each section gets a share of what's left, and passes on what it
	// doesn't spend. The file's own declarations come last: whoever edits
	// it reads those anyway; the rest is what they can't see from it.
	add := func(title string, items []string, share float64) {
		if len(items) == 0 {
			return
		}
		limit := used + int(float64(budget-used)*share)
		head := "\n## " + title + "\n"
		if used+estimateTokens(head)+8 > limit {
			fmt.Fprintf(&b, "\n## %s: %d (raise --budget)\n", title, len(items))
			used += 6
			return
		}
		b.WriteString(head)
		used += estimateTokens(head)
		for i, it := range items {
			line := it + "\n"
			if used+estimateTokens(line) > limit {
				fmt.Fprintf(&b, "+%d more\n", len(items)-i)
				used += 3
				return
			}
			b.WriteString(line)
			used += estimateTokens(line)
		}
	}

	var depLines []string
	for _, d := range deps {
		depLines = append(depLines, strings.TrimSuffix(d.Path+" — "+d.Reason, " — "))
		for _, s := range capList(usedSymbols(g.Files[d.id].Symbols, src, g.UsedNames(id, d.id)), 4) {
			depLines = append(depLines, "  "+s)
		}
	}
	add("depends on (and what it uses from each)", depLines, 0.45)

	var userLines []string
	for _, u := range code {
		userLines = append(userLines, strings.TrimSuffix(u.Path+" — "+u.Reason, " — "))
	}
	add("used by", userLines, 0.4)

	var testLines []string
	if pkgs := goTestPackages(tests); len(pkgs) > 0 { // sorted by package
		testLines = append(testLines, "go test "+strings.Join(capList(pkgs, 8), " "))
	}
	testLines = append(testLines, capList(tests, 8)...)
	add("tests that reach it", testLines, 0.45)

	add("declares", f.Symbols, 1)
	return b.String()
}

var declName = regexp.MustCompile(`(?:func\s+(?:\([^)]*\)\s*)?|type\s+|class\s+|def\s+|fn\s+|struct\s+|enum\s+|trait\s+|interface\s+|const\s+|let\s+|var\s+|function\*?\s+|object\s+|fun\s+|record\s+|mod\s+)([A-Za-z_$][\w$]*)`)

// usedSymbols keeps the declarations a file uses from one of its
// dependencies. With the exact names (Go), those, and methods on those
// types that src calls; otherwise, declarations whose name appears in src.
func usedSymbols(symbols []string, src string, names []string) []string {
	exact := map[string]bool{}
	for _, n := range names {
		exact[n] = true
	}
	var out []string
	for _, s := range symbols {
		m := declName.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		switch {
		case len(exact) == 0:
			if wordIn(src, m[1]) {
				out = append(out, s)
			}
		case exact[m[1]] && !goMethod.MatchString(s):
			out = append(out, s)
		case goMethod.MatchString(s):
			if r := goMethod.FindStringSubmatch(s); exact[r[1]] && wordIn(src, m[1]) {
				out = append(out, s)
			}
		}
	}
	return out
}

// goMethod captures a Go method's receiver type: func (g *Graph) ...
var goMethod = regexp.MustCompile(`^func \(\w*\s*\*?(\w+)`)

// wordIn reports whether name occurs in src as a whole identifier.
func wordIn(src, name string) bool {
	isWord := func(c byte) bool {
		return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	for i := 0; ; {
		j := strings.Index(src[i:], name)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(name)
		if (j == 0 || !isWord(src[j-1])) && (end == len(src) || !isWord(src[end])) {
			return true
		}
		i = j + 1
	}
}
