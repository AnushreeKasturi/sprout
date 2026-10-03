package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	goprinter "go/printer"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
)

// The analyzers here read source files and turn them into the Graph in
// graph.go: which files exist, their top-level declarations, and which files
// depend on which. Go is parsed with the standard library; other languages
// use line-oriented patterns, which cover the common declaration and import
// forms without a parser dependency.
//
// JS and TS imports skip comments and span lines, and resolve through
// tsconfig paths and workspace packages (jsresolve.go).

const (
	maxGraphFiles = 50000
	maxSourceSize = 512 << 10
)

func isTestFile(rel string) bool {
	base := path.Base(rel)
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_")
}

// Test code lives in test files or test directories. Fixtures, examples and
// vendored code aren't the project's own code, so they stay out of the graph.
var (
	testDirs  = map[string]bool{"test": true, "tests": true, "__tests__": true}
	graphSkip = map[string]bool{"testdata": true, "examples": true, "example": true, "fixtures": true, "vendor": true, "third_party": true}
)

// inDir reports whether any directory in rel's path is in dirs. It runs for
// every file in the tree, so it doesn't allocate.
func inDir(rel string, dirs map[string]bool) bool {
	dir := path.Dir(rel)
	for dir != "." && dir != "" {
		part := dir
		if i := strings.IndexByte(dir, '/'); i >= 0 {
			part, dir = dir[:i], dir[i+1:]
		} else {
			dir = ""
		}
		if dirs[part] {
			return true
		}
	}
	return false
}

func isTestRel(rel string) bool { return isTestFile(rel) || inDir(rel, testDirs) }

// fileFacts is what one analyzer pass learns about one file.
type fileFacts struct {
	symbols []string
	targets []string // resolved dependencies (non-Go)
	goFacts *goFacts // Go files; resolved once every file is parsed
}

// buildGraph analyzes the source files in t. With tests, test files join
// the graph too, so each file's tests can be found; --entry and --ai don't
// need them and skip reading them, which on Go-heavy repos is a third of
// the parsing. With symbols, each file keeps its declarations' signatures,
// which only --ai prints.
func buildGraph(root string, t *Tree, tests, symbols bool) *Graph {
	var nodes []*Node
	var goMods []*Node
	var jsConfigs, jsManifests, pyRoots, cargos []string
	walk(t.Root, func(n *Node) {
		if n.IsDir || n.Missing {
			return
		}
		switch n.Name {
		case "tsconfig.json", "jsconfig.json":
			jsConfigs = append(jsConfigs, n.Rel)
		case "package.json":
			jsManifests = append(jsManifests, n.Rel)
		case "Cargo.toml":
			cargos = append(cargos, n.Rel)
		case "pyproject.toml", "setup.py", "setup.cfg":
			pyRoots = append(pyRoots, path.Dir(n.Rel), path.Join(path.Dir(n.Rel), "src"))
		}
		isMod, lang := n.Name == "go.mod", langOf(n.Name) // cheap checks first: this visits every file
		if (!isMod && lang == "") || inDir(n.Rel, graphSkip) {
			return
		}
		if isMod {
			goMods = append(goMods, n)
		} else if len(nodes) < maxGraphFiles && (tests || !isTestRel(n.Rel)) {
			nodes = append(nodes, n)
		}
	})
	// Parsing allocates heavily and briefly; trade some memory for less GC.
	defer debug.SetGCPercent(debug.SetGCPercent(400))

	b := newGraphBuilder(len(nodes))
	exists := map[string]bool{}
	for _, n := range nodes {
		b.add(GraphFile{Rel: n.Rel, Test: isTestRel(n.Rel)})
		exists[n.Rel] = true
	}
	r := resolver{exists: exists, goModules: goModules(t, goMods), javaIndex: javaIndex(nodes), pyRoots: pyRoots}
	r.rs = loadRSProject(cargos, func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }, exists)
	if len(jsConfigs)+len(jsManifests) > 0 {
		r.js = loadJSProject(jsConfigs, jsManifests, func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) })
	}

	// Parse in parallel: reading and parsing dominate on large repos.
	facts := make([]fileFacts, len(nodes))
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				facts[i] = parseSource(nodes[i], t.FSPath(nodes[i]), symbols && !b.files[i].Test, r)
			}
		}()
	}
	for i := range nodes {
		next <- i
	}
	close(next)
	wg.Wait()

	for i, f := range facts {
		b.files[i].Symbols = f.symbols
		for _, target := range f.targets {
			b.link(FileID(i), b.byRel[target])
		}
	}
	linkGo(b, facts, r)
	g := b.build()
	g.why = explainer(t, nodes, r)
	return g
}

// explainer answers Graph.Why by re-reading the two files: for Go, the
// names one uses from the other; for the rest, the import that resolved to
// it. Graph.Files[i] is nodes[i]. It isn't safe for concurrent use.
func explainer(t *Tree, nodes []*Node, r resolver) func(from, to FileID) string {
	// A query asks about many edges into or out of one file: parse each file once.
	goCache, specCache := map[FileID]*goFacts{}, map[FileID][]string{}
	parseGo := func(id FileID) *goFacts {
		if gf, ok := goCache[id]; ok {
			return gf
		}
		var gf *goFacts
		if src, err := os.ReadFile(t.FSPath(nodes[id])); err == nil {
			_, gf = goDecls(nodes[id].Name, src, false)
		}
		goCache[id] = gf
		return gf
	}
	return func(from, to FileID) string {
		fn, tn := nodes[from], nodes[to]
		lang := langOf(fn.Name)
		if lang != "go" {
			specs, ok := specCache[from]
			if !ok {
				if src, err := os.ReadFile(t.FSPath(fn)); err == nil {
					_, specs = scanDecls(lang, src)
					if lang == "rs" {
						specs = append(specs, rsCodePaths(src)...)
					}
				}
				specCache[from] = specs
			}
			for _, spec := range specs {
				for _, target := range r.resolve(lang, fn.Rel, spec) {
					if target == tn.Rel {
						return "imports " + spec
					}
				}
			}
			return ""
		}
		ff, tf := parseGo(from), parseGo(to)
		if ff == nil || tf == nil {
			return ""
		}
		declared := map[string]bool{}
		for _, d := range tf.declares {
			declared[d] = true
		}
		var names []string
		if path.Dir(fn.Rel) == path.Dir(tn.Rel) && ff.pkg == tf.pkg {
			for name := range ff.uses {
				if declared[name] {
					names = append(names, name)
				}
			}
			return "uses " + someOf(names)
		}
		for _, imp := range ff.imports {
			if dir, ok := r.goPackageDir(imp.path); !ok || dir != path.Dir(tn.Rel) {
				continue
			}
			pkg := imp.name
			if pkg == "" {
				pkg = tf.pkg
			}
			for sel := range ff.selectors {
				if sel[0] == pkg && declared[sel[1]] {
					names = append(names, pkg+"."+sel[1])
				}
			}
			if len(names) == 0 {
				return "imports " + imp.path
			}
			return "uses " + someOf(names)
		}
		return ""
	}
}

// someOf lists up to three names, sorted, and counts the rest.
func someOf(names []string) string {
	sort.Strings(names)
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d more", len(names)-3)
}

// parseSource reads one file and resolves what it can on its own. The
// resolver is read-only, so this is safe to run concurrently.
func parseSource(n *Node, fsPath string, wantSymbols bool, r resolver) fileFacts {
	var f fileFacts
	if n.Size > maxSourceSize {
		return f
	}
	src, err := os.ReadFile(fsPath)
	if err != nil || bytes.IndexByte(src, 0) >= 0 {
		return f
	}
	lang := langOf(n.Name)
	if lang == "go" {
		f.symbols, f.goFacts = goDecls(fsPath, src, wantSymbols)
		return f
	}
	var specs []string
	f.symbols, specs = scanDecls(lang, src)
	if lang == "rs" {
		specs = append(specs, rsCodePaths(src)...)
	}
	if !wantSymbols {
		f.symbols = nil
	}
	for _, spec := range specs {
		for _, target := range r.resolve(lang, n.Rel, spec) {
			if target != n.Rel {
				f.targets = append(f.targets, target)
			}
		}
	}
	return f
}

func langOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
		return "js"
	case ".py":
		return "py"
	case ".rs":
		return "rs"
	case ".java":
		return "java"
	case ".kt":
		return "kt"
	}
	return ""
}

// ---------- Go ----------

// goFacts is what a Go file declares and references. Edges come from it
// once every file is parsed, because they depend on what other files declare.
type goFacts struct {
	pkg       string
	declares  []string        // top-level names
	uses      map[string]bool // every identifier: same-package references
	imports   []goImport
	selectors map[[2]string]bool // {"pkg", "Name"} for every pkg.Name selector on a bare identifier
}

type goImport struct{ path, name string } // name is "" unless the import is renamed

// goDecls parses a Go file for its declarations (with signatures when
// wantSymbols) and its references.
func goDecls(filename string, src []byte, wantSymbols bool) ([]string, *goFacts) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil
	}
	gf := &goFacts{pkg: file.Name.Name, uses: map[string]bool{}, selectors: map[[2]string]bool{}}
	// Only selectors that could be on an imported package are kept: every
	// file's selectors are held until linking, so this bounds memory. If a
	// package's name isn't guessable from its path, its import falls back to
	// depending on the whole package, so nothing is lost.
	pkgNames := map[string]bool{}
	for _, imp := range file.Imports {
		gi := goImport{path: strings.Trim(imp.Path.Value, `"`)}
		if imp.Name != nil {
			gi.name = imp.Name.Name
			pkgNames[gi.name] = true
		} else {
			for _, n := range guessPkgNames(gi.path) {
				pkgNames[n] = true
			}
		}
		gf.imports = append(gf.imports, gi)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			gf.uses[x.Name] = true
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && pkgNames[id.Name] {
				gf.selectors[[2]string{id.Name, x.Sel.Name}] = true
			}
		}
		return true
	})
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name != "main" && d.Name.Name != "init" {
				gf.declares = append(gf.declares, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					gf.declares = append(gf.declares, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name != "_" {
							gf.declares = append(gf.declares, n.Name)
						}
					}
				}
			}
		}
	}
	if !wantSymbols {
		return nil, gf
	}
	// Exported API first; a program's unexported functions come after,
	// since in package main nothing is exported.
	var symbols, private []string
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			sig := &ast.FuncDecl{Recv: d.Recv, Name: d.Name, Type: d.Type} // no body, no doc
			var b bytes.Buffer
			goprinter.Fprint(&b, fset, sig)
			if d.Name.IsExported() {
				symbols = append(symbols, oneLine(b.String()))
			} else if d.Name.Name != "init" {
				private = append(private, oneLine(b.String()))
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok {
					decl := "type " + ts.Name.Name + " " + goTypeKind(ts.Type)
					if ts.Name.IsExported() {
						symbols = append(symbols, strings.TrimSpace(decl))
					} else {
						private = append(private, strings.TrimSpace(decl))
					}
				}
			}
		}
	}
	return append(symbols, private...), gf
}

// linkGo adds the Go edges, file to file:
//
//   - same package: a file depends on the files in its directory and
//     package that declare the names it uses (tests may also use test files);
//   - imports: a file depends on the files in the imported package that
//     declare the names it selects (pkg.Name). Blank and dot imports, and
//     imports whose selectors match nothing we parsed, depend on the whole
//     package, so no dependency is lost.
func linkGo(b *graphBuilder, facts []fileFacts, r resolver) {
	// Declarations per package, keyed by directory and package name, so
	// each identifier costs one short-string lookup: this loop visits every
	// identifier in every file.
	type decls map[string]FileID
	owner := map[string]decls{}     // declared in non-test files
	testOwner := map[string]decls{} // declared in test files
	pkgOf := map[string]string{}    // dir -> package name of its non-test files
	pkgFiles := map[string][]FileID{}
	pkgKey := func(dir, pkg string) string { return dir + "\x00" + pkg }
	for i, f := range facts {
		gf := f.goFacts
		if gf == nil {
			continue
		}
		id, dir := FileID(i), path.Dir(b.files[i].Rel)
		table := owner
		if b.files[i].Test {
			table = testOwner
		} else {
			if _, ok := pkgOf[dir]; !ok {
				pkgOf[dir] = gf.pkg
			}
			if pkgOf[dir] == gf.pkg {
				pkgFiles[dir] = append(pkgFiles[dir], id)
			}
		}
		k := pkgKey(dir, gf.pkg)
		d := table[k]
		if d == nil {
			d = decls{}
			table[k] = d
		}
		for _, name := range gf.declares {
			if _, taken := d[name]; !taken {
				d[name] = id
			}
		}
	}

	dirOf := map[string]string{} // import path -> package dir ("" if not in the tree)
	for i, f := range facts {
		gf := f.goFacts
		if gf == nil {
			continue
		}
		id, dir, test := FileID(i), path.Dir(b.files[i].Rel), b.files[i].Test

		k := pkgKey(dir, gf.pkg)
		same, sameTests := owner[k], testOwner[k]
		for name := range gf.uses {
			if t, ok := same[name]; ok {
				b.link(id, t)
			} else if test {
				if t, ok := sameTests[name]; ok {
					b.link(id, t)
				}
			}
		}

		local := map[string]string{} // name the file refers to a package by -> its dir
		for _, imp := range gf.imports {
			dir, seen := dirOf[imp.path]
			if !seen {
				dir, _ = r.goPackageDir(imp.path)
				dirOf[imp.path] = dir
			}
			if dir == "" || len(pkgFiles[dir]) == 0 {
				continue
			}
			name := imp.name
			if name == "" {
				name = pkgOf[dir]
			}
			if name == "_" || name == "." {
				for _, t := range pkgFiles[dir] {
					b.link(id, t)
				}
				continue
			}
			local[name] = dir
		}
		if len(local) == 0 {
			continue
		}
		resolved := map[string]bool{}
		for sel := range gf.selectors {
			dir, ok := local[sel[0]]
			if !ok {
				continue
			}
			if t, ok := owner[pkgKey(dir, pkgOf[dir])][sel[1]]; ok {
				b.link(id, t)
				resolved[dir] = true
			}
		}
		for _, dir := range local {
			if !resolved[dir] {
				for _, t := range pkgFiles[dir] {
					b.link(id, t)
				}
			}
		}
	}
}

// guessPkgNames are the likely package names for an import path: its last
// element without a gopkg.in-style ".vN" suffix or a "go-" prefix, and for
// a major-version path like example.com/bar/v2, also "bar".
func guessPkgNames(importPath string) []string {
	clean := func(name string) string {
		if i := strings.IndexByte(name, '.'); i > 0 {
			name = name[:i]
		}
		return strings.TrimPrefix(name, "go-")
	}
	base := path.Base(importPath)
	names := []string{clean(base)}
	if len(base) > 1 && base[0] == 'v' && strings.Trim(base[1:], "0123456789") == "" {
		names = append(names, clean(path.Base(path.Dir(importPath))))
	}
	return names
}

func goTypeKind(e ast.Expr) string {
	switch e.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	case *ast.FuncType:
		return "func"
	case *ast.MapType:
		return "map"
	}
	return ""
}

// goModule is one go.mod in the tree: its module path and directory.
type goModule struct{ path, dir string }

// goModules reads every go.mod in the tree, longest module path first, so
// imports resolve in multi-module repositories (kubernetes' staging/, for
// one), not just against the root module.
func goModules(t *Tree, mods []*Node) []goModule {
	var out []goModule
	for _, n := range mods {
		data, err := os.ReadFile(t.FSPath(n))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				out = append(out, goModule{strings.Trim(strings.TrimSpace(m), `"`), path.Dir(n.Rel)})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].path) > len(out[j].path) })
	return out
}

// ---------- other languages ----------

var declPatterns = map[string][]*regexp.Regexp{
	"js": {
		regexp.MustCompile(`^export\s+(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?(?:async\s+)?(?:function\*?|class|interface|type|enum|const|let|var)\s+[A-Za-z_$][\w$]*.*`),
	},
	"py": {
		regexp.MustCompile(`^(?:async\s+)?def\s+[A-Za-z]\w*\s*\(.*`),
		regexp.MustCompile(`^class\s+[A-Za-z]\w*.*`),
	},
	"rs": {
		regexp.MustCompile(`^\s*pub(?:\([^)]*\))?\s+(?:async\s+)?(?:unsafe\s+)?(?:fn|struct|enum|trait|type|const)\s+\w+.*`),
	},
	"java": {
		regexp.MustCompile(`^\s*public\s+(?:(?:final|abstract|static|sealed)\s+)*(?:class|interface|enum|record)\s+\w+`),
	},
	"kt": {
		regexp.MustCompile(`^(?:(?:public|internal|data|sealed|open|abstract|enum)\s+)*(?:class|interface|object|fun)\s+[\w.<>]+.*`),
	},
}

var importPatterns = map[string][]*regexp.Regexp{
	"js": {
		regexp.MustCompile(`\b(?:import|export)\s[^'";]*?from\s*['"]([^'"]+)['"]`),
		regexp.MustCompile(`import\s*\(?\s*['"]([^'"]+)['"]`),
		regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`),
	},
	"java": {regexp.MustCompile(`^\s*import\s+(?:static\s+)?([\w.]+)\s*;`)},
	"kt":   {regexp.MustCompile(`^\s*import\s+([\w.]+)`)},
}

func scanDecls(lang string, src []byte) (symbols, imports []string) {
	for _, line := range strings.Split(string(src), "\n") {
		for _, re := range declPatterns[lang] {
			if m := re.FindString(line); m != "" {
				symbols = append(symbols, trimDecl(m))
				break
			}
		}
		if lang != "js" && lang != "py" && lang != "rs" {
			for _, re := range importPatterns[lang] {
				for _, m := range re.FindAllStringSubmatch(line, -1) {
					imports = append(imports, m[1])
				}
			}
		}
	}
	switch lang {
	case "js":
		imports = jsImports(src)
	case "py":
		imports = pyImports(src)
	case "rs":
		imports = rsImports(src)
	}
	return symbols, imports
}

// trimDecl keeps a declaration's signature and drops its body or value:
// it cuts at the first "{" or "=" outside brackets, so generic defaults
// like <T = unknown> and default arguments survive.
func trimDecl(s string) string {
	depth := 0
	cut := len(s)
scan:
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '<':
			depth++
		case ')', ']':
			depth--
		case '>':
			if i > 0 && s[i-1] == '=' { // "=>" is an arrow, not a bracket
				continue
			}
			depth--
		case '{':
			if depth <= 0 {
				cut = i
				break scan
			}
		case '=':
			next := byte(0)
			if i+1 < len(s) {
				next = s[i+1]
			}
			if depth <= 0 && next != '=' && next != '>' {
				cut = i
				break scan
			}
		}
	}
	s = strings.TrimSpace(s[:cut])
	s = strings.TrimSuffix(strings.TrimSuffix(s, ":"), ";")
	if strings.HasSuffix(s, "(") {
		s += "…)" // parameters continue on the next lines
	}
	return oneLine(s)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}

// ---------- import resolution ----------

type resolver struct {
	exists    map[string]bool
	goModules []goModule
	javaIndex map[string]string // "com/acme/Foo" -> rel path
	js        *jsProject
	pyRoots   []string // folders with a pyproject.toml, setup.py or setup.cfg, and their src/
	rs        *rsProject
}

// goPackageDir maps a Go import path to the directory it lives in, if a
// module in the tree provides it.
func (r resolver) goPackageDir(importPath string) (string, bool) {
	for _, m := range r.goModules {
		if importPath == m.path || strings.HasPrefix(importPath, m.path+"/") {
			dir := path.Join(m.dir, strings.TrimPrefix(importPath, m.path))
			if dir == "" {
				dir = "."
			}
			return dir, true
		}
	}
	return "", false
}

func (r resolver) resolve(lang, from, spec string) []string {
	dir := path.Dir(from)
	switch lang {
	case "js":
		if !strings.HasPrefix(spec, ".") {
			return r.resolveBare(from, spec)
		}
		if f, ok := r.probeJS(path.Join(dir, spec)); ok {
			return []string{f}
		}
	case "py":
		return r.resolvePython(from, spec)
	case "rs":
		return r.resolveRust(from, spec)
	case "java", "kt":
		if f, ok := r.javaIndex[strings.ReplaceAll(spec, ".", "/")]; ok {
			return []string{f}
		}
	}
	return nil
}

// javaIndex maps package paths like com/acme/Foo to the file declaring
// them, whatever source root (src/main/java, app/src/...) they live under.
func javaIndex(paths []*Node) map[string]string {
	idx := map[string]string{}
	for _, n := range paths {
		if l := langOf(n.Name); l != "java" && l != "kt" {
			continue
		}
		p := strings.TrimSuffix(n.Rel, path.Ext(n.Rel))
		// index every suffix, so com/acme/Foo matches src/main/java/com/acme/Foo
		for i := 0; i < len(p); i++ {
			if i == 0 || p[i-1] == '/' {
				if _, taken := idx[p[i:]]; !taken {
					idx[p[i:]] = n.Rel
				}
			}
		}
	}
	return idx
}
