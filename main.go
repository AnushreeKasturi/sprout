package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// usage is printed by --help. Lines that don't start with a space are
// headings, bold on a terminal (printUsage).
const usage = `sprout: map your codebase, for you and your AI agent

Usage
  sprout [path] [flags]           tree of a folder (default .)
  sprout github.com/owner/repo    a remote repository; any git URL works

Commands
  tour [path]           purpose, layout and where to start reading
  deps FILE             what FILE depends on, and why
  dependents FILE       what depends on FILE, tests included
  impact [FILE...]      what a change could break, and the tests to run
  context FILE          what to know before editing FILE, in a token budget
  mcp [root]            serve all of this to coding agents over MCP
  Each command has --help. To map a folder named like one, write ./deps.

Views
  --ai                  project map for LLMs, with signatures (--budget N)
  --entry               reading order: README, entry points, most-used files
  --diff REV            only what changed in REV, e.g. main...HEAD or HEAD~3
  --stats               files, size, languages and detected stack
  --json                tree and stats as JSON (--pretty to indent)

Sizes and order
  --size                file sizes and true folder totals, even past --depth
  --sort KEY            name, size or time; -r reverses; --dirs-first
  --si                  sizes in powers of 1000 instead of 1024

Annotations
  --git                 mark changed files: M, A, D, R, ? untracked, U conflict
  --churn               commits per path to spot hotspots; --since '90 days ago'

Filtering
  -L, --depth N         limit depth
  -a, --all             show hidden and ignored entries
  --hidden              show dotfiles
  --no-ignore           don't hide .gitignore, .sproutignore or built-in matches
  --ignore PATTERNS     hide matches, gitignore syntax: '*.log', 'src/gen/'
  --only PATTERNS       show only matches: '*.go', 'web/src/**/*.tsx'
  --changed-within AGE  only files modified recently: 30m, 12h, 7d, 2w
  --max-files N         at most N files per folder, then "… 37 more files"
  --hyperlink           clickable file names in terminals that support links

Config
  --no-config           ignore config files (~/.config/sprout/config, .sproutrc)
  --completion SHELL    completions for bash, zsh, fish or powershell
  --man                 print the man page
  --version             print the version

Examples
  sprout -L 2
  sprout tour
  sprout --ai | pbcopy
  sprout impact --diff main...HEAD
  sprout github.com/charmbracelet/bubbletea --entry
`

// printUsage prints usage with bold headings when w is a terminal.
func printUsage(w io.Writer, color bool) {
	for _, line := range strings.SplitAfter(usage, "\n") {
		if line != "\n" && !strings.HasPrefix(line, " ") {
			line = paint(color, bold, strings.TrimSuffix(line, "\n")) + "\n"
		}
		io.WriteString(w, line)
	}
}

// version is set at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(serveMCP(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// flags is everything the command line (and config files) can set.
type flags struct {
	all, hidden, noIgnore, noConfig       bool
	stats, json, git, churn, ai, showVers bool
	entry, man, pretty                    bool
	size, si, reverse, dirsFirst, links   bool
	depth, budget, maxFiles               int
	ignore, only                          patternList
	since, diff, sortBy, within, complete string
}

func newFlagSet(f *flags, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("sprout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {} // errors get a one-line hint; --help gets usage on stdout

	fs.BoolVar(&f.all, "all", false, "show everything: hidden files and ignored entries")
	fs.BoolVar(&f.all, "a", false, "shorthand for --all")
	fs.BoolVar(&f.hidden, "hidden", false, "show hidden files and directories")
	fs.BoolVar(&f.noIgnore, "no-ignore", false, "don't apply .gitignore, .sproutignore or the built-in ignore list")
	fs.BoolVar(&f.noConfig, "no-config", false, "ignore config files")
	fs.IntVar(&f.depth, "depth", -1, "limit directory depth (-1 for unlimited)")
	fs.IntVar(&f.depth, "L", -1, "shorthand for --depth")
	fs.Var(&f.ignore, "ignore", "gitignore-style patterns to hide, comma-separated or repeated")
	fs.Var(&f.only, "only", "show only files matching these patterns, e.g. '*.go' or 'src/**/*.ts'")
	fs.BoolVar(&f.size, "size", false, "show file sizes and total directory sizes")
	fs.BoolVar(&f.si, "si", false, "sizes in powers of 1000 instead of 1024")
	fs.StringVar(&f.sortBy, "sort", "name", "order entries by name, size (largest first) or time (newest first)")
	fs.BoolVar(&f.reverse, "reverse", false, "reverse the sort order")
	fs.BoolVar(&f.reverse, "r", false, "shorthand for --reverse")
	fs.StringVar(&f.within, "changed-within", "", "only files modified within this long, e.g. 30m, 12h, 7d, 2w")
	fs.IntVar(&f.maxFiles, "max-files", 0, "list at most N files per directory (0 for all)")
	fs.BoolVar(&f.links, "hyperlink", false, "make names clickable in terminals that support OSC 8 links")
	fs.BoolVar(&f.dirsFirst, "dirs-first", false, "list directories before files")
	fs.BoolVar(&f.stats, "stats", false, "show project statistics instead of the tree")
	fs.BoolVar(&f.json, "json", false, "print the tree and statistics as JSON")
	fs.BoolVar(&f.pretty, "pretty", false, "with --json: indent the output for reading")
	fs.BoolVar(&f.git, "git", false, "mark changed files with their git status")
	fs.BoolVar(&f.churn, "churn", false, "show how many commits touched each path (hotspots)")
	fs.StringVar(&f.since, "since", "", "with --churn: only count commits since this date, e.g. '90 days ago'")
	fs.BoolVar(&f.ai, "ai", false, "print a compact project map for LLM prompts and agents")
	fs.BoolVar(&f.entry, "entry", false, "suggest a reading order: README, entry points, then the most used files")
	fs.IntVar(&f.budget, "budget", 2000, "with --ai: approximate token budget")
	fs.StringVar(&f.diff, "diff", "", "show only paths changed in a git revision range, e.g. main...HEAD")
	fs.StringVar(&f.complete, "completion", "", "print a shell completion script: bash, zsh, fish or powershell")
	fs.BoolVar(&f.man, "man", false, "print the man page (roff)")
	fs.BoolVar(&f.showVers, "version", false, "print version and exit")
	return fs
}

// parseFlags reads the command line, then re-reads it on top of the config
// files for the target directory, so the command line always wins.
func parseFlags(args []string, stderr io.Writer) (*flags, string, error) {
	f := &flags{}
	path, err := parseArgs(newFlagSet(f, stderr), args)
	if err != nil || f.noConfig || f.showVers || f.man || f.complete != "" {
		return f, path, err
	}
	dir := path
	if _, remote := remoteURL(path); remote {
		dir = "" // a cloned repo's own .sproutrc is untrusted; only the user's config applies
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		fmt.Fprintln(stderr, "sprout: config:", err)
		return f, path, err
	}
	if len(cfg) == 0 {
		return f, path, nil
	}
	f = &flags{}
	fs := newFlagSet(f, stderr)
	if err := fs.Parse(cfg); err != nil {
		fmt.Fprintln(stderr, "sprout: in a config file (run with --no-config to skip them)")
		return f, path, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "sprout: config files can only set flags, not %q\n", fs.Arg(0))
		return f, path, fmt.Errorf("positional argument in config")
	}
	_, err = parseArgs(fs, args)
	return f, path, err
}

// runSubcommand runs a graph subcommand, or reports that name isn't one.
func runSubcommand(name string, args []string, stdout, stderr io.Writer) (int, bool) {
	switch name {
	case "deps", "dependents":
		return runQuery(name, args, "", stdout, stderr), true
	case "impact":
		return runImpact(args, "", stdout, stderr), true
	case "context":
		return runContext(args, "", stdout, stderr), true
	case "tour":
		return runTour(args, stdout, stderr), true
	}
	return 0, false
}

func run(args []string, out, stderr io.Writer) int { // skipcq: GO-R1005 pre-existing; mode dispatch is one flat list of flags
	// One write per line made printing a 30k-file tree take seconds.
	buf := bufio.NewWriterSize(out, 64<<10)
	defer buf.Flush()
	var stdout io.Writer = buf

	if len(args) > 0 {
		if code, ok := runSubcommand(args[0], args[1:], stdout, stderr); ok {
			return code
		}
	}

	f, path, err := parseFlags(args, stderr)
	if err == flag.ErrHelp {
		printUsage(stdout, useColor(out))
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "Run 'sprout --help' for usage.")
		return 2
	}

	if f.showVers {
		fmt.Fprintln(stdout, "sprout", resolveVersion())
		return 0
	}
	if f.man {
		writeManPage(stdout)
		return 0
	}
	if f.complete != "" {
		if err := writeCompletion(stdout, f.complete); err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 2
		}
		return 0
	}

	if err := validSort(f.sortBy); err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 2
	}
	if f.depth < -1 {
		fmt.Fprintln(stderr, "sprout: --depth must be -1 (unlimited) or more")
		return 2
	}
	if f.budget < 1 {
		fmt.Fprintln(stderr, "sprout: --budget must be at least 1")
		return 2
	}

	var since time.Time
	if f.within != "" {
		age, err := parseAge(f.within)
		if err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 2
		}
		since = time.Now().Add(-age)
	}

	shown := path
	if url, ok := remoteURL(path); ok {
		dir, cleanup, err := cloneRemote(url, f.git || f.churn || f.diff != "", stderr)
		if err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 1
		}
		defer cleanup()
		path, shown = dir, filepath.Base(dir)
	}

	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(stderr, "sprout: %s is not a directory\n", path)
		return 1
	}

	opts := Options{
		// --ai wants .github/ and friends; ignore rules still drop the junk.
		ShowHidden: f.hidden || f.all || f.ai || f.entry,
		MaxDepth:   f.depth,
		Ignore:     f.ignore,
		Only:       f.only,
		NoIgnore:   f.noIgnore || f.all,
		Sizes:      f.size || f.sortBy != "name", // sorting a collapsed folder needs what's inside it
		Since:      since,
		Stat:       f.json || f.stats || f.ai || f.entry,
	}

	tree, branch, err := load(path, opts, f.git, f.diff, stderr)
	if err == nil && f.churn {
		err = addChurn(path, tree, f.since)
	}
	if err != nil {
		fmt.Fprintln(stderr, "sprout:", err)
		return 1
	}

	if f.diff == "" && (f.sortBy != "name" || f.reverse || f.dirsFirst) {
		sortTree(tree.Root, f.sortBy, f.reverse, f.dirsFirst)
	}

	if f.maxFiles > 0 && !f.ai {
		capFiles(tree.Root, f.maxFiles)
	}

	if f.entry {
		steps := readingOrder(path, tree, buildGraph(path, tree, false, false), 15)
		printReadingOrder(stdout, tree.Root.Name, steps)
		return 0
	}

	if f.ai {
		m := AIMap(tree, path, f.budget)
		if n := estimateTokens(m); n > f.budget {
			fmt.Fprintf(stderr, "sprout: the smallest map of this project is about %d tokens, over --budget %d\n", n, f.budget)
		}
		fmt.Fprint(stdout, m)
		return 0
	}

	if f.json {
		if err := WriteJSON(stdout, tree, path, f.pretty); err != nil {
			fmt.Fprintln(stderr, "sprout:", err)
			return 1
		}
		return 0
	}

	if f.stats {
		PrintStats(stdout, tree, path, f.si)
		return 0
	}

	p := printer{w: stdout, color: useColor(out), sizes: f.size, si: f.si, fs: tree}
	if f.links && isTerminal(out) { // never write escape codes into pipes
		p.links = true
		p.host, _ = os.Hostname()
	}
	if f.churn {
		p.churnFiles, p.churnDirs = churnMax(tree.Root)
	}
	header := paint(p.color, blue+";"+bold, shown)
	if branch != "" {
		header += paint(p.color, dim, " "+branch)
	}
	fmt.Fprintln(stdout, header)
	p.tree(tree.Root, "")
	if f.diff != "" {
		fmt.Fprintf(stdout, "\n%s changed, +%d -%d\n", plural(tree.Root.Changes, "file"), tree.Root.Added, tree.Root.Deleted)
	} else {
		printSummary(stdout, tree)
	}
	return 0
}

func addChurn(path string, t *Tree, since string) error {
	repo, err := openRepo(path)
	if err != nil {
		return err
	}
	counts, err := repo.churn(since)
	if err != nil {
		return err
	}
	applyChurn(t.Root, counts)
	return nil
}

// load builds the tree for the requested mode: a filesystem walk, optionally
// annotated with git status, or a tree of just the paths in a git diff.
func load(path string, opts Options, gitStatus bool, diffRev string, stderr io.Writer) (*Tree, string, error) {
	if !gitStatus && diffRev == "" {
		t, err := BuildTree(path, opts)
		return t, "", err
	}

	repo, err := openRepo(path)
	if err != nil && diffRev == "" {
		// --git has nothing to mark outside a repository; the tree still helps.
		fmt.Fprintf(stderr, "sprout: %v; showing the tree without --git\n", err)
		t, err := BuildTree(path, opts)
		return t, "", err
	}
	if err != nil {
		return nil, "", err
	}
	if diffRev != "" {
		files, err := repo.diff(diffRev)
		if err != nil {
			return nil, "", err
		}
		return diffTree(displayName(path), files, opts.MaxDepth), diffRev, nil
	}

	t, err := BuildTree(path, opts)
	if err != nil {
		return nil, "", err
	}
	changes, err := repo.status()
	if err != nil {
		return nil, "", err
	}
	applyChanges(t, changes)
	return t, "on " + repo.branch(), nil
}

// printSummary mirrors tree's closing line, and says what was left out so
// automatic filtering is never silent.
func printSummary(w io.Writer, t *Tree) {
	dirs, files := t.Root.Count()
	fmt.Fprintf(w, "\n%s, %s", plural(dirs, "directory"), plural(files, "file"))
	if t.Skipped > 0 {
		fmt.Fprintf(w, " (%d hidden or ignored, --all to show)", t.Skipped)
	}
	fmt.Fprintln(w)
}

// parseArgs lets flags appear before or after the path. The flag package
// stops at the first positional argument, so `sprout src --depth 2` would
// otherwise silently ignore --depth.
func parseArgs(fs *flag.FlagSet, args []string) (string, error) {
	path := ""
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		if path != "" {
			fmt.Fprintf(fs.Output(), "sprout: unexpected argument %q\n", fs.Arg(0))
			return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
		path = fs.Arg(0)
		args = fs.Args()[1:]
	}
	if path == "" {
		path = "."
	}
	return path, nil
}

// resolveVersion prefers the release ldflag, falling back to the module
// version recorded by `go install ...@vX.Y.Z`.
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}
