package main

import (
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
)

// gitRepo runs git scoped to the directory sprout was pointed at.
type gitRepo struct {
	dir    string
	prefix string // root's path inside the repo, e.g. "cmd/api/"; "" at the top
}

func openRepo(dir string) (*gitRepo, error) {
	r := &gitRepo{dir: dir}
	prefix, err := r.run("rev-parse", "--show-prefix")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository", dir)
	}
	r.prefix = strings.TrimSpace(prefix)
	return r, nil
}

func (r *gitRepo) run(args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", r.dir}, args...)...).Output()
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
	}
	return string(out), err
}

// rel maps a repo-relative path from git output to one relative to root.
func (r *gitRepo) rel(repoPath string) (string, bool) {
	return strings.CutPrefix(repoPath, r.prefix)
}

func (r *gitRepo) branch() string {
	b, err := r.run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(b)
}

// status returns a one-letter code per changed path under root:
// M modified, A added, D deleted, R renamed, ? untracked, U conflicted.
func (r *gitRepo) status() (map[string]string, error) {
	out, err := r.run("status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ".")
	if err != nil {
		return nil, err
	}
	changes := map[string]string{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		e := fields[i]
		if len(e) < 4 {
			continue
		}
		xy, p := e[:2], e[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // -z puts the rename source in the next field
		}
		code := string(xy[1])
		switch {
		case xy == "??":
			code = "?"
		case xy[0] == 'U' || xy[1] == 'U' || xy == "AA" || xy == "DD":
			code = "U"
		case xy[1] == ' ':
			code = string(xy[0])
		}
		if rel, ok := r.rel(p); ok {
			changes[rel] = code
		}
	}
	return changes, nil
}

// applyChanges marks each changed path on the tree, inserts paths that no
// longer exist on disk (deleted files) so they show in place, and counts
// changes on every ancestor directory.
func applyChanges(t *Tree, changes map[string]string) {
	counts := map[string]int{}
	for p, code := range changes {
		if n := insertPath(t, p); n != nil {
			n.Status = code
		}
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			counts[d]++
		}
	}
	walk(t.Root, func(n *Node) {
		if n.IsDir {
			n.Changes = counts[n.Rel]
		}
	})
}

// insertPath finds the node for rel, creating it (and any missing parent
// directories) if it isn't on disk. It returns nil when rel falls inside a
// directory the walk didn't expand (depth limit) or one it filtered out.
func insertPath(t *Tree, rel string) *Node {
	cur := t.Root
	parts := strings.Split(rel, "/")
	for i, name := range parts {
		if cur.Truncated || t.hides(strings.Join(parts[:i+1], "/"), name, i < len(parts)-1) {
			return nil
		}
		idx := sort.Search(len(cur.Children), func(j int) bool { return cur.Children[j].Name >= name })
		if idx < len(cur.Children) && cur.Children[idx].Name == name {
			cur = cur.Children[idx]
			continue
		}
		child := &Node{Name: name, Rel: strings.Join(parts[:i+1], "/"), IsDir: i < len(parts)-1, Missing: true}
		cur.Children = append(cur.Children, nil)
		copy(cur.Children[idx+1:], cur.Children[idx:])
		cur.Children[idx] = child
		cur = child
	}
	return cur
}

func walk(n *Node, fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}

// diff returns per-file change codes and line counts for a revision range,
// with paths relative to root. rev is passed to git as a single revision
// argument, e.g. "main...HEAD", "HEAD~3", or "v1.0..v1.1".
func (r *gitRepo) diff(rev string) (map[string]*Node, error) {
	// A leading "-" would let a crafted rev inject git options (e.g. --output).
	if rev == "" || strings.HasPrefix(rev, "-") {
		return nil, fmt.Errorf("invalid revision %q", rev)
	}
	base := []string{"diff", "--relative", "--find-renames", "-z"}
	names, err := r.run(append(base, "--name-status", "--end-of-options", rev, "--")...)
	if err != nil {
		return nil, err
	}
	nums, err := r.run(append(base, "--numstat", "--end-of-options", rev, "--")...)
	if err != nil {
		return nil, err
	}

	files := map[string]*Node{}
	f := strings.Split(names, "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		code := f[i][:1]
		if code == "R" || code == "C" {
			i++ // "R100\0old\0new": keep the new path
		}
		files[f[i+1]] = &Node{Status: code}
	}

	f = strings.Split(nums, "\x00")
	for i := 0; i < len(f); i++ {
		cols := strings.SplitN(f[i], "\t", 3)
		if len(cols) != 3 {
			continue
		}
		p := cols[2]
		if p == "" { // rename: "a\td\t\0old\0new"
			if i+2 >= len(f) {
				break
			}
			p, i = f[i+2], i+2
		}
		if n := files[p]; n != nil {
			fmt.Sscan(cols[0], &n.Added) // "-" for binary files leaves 0
			fmt.Sscan(cols[1], &n.Deleted)
		}
	}
	return files, nil
}

// staged returns the status code of each staged path under root.
func (r *gitRepo) staged() (map[string]string, error) {
	out, err := r.run("diff", "--cached", "--relative", "--find-renames", "--name-status", "-z", "--")
	if err != nil {
		return nil, err
	}
	return nameStatus(out), nil
}

// changedIn returns the status code of each path changed in a revision or
// range, e.g. main...HEAD, or HEAD^! for one commit.
func (r *gitRepo) changedIn(rev string) (map[string]string, error) {
	files, err := r.diff(rev)
	if err != nil {
		return nil, err
	}
	codes := make(map[string]string, len(files))
	for p, n := range files {
		codes[p] = n.Status
	}
	return codes, nil
}

// nameStatus parses `git diff --name-status -z`, keeping a rename's new path.
func nameStatus(out string) map[string]string {
	codes := map[string]string{}
	f := strings.Split(out, "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		code := f[i][:1]
		if code == "R" || code == "C" {
			i++
		}
		codes[f[i+1]] = code
	}
	return codes
}

// diffTree builds a tree of only the changed paths, so a PR's structural
// footprint reads at a glance. Directories sum their children's line counts;
// below maxDepth (if >= 0) subtrees collapse into those totals.
func diffTree(name string, files map[string]*Node, maxDepth int) *Tree {
	t := &Tree{Root: &Node{Name: name, IsDir: true}, hides: func(string, string, bool) bool { return false }}
	for p, f := range files {
		n := insertPath(t, p)
		n.Status, n.Added, n.Deleted = f.Status, f.Added, f.Deleted
	}
	rollup(t.Root, 0, maxDepth)
	return t
}

func rollup(n *Node, depth, maxDepth int) {
	for _, c := range n.Children {
		if c.IsDir {
			rollup(c, depth+1, maxDepth)
			n.Changes += c.Changes
		} else {
			n.Changes++
		}
		n.Added += c.Added
		n.Deleted += c.Deleted
	}
	if maxDepth >= 0 && depth >= maxDepth && n.Children != nil {
		n.Children, n.Truncated = nil, true
	}
}
