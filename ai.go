package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// estimateTokens uses the common ~4 bytes/token rule of thumb. It's only
// used to pick a level of detail, so being roughly right is enough.
func estimateTokens(s string) int { return (len(s) + 3) / 4 }

// AIMap renders a compact, structure-first description of a project for
// an LLM prompt or agent context: what it is, where things live, which files
// matter, and what's changing — fitted to about budget tokens.
func AIMap(t *Tree, root string, budget int) string {
	// Parse sources while git works out status and history.
	graph := make(chan *Graph, 1)
	go func() { graph <- buildGraph(root, t, false, true) }()

	var b strings.Builder
	s := &Stats{Languages: map[string]int{}}
	collectStats(t.Root, s)

	fmt.Fprintf(&b, "# %s\n", t.Root.Name)
	if desc := readmeSummary(root); desc != "" {
		fmt.Fprintf(&b, "%s\n", desc)
	}
	b.WriteString("\n")

	var stack []string
	for _, p := range DetectProject(root) {
		stack = append(stack, fmt.Sprintf("%s (%s)", p.Language, p.Manifest))
	}
	if len(stack) > 0 {
		fmt.Fprintf(&b, "stack: %s\n", strings.Join(stack, ", "))
	}
	if langs := topLanguages(s.Languages, s.Files, 6); langs != "" {
		fmt.Fprintf(&b, "languages: %s\n", langs)
	}
	fmt.Fprintf(&b, "size: %s, %s\n", plural(s.Files, "file"), plural(s.Directories, "directory"))

	if repo, err := openRepo(root); err == nil {
		writeGitContext(&b, repo)
	}

	entries, config := keyFiles(t.Root)
	if len(entries) > 0 {
		fmt.Fprintf(&b, "entry points: %s\n", strings.Join(entries, ", "))
	}
	if len(config) > 0 {
		fmt.Fprintf(&b, "config: %s\n", strings.Join(config, ", "))
	}

	// The structure gets three fifths of what's left and the key files the
	// rest, plus whatever the structure didn't need.
	g := <-graph
	ranked := g.Ranked()
	b.WriteString("\n## structure\n")
	remaining := budget - estimateTokens(b.String())
	share := remaining
	if len(ranked) > 0 {
		share = remaining * 3 / 5
	}
	structure := fitStructure(t.Root, share)
	b.WriteString(structure)
	writeKeyFiles(&b, g, ranked, remaining-estimateTokens(structure))
	return b.String()
}

// fitStructure spends the budget breadth-first: every top-level directory
// starts collapsed to a one-line summary, then directories are expanded in
// BFS order while the result still fits. Shallow structure always beats
// deep detail in one corner, and a directory too big to open doesn't stop
// cheaper siblings from opening.
func fitStructure(root *Node, budget int) string {
	budgetBytes := budget * 4
	if full := renderStructure(root, nil, 1<<30, 1<<30); len(full) <= budgetBytes {
		return full
	}

	const maxFiles, maxRootFiles = 8, 40
	sums := summaries(root)
	collapsed := func(n *Node, depth int) int {
		return len(strings.Repeat("  ", depth)+n.Name+"/ ("+sums[n]+")") + 1
	}

	open := map[*Node]bool{root: true}
	files, dirs := split(root)
	used := 0
	if len(files) > 0 {
		used += len("./ "+fileList(files, maxRootFiles)) + 1
	}
	type item struct {
		n     *Node
		depth int
	}
	// Examples, fixtures and vendored code only get what's left once the
	// project's own code has been laid out.
	var queue, later []item
	enqueue := func(n *Node, depth int) {
		if lowValueDirs[n.Name] {
			later = append(later, item{n, depth})
		} else {
			queue = append(queue, item{n, depth})
		}
	}
	for _, d := range dirs {
		used += collapsed(d, 0)
		enqueue(d, 0)
	}

	for len(queue)+len(later) > 0 {
		if len(queue) == 0 {
			queue, later = later, nil
		}
		it := queue[0]
		queue = queue[1:]
		files, dirs := split(it.n)
		cost := len(strings.Repeat("  ", it.depth)+it.n.Name+"/") + 1
		if len(files) > 0 {
			cost += len(" " + fileList(files, maxFiles))
		}
		for _, d := range dirs {
			cost += collapsed(d, it.depth+1)
		}
		if delta := cost - collapsed(it.n, it.depth); used+delta <= budgetBytes {
			used += delta
			open[it.n] = true
			for _, d := range dirs {
				enqueue(d, it.depth+1)
			}
		}
	}

	out := renderStructure(root, open, maxFiles, maxRootFiles)
	if len(out) > budgetBytes {
		out += "(truncated: raise --budget for more detail)\n"
	}
	return out
}

// renderStructure writes one line per directory with its files inline,
// which costs far fewer tokens than one line per file. Directories not in
// open are collapsed to a summary; a nil open expands everything.
func renderStructure(root *Node, open map[*Node]bool, maxFiles, maxRootFiles int) string {
	var b strings.Builder
	files, dirs := split(root)
	if len(files) > 0 {
		b.WriteString("./ " + fileList(files, maxRootFiles) + "\n")
	}
	var sums map[*Node]string
	if open != nil {
		sums = summaries(root)
	}
	var write func(n *Node, depth int)
	write = func(n *Node, depth int) {
		indent := strings.Repeat("  ", depth)
		switch {
		case n.Truncated:
			fmt.Fprintf(&b, "%s%s/ (not expanded)\n", indent, n.Name)
			return
		case open != nil && !open[n]:
			fmt.Fprintf(&b, "%s%s/ (%s)\n", indent, n.Name, sums[n])
			return
		}
		files, dirs := split(n)
		line := indent + n.Name + "/"
		if len(files) > 0 {
			line += " " + fileList(files, maxFiles)
		}
		b.WriteString(line + "\n")
		for _, d := range dirs {
			write(d, depth+1)
		}
	}
	for _, d := range dirs {
		write(d, 0)
	}
	return b.String()
}

// split separates files from directories, listing dotfiles last since
// they're usually tooling config rather than what a reader is looking for.
func split(n *Node) (files, dirs []*Node) {
	var dotfiles []*Node
	for _, c := range n.Children {
		switch {
		case c.IsDir:
			dirs = append(dirs, c)
		case c.Name[0] == '.':
			dotfiles = append(dotfiles, c)
		default:
			files = append(files, c)
		}
	}
	return append(files, dotfiles...), dirs
}

func fileList(files []*Node, max int) string {
	if max == 0 {
		return "(" + plural(len(files), "file") + ")"
	}
	var names []string
	for i, f := range files {
		if i == max {
			names = append(names, fmt.Sprintf("+%d more", len(files)-max))
			break
		}
		names = append(names, f.Name)
	}
	return strings.Join(names, " ")
}

// summaries describes every directory for its collapsed form, e.g.
// "42 files, mostly .tsx", in one pass over the tree.
func summaries(root *Node) map[*Node]string {
	out := map[*Node]string{}
	var count func(n *Node) map[string]int
	count = func(n *Node) map[string]int {
		exts := map[string]int{}
		for _, c := range n.Children {
			if c.IsDir {
				for e, k := range count(c) {
					exts[e] += k
				}
			} else {
				exts[filepath.Ext(c.Name)]++
			}
		}
		total, best, bestN := 0, "", 0
		for e, k := range exts {
			total += k
			if e != "" && (k > bestN || k == bestN && e < best) {
				best, bestN = e, k
			}
		}
		s := plural(total, "file")
		if bestN*2 > total {
			s += ", mostly " + best
		}
		out[n] = s
		return exts
	}
	count(root)
	return out
}

func topLanguages(langs map[string]int, total, limit int) string {
	type lc struct {
		name string
		n    int
	}
	// Only real languages: a pile of .gif or .golden files isn't a stack.
	known := map[string]bool{}
	for _, l := range languageNames {
		known[l] = true
	}
	var list []lc
	total = 0
	for name, n := range langs {
		if known[name] {
			list = append(list, lc{name, n})
			total += n
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].name < list[j].name
	})
	var parts []string
	for i, l := range list {
		if i == limit || total == 0 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d%%", l.name, (l.n*100+total/2)/total))
	}
	return strings.Join(parts, ", ")
}

// readmeSummary returns the first prose line of the README: usually the
// one-sentence pitch, which tells a model more than any file name.
func readmeSummary(root string) string {
	for _, name := range []string{"README.md", "README", "readme.md", "README.rst", "README.txt"} {
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			continue
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for i := 0; sc.Scan() && i < 40; i++ {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") ||
				strings.HasPrefix(line, "[!") || strings.HasPrefix(line, "<") || strings.HasPrefix(line, "=") {
				continue
			}
			line = strings.TrimLeft(line, "-*> ")
			if len(line) > 200 {
				line = line[:200] + "…"
			}
			return line
		}
		return ""
	}
	return ""
}

var entryNames = map[string]bool{
	"main.go": true, "main.py": true, "__main__.py": true, "app.py": true, "manage.py": true,
	"main.rs": true, "lib.rs": true, "index.js": true, "index.ts": true, "index.tsx": true,
	"main.js": true, "main.ts": true, "main.tsx": true, "server.js": true, "server.ts": true,
	"app.js": true, "app.ts": true, "Main.java": true, "Program.cs": true, "main.c": true,
	"main.cpp": true, "main.swift": true, "main.kt": true,
}

var configNames = map[string]bool{
	"Dockerfile": true, "Makefile": true, "Justfile": true, "Procfile": true,
	"docker-compose.yml": true, "docker-compose.yaml": true, "compose.yml": true, "compose.yaml": true,
	".env.example": true, "tsconfig.json": true, "vercel.json": true, "netlify.toml": true,
	"fly.toml": true, ".goreleaser.yaml": true, ".goreleaser.yml": true, "turbo.json": true,
	"pnpm-workspace.yaml": true, "go.work": true,
}

// keyFiles finds likely entry points and the config "control plane"
// (build, deploy, CI) wherever they live in the tree.
func keyFiles(root *Node) (entries, config []string) {
	walk(root, func(n *Node) {
		if n.IsDir || n == root {
			return
		}
		depth := strings.Count(n.Rel, "/")
		switch {
		case entryNames[n.Name] && depth <= 3 && !isTestPath(n.Rel):
			entries = append(entries, n.Rel)
		case configNames[n.Name],
			strings.HasPrefix(n.Rel, ".github/workflows/"),
			n.Name == ".gitlab-ci.yml",
			strings.Contains(n.Name, ".config.") && depth <= 1:
			config = append(config, n.Rel)
		}
	})
	return capList(entries, 10), capList(config, 15)
}

// lowValueDirs hold code that isn't the project itself: tests, samples,
// fixtures, vendored dependencies.
var lowValueDirs = map[string]bool{
	"test": true, "tests": true, "testdata": true, "__tests__": true, "examples": true,
	"example": true, "fixtures": true, "vendor": true, "third_party": true,
}

func isTestPath(rel string) bool {
	for _, part := range strings.Split(path.Dir(rel), "/") {
		if lowValueDirs[part] {
			return true
		}
	}
	return false
}

func capList(list []string, n int) []string {
	if len(list) > n {
		// Copy: appending to list[:n] would overwrite the caller's list[n].
		return append(append([]string{}, list[:n]...), fmt.Sprintf("+%d more", len(list)-n))
	}
	return list
}

// writeGitContext adds what a fresh reader can't see from the tree: the
// branch, uncommitted work, and where development has been concentrated.
func writeGitContext(b *strings.Builder, repo *gitRepo) {
	if br := repo.branch(); br != "" {
		fmt.Fprintf(b, "git: on %s", br)
		if changes, err := repo.status(); err == nil && len(changes) > 0 {
			var list []string
			for p, code := range changes {
				list = append(list, code+" "+p)
			}
			sort.Strings(list)
			fmt.Fprintf(b, ", uncommitted: %s", strings.Join(capList(list, 8), ", "))
		}
		b.WriteString("\n")
	}
	counts, err := repo.churn("90 days ago")
	if err != nil {
		return
	}
	type fc struct {
		p string
		n int
	}
	var hot []fc
	for p, n := range counts {
		if isFile(repo.dir, p) { // skip directories and files deleted since
			hot = append(hot, fc{p, n})
		}
	}
	sort.Slice(hot, func(i, j int) bool {
		if hot[i].n != hot[j].n {
			return hot[i].n > hot[j].n
		}
		return hot[i].p < hot[j].p
	})
	var parts []string
	for i, h := range hot {
		if i == 5 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", h.p, h.n))
	}
	if len(parts) > 0 {
		fmt.Fprintf(b, "most changed (90d, commits): %s\n", strings.Join(parts, ", "))
	}
}

func isFile(root, rel string) bool {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
}
