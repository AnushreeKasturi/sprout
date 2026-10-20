package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Function-level impact inside one Go package. The graph links files, and
// in a package of many files nearly every file reaches every other, so
// every test reaches every change. Following declarations instead tells
// which functions, and so which tests, a change actually reaches.

// goDecl is a top-level declaration and the package-level names its code
// mentions. A method is keyed "T.name", and so is a call x.name() where x is
// a receiver or parameter of this package's type T; any other x.name() is
// ".name", which could be any type's method, as without type checking
// there's no telling.
type goDecl struct {
	file, name string
	refs       map[string]bool
	result     string // a function's first result, when it's a type of this package: T in (*T, error)
}

// packageDecls are the declarations of the Go files in dir, tests included.
func packageDecls(root, dir string, g *Graph) []goDecl {
	var decls []goDecl
	for _, f := range g.Files {
		if path.Dir(f.Rel) == dir && strings.HasSuffix(f.Rel, ".go") {
			decls = append(decls, fileDecls(root, f.Rel)...)
		}
	}
	return resolveMade(decls)
}

// resolveMade names the type behind x.f() for an x := newT(...): "@newT.f"
// becomes "T.f" when newT returns this package's T, else ".f".
func resolveMade(decls []goDecl) []goDecl {
	results := map[string]string{}
	for _, d := range decls {
		results[d.name] = d.result
	}
	for _, d := range decls {
		for k := range d.refs {
			if fn, method, ok := strings.Cut(k, "."); ok && strings.HasPrefix(fn, "@") {
				delete(d.refs, k)
				d.refs[results[fn[1:]]+"."+method] = true
			}
		}
	}
	return decls
}

// fileDecls are the declarations of one Go file.
func fileDecls(root, rel string) []goDecl {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
	if err != nil {
		return nil
	}
	top := map[any]bool{}
	for _, d := range file.Decls {
		for _, node := range declNodes(d) {
			top[node] = true
		}
	}
	var decls []goDecl
	for _, d := range file.Decls {
		for name, node := range declNodes(d) {
			refs := declRefs(node, top)
			delete(refs, name)
			var result string
			if fd, ok := node.(*ast.FuncDecl); ok && fd.Type.Results != nil && len(fd.Type.Results.List) > 0 {
				result = typeName(fd.Type.Results.List[0].Type)
			}
			decls = append(decls, goDecl{rel, name, refs, result})
		}
	}
	return decls
}

// declRefs are the package-level names node mentions; top holds the file's
// top-level declarations, to tell them from locals.
func declRefs(node ast.Node, top map[any]bool) map[string]bool {
	refs := map[string]bool{}
	typed := varTypes(node)
	var walk func(ast.Node) bool
	walk = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			key := "." + x.Sel.Name
			if id, ok := x.X.(*ast.Ident); ok && id.Obj != nil && typed[id.Obj] != "" {
				key = typed[id.Obj] + key
			}
			refs[key] = true
			ast.Inspect(x.X, walk)
			return false
		case *ast.FuncDecl: // its own name isn't a reference
			if x.Recv != nil {
				ast.Inspect(x.Recv, walk)
			}
			ast.Inspect(x.Type, walk)
			if x.Body != nil {
				ast.Inspect(x.Body, walk)
			}
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

// varTypes maps a function's receiver, parameters and the locals it makes
// as x := T{...}, x := &T{...} or var x T to the name of their type, where
// it's one of this package's: so that x.f() can name the f it calls. A
// local x := newT(...) maps to "@newT", for resolveMade to look up.
func varTypes(node ast.Node) map[*ast.Object]string {
	fd, ok := node.(*ast.FuncDecl)
	if !ok {
		return nil
	}
	typed := map[*ast.Object]string{}
	set := func(id *ast.Ident, t ast.Expr) {
		if id.Obj != nil && t != nil {
			typed[id.Obj] = typeName(t)
		}
	}
	for _, list := range []*ast.FieldList{fd.Recv, fd.Type.Params} {
		if list != nil {
			for _, f := range list.List {
				for _, n := range f.Names {
					set(n, f.Type)
				}
			}
		}
	}
	ast.Inspect(fd, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			assignTypes(x, typed)
		case *ast.ValueSpec:
			for _, id := range x.Names {
				set(id, x.Type)
			}
		}
		return true
	})
	return typed
}

// assignTypes adds the locals x := defines to typed: T for T{...} or
// &T{...}, "@f" for f(...).
func assignTypes(x *ast.AssignStmt, typed map[*ast.Object]string) {
	if x.Tok != token.DEFINE {
		return
	}
	for i, lhs := range x.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Obj == nil {
			continue
		}
		var rhs ast.Expr
		switch {
		case len(x.Rhs) == len(x.Lhs):
			rhs = x.Rhs[i]
		case i == 0: // x, err := newT(): x is newT's first result
			rhs = x.Rhs[0]
		default:
			continue
		}
		if call, ok := rhs.(*ast.CallExpr); ok {
			if fn, ok := call.Fun.(*ast.Ident); ok {
				typed[id.Obj] = "@" + fn.Name
			}
		} else if t := literalType(rhs); t != nil {
			typed[id.Obj] = typeName(t)
		}
	}
}

// literalType is the type of T{...} or &T{...}, else nil.
func literalType(e ast.Expr) ast.Expr {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	if c, ok := e.(*ast.CompositeLit); ok {
		return c.Type
	}
	return nil
}

// declNodes keys what d declares: a function, a method ("T.name"), or each
// type, var and const.
func declNodes(d ast.Decl) map[string]ast.Node {
	nodes := map[string]ast.Node{}
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil {
			nodes[recvType(d.Recv)+"."+d.Name.Name] = d
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

// reaching is every declaration that mentions a seed, directly or through
// other declarations, seeds included.
func reaching(decls []goDecl, seeds []string) map[string]bool {
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.name] = true
	}
	hit, methods := map[string]bool{}, map[string]bool{} // methods: names of hit methods, any type
	mark := func(k string) {
		hit[k] = true
		if i := strings.LastIndex(k, "."); i >= 0 {
			methods[k[i+1:]] = true
		}
	}
	for _, s := range seeds {
		mark(s)
	}
	for grew := true; grew; {
		grew = false
		for _, d := range decls {
			if hit[d.name] {
				continue
			}
			for r := range d.refs {
				if refHits(r, hit, methods, declared) {
					mark(d.name)
					grew = true
					break
				}
			}
		}
	}
	return hit
}

// refHits reports whether reference r is to something in hit. An untyped
// x.name(), or a call on a type that doesn't declare name itself (promoted
// from a field, or an interface's), counts for any hit method of that name.
// ponytail: those still match by name alone; type checking would pin them.
func refHits(r string, hit, methods, declared map[string]bool) bool {
	i := strings.LastIndex(r, ".")
	switch {
	case i < 0:
		return hit[r]
	case i > 0 && declared[r]:
		return hit[r]
	}
	return methods[r[i+1:]]
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
// the file graph), or TestMain does, which wraps every test. reach and of
// count them, to say why when there's no narrowing.
// ponytail: init is left out: one that mentions a function usually
// registers it rather than calls it, and counting it would narrow nothing.
func testsReaching(decls []goDecl, hit map[string]bool) (run []string, reach, of int) {
	if hit["TestMain"] {
		return nil, 0, 0
	}
	var all []string
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
		return nil, len(run), len(all)
	}
	sort.Strings(run)
	return run, len(run), len(all)
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
// changed packages, and in the packages importing them directly. reach says,
// for a package it doesn't narrow, how many of its tests reach the change.
func goTestRun(root string, g *Graph, changed []string, symbol string, affected []queryHit) (run map[string][]string, reach map[string][2]int) {
	dirs := map[string]bool{}
	for _, rel := range changed {
		if strings.HasSuffix(rel, ".go") {
			dirs[path.Dir(rel)] = true
		}
	}
	run, reach = map[string][]string{}, map[string][2]int{}
	narrow := func(dir string, decls []goDecl, hit map[string]bool) {
		tests, n, of := testsReaching(decls, hit)
		if tests != nil {
			run[goPkg(dir)] = tests
		} else if n > 0 {
			reach[goPkg(dir)] = [2]int{n, of}
		}
	}
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
		narrow(dir, decls, hit)
		for k := range exportedKeys(hit) {
			exported = append(exported, k)
		}
	}
	for _, dir := range directImporters(g, dirs, affected) {
		decls := packageDecls(root, dir, g)
		narrow(dir, decls, reaching(decls, exported))
	}
	if len(run) == 0 {
		run = nil
	}
	return run, reach
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

// filesReaching are the files with declarations in hit, the changed file
// rel included (its other functions may call the symbol), in order, with
// those declarations' names, and whether one uses a seed itself.
func filesReaching(decls []goDecl, rel string, seeds []string, hit map[string]bool) (files []string, names map[string][]string, direct map[string]bool) {
	names, direct = map[string][]string{}, map[string]bool{}
	declared := map[string]bool{}
	for _, d := range decls {
		declared[d.name] = true
	}
	seedHit, methods := map[string]bool{}, map[string]bool{}
	for _, k := range seeds {
		seedHit[k] = true
		if i := strings.LastIndex(k, "."); i >= 0 {
			methods[k[i+1:]] = true
		}
	}
	for _, d := range decls {
		if !hit[d.name] || d.file == rel && slices.Contains(seeds, d.name) {
			continue
		}
		if names[d.file] == nil {
			files = append(files, d.file)
		}
		names[d.file] = append(names[d.file], strings.TrimPrefix(d.name, "."))
		for ref := range d.refs {
			direct[d.file] = direct[d.file] || refHits(ref, seedHit, methods, declared)
		}
	}
	return files, names, direct
}

// mentionImpact is --symbol outside Go, where Sprout doesn't trace code
// function by function: the files depending on id that mention symbol as a
// word, and what depends on them.
// ponytail: a mention, not a resolved use: a comment or a local of the same
// name counts too, erring towards more tests.
func mentionImpact(root string, g *Graph, id FileID, symbol string, depth int) []queryHit {
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	seen := map[FileID]bool{id: true}
	var direct []FileID
	var hits []queryHit
	for _, c := range g.Dependents(id) {
		if src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(g.Files[c].Rel))); err == nil && word.Match(src) {
			seen[c] = true
			direct = append(direct, c)
			hits = append(hits, queryHit{Path: g.Files[c].Rel, Depth: 1, Test: g.Files[c].Test, id: c, from: id})
		}
	}
	if depth != 1 {
		for _, h := range follow(g, direct, true, -1, false) {
			if !seen[h.id] && (depth < 0 || h.Depth < depth) {
				h.Depth++
				h.Via = g.Files[h.from].Rel
				hits = append(hits, h)
			}
		}
	}
	sortHits(hits)
	return hits
}

// declaresWord reports whether one of the declarations Sprout reads in a
// non-Go file has name in its signature.
func declaresWord(root, rel, name string) bool {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	symbols, _ := scanDecls(langOf(rel), src, true)
	for _, s := range symbols {
		if word.MatchString(s) {
			return true
		}
	}
	return false
}

// exportedKeys are the names in hit another package can use, keyed as its
// references are: ".Name", for pkg.Name and x.Name() alike.
func exportedKeys(hit map[string]bool) map[string]bool {
	keys := map[string]bool{}
	for name := range hit {
		if n := name[strings.LastIndex(name, ".")+1:]; ast.IsExported(n) {
			keys["."+n] = true
		}
	}
	return keys
}

// importersUsing are the files of other packages that depend on dir's and
// mention one of keys, with the names they mention. A method call counts
// too, which the graph's reasons don't see: x.Name() on a type from dir.
func importersUsing(root string, g *Graph, dir string, keys map[string]bool) ([]FileID, map[FileID][]string) {
	seen := map[FileID]bool{}
	var files []FileID
	used := map[FileID][]string{}
	for i, f := range g.Files {
		if path.Dir(f.Rel) != dir {
			continue
		}
		for _, c := range g.Dependents(FileID(i)) {
			if path.Dir(g.Files[c].Rel) == dir || seen[c] {
				continue
			}
			seen[c] = true
			for _, d := range resolveMade(fileDecls(root, g.Files[c].Rel)) {
				for k := range d.refs {
					if n := strings.TrimPrefix(k, "."); keys[k] && !slices.Contains(used[c], n) {
						used[c] = append(used[c], n)
					}
				}
			}
			if used[c] != nil {
				files = append(files, c)
			}
		}
	}
	return files, used
}

// symbolKeys are the keys of what the Go file rel declares as name: a
// function, type or variable, or methods, as Name or Type.Name.
func symbolKeys(decls []goDecl, rel, name string) []string {
	var keys []string
	for _, d := range decls {
		short := d.name[strings.LastIndex(d.name, ".")+1:]
		if d.file == rel && (d.name == name || short == name) && !slices.Contains(keys, d.name) {
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
