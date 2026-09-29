// Command gotruth is the ground truth for Go, at package level: each file's
// imports come from go/parser, and an import path maps to the directory of
// the repository module with the longest matching module path, which is
// how the go command resolves packages when every module is local.
//
//	go run ./tools/accuracy/truth/gotruth <repo> <graph.json>   (prints {rel: {specs, dirs}})
//
// Sprout's Go edges point at the file that declares what's used; checking
// that exact file needs a type checker, so only the package is checked.
package main

import (
	"bufio"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	repo, graphFile := os.Args[1], os.Args[2]
	var graph struct {
		Files []struct {
			Rel, Lang string
			Test      bool
		}
	}
	data, err := os.ReadFile(graphFile)
	check(err)
	check(json.Unmarshal(data, &graph))

	hasCode := map[string]bool{} // directories with a non-test Go file in scope
	for _, f := range graph.Files {
		if f.Lang == "go" && !strings.HasSuffix(f.Rel, "_test.go") {
			hasCode[path.Dir(f.Rel)] = true
		}
	}

	modules := map[string]string{} // module path -> directory
	check(filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != repo && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.Name() == "go.mod" {
			if mod := modulePath(p); mod != "" {
				rel, _ := filepath.Rel(repo, filepath.Dir(p))
				modules[mod] = filepath.ToSlash(rel)
			}
		}
		return nil
	}))

	out := map[string]map[string][]string{}
	fset := token.NewFileSet()
	for _, f := range graph.Files {
		if f.Lang != "go" {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(repo, f.Rel), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		specs, dirs := []string{}, []string{}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			specs = append(specs, p)
			if dir, ok := resolve(modules, p); ok && hasCode[dir] {
				dirs = append(dirs, dir)
			}
		}
		out[f.Rel] = map[string][]string{"specs": specs, "dirs": dirs}
	}
	check(json.NewEncoder(os.Stdout).Encode(out))
}

func resolve(modules map[string]string, importPath string) (string, bool) {
	best := ""
	for mod := range modules {
		if (importPath == mod || strings.HasPrefix(importPath, mod+"/")) && len(mod) > len(best) {
			best = mod
		}
	}
	if best == "" {
		return "", false
	}
	return path.Clean(path.Join(modules[best], strings.TrimPrefix(importPath, best))), true
}

func modulePath(gomod string) string {
	f, err := os.Open(gomod)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(s.Text()), "module"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
