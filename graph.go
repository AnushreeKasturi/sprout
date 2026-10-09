package main

import "sort"

// The dependency graph is the shared, language-agnostic model that --entry
// and --ai rank from, and that dependency, impact and context queries will
// traverse. It knows nothing about languages: the analyzers in codegraph.go
// produce its files and edges.
//
// Files are identified by a small integer FileID (their index in Files).
// Edges are stored in both directions as compressed sparse rows: the files
// a file depends on are one contiguous slice, and so are the files that
// depend on it. Both are built once, so a query is a slice lookup rather
// than a scan.

// FileID identifies a file in a Graph: its index in Graph.Files.
type FileID int32

// GraphFile is one source file.
type GraphFile struct {
	Rel     string   // slash-separated path relative to the root
	Test    bool     // a test: its edges are kept, but it never ranks or counts as a user
	Symbols []string // top-level declarations with signatures, public API first
}

type Graph struct {
	Files []GraphFile

	byRel  map[string]FileID
	fwdOff []int32 // Deps(id) is fwd[fwdOff[id]:fwdOff[id+1]]
	fwd    []FileID
	revOff []int32 // Dependents(id) is rev[revOff[id]:revOff[id+1]]
	rev    []FileID
	usedBy []int32 // dependents that aren't tests

	why func(from, to FileID) (reason string, names []string) // set by the analyzers; see Why
}

// Why says what makes from depend on to: the import, or the names it uses.
// It re-reads the two files, so it's for answering queries about a few
// edges, not for ranking the whole graph.
func (g *Graph) Why(from, to FileID) string {
	if g.why == nil {
		return ""
	}
	reason, _ := g.why(from, to)
	return reason
}

// UsedNames are the names from declares that from uses, where the analyzer
// knows them exactly (Go); nil otherwise.
func (g *Graph) UsedNames(from, to FileID) []string {
	if g.why == nil {
		return nil
	}
	_, names := g.why(from, to)
	return names
}

// ID looks a file up by its relative path.
func (g *Graph) ID(rel string) (FileID, bool) {
	id, ok := g.byRel[rel]
	return id, ok
}

// Deps are the files id depends on, in ID order. The slice is shared: don't modify it.
func (g *Graph) Deps(id FileID) []FileID { return g.fwd[g.fwdOff[id]:g.fwdOff[id+1]] }

// Dependents are the files that depend on id, in ID order. The slice is shared: don't modify it.
func (g *Graph) Dependents(id FileID) []FileID { return g.rev[g.revOff[id]:g.revOff[id+1]] }

// UsedBy counts id's dependents that aren't tests.
func (g *Graph) UsedBy(id FileID) int { return int(g.usedBy[id]) }

// Ranked returns the non-test files that other code uses, most used first.
func (g *Graph) Ranked() []FileID {
	var out []FileID
	for id, f := range g.Files {
		if !f.Test && g.usedBy[id] > 0 {
			out = append(out, FileID(id))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if g.usedBy[a] != g.usedBy[b] {
			return g.usedBy[a] > g.usedBy[b]
		}
		return g.Files[a].Rel < g.Files[b].Rel
	})
	return out
}

// graphBuilder collects files and edges, then freezes them into a Graph.
type graphBuilder struct {
	files []GraphFile
	byRel map[string]FileID
	edges []graphEdge
}

type graphEdge struct{ from, to FileID }

func newGraphBuilder(capacity int) *graphBuilder {
	return &graphBuilder{files: make([]GraphFile, 0, capacity), byRel: make(map[string]FileID, capacity)}
}

func (b *graphBuilder) add(f GraphFile) FileID {
	id := FileID(len(b.files))
	b.files = append(b.files, f)
	b.byRel[f.Rel] = id
	return id
}

// link records that from depends on to. Self-edges and duplicates are
// dropped when the graph is built.
func (b *graphBuilder) link(from, to FileID) {
	if from != to {
		b.edges = append(b.edges, graphEdge{from, to})
	}
}

func (b *graphBuilder) build() *Graph {
	e := b.edges
	sort.Slice(e, func(i, j int) bool {
		if e[i].from != e[j].from {
			return e[i].from < e[j].from
		}
		return e[i].to < e[j].to
	})
	uniq := e[:0]
	for i, x := range e {
		if i == 0 || x != e[i-1] {
			uniq = append(uniq, x)
		}
	}

	n := len(b.files)
	g := &Graph{
		Files:  b.files,
		byRel:  b.byRel,
		fwdOff: make([]int32, n+1),
		fwd:    make([]FileID, len(uniq)),
		revOff: make([]int32, n+1),
		rev:    make([]FileID, len(uniq)),
		usedBy: make([]int32, n),
	}
	for _, x := range uniq {
		g.fwdOff[x.from+1]++
		g.revOff[x.to+1]++
		if !b.files[x.from].Test {
			g.usedBy[x.to]++
		}
	}
	for i := 0; i < n; i++ {
		g.fwdOff[i+1] += g.fwdOff[i]
		g.revOff[i+1] += g.revOff[i]
	}
	// Edges are sorted by (from, to), so forward rows fill in order and
	// every reverse row comes out sorted by from.
	next := make([]int32, n)
	copy(next, g.revOff[:n])
	for i, x := range uniq {
		g.fwd[i] = x.to
		g.rev[next[x.to]] = x.from
		next[x.to]++
	}
	b.edges = nil
	return g
}
