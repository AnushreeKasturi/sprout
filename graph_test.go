package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestGraphBuilder(t *testing.T) {
	b := newGraphBuilder(0)
	lib := b.add(GraphFile{Rel: "lib.go"})
	app := b.add(GraphFile{Rel: "app.go"})
	cli := b.add(GraphFile{Rel: "cli.go"})
	test := b.add(GraphFile{Rel: "lib_test.go", Test: true})
	b.link(app, lib)
	b.link(app, lib) // duplicate
	b.link(app, app) // self-edge
	b.link(cli, lib)
	b.link(cli, app)
	b.link(test, lib)
	g := b.build()

	if got := g.Deps(app); !reflect.DeepEqual(got, []FileID{lib}) {
		t.Errorf("Deps(app) = %v, want [lib]", got)
	}
	if got := g.Deps(cli); !reflect.DeepEqual(got, []FileID{lib, app}) {
		t.Errorf("Deps(cli) = %v, want [lib app] in ID order", got)
	}
	if got := g.Dependents(lib); !reflect.DeepEqual(got, []FileID{app, cli, test}) {
		t.Errorf("Dependents(lib) = %v, want [app cli test] in ID order", got)
	}
	if got := g.Dependents(test); len(got) != 0 {
		t.Errorf("Dependents(test) = %v, want none", got)
	}
	if g.UsedBy(lib) != 2 || g.UsedBy(app) != 1 || g.UsedBy(cli) != 0 {
		t.Errorf("UsedBy lib/app/cli = %d/%d/%d, want 2/1/0 (tests don't count)", g.UsedBy(lib), g.UsedBy(app), g.UsedBy(cli))
	}
	if got := testsOf(g, lib); !reflect.DeepEqual(got, []FileID{test}) {
		t.Errorf("Tests(lib) = %v, want [test]", got)
	}
	if got := g.Ranked(); !reflect.DeepEqual(got, []FileID{lib, app}) {
		t.Errorf("Ranked = %v, want [lib app]", got)
	}
	if id, ok := g.ID("cli.go"); !ok || id != cli {
		t.Errorf("ID(cli.go) = %v %v", id, ok)
	}

	empty := newGraphBuilder(0).build()
	if len(empty.Ranked()) != 0 {
		t.Error("empty graph should rank nothing")
	}
}

// rels turns FileIDs back into sorted paths for readable assertions.
func rels(g *Graph, ids []FileID) []string {
	out := []string{}
	for _, id := range ids {
		out = append(out, g.Files[id].Rel)
	}
	sort.Strings(out)
	return out
}

func deps(t *testing.T, g *Graph, rel string) []string {
	t.Helper()
	id, ok := g.ID(rel)
	if !ok {
		t.Fatalf("%s not in graph", rel)
	}
	return rels(g, g.Deps(id))
}

func TestPreciseGoEdges(t *testing.T) {
	g := graphFor(t, map[string]string{
		"go.mod":           "module example.com/app\n",
		"store/open.go":    "package store\nfunc Open() {}\n",
		"store/query.go":   "package store\nfunc Query() {}\n",
		"store/types.go":   "package store\ntype Row struct{}\n",
		"uses_one.go":      "package main\nimport \"example.com/app/store\"\nfunc a() { store.Open() }\n",
		"renamed.go":       "package main\nimport db \"example.com/app/store\"\nfunc b() { var r db.Row; _ = r; db.Query() }\n",
		"blank.go":         "package main\nimport _ \"example.com/app/store\"\n",
		"dot.go":           "package main\nimport . \"example.com/app/store\"\nfunc d() { Open() }\n",
		"unresolved.go":    "package main\nimport \"example.com/app/store\"\nfunc e() { store.Generated() }\n",
		"external.go":      "package main\nimport \"fmt\"\nfunc f() { fmt.Println() }\n",
		"cmd/tool/main.go": "package main\nfunc main() {}\n",
	})
	all := []string{"store/open.go", "store/query.go", "store/types.go"}
	cases := map[string][]string{
		"uses_one.go":   {"store/open.go"},                    // only the file declaring Open
		"renamed.go":    {"store/query.go", "store/types.go"}, // by its local name, db
		"blank.go":      all,                                  // side effects: the whole package
		"dot.go":        all,                                  // unqualified names: the whole package
		"unresolved.go": all,                                  // nothing matched: don't lose the dependency
		"external.go":   {},                                   // standard library isn't in the tree
	}
	for file, want := range cases {
		if got := deps(t, g, file); !reflect.DeepEqual(got, want) {
			t.Errorf("Deps(%s) = %v, want %v", file, got, want)
		}
	}
}

func TestGoTestRelationships(t *testing.T) {
	g := graphFor(t, map[string]string{
		"go.mod":                  "module example.com/app\n",
		"calc/add.go":             "package calc\nfunc Add(a, b int) int { return a + b }\n",
		"calc/mul.go":             "package calc\nfunc Mul(a, b int) int { helper := 1; return a * b * helper }\n",
		"calc/add_test.go":        "package calc\nimport \"testing\"\nfunc helper() {}\nfunc TestAdd(t *testing.T) { Add(1, 2); helper() }\n",
		"calc/mul_ext_test.go":    "package calc_test\nimport (\"testing\"; \"example.com/app/calc\")\nfunc TestMul(t *testing.T) { calc.Mul(2, 3) }\n",
		"calc/helpers_test.go":    "package calc\nfunc setup() {}\n",
		"calc/uses_setup_test.go": "package calc\nimport \"testing\"\nfunc TestX(t *testing.T) { setup() }\n",
	})
	tests := func(rel string) []string {
		id, _ := g.ID(rel)
		return rels(g, testsOf(g, id))
	}
	if got := tests("calc/add.go"); !reflect.DeepEqual(got, []string{"calc/add_test.go"}) {
		t.Errorf("Tests(add.go) = %v", got)
	}
	if got := tests("calc/mul.go"); !reflect.DeepEqual(got, []string{"calc/mul_ext_test.go"}) {
		t.Errorf("Tests(mul.go) = %v (external test package via selector)", got)
	}
	// mul.go has a local variable named helper; a test declares helper().
	// Non-test code can't depend on test files.
	if got := deps(t, g, "calc/mul.go"); len(got) != 0 {
		t.Errorf("Deps(mul.go) = %v, want none: never into test files", got)
	}
	// Tests may use helpers declared in other test files.
	if got := deps(t, g, "calc/uses_setup_test.go"); !reflect.DeepEqual(got, []string{"calc/helpers_test.go"}) {
		t.Errorf("Deps(uses_setup_test.go) = %v", got)
	}
	if n := usedBy(g, "calc/add.go"); n != 0 {
		t.Errorf("tests must not count as users: UsedBy(add.go) = %d", n)
	}
}

func TestOtherLanguageTestRelationships(t *testing.T) {
	g := graphFor(t, map[string]string{
		"src/http.ts":        "export function get() {}\n",
		"src/http.test.ts":   "import { get } from './http'\n",
		"tests/e2e.spec.ts":  "import { get } from '../src/http'\n",
		"src/pkg/core.py":    "def run(): pass\n",
		"tests/test_core.py": "from pkg.core import run\n", // "from pkg import core" (a submodule by name) isn't resolved yet
	})
	for rel, want := range map[string][]string{
		"src/http.ts":     {"src/http.test.ts", "tests/e2e.spec.ts"},
		"src/pkg/core.py": {"tests/test_core.py"},
	} {
		id, _ := g.ID(rel)
		if got := rels(g, testsOf(g, id)); !reflect.DeepEqual(got, want) {
			t.Errorf("Tests(%s) = %v, want %v", rel, got, want)
		}
	}
}

func TestGraphWithoutTests(t *testing.T) {
	dir := t.TempDir()
	for rel, src := range map[string]string{
		"go.mod":        "module example.com/app\n",
		"a.go":          "package app\nfunc A() {}\n",
		"a_test.go":     "package app\nfunc TestA() { A() }\n",
		"tests/x.ts":    "export const x = 1\n",
		"vendor/v/v.go": "package v\n",
	} {
		write(t, dir, rel, src)
	}
	tree, _ := BuildTree(dir, Options{MaxDepth: -1, ShowHidden: true, NoIgnore: true})
	g := buildGraph(dir, tree, false, true)
	for _, f := range g.Files {
		if f.Test || f.Rel == "vendor/v/v.go" {
			t.Errorf("%s shouldn't be read when tests are off (or ever, for vendor)", f.Rel)
		}
	}
}

func TestGoMultiModule(t *testing.T) {
	g := graphFor(t, map[string]string{
		"go.mod":                      "module example.com/root\n",
		"main.go":                     "package main\nimport \"example.com/lib/util\"\nfunc main() { util.Do() }\n",
		"staging/lib/go.mod":          "module example.com/lib\n",
		"staging/lib/util/do.go":      "package util\nfunc Do() {}\n",
		"staging/lib/util/other.go":   "package util\nfunc Other() {}\n",
		"staging/lib/internal/x/x.go": "package x\nfunc X() {}\n",
		"staging/lib/util/uses_x.go":  "package util\nimport \"example.com/lib/internal/x\"\nfunc Y() { x.X() }\n",
	})
	if got := deps(t, g, "main.go"); !reflect.DeepEqual(got, []string{"staging/lib/util/do.go"}) {
		t.Errorf("Deps(main.go) = %v: imports of a nested module should resolve", got)
	}
	if got := deps(t, g, "staging/lib/util/uses_x.go"); !reflect.DeepEqual(got, []string{"staging/lib/internal/x/x.go"}) {
		t.Errorf("Deps(uses_x.go) = %v", got)
	}
}

func TestGuessPkgNames(t *testing.T) {
	cases := map[string][]string{
		"example.com/app/store":   {"store"},
		"k8s.io/api/core/v1":      {"v1", "core"},
		"github.com/foo/bar/v2":   {"v2", "bar"},
		"gopkg.in/yaml.v3":        {"yaml"},
		"github.com/x/go-isatty":  {"isatty"},
		"github.com/x/version9ab": {"version9ab"},
	}
	for in, want := range cases {
		if got := guessPkgNames(in); !reflect.DeepEqual(got, want) {
			t.Errorf("guessPkgNames(%q) = %v, want %v", in, got, want)
		}
	}
}

// A package whose name can't be guessed from its path still gets linked:
// the import falls back to depending on the whole package.
func TestUnguessablePackageNameFallsBack(t *testing.T) {
	g := graphFor(t, map[string]string{
		"go.mod":         "module example.com/app\n",
		"weird/dir/a.go": "package actual\nfunc A() {}\n",
		"weird/dir/b.go": "package actual\nfunc B() {}\n",
		"main.go":        "package main\nimport \"example.com/app/weird/dir\"\nfunc main() { actual.A() }\n",
	})
	if got := deps(t, g, "main.go"); !reflect.DeepEqual(got, []string{"weird/dir/a.go", "weird/dir/b.go"}) {
		t.Errorf("Deps(main.go) = %v, want the whole package", got)
	}
}

// In Go only _test.go files are tests: helpers under test/ are code that
// tests and e2e suites import, so they're in the graph and can be found.
func TestGoTestHelpersAreCode(t *testing.T) {
	g := graphFor(t, map[string]string{
		"go.mod":                "module ex\n\ngo 1.22\n",
		"test/utils/helpers.go": "package utils\n\nfunc Help() {}\n",
		"pkg/x/x.go":            "package x\n\nfunc X() {}\n",
		"pkg/x/x_test.go":       "package x\n\nimport (\n\t\"testing\"\n\t\"ex/test/utils\"\n)\n\nfunc TestX(t *testing.T) { utils.Help(); X() }\n",
		"py/tests/test_api.py":  "import os\n",
		"web/app.spec.ts":       "export {};\n",
	})
	for rel, test := range map[string]bool{"test/utils/helpers.go": false, "pkg/x/x_test.go": true, "py/tests/test_api.py": true, "web/app.spec.ts": true} {
		id, ok := g.ID(rel)
		if !ok || g.Files[id].Test != test {
			t.Errorf("%s: in graph %v, test %v; want test %v", rel, ok, ok && g.Files[id].Test, test)
		}
	}
	wantDeps(t, g, "pkg/x/x_test.go", "pkg/x/x.go", "test/utils/helpers.go")
}

// testsOf is the test files that depend on id.
func testsOf(g *Graph, id FileID) []FileID {
	var out []FileID
	for _, d := range g.Dependents(id) {
		if g.Files[d].Test {
			out = append(out, d)
		}
	}
	return out
}

// A test fixture that copies the project's go.mod mustn't take over its
// imports (cli/cli keeps one under .github/codeql/tests).
func TestDuplicateGoModulePath(t *testing.T) {
	fixture := ".github/codeql/tests/case/"
	g := graphFor(t, map[string]string{
		"go.mod":                 "module example.com/app\n",
		"pkg/io/io.go":           "package io\nfunc New() {}\n",
		"cmd/main.go":            "package main\nimport \"example.com/app/pkg/io\"\nfunc main() { io.New() }\n",
		fixture + "go.mod":       "module example.com/app\n",
		fixture + "pkg/io/io.go": "package io\nfunc New() {}\n",
		fixture + "cmd/main.go":  "package main\nimport \"example.com/app/pkg/io\"\nfunc main() { io.New() }\n",
	})
	if got := deps(t, g, "cmd/main.go"); !reflect.DeepEqual(got, []string{"pkg/io/io.go"}) {
		t.Fatalf("the project's own import resolved to %v", got)
	}
}
