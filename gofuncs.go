package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Function-level impact inside one Go package. The graph links files, and
// in a package of many files nearly every file reaches every other, so
// every test reaches every change. Following declarations instead tells
// which functions, and so which tests, a change actually reaches.

// goDecl is a top-level declaration and the package-level names its code
// mentions. A method's name, and a call like x.name(), are keyed ".name":
// a method can't be told apart by its receiver without type checking, but
// it can be told apart from a function or a variable.
type goDecl struct {
	file, name string
	refs       map[string]bool
}

// packageDecls are the declarations of the Go files in dir, tests included.
func packageDecls(root, dir string, g *Graph) []goDecl {
	var decls []goDecl
	for _, f := range g.Files {
		if path.Dir(f.Rel) != dir || !strings.HasSuffix(f.Rel, ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Rel)))
		if err != nil {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f.Rel, src, 0)
		if err != nil {
			continue
		}
		top := map[any]bool{}
		for _, d := range file.Decls {
			for _, node := range declNodes(d) {
				top[node] = true
			}
		}
		for _, d := range file.Decls {
			for name, node := range declNodes(d) {
				refs := declRefs(node, top)
				delete(refs, name)
				decls = append(decls, goDecl{f.Rel, name, refs})
			}
		}
	}
	return decls
}

// declRefs are the package-level names node mentions; top holds the file's
// top-level declarations, to tell them from locals.
func declRefs(node ast.Node, top map[any]bool) map[string]bool {
	refs := map[string]bool{}
	var walk func(ast.Node) bool
	walk = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			refs["."+x.Sel.Name] = true
			ast.Inspect(x.X, walk)
			return false
		case *ast.Field: // parameter and field names aren't references
			ast.Inspect(x.Type, walk)
			return false
		case *ast.KeyValueExpr:
			if _, ok := x.Key.(*ast.Ident); ok { // a struct literal's field name
				ast.Inspect(x.Value, walk)
				return false
			}
		case *ast.Ident:
			// Unresolved is another file's declaration; resolved to
			// something other than a top-level declaration is a local.
			if x.Obj == nil || top[x.Obj.Decl] {
				refs[x.Name] = true
			}
		}
		return true
	}
	ast.Inspect(node, walk)
	return refs
}

// declNodes keys what d declares: a function, a method (".name"), or each
// type, var and const.
func declNodes(d ast.Decl) map[string]ast.Node {
	nodes := map[string]ast.Node{}
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil {
			nodes["."+d.Name.Name] = d
		} else {
			nodes[d.Name.Name] = d
		}
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				nodes[s.Name.Name] = s
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.Name != "_" {
						nodes[n.Name] = s
					}
				}
			}
		}
	}
	return nodes
}

// reaching is every name whose declaration mentions a seed, directly or
// through other declarations, seeds included.
// ponytail: matches by name, so a local variable or a method on another
// type with a seed's name counts too; that errs towards running more tests.
func reaching(decls []goDecl, seeds []string) map[string]bool {
	hit := map[string]bool{}
	for _, s := range seeds {
		hit[s] = true
	}
	for grew := true; grew; {
		grew = false
		for _, d := range decls {
			if hit[d.name] {
				continue
			}
			for r := range d.refs {
				if hit[r] {
					hit[d.name], grew = true, true
					break
				}
			}
		}
	}
	return hit
}

// isGoTest is a function go test -run selects in a test file.
func isGoTest(d goDecl) bool {
	if !strings.HasSuffix(d.file, "_test.go") {
		return false
	}
	for _, p := range []string{"Test", "Example", "Fuzz"} {
		if strings.HasPrefix(d.name, p) && d.name != "TestMain" {
			return true
		}
	}
	return false
}

// testsReaching are the tests in decls that reach hit, or nil when there's
// little narrowing to do: more than half of them do, none do (then trust
// the file graph), or TestMain does, which wraps every test.
// ponytail: init is left out: one that mentions a function usually
// registers it rather than calls it, and counting it would narrow nothing.
func testsReaching(decls []goDecl, hit map[string]bool) []string {
	if hit["TestMain"] {
		return nil
	}
	var all, run []string
	for _, d := range decls {
		if isGoTest(d) {
			all = append(all, d.name)
			if hit[d.name] {
				run = append(run, d.name)
			}
		}
	}
	// ponytail: a -run listing most of the tests saves little and reads
	// worse than the plain package; narrow only when it halves them.
	if len(run) == 0 || len(run) > len(all)/2 {
		return nil
	}
	sort.Strings(run)
	return run
}

// goPkg is how go test names the package in dir.
func goPkg(dir string) string {
	if dir == "." {
		return "."
	}
	return "./" + dir
}

// goTestRun narrows go test, package by package, to the tests that reach
// the changed Go files' declarations, or symbol alone when given: in the
// changed packages, and in the packages importing them directly.
func goTestRun(root string, g *Graph, changed []string, symbol string, affected []queryHit) map[string][]string {
	dirs := map[string]bool{}
	for _, rel := range changed {
		if strings.HasSuffix(rel, ".go") {
			dirs[path.Dir(rel)] = true
		}
	}
	run := map[string][]string{}
	var exported []string // what importers can use, as exportedKeys
	for dir := range dirs {
		decls := packageDecls(root, dir, g)
		var names []string
		for _, d := range decls {
			if symbol == "" && slices.Contains(changed, d.file) {
				names = append(names, d.name)
			}
		}
		if symbol != "" {
			names = symbolKeys(decls, changed[0], symbol)
		}
		hit := reaching(decls, names)
		if tests := testsReaching(decls, hit); tests != nil {
			run[goPkg(dir)] = tests
		}
		for k := range exportedKeys(hit) {
			exported = append(exported, k)
		}
	}
	for _, dir := range directImporters(g, dirs, affected) {
		decls := packageDecls(root, dir, g)
		if tests := testsReaching(decls, reaching(decls, exported)); tests != nil {
			run[goPkg(dir)] = tests
		}
	}
	if len(run) == 0 {
		return nil
	}
	return run
}

// directImporters are the affected Go packages that reach the changed
// packages only directly. One that also reaches them through another
// affected package can have tests affected without naming anything changed,
// so it's tested whole.
func directImporters(g *Graph, changed map[string]bool, affected []queryHit) []string {
	isAffected := map[string]bool{}
	for _, h := range affected {
		isAffected[h.Path] = true
	}
	safe := map[string]bool{}
	for _, h := range affected {
		dir := path.Dir(h.Path)
		if changed[dir] || !strings.HasSuffix(h.Path, ".go") {
			continue
		}
		if _, seen := safe[dir]; !seen {
			safe[dir] = true
		}
		for _, d := range g.Deps(h.id) {
			dd := path.Dir(g.Files[d].Rel)
			if dd != dir && !changed[dd] && isAffected[g.Files[d].Rel] {
				safe[dir] = false
			}
		}
	}
	var out []string
	for dir, ok := range safe {
		if ok {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out
}

// symbolImpact is what changing one declaration reaches: the files of its
// package with declarations that reach it, the files of other packages that
// use one of those, and what depends on them in turn.
func symbolImpact(root string, g *Graph, id FileID, symbol string, depth int) []queryHit {
	rel, dir := g.Files[id].Rel, path.Dir(g.Files[id].Rel)
	decls := packageDecls(root, dir, g)
	seeds := symbolKeys(decls, rel, symbol)
	hit := reaching(decls, seeds)
	files, names, direct := filesReaching(decls, rel, seeds, hit)
	seen := map[FileID]bool{id: true}
	var hits []queryHit
	add := func(h queryHit) {
		seen[h.id] = true
		h.Test = g.Files[h.id].Test
		if h.Depth > 1 && h.from != id {
			h.Via = g.Files[h.from].Rel
		}
		hits = append(hits, h)
	}
	for _, f := range files {
		fid, _ := g.ID(f)
		add(queryHit{Path: f, Depth: depthIf(direct[f]), Reason: "in " + someOf(names[f]), id: fid, from: id})
	}
	outside, used := importersUsing(root, g, dir, exportedKeys(hit))
	for _, c := range outside {
		depth := depthIf(slices.Contains(used[c], symbol))
		add(queryHit{Path: g.Files[c].Rel, Depth: depth, Reason: "uses " + someOf(used[c]), id: c, from: id})
	}
	for _, h := range follow(g, outside, true, -1, false) {
		if !seen[h.id] && path.Dir(h.Path) != dir {
			h.Depth++
			h.Via = ""
			add(h)
		}
	}
	kept := hits[:0]
	for _, h := range hits {
		if depth < 0 || h.Depth <= depth {
			kept = append(kept, h)
		}
	}
	sortHits(kept)
	return kept
}

// filesReaching are the files other than rel with declarations in hit, in
// order, with those declarations' names, and whether one uses a seed itself.
func filesReaching(decls []goDecl, rel string, seeds []string, hit map[string]bool) (files []string, names map[string][]string, direct map[string]bool) {
	names, direct = map[string][]string{}, map[string]bool{}
	for _, d := range decls {
		if d.file != rel && hit[d.name] {
			if names[d.file] == nil {
				files = append(files, d.file)
			}
			names[d.file] = append(names[d.file], strings.TrimPrefix(d.name, "."))
			direct[d.file] = direct[d.file] || usesAny(seeds, d.refs)
		}
	}
	return files, names, direct
}

// exportedKeys are the names in hit another package can use, keyed as its
// references are: ".Name", for pkg.Name and x.Name() alike.
func exportedKeys(hit map[string]bool) map[string]bool {
	keys := map[string]bool{}
	for name := range hit {
		if n := strings.TrimPrefix(name, "."); ast.IsExported(n) {
			keys["."+n] = true
		}
	}
	return keys
}

// importersUsing are the files of other packages that depend on dir's and
// mention one of keys, with the names they mention. A method call counts
// too, which the graph's reasons don't see: x.Name() on a type from dir.
func importersUsing(root string, g *Graph, dir string, keys map[string]bool) ([]FileID, map[FileID][]string) {
	byDir := map[string][]FileID{}
	seen := map[FileID]bool{}
	for i, f := range g.Files {
		if path.Dir(f.Rel) != dir {
			continue
		}
		for _, c := range g.Dependents(FileID(i)) {
			if cd := path.Dir(g.Files[c].Rel); cd != dir && !seen[c] {
				seen[c] = true
				byDir[cd] = append(byDir[cd], c)
			}
		}
	}
	var files []FileID
	used := map[FileID][]string{}
	for cd, ids := range byDir {
		for _, d := range packageDecls(root, cd, g) {
			id, ok := g.ID(d.file)
			if !ok || !seen[id] {
				continue
			}
			for k := range d.refs {
				if n := strings.TrimPrefix(k, "."); keys[k] && !slices.Contains(used[id], n) {
					used[id] = append(used[id], n)
				}
			}
		}
		for _, id := range ids {
			if used[id] != nil {
				files = append(files, id)
			}
		}
	}
	return files, used
}

// symbolKeys are the keys of what the Go file rel declares as name: a
// function, type or variable, or methods.
func symbolKeys(decls []goDecl, rel, name string) []string {
	var keys []string
	for _, d := range decls {
		if d.file == rel && strings.TrimPrefix(d.name, ".") == name && !slices.Contains(keys, d.name) {
			keys = append(keys, d.name)
		}
	}
	return keys
}

func depthIf(direct bool) int {
	if direct {
		return 1
	}
	return 2
}

// usesAny reports whether any of names is in hit.
func usesAny(names []string, hit map[string]bool) bool {
	for _, n := range names {
		if hit[n] {
			return true
		}
	}
	return false
}
