package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContext(t *testing.T) {
	dir := queryRepo(t)
	out, errOut, code := runCLI(t, "context", filepath.Join(dir, "b/b.go"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"# b/b.go",
		"1 dependency · 1 direct user · 1 test reach it",
		"## depends on (and what it uses from each)\na/a.go — uses a.Hello\n  func Hello() string\n",
		"## used by\nc/c.go — uses b.B\n",
		"## tests that reach it\ngo test ./b\nb/b_test.go\n",
		"## declares\nfunc B() string\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Only what b uses from a: not the Unused type a also declares.
	if strings.Contains(out, "Unused") {
		t.Errorf("listed a declaration b doesn't use:\n%s", out)
	}

	// A small budget keeps the sections but says what was left out.
	out, _, _ = runCLI(t, "context", filepath.Join(dir, "b/b.go"), "--budget", "40")
	if estimateTokens(out) > 60 || !strings.Contains(out, "raise --budget") {
		t.Errorf("--budget 40 (%d tokens):\n%s", estimateTokens(out), out)
	}
}

func TestContextJSON(t *testing.T) {
	dir := queryRepo(t)
	// A budget too small for the text: JSON ignores it and reports in full.
	out, errOut, code := runCLI(t, "context", filepath.Join(dir, "b/b.go"), "--json", "--budget", "10")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var r contextResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := contextResult{
		SchemaVersion: 1,
		Command:       "context",
		File:          "b/b.go",
		Dependencies:  []contextDep{{"a/a.go", "uses a.Hello", []string{"func Hello() string"}}},
		Users:         []contextUser{{"c/c.go", "uses b.B"}},
		Tests:         []string{"b/b_test.go"},
		Declarations:  []string{"func B() string"},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("got %+v\nwant %+v", r, want)
	}

	// Empty parts are empty lists, not null.
	out, _, _ = runCLI(t, "context", filepath.Join(dir, "a/a.go"), "--json")
	if !strings.Contains(out, `"dependencies":[]`) {
		t.Errorf("no dependencies should be an empty list, not null: %s", out)
	}
}

func TestUsedSymbols(t *testing.T) {
	syms := []string{"type Graph struct", "func (g *Graph) Deps(id FileID) []FileID", "func (g *Graph) Ranked() []FileID", "func helper()", "type Other struct"}
	got := usedSymbols(syms, "g.Deps(x)", []string{"Graph"})
	if strings.Join(got, "; ") != "type Graph struct; func (g *Graph) Deps(id FileID) []FileID" {
		t.Errorf("exact names: %q", got)
	}
	got = usedSymbols([]string{"export const x", "export function helper(a)", "export class Unused"}, "import {x, helper} from './m'", nil)
	if strings.Join(got, "; ") != "export const x; export function helper(a)" {
		t.Errorf("by word: %q", got)
	}
}

func TestContextErrors(t *testing.T) {
	dir := queryRepo(t)
	chdir(t, dir)
	for _, arg := range []string{".", "a", filepath.Join(dir, "a")} {
		out, errOut, code := runCLI(t, "context", arg)
		want := "sprout: context needs a file, and " + arg + " is a folder (did you mean `sprout " + arg + " --entry`?)\n"
		if code != 2 || errOut != want || out != "" {
			t.Errorf("context %s: exit %d, stdout %q, stderr %q; want exit 2, empty stdout, stderr %q", arg, code, out, errOut, want)
		}
	}
}
