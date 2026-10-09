package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
)

type tourStep struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type tourDirectory struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
}

type tourPurpose struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type tourResult struct {
	SchemaVersion  int             `json:"schemaVersion"`
	Command        string          `json:"command"`
	Project        string          `json:"project"`
	Purpose        *tourPurpose    `json:"purpose"`
	Projects       []ProjectInfo   `json:"projects"`
	Languages      map[string]int  `json:"languages"`
	Files          int             `json:"files"`
	Directories    int             `json:"directories"`
	Skipped        int             `json:"skipped"`
	Layout         []tourDirectory `json:"layout"`
	ReadingOrder   []tourStep      `json:"readingOrder"`
	OmittedLayout  int             `json:"omittedLayout"`
	OmittedReading int             `json:"omittedReading"`
	NextCommands   [][]string      `json:"nextCommands"`
	Caveats        []string        `json:"caveats"`
}

func runTour(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sprout tour", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	asJSON := fs.Bool("json", false, "print versioned JSON")
	limit := fs.Int("limit", 8, "maximum reading steps and top-level directories (1-50)")
	root, err := parseArgs(fs, args)
	if err == flag.ErrHelp {
		fmt.Fprint(stdout, "Usage: sprout tour [path] [--json] [--limit N]\n\nAn offline reading plan for a local directory (default .).\n--limit defaults to 8 (1-50); limits output, not analysis.\nLike graph subcommands, tour does not load tree-view config.\n")
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "Run 'sprout tour --help' for usage.")
		return 2
	}
	if *limit < 1 || *limit > 50 {
		fmt.Fprintln(stderr, "sprout: tour --limit must be between 1 and 50")
		return 2
	}
	root, err = resolveRoot(root)
	if err != nil {
		fmt.Fprintln(stderr, "sprout: tour needs an existing local directory:", errReason(err))
		return 1
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(stderr, "sprout: tour needs a directory")
		return 1
	}
	// BuildTree reads this file before walking. Do not follow a link or
	// block on a FIFO supplied as an ignore file by an untrusted project.
	if info, err := os.Lstat(filepath.Join(root, ".sproutignore")); err == nil && !info.Mode().IsRegular() {
		fmt.Fprintln(stderr, "sprout: tour requires .sproutignore to be a regular file, not a link or special file")
		return 1
	}
	tree, err := BuildTree(root, Options{MaxDepth: -1, ShowHidden: true, Stat: true})
	if err != nil {
		fmt.Fprintln(stderr, "sprout: cannot read tour directory:", errReason(err))
		return 1
	}
	visible := tourFiles(tree, tree.Root)
	g := buildGraph(root, tree, false, false)
	result := gatherTour(root, tree, g, visible, *limit)
	if *asJSON {
		err = json.NewEncoder(stdout).Encode(result)
	} else {
		err = printTour(stdout, result)
	}
	if err != nil {
		fmt.Fprintln(stderr, "sprout: writing tour:", err)
		return 1
	}
	return 0
}

// tourFiles removes links and special files before any source or metadata
// reads. The tree walk itself never descends into symlinked directories.
func tourFiles(t *Tree, n *Node) map[string]bool {
	visible := map[string]bool{}
	var prune func(*Node)
	prune = func(n *Node) {
		kept := n.Children[:0]
		for _, c := range n.Children {
			if c.Special {
				t.Skipped++
				continue
			}
			kept = append(kept, c)
			if c.IsDir {
				prune(c)
			} else {
				visible[c.Rel] = true
			}
		}
		n.Children = kept
	}
	prune(n)
	return visible
}

func gatherTour(root string, t *Tree, g *Graph, visible map[string]bool, limit int) tourResult {
	s := &Stats{Languages: map[string]int{}}
	collectStats(t.Root, s)
	r := tourResult{
		SchemaVersion: 1, Command: "tour", Project: t.Root.Name,
		Projects: []ProjectInfo{}, Languages: s.Languages,
		Files: s.Files, Directories: s.Directories, Skipped: t.Skipped,
		Layout: []tourDirectory{}, ReadingOrder: []tourStep{}, NextCommands: [][]string{},
		Caveats: tourCaveats(t),
	}
	for _, p := range DetectProject(root) {
		if visible[p.Manifest] {
			r.Projects = append(r.Projects, p)
		}
	}
	for _, n := range t.Root.Children {
		if !n.IsDir || strings.HasPrefix(n.Name, ".") {
			continue // .github and editor folders aren't where the code lives
		}
		if len(r.Layout) == limit {
			r.OmittedLayout++
			continue
		}
		_, files := n.Count()
		r.Layout = append(r.Layout, tourDirectory{n.Rel, files})
	}
	var steps []tourStep
	r.Purpose, steps = tourReadingOrder(root, t, g, visible)
	if len(steps) > limit {
		r.OmittedReading, steps = len(steps)-limit, steps[:limit]
	}
	r.ReadingOrder = append(r.ReadingOrder, steps...) // [] rather than null in JSON
	r.NextCommands = tourNext(g, r.ReadingOrder)
	return r
}

func tourCaveats(t *Tree) []string {
	caveats := []string{"Entry points are filename heuristics; dependency counts describe static local relationships, not runtime behavior.",
		"Graph analysis covers Go, JS/TS, Python, Rust, Java and Kotlin; tests, examples and vendored code are not ranked. At most 50000 source files are considered; sources over 512 KiB are not parsed."}
	if !t.GitAware {
		caveats = append(caveats, "Git ignore rules are unavailable; using built-in exclusions and .sproutignore.")
	}
	unreadable := 0
	walk(t.Root, func(n *Node) {
		if n.Err != nil {
			unreadable++
		}
	})
	if unreadable > 0 {
		caveats = append(caveats, plural(unreadable, "directory")+" could not be read.")
	}
	return caveats
}

// tourNext suggests sprout context for the first reading step in the graph.
func tourNext(g *Graph, steps []tourStep) [][]string {
	var next [][]string
	for _, step := range steps {
		if _, ok := g.ID(step.Path); ok {
			next = append(next, []string{"sprout", "context", "./" + step.Path})
			break
		}
	}
	return append(next, []string{"sprout", "--ai"}, []string{"sprout", "-L", "2"})
}

func tourReadingOrder(root string, t *Tree, g *Graph, visible map[string]bool) (*tourPurpose, []tourStep) {
	var purpose *tourPurpose
	var steps []tourStep
	if name := readmeFile(root); visible[name] {
		steps = append(steps, tourStep{name, "project overview (README)"})
		f, err := os.Open(filepath.Join(root, name))
		if err == nil {
			text := readmeProse(io.LimitReader(f, 64<<10))
			f.Close()
			if text != "" {
				purpose = &tourPurpose{name, tourExcerpt(text, root)}
			}
		}
	}
	for _, step := range readingOrder(root, t, g, maxGraphFiles+16) {
		if !visible[step.rel] || step.why == "what the project is" {
			continue
		}
		why := step.why
		if why == "entry point" {
			why = "likely entry point (filename convention)"
		}
		steps = append(steps, tourStep{step.rel, why})
	}
	return purpose, steps
}

var tourURLs = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s<>"']+`)

// An excerpt is repository prose, not a generated architecture claim.
// Remove URL credentials/query values and this machine's private path prefix.
func tourExcerpt(text, root string) string {
	text = tourURLs.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[URL omitted]"
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return u.String()
	})
	text = strings.ReplaceAll(text, root, ".")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		text = strings.ReplaceAll(text, home, "~")
	}
	return text
}

// Keep repository-controlled names and prose from injecting terminal escapes.
func tourText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

func printTour(w io.Writer, r tourResult) error {
	var b strings.Builder
	writeTourSummary(&b, r)
	writeTourLayout(&b, r)
	writeTourReading(&b, r)
	writeTourNext(&b, r.NextCommands)
	// The first two caveats hold for every tour; JSON keeps them for scripts.
	fmt.Fprintln(&b, "\nNote: suggestions are heuristics from static analysis, not runtime behavior.")
	for _, caveat := range r.Caveats[min(2, len(r.Caveats)):] {
		fmt.Fprintf(&b, "Note: %s\n", tourText(caveat))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeTourSummary(b *strings.Builder, r tourResult) {
	fmt.Fprintf(b, "Tour of %s\n\n", tourText(r.Project))
	if r.Purpose != nil {
		fmt.Fprintf(b, "Purpose (%s excerpt): %s\n", tourText(r.Purpose.Path), tourText(r.Purpose.Text))
	} else {
		fmt.Fprintln(b, "Purpose: no readable README prose found.")
	}
	fmt.Fprintf(b, "%s, %s (%s ignored or skipped)\n", plural(r.Files, "file"), plural(r.Directories, "directory"), plural(r.Skipped, "entry"))
	if len(r.Projects) == 0 {
		fmt.Fprintln(b, "Ecosystems: no recognized root manifest found.")
	}
	for _, p := range r.Projects {
		fmt.Fprintf(b, "Ecosystem: %s (%s; %s)\n", p.Language, p.Manifest, p.PackageManager)
	}
	if langs := topLanguages(r.Languages, 6); langs != "" {
		fmt.Fprintf(b, "Languages: %s\n", langs)
	}
}

func writeTourLayout(b *strings.Builder, r tourResult) {
	fmt.Fprintln(b, "\nLayout (top-level directories):")
	if len(r.Layout) == 0 {
		fmt.Fprintln(b, "  No visible subdirectories.")
	}
	for _, d := range r.Layout {
		fmt.Fprintf(b, "  %s/ (%s)\n", tourText(d.Path), plural(d.Files, "file"))
	}
	if r.OmittedLayout > 0 {
		fmt.Fprintf(b, "  +%d directories omitted; raise --limit (maximum 50) or use sprout -L 2.\n", r.OmittedLayout)
	}
}

func writeTourReading(b *strings.Builder, r tourResult) {
	fmt.Fprintln(b, "\nStart here:")
	if len(r.ReadingOrder) == 0 {
		fmt.Fprintln(b, "  No README, likely entry points or local dependencies found to rank.")
	}
	for i, step := range r.ReadingOrder {
		fmt.Fprintf(b, "  %d. %s — %s\n", i+1, tourText(step.Path), tourText(step.Reason))
	}
	if r.OmittedReading > 0 {
		fmt.Fprintf(b, "  +%d reading candidates omitted; raise --limit (maximum 50).\n", r.OmittedReading)
	}
}

func writeTourNext(b *strings.Builder, commands [][]string) {
	fmt.Fprintln(b, "\nNext (run from the directory you toured; POSIX shell or PowerShell):")
	for _, argv := range commands {
		// Only the context path is repository-controlled. Single quotes
		// prevent expansion; PowerShell and POSIX escape apostrophes differently.
		parts := append([]string{}, argv...)
		if len(parts) == 3 && parts[1] == "context" {
			if tourText(parts[2]) != parts[2] {
				continue // no misleading executable suggestion for control characters
			}
			escape := `'"'"'`
			if runtime.GOOS == "windows" {
				escape = "''"
			}
			parts[2] = "'" + strings.ReplaceAll(parts[2], "'", escape) + "'"
		}
		fmt.Fprintf(b, "  %s\n", strings.Join(parts, " "))
	}
}
