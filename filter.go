package main

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// defaultIgnores are common development artifacts hidden outside git repos,
// where there's no .gitignore to tell us what matters.
var defaultIgnores = []string{
	"node_modules",
	"dist",
	"build",
	".cache",
	"__pycache__",
	".venv",
	"venv",
	".idea",
	".vscode",
	"target",
	"obj",
	"coverage",
	".next",
	".nuxt",
	".pytest_cache",
	".mypy_cache",
	".DS_Store",
	"*.egg-info",
}

// patternList is a repeatable, comma-separated flag: --ignore a,b --ignore c.
type patternList []string

func (p *patternList) String() string { return strings.Join(*p, ",") }

func (p *patternList) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*p = append(*p, s)
		}
	}
	return nil
}

// rule is one gitignore-style pattern. Patterns without a slash match a
// name at any depth; patterns with one match the path from the root.
type rule struct {
	re       *regexp.Regexp
	neg      bool
	dirOnly  bool
	anchored bool
}

// matcher applies rules in order; the last one that matches wins, so a
// later "!keep.log" re-includes what an earlier "*.log" excluded.
type matcher []rule

func compile(patterns []string) matcher {
	var m matcher
	for _, p := range patterns {
		p = strings.TrimRight(p, " \t\r")
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		var r rule
		if r.neg = strings.HasPrefix(p, "!"); r.neg {
			p = p[1:]
		}
		if r.dirOnly = strings.HasSuffix(p, "/"); r.dirOnly {
			p = strings.TrimRight(p, "/")
		}
		r.anchored = strings.Contains(p, "/")
		p = strings.TrimPrefix(p, "/")
		re, err := regexp.Compile("^" + globRegexp(p) + "$")
		if err != nil {
			continue // a malformed pattern shouldn't take the whole walk down
		}
		r.re = re
		m = append(m, r)
	}
	return m
}

// match reports whether the entry at rel (slash-separated, relative to
// the root) is selected by the rules.
func (m matcher) match(rel, name string, isDir bool) bool {
	matched := false
	for _, r := range m {
		if r.dirOnly && !isDir {
			continue
		}
		subject := name
		if r.anchored {
			subject = rel
		}
		if r.re.MatchString(subject) {
			matched = !r.neg
		}
	}
	return matched
}

// globRegexp translates gitignore glob syntax: * and ? stay within one
// path segment, ** crosses segments, [...] is a character class.
func globRegexp(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		switch c := g[i]; {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			j := strings.IndexByte(g[i+1:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+1+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += j + 1
		case c == '\\' && i+1 < len(g):
			i++
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// readIgnoreFile returns the patterns in root/.sproutignore, if any.
func readIgnoreFile(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".sproutignore"))
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// gitVisible asks git which paths under root belong to the project: tracked
// files plus untracked files that aren't gitignored, and every parent
// directory of those. ok is false outside a work tree or without git.
//
// ponytail: an untracked nested repo is listed as "dir/" and shows up empty;
// walk into it separately if that ever matters.
func gitVisible(root string) (map[string]bool, bool) {
	// Ignore discovery must not execute a repository-configured fsmonitor hook.
	out, err := exec.Command("git", "-c", "core.fsmonitor=false", "-C", root, "ls-files",
		"--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, false
	}
	set := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		for p = strings.TrimSuffix(p, "/"); p != "" && p != "." && !set[p]; p = path.Dir(p) {
			set[p] = true
		}
	}
	return set, true
}
