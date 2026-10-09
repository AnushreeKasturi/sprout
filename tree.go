package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Options controls how the tree is built.
type Options struct {
	ShowHidden bool
	MaxDepth   int       // -1 means unlimited
	Ignore     []string  // user patterns (gitignore syntax), always applied
	Only       []string  // if set, only files matching these patterns are shown
	NoIgnore   bool      // disable .gitignore, .sproutignore and the built-in list
	Sizes      bool      // measure past MaxDepth: true directory sizes and newest times
	Since      time.Time // if set, only files modified at or after this time
	Stat       bool      // read sizes and times; a plain tree doesn't need them
}

// Node is one file or directory in the tree.
type Node struct {
	Name     string
	Rel      string // slash-separated path relative to the root; Tree.FSPath gives the filesystem path
	IsDir    bool
	Missing  bool      // not on disk: inserted for a git deletion, or part of a --diff tree
	Special  bool      // a symlink, pipe, socket or device rather than a regular file or directory
	Size     int64     // files: bytes; directories: total below, with Options.Sizes
	ModTime  time.Time // directories: newest file below
	Children []*Node
	Err      error // set when a directory couldn't be read; the walk continues

	Truncated bool   // directory not expanded because of --depth
	Status    string // --git / --diff change code: M, A, D, R, ?, U
	Changes   int    // directories: changed paths anywhere below
	Added     int    // --diff: lines added (directories: total below)
	Deleted   int    // --diff: lines deleted (directories: total below)
	Churn     int    // --churn: commits that touched this path
	More      int    // --max-files: files not listed in this directory
}

// Tree is a walked directory plus what the walk left out.
type Tree struct {
	Root     *Node
	Skipped  int  // entries hidden by dotfile or ignore rules
	GitAware bool // .gitignore rules were applied

	hides func(rel, name string, isDir bool) bool // the walk's filter, reused for inserted paths

	Dir string // filesystem path the tree was walked from; "" for --diff trees
}

// FSPath is where n lives on disk, or "" for nodes that aren't on disk.
// Nodes store only their relative path: rebuilding the full one on demand
// saves an allocation and ~90 bytes per node on large repositories.
func (t *Tree) FSPath(n *Node) string {
	if t.Dir == "" || n.Missing {
		return ""
	}
	if n.Rel == "" {
		return t.Dir
	}
	return filepath.Join(t.Dir, filepath.FromSlash(n.Rel))
}

type walker struct {
	opts    Options
	ignore  matcher
	only    matcher         // nil: every file
	visible map[string]bool // nil unless GitAware
	skipped int
}

// BuildTree walks root according to opts and returns the resulting tree.
// It's the single traversal every output mode builds on.
//
// Ignore rules: inside a git work tree, .gitignore is the source of truth
// (asked of git itself, so every gitignore feature is honoured). Outside git,
// a built-in list of common build/dependency directories is used instead.
// A .sproutignore in root and --ignore patterns apply on top of either.
func BuildTree(root string, opts Options) (*Tree, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}

	patterns := []string{".git/"}
	t := &Tree{}
	w := &walker{opts: opts}
	if !opts.NoIgnore {
		if w.visible, t.GitAware = gitVisible(root); !t.GitAware {
			patterns = append(patterns, defaultIgnores...)
		}
		patterns = append(patterns, readIgnoreFile(root)...)
	}
	w.ignore = compile(append(patterns, opts.Ignore...))
	if len(opts.Only) > 0 {
		w.only = compile(opts.Only)
	}

	t.Dir = root
	node := &Node{Name: displayName(root), IsDir: info.IsDir()}
	if node.IsDir {
		if err := w.populate(node, root, 0); err != nil {
			return nil, err
		}
	} else {
		node.Size = info.Size()
	}

	if w.only != nil || !opts.Since.IsZero() {
		pruneEmpty(node)
	}
	t.Root, t.Skipped, t.hides = node, w.skipped, w.hides
	return t, nil
}

func (w *walker) hides(rel, name string, isDir bool) bool {
	return (!w.opts.ShowHidden && name[0] == '.') ||
		w.ignore.match(rel, name, isDir) ||
		(!isDir && w.only != nil && !w.only.match(rel, name, false))
}

// pruneEmpty drops directories left with nothing in them after --only or
// --changed-within.
// Directories cut off by --depth or unreadable ones stay: we don't know
// what's inside.
func pruneEmpty(n *Node) bool {
	kept := n.Children[:0]
	for _, c := range n.Children {
		if !c.IsDir || pruneEmpty(c) || c.Truncated || c.Err != nil {
			kept = append(kept, c)
		}
	}
	n.Children = kept
	return len(kept) > 0
}

// populate fills node, the directory at dir on disk.
func (w *walker) populate(node *Node, dir string, depth int) error {
	// Past --depth nothing is shown, but with --size the walk continues so
	// directory totals are true totals, not just what's visible.
	beyond := w.opts.MaxDepth >= 0 && depth >= w.opts.MaxDepth
	if beyond && !w.opts.Sizes {
		node.Truncated = true
		return nil
	}

	// os.ReadDir already returns entries sorted by name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		if depth == 0 {
			return err
		}
		// One unreadable subdirectory (e.g. permission denied) shouldn't
		// abort the whole walk; record it and keep going.
		node.Err = err
		return nil
	}

	// One allocation for every child node in this directory instead of one
	// per node, and Children sized up front instead of grown by append.
	// Children point into slab, which is safe only because it never grows
	// past its capacity: at most one node per entry.
	slab := make([]Node, 0, len(entries))
	node.Children = make([]*Node, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		rel := name
		if node.Rel != "" {
			rel = node.Rel + "/" + name
			// Share rel's bytes rather than keeping the directory entry's
			// own copy of the name alive.
			name = rel[len(node.Rel)+1:]
		}

		if w.hides(rel, name, entry.IsDir()) || (w.visible != nil && !w.visible[rel]) {
			if !beyond {
				w.skipped++
			}
			continue
		}

		// Stat costs a syscall per entry; skip it when nothing will use it.
		var info os.FileInfo
		if w.opts.Stat || w.opts.Sizes || !w.opts.Since.IsZero() {
			if info, err = entry.Info(); err != nil {
				continue // vanished between ReadDir and Lstat
			}
		}
		if !w.opts.Since.IsZero() && !entry.IsDir() && info.ModTime().Before(w.opts.Since) {
			if !beyond {
				w.skipped++
			}
			continue
		}

		special := !entry.IsDir() && !entry.Type().IsRegular() // from ReadDir: no extra syscall
		slab = append(slab, Node{Name: name, Rel: rel, IsDir: entry.IsDir(), Special: special})
		child := &slab[len(slab)-1]

		if child.IsDir {
			if err := w.populate(child, filepath.Join(dir, name), depth+1); err != nil {
				return err
			}
		} else if info != nil {
			child.Size, child.ModTime = info.Size(), info.ModTime()
		}

		if w.opts.Sizes {
			node.Size += child.Size
		}
		if child.ModTime.After(node.ModTime) {
			node.ModTime = child.ModTime
		}
		node.Children = append(node.Children, child)
	}

	if beyond {
		node.Children, node.Truncated = nil, true
	}
	return nil
}

// Count returns the number of directories and files below node, including
// files elided by --max-files.
func (n *Node) Count() (dirs, files int) {
	files = n.More
	for _, c := range n.Children {
		if c.IsDir {
			d, f := c.Count()
			dirs, files = dirs+d+1, files+f
		} else {
			files++
		}
	}
	return dirs, files
}

// printer renders a tree with the familiar ├──/└── connectors.
type printer struct {
	w     io.Writer
	color bool
	sizes bool // show sizes; si picks powers of 1000
	si    bool
	links bool   // OSC 8 hyperlinks on names
	host  string // for file:// links
	fs    *Tree  // for links: where nodes live on disk

	churnFiles, churnDirs int // --churn scale; 0 when off
}

func (p printer) tree(node *Node, prefix string) {
	last := len(node.Children) - 1
	if node.More > 0 {
		last++ // the "… N more files" line closes the directory
	}
	for i, child := range node.Children {
		connector := "├── "
		nextPrefix := prefix + "│   "
		if i == last {
			connector = "└── "
			nextPrefix = prefix + "    "
		}
		fmt.Fprintln(p.w, paint(p.color, dim, prefix+connector)+p.label(child))
		if child.IsDir {
			p.tree(child, nextPrefix)
		}
	}
	if node.More > 0 {
		fmt.Fprintln(p.w, paint(p.color, dim, prefix+"└── … "+plural(node.More, "more file")))
	}
}

func (p printer) label(n *Node) string {
	name := n.Name
	switch {
	case n.IsDir:
		name = paint(p.color, blue+";"+bold, name+"/")
	case n.Status == "D":
		name = paint(p.color, red, name)
	}
	if p.links && p.fs != nil && n.Status != "D" {
		if path := p.fs.FSPath(n); path != "" {
			name = hyperlink(name, path, p.host)
		}
	}
	if p.sizes && (n.Size > 0 || !n.IsDir) {
		name += "  " + paint(p.color, cyan, humanSize(n.Size, p.si))
	}
	if n.Status != "" {
		name += "  " + paint(p.color, statusColor[n.Status], n.Status)
	}
	if n.Changes > 0 {
		name += "  " + paint(p.color, dim, fmt.Sprintf("(%d changed)", n.Changes))
	}
	if n.Added+n.Deleted > 0 {
		name += "  " + paint(p.color, green, fmt.Sprintf("+%d", n.Added)) + " " + paint(p.color, red, fmt.Sprintf("-%d", n.Deleted))
	}
	if n.Churn > 0 {
		scale := p.churnFiles
		if n.IsDir {
			scale = p.churnDirs
		}
		c := dim
		if n.Churn*3 > scale*2 {
			c = red
		} else if n.Churn*3 > scale {
			c = yellow
		}
		name += "  " + paint(p.color, c, churnBar(n.Churn, scale))
	}
	if n.Err != nil {
		name += "  " + paint(p.color, red, "["+errReason(n.Err)+"]")
	}
	return name
}

// errReason strips the path from *PathError so the tree shows just
// "permission denied" next to the entry that already names the path.
func errReason(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}
