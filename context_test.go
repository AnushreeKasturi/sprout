package main

import (
	"encoding/json"
	"path/filepath"
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
	if r.SchemaVersion != 1 || r.Command != "context" || r.File != "b/b.go" {
		t.Errorf("unexpected header: %s", out)
	}
	if len(r.Dependencies) != 1 || r.Dependencies[0].Path != "a/a.go" || r.Dependencies[0].Reason != "uses a.Hello" ||
		strings.Join(r.Dependencies[0].Signatures, "; ") != "func Hello() string" {
		t.Errorf("dependencies: %+v", r.Dependencies)
	}
	if len(r.Users) != 1 || r.Users[0].Path != "c/c.go" || r.Users[0].Reason != "uses b.B" {
		t.Errorf("users: %+v", r.Users)
	}
	if strings.Join(r.Tests, "; ") != "b/b_test.go" {
		t.Errorf("tests: %q", r.Tests)
	}
	if strings.Join(r.Declarations, "; ") != "func B() string" {
		t.Errorf("declarations: %q", r.Declarations)
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
