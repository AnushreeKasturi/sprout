package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real repositories, pinned, checking what broke on them before: cli/cli's
// duplicate go.mod, bubbletea's wrapped README and tutorials, click's and
// ky's summaries and graphs. Slow and networked, so it only runs with
// SPROUT_REAL_REPOS set to a cache folder:
//
//	SPROUT_REAL_REPOS=/tmp/sprout-real go test -run TestRealRepos -v .
var realRepos = []struct{ name, url, commit string }{
	{"cli", "https://github.com/cli/cli", "ec5b512045db67e5a2a4ff4a1b02660b2fb24390"},
	{"bubbletea", "https://github.com/charmbracelet/bubbletea", "f9df43c4f2c021d8740a2628d59cd810a862eb98"},
	{"click", "https://github.com/pallets/click", "2247b35ea1c47c727d7a06e51fa280e12a863ff6"},
	{"ky", "https://github.com/sindresorhus/ky", "3541888878c4d303a8bc2ad61815f4e048f55a5d"},
}

func TestRealRepos(t *testing.T) {
	cache := os.Getenv("SPROUT_REAL_REPOS")
	if cache == "" {
		t.Skip("set SPROUT_REAL_REPOS to a cache folder to test on real repositories")
	}
	dirs := map[string]string{}
	for _, r := range realRepos {
		dirs[r.name] = fetchPinned(t, cache, r.name, r.url, r.commit)
	}

	t.Run("cli: a fixture's copy of go.mod doesn't take over", func(t *testing.T) {
		if n := dependentsOf(t, dirs["cli"], "pkg/cmdutil/factory.go"); n < 350 {
			t.Errorf("factory.go has %d dependents, want 350+ (2 when the CodeQL fixture won)", n)
		}
		order := readingPaths(t, dirs["cli"])
		for _, p := range order {
			if strings.HasPrefix(p, ".github/") || strings.HasPrefix(p, "script/") {
				t.Errorf("reading order suggests %s: %v", p, order)
			}
		}
		if !contains(order[:min(5, len(order))], "pkg/cmdutil/factory.go") {
			t.Errorf("factory.go, the most used file, isn't in the first five: %v", order)
		}
	})

	t.Run("bubbletea: whole README sentence, no tutorials", func(t *testing.T) {
		r, _ := tourJSON(t, dirs["bubbletea"])
		if r.Purpose == nil || !strings.HasSuffix(r.Purpose.Text, "based on The Elm Architecture.") {
			t.Errorf("purpose cut short: %+v", r.Purpose)
		}
		order := readingPaths(t, dirs["bubbletea"])
		if len(order) < 2 || order[1] != "tea.go" {
			t.Errorf("tea.go should come right after the README: %v", order)
		}
		for _, p := range order {
			if strings.HasPrefix(p, "tutorials/") {
				t.Errorf("reading order suggests a tutorial: %v", order)
			}
		}
	})

	t.Run("click: summary and graph", func(t *testing.T) {
		r, _ := tourJSON(t, dirs["click"])
		if r.Purpose == nil || !strings.HasPrefix(r.Purpose.Text, "Click is a Python package for creating beautiful command line interfaces in a composable way") {
			t.Errorf("purpose: %+v", r.Purpose)
		}
		if n := dependentsOf(t, dirs["click"], "src/click/core.py"); n < 10 {
			t.Errorf("core.py has %d dependents, want 10+", n)
		}
	})

	t.Run("ky: lowercase readme, TypeScript graph", func(t *testing.T) {
		r, _ := tourJSON(t, dirs["ky"])
		if r.Purpose == nil || r.Purpose.Path != "readme.md" || !strings.HasPrefix(r.Purpose.Text, "Ky is a tiny and elegant HTTP client") {
			t.Errorf("purpose: %+v", r.Purpose)
		}
		if order := readingPaths(t, dirs["ky"]); !contains(order, "source/index.ts") {
			t.Errorf("source/index.ts missing from the reading order: %v", order)
		}
	})
}

// fetchPinned checks out commit of url in cache/name, fetching only that
// commit, and reuses it on later runs.
func fetchPinned(t *testing.T, cache, name, url, commit string) string {
	t.Helper()
	dir := filepath.Join(cache, name)
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); err == nil && strings.TrimSpace(string(out)) == commit {
		return dir
	}
	os.RemoveAll(dir)
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "fetch", "-q", "--depth=1", url, commit},
		{"-C", dir, "checkout", "-q", "FETCH_HEAD"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func dependentsOf(t *testing.T, dir, file string) int {
	t.Helper()
	chdir(t, dir)
	out, errOut, code := runCLI(t, "dependents", file, "--json")
	if code != 0 {
		t.Fatalf("dependents %s: exit %d: %s", file, code, errOut)
	}
	var r struct{ Results []json.RawMessage }
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	return len(r.Results)
}

func readingPaths(t *testing.T, dir string) []string {
	t.Helper()
	r, _ := tourJSON(t, dir)
	var paths []string
	for _, s := range r.ReadingOrder {
		paths = append(paths, s.Path)
	}
	return paths
}
