package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// TestDumpGraph writes the dependency graph Sprout builds for a repository,
// for tools/accuracy to score against each language's own tooling:
//
//	SPROUT_GRAPH_DIR=/path/to/repo SPROUT_GRAPH_OUT=graph.json go test -run TestDumpGraph
//
// Besides the edges, it records the raw import specs the line scanner found,
// so a missed edge can be traced to scanning or to resolution.
func TestDumpGraph(t *testing.T) {
	dir, out := os.Getenv("SPROUT_GRAPH_DIR"), os.Getenv("SPROUT_GRAPH_OUT")
	if dir == "" || out == "" {
		t.Skip("set SPROUT_GRAPH_DIR and SPROUT_GRAPH_OUT")
	}
	// The options --entry and --ai use.
	tree, err := BuildTree(dir, Options{MaxDepth: -1, ShowHidden: true, Stat: true})
	if err != nil {
		t.Fatal(err)
	}
	g := buildGraph(dir, tree, true)

	fsPath := map[string]string{}
	walk(tree.Root, func(n *Node) { fsPath[n.Rel] = tree.FSPath(n) })

	type file struct {
		Rel   string   `json:"rel"`
		Lang  string   `json:"lang"`
		Test  bool     `json:"test"`
		Specs []string `json:"specs"`
		Deps  []string `json:"deps"`
	}
	files := make([]file, len(g.Files))
	for i, gf := range g.Files {
		f := file{Rel: gf.Rel, Lang: langOf(gf.Rel), Test: gf.Test, Specs: []string{}, Deps: []string{}}
		if f.Lang != "go" {
			// The same guards parseSource applies.
			if src, err := os.ReadFile(fsPath[gf.Rel]); err == nil && len(src) <= maxSourceSize && bytes.IndexByte(src, 0) < 0 {
				_, specs := scanDecls(f.Lang, src)
				f.Specs = append(f.Specs, specs...)
			}
		}
		for _, d := range g.Deps(FileID(i)) {
			f.Deps = append(f.Deps, g.Files[d].Rel)
		}
		files[i] = f
	}
	data, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
