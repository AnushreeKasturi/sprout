package main

import (
	"encoding/json"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// TypeScript and JavaScript resolution beyond relative paths: tsconfig or
// jsconfig "paths" and "baseUrl" (following "extends"), and packages of the
// same repository, found by their package.json "name", "exports" and "main".
// Everything is read once, before parsing, and is read-only after.

// jsImports lists the module specifiers in a JS or TS file. Comments are
// skipped and statements may span lines, so a commented-out import isn't a
// dependency and a multi-line `import {\n a,\n b\n} from './x'` is.
func jsImports(src []byte) []string {
	code := stripJSComments(src)
	var specs []string
	for _, re := range importPatterns["js"] {
		for _, m := range re.FindAllSubmatch(code, -1) {
			specs = append(specs, string(m[1]))
		}
	}
	return specs
}

// stripJSComments blanks // and /* */ comments, leaving string and template
// literals alone and every newline in place.
func stripJSComments(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	for i := 0; i < len(out); i++ {
		switch c := out[i]; {
		case c == '"' || c == '\'' || c == '`':
			for i++; i < len(out) && out[i] != c; i++ {
				if out[i] == '\\' {
					i++
				} else if out[i] == '\n' && c != '`' {
					break // an unterminated string ends at the line
				}
			}
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			for ; i < len(out) && out[i] != '\n'; i++ {
				out[i] = ' '
			}
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			for ; i < len(out) && !(out[i] == '*' && i+1 < len(out) && out[i+1] == '/'); i++ {
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
			if i+1 < len(out) {
				out[i], out[i+1] = ' ', ' '
				i++
			}
		}
	}
	return out
}

type jsProject struct {
	configs  map[string]*jsConfig  // directory -> the tsconfig or jsconfig there, extends applied
	packages map[string]*jsPackage // package name -> package
	pkgNames []string              // longest first, so @acme/ui-kit isn't matched as @acme/ui
}

type jsConfig struct {
	baseURL  string // directory, relative to the root; "" when unset
	hasBase  bool
	paths    []jsPath
	pathsDir string // where paths resolve from when there's no baseUrl
}

type jsPath struct {
	pattern string
	targets []string
}

type jsPackage struct {
	dir     string
	exports any
	entry   []string // main, module, source and types, as written
}

type rawTSConfig struct {
	Extends         any `json:"extends"`
	CompilerOptions struct {
		BaseURL *string             `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

// readJSONC reads JSON that may have comments and trailing commas, as
// tsconfig files do.
func readJSONC(file string, v any) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	data = trailingComma.ReplaceAll(stripJSComments(data), []byte("$1"))
	return json.Unmarshal(data, v) == nil
}

// loadJSProject reads every tsconfig.json, jsconfig.json and package.json
// in the tree. fsPath maps a relative path to its filesystem path.
func loadJSProject(configs, manifests []string, fsPath func(rel string) string) *jsProject {
	p := &jsProject{configs: map[string]*jsConfig{}, packages: map[string]*jsPackage{}}
	for _, rel := range manifests {
		var m struct {
			Name    string `json:"name"`
			Exports any    `json:"exports"`
			Main    string `json:"main"`
			Module  string `json:"module"`
			Source  string `json:"source"`
			Types   string `json:"types"`
		}
		if !readJSONC(fsPath(rel), &m) || m.Name == "" {
			continue
		}
		pkg := &jsPackage{dir: path.Dir(rel), exports: m.Exports}
		for _, e := range []string{m.Source, m.Module, m.Main, m.Types} {
			if e != "" {
				pkg.entry = append(pkg.entry, e)
			}
		}
		if _, dup := p.packages[m.Name]; !dup {
			p.packages[m.Name] = pkg
			p.pkgNames = append(p.pkgNames, m.Name)
		}
	}
	sort.Slice(p.pkgNames, func(i, j int) bool { return len(p.pkgNames[i]) > len(p.pkgNames[j]) })

	raw := map[string]*rawTSConfig{} // relative path -> parsed, for extends
	read := func(rel string) *rawTSConfig {
		if c, ok := raw[rel]; ok {
			return c
		}
		var c rawTSConfig
		if !readJSONC(fsPath(rel), &c) {
			raw[rel] = nil
			return nil
		}
		raw[rel] = &c
		return &c
	}
	for _, rel := range configs {
		if c := p.effective(rel, read, map[string]bool{}); c != nil {
			// A jsconfig.json only counts where there's no tsconfig.json.
			if _, taken := p.configs[path.Dir(rel)]; !taken || path.Base(rel) == "tsconfig.json" {
				p.configs[path.Dir(rel)] = c
			}
		}
	}
	return p
}

// effective applies a config's extends chain: what it sets overrides what it
// inherits, and inherited settings stay relative to the file that set them.
func (p *jsProject) effective(rel string, read func(string) *rawTSConfig, seen map[string]bool) *jsConfig {
	if seen[rel] {
		return nil
	}
	seen[rel] = true
	c := read(rel)
	if c == nil {
		return nil
	}
	out := &jsConfig{}
	var parents []string
	switch e := c.Extends.(type) {
	case string:
		parents = []string{e}
	case []any:
		for _, x := range e {
			if s, ok := x.(string); ok {
				parents = append(parents, s)
			}
		}
	}
	for _, e := range parents {
		if base := p.configFile(path.Dir(rel), e); base != "" {
			if pc := p.effective(base, read, seen); pc != nil {
				*out = *pc
			}
		}
	}
	dir := path.Dir(rel)
	if b := c.CompilerOptions.BaseURL; b != nil {
		out.baseURL, out.hasBase = path.Join(dir, *b), true
	}
	if c.CompilerOptions.Paths != nil {
		out.paths, out.pathsDir = nil, dir
		for pat, targets := range c.CompilerOptions.Paths {
			out.paths = append(out.paths, jsPath{pat, targets})
		}
		// The longest prefix before the * wins, as in TypeScript.
		sort.Slice(out.paths, func(i, j int) bool {
			a, b := strings.Index(out.paths[i].pattern+"*", "*"), strings.Index(out.paths[j].pattern+"*", "*")
			if a != b {
				return a > b
			}
			return out.paths[i].pattern < out.paths[j].pattern
		})
	}
	return out
}

// configFile finds the file an "extends" names: a path, or a file in a
// package of this repository.
func (p *jsProject) configFile(dir, ext string) string {
	withJSON := func(f string) string {
		if !strings.HasSuffix(f, ".json") {
			f += ".json"
		}
		return f
	}
	if strings.HasPrefix(ext, ".") {
		return withJSON(path.Join(dir, ext))
	}
	for _, name := range p.pkgNames {
		if ext == name {
			return path.Join(p.packages[name].dir, "tsconfig.json")
		}
		if rest, ok := strings.CutPrefix(ext, name+"/"); ok {
			return withJSON(path.Join(p.packages[name].dir, rest))
		}
	}
	return "" // a package from node_modules, which isn't part of the project
}

// configFor is the tsconfig or jsconfig nearest above dir.
func (p *jsProject) configFor(dir string) *jsConfig {
	for {
		if c, ok := p.configs[dir]; ok {
			return c
		}
		if dir == "." || dir == "/" || dir == "" {
			return nil
		}
		dir = path.Dir(dir)
	}
}

// resolveBare resolves a specifier that isn't a relative path: through the
// nearest config's paths, then a package of this repository, then baseUrl.
func (r resolver) resolveBare(from, spec string) []string {
	p := r.js
	if p == nil {
		return nil
	}
	if c := p.configFor(path.Dir(from)); c != nil {
		base := c.pathsDir
		if c.hasBase {
			base = c.baseURL
		}
		for _, m := range c.paths {
			star, ok := matchPattern(m.pattern, spec)
			if !ok {
				continue
			}
			for _, t := range m.targets {
				if f, ok := r.probeJS(path.Join(base, strings.Replace(t, "*", star, 1))); ok {
					return []string{f}
				}
			}
			break // TypeScript tries only the best-matching pattern
		}
	}
	for _, name := range p.pkgNames {
		if spec == name || strings.HasPrefix(spec, name+"/") {
			if f, ok := r.packageFile(p.packages[name], "."+strings.TrimPrefix(spec, name)); ok {
				return []string{f}
			}
			break
		}
	}
	if c := p.configFor(path.Dir(from)); c != nil && c.hasBase {
		if f, ok := r.probeJS(path.Join(c.baseURL, spec)); ok {
			return []string{f}
		}
	}
	return nil
}

// matchPattern matches spec against a paths pattern with at most one *,
// returning what the * stood for.
func matchPattern(pattern, spec string) (string, bool) {
	pre, post, wild := strings.Cut(pattern, "*")
	if !wild {
		return "", spec == pattern
	}
	if len(spec) >= len(pre)+len(post) && strings.HasPrefix(spec, pre) && strings.HasSuffix(spec, post) {
		return spec[len(pre) : len(spec)-len(post)], true
	}
	return "", false
}

// packageFile resolves a subpath ("." or "./button") of a package in the
// repository: its exports map if it has one, else its entry fields and
// the usual index files.
func (r resolver) packageFile(pkg *jsPackage, sub string) (string, bool) {
	try := func(targets []string) (string, bool) {
		for _, t := range targets {
			if f, ok := r.probeJS(path.Join(pkg.dir, t)); ok {
				return f, true
			}
		}
		return "", false
	}
	if pkg.exports != nil {
		exp := pkg.exports
		m, isMap := exp.(map[string]any)
		if !isMap || !hasSubpathKeys(m) {
			exp = map[string]any{".": exp} // a string, an array, or conditions for "."
			m = exp.(map[string]any)
		}
		if v, ok := m[sub]; ok {
			return try(leaves(v, ""))
		}
		for k, v := range m {
			if star, ok := matchPattern(k, sub); ok && strings.Contains(k, "*") {
				return try(leaves(v, star))
			}
		}
		return "", false
	}
	if sub == "." {
		return try(append(append([]string{}, pkg.entry...), "index", "src/index"))
	}
	return try([]string{sub, path.Join("src", sub)})
}

func hasSubpathKeys(m map[string]any) bool {
	for k := range m {
		return strings.HasPrefix(k, ".")
	}
	return false
}

// leaves lists the paths in an exports value, in order: a string, an array,
// or a map of conditions, with * replaced.
func leaves(v any, star string) []string {
	switch x := v.(type) {
	case string:
		return []string{strings.ReplaceAll(x, "*", star)}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, leaves(e, star)...)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		// Source-like conditions first; "types" often points at built output.
		rank := map[string]int{"source": 0, "development": 1, "import": 2, "default": 3, "require": 4, "node": 5, "types": 9}
		sort.Slice(keys, func(i, j int) bool {
			ri, oki := rank[keys[i]]
			rj, okj := rank[keys[j]]
			if !oki {
				ri = 6
			}
			if !okj {
				rj = 6
			}
			if ri != rj {
				return ri < rj
			}
			return keys[i] < keys[j]
		})
		var out []string
		for _, k := range keys {
			out = append(out, leaves(x[k], star)...)
		}
		return out
	}
	return nil
}

var jsExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".d.ts"}

// probeJS finds the source file a module path means: the path itself, with
// an extension added, as a directory's index, or a .js written for a .ts.
func (r resolver) probeJS(base string) (string, bool) {
	base = path.Clean(base)
	if r.exists[base] {
		return base, true
	}
	for _, ext := range jsExts {
		if r.exists[base+ext] {
			return base + ext, true
		}
	}
	for _, ext := range jsExts {
		if r.exists[base+"/index"+ext] {
			return base + "/index" + ext, true
		}
	}
	if trimmed := strings.TrimSuffix(base, path.Ext(base)); trimmed != base {
		for _, ext := range jsExts[:2] {
			if r.exists[trimmed+ext] {
				return trimmed + ext, true
			}
		}
		if path.Ext(base) == ".mjs" && r.exists[trimmed+".mts"] {
			return trimmed + ".mts", true
		}
	}
	return "", false
}
