package main

import (
	"bufio"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Rust imports, resolved by Rust's module rules: each crate's modules live
// under its own src/; "use" trees are expanded; crate::, self:: and super::
// are relative to the right module; and other crates of the workspace are
// found by their Cargo.toml name. Macros and cfg aren't evaluated.

// A crate has one or more roots: its library (src/lib.rs or [lib] path) and
// binaries (src/main.rs and [[bin]] paths). A root's modules live in its
// folder. Files outside every root's folder (tests/, benches/, examples/,
// src/bin/) are crate roots of their own.
type rsCrate struct {
	dir   string
	roots []rsRoot // the library first
	lib   string   // the library root, or "" for a binary-only crate
}

type rsRoot struct{ dir, file string }

type rsProject struct {
	crates   map[string]*rsCrate // by Cargo.toml folder
	byName   map[string]*rsCrate // library crates, by name with - as _
	fallback *rsCrate            // the repository root, for files with no Cargo.toml above them
}

func newRSCrate(dir, libPath string, bins []string, exists map[string]bool) *rsCrate {
	c := &rsCrate{dir: dir}
	if libPath == "" && exists[path.Join(dir, "src/lib.rs")] {
		libPath = "src/lib.rs"
	}
	if libPath != "" {
		c.lib = path.Join(dir, libPath)
		c.roots = append(c.roots, rsRoot{path.Dir(c.lib), c.lib})
	}
	for _, b := range append([]string{"src/main.rs"}, bins...) {
		if f := path.Join(dir, b); exists[f] {
			c.roots = append(c.roots, rsRoot{path.Dir(f), f})
		}
	}
	return c
}

func loadRSProject(manifests []string, fsPath func(rel string) string, exists map[string]bool) *rsProject {
	p := &rsProject{crates: map[string]*rsCrate{}, byName: map[string]*rsCrate{}, fallback: newRSCrate(".", "", nil, exists)}
	for _, rel := range manifests {
		m, ok := readCargo(fsPath(rel))
		if !ok {
			continue // a workspace manifest without a [package]
		}
		c := newRSCrate(path.Dir(rel), m.libPath, m.bins, exists)
		p.crates[c.dir] = c
		name := m.libName
		if name == "" {
			name = m.name
		}
		if c.lib != "" {
			p.byName[strings.ReplaceAll(name, "-", "_")] = c
		}
	}
	return p
}

type cargoManifest struct {
	name, libName, libPath string
	bins                   []string
}

// readCargo reads the few Cargo.toml keys resolution needs.
func readCargo(file string) (m cargoManifest, ok bool) {
	f, err := os.Open(file)
	if err != nil {
		return m, false
	}
	defer f.Close()
	section := ""
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			ok = ok || section == "package"
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
				v = v[1 : end+1]
			}
		}
		switch {
		case section == "package" && k == "name":
			m.name = v
		case section == "lib" && k == "name":
			m.libName = v
		case section == "lib" && k == "path":
			m.libPath = v
		case section == "bin" && k == "path":
			m.bins = append(m.bins, v)
		}
	}
	return m, ok
}

// crateOf is the crate a file belongs to: the nearest Cargo.toml above it.
// It's never nil.
func (p *rsProject) crateOf(file string) *rsCrate {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if c, ok := p.crates[dir]; ok {
			return c
		}
		if dir == "." || dir == "/" {
			return p.fallback
		}
	}
}

// module is a file's module path (a/b.rs and a/b/mod.rs are a::b) under
// the root whose folder holds it, and that root.
func (c *rsCrate) module(file string) ([]string, rsRoot) {
	own := rsRoot{path.Dir(file), file} // a crate root of its own
	var best *rsRoot
	for i, r := range c.roots {
		if file == r.file {
			return nil, r
		}
		if strings.HasPrefix(file, r.dir+"/") && (best == nil || len(r.dir) > len(best.dir)) {
			best = &c.roots[i]
		}
	}
	if best == nil {
		return nil, own
	}
	rel := strings.TrimPrefix(file, best.dir+"/")
	if strings.HasPrefix(rel, "bin/") {
		return nil, own
	}
	rel = strings.TrimSuffix(strings.TrimSuffix(rel, ".rs"), "/mod")
	return strings.Split(rel, "/"), *best
}

// rsModuleFile finds the file of the longest prefix of mod that is a module.
func (r resolver) rsModuleFile(root rsRoot, mod []string) string {
	for k := len(mod); k > 0; k-- {
		p := path.Join(root.dir, strings.Join(mod[:k], "/"))
		for _, cand := range []string{p + ".rs", path.Join(p, "mod.rs")} {
			if r.exists[cand] {
				return cand
			}
		}
	}
	return root.file
}

func (r resolver) resolveRust(from, spec string) []string {
	if !strings.Contains(spec, "::") {
		return r.rsModDecl(from, spec)
	}
	p := r.rs
	segs := strings.Split(spec, "::")
	c := p.crateOf(from)
	here, root := c.module(from)
	var target string
	switch first := segs[0]; {
	case first == "crate":
		target = r.rsModuleFile(root, segs[1:])
	case first == "self" || first == "super":
		mod := append([]string{}, here...)
		i := 0
		for ; i < len(segs) && (segs[i] == "self" || segs[i] == "super"); i++ {
			if segs[i] == "super" && len(mod) > 0 {
				mod = mod[:len(mod)-1]
			}
		}
		target = r.rsModuleFile(root, append(mod, segs[i:]...))
	case p.byName[first] != nil:
		lc := p.byName[first]
		target = r.rsModuleFile(rsRoot{path.Dir(lc.lib), lc.lib}, segs[1:])
	default:
		// A child module in scope: `mod a;` here, then `use a::b`.
		if len(r.rsModDecl(from, first)) > 0 {
			target = r.rsModuleFile(root, append(append([]string{}, here...), segs...))
		}
	}
	if target == "" || target == from {
		return nil
	}
	return []string{target}
}

// rsModDecl resolves `mod x;`: x.rs or x/mod.rs next to a crate root or a
// mod.rs, and in a folder named after any other file.
func (r resolver) rsModDecl(from, name string) []string {
	modDir := path.Dir(from)
	if here, _ := r.rs.crateOf(from).module(from); len(here) > 0 && path.Base(from) != "mod.rs" {
		modDir = strings.TrimSuffix(from, ".rs")
	}
	for _, cand := range []string{path.Join(modDir, name+".rs"), path.Join(modDir, name, "mod.rs")} {
		if r.exists[cand] {
			return []string{cand}
		}
	}
	return nil
}

var (
	rsModDecl = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([^)]*\))?[ \t]+)?mod[ \t]+(\w+)[ \t]*;`)
	rsUse     = regexp.MustCompile(`\buse[ \t\n]+([^;]+);`)
	rsSep     = regexp.MustCompile(` ?(::|[{},]) ?`)
)

// rsImports lists a file's module declarations (bare names) and the paths
// its use declarations import, expanded: use a::{b, c::d} is a::b and a::c::d.
func rsImports(src []byte) []string {
	code := stripRustComments(src)
	var specs []string
	for _, m := range rsModDecl.FindAllSubmatch(code, -1) {
		specs = append(specs, string(m[1]))
	}
	inline := inlineModules(code)
	for _, m := range rsUse.FindAllSubmatchIndex(code, -1) {
		tree := rsSep.ReplaceAllString(strings.Join(strings.Fields(string(code[m[2]:m[3]])), " "), "$1")
		depth := 0
		for _, r := range inline {
			if m[0] > r[0] && m[0] < r[1] {
				depth++
			}
		}
		for _, p := range expandUse(tree, "") {
			if strings.Contains(p, "::") {
				specs = append(specs, outOfInline(p, depth))
			}
		}
	}
	return specs
}

var rsPath = regexp.MustCompile(`\b[A-Za-z_]\w*(?:::[A-Za-z_]\w*)+`)

// rsCodePaths lists the qualified paths a file uses in its code, such as
// crate::flags::parse::lookup(..) or grep::printer::Stats, which depend on
// a module without a use declaration. Each is listed once; ones that start
// with something other than a module of the project resolve to nothing.
func rsCodePaths(src []byte) []string {
	code := stripRustComments(src)
	inline := inlineModules(code)
	seen := map[string]bool{}
	var out []string
	for _, m := range rsPath.FindAllIndex(code, -1) {
		if m[0] >= 2 && code[m[0]-1] == ':' {
			continue // the tail of a longer path
		}
		depth := 0
		for _, r := range inline {
			if m[0] > r[0] && m[0] < r[1] {
				depth++
			}
		}
		p := outOfInline(string(code[m[0]:m[1]]), depth)
		if first, _, _ := strings.Cut(p, "::"); first == "std" || first == "core" || first == "alloc" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

var rsInlineMod = regexp.MustCompile(`\bmod\s+\w+\s*\{`)

// inlineModules finds the byte ranges of `mod name { ... }` blocks.
func inlineModules(code []byte) [][2]int {
	var out [][2]int
	for _, m := range rsInlineMod.FindAllIndex(code, -1) {
		depth := 0
		for i := m[1] - 1; i < len(code); i++ {
			if code[i] == '{' {
				depth++
			} else if code[i] == '}' {
				if depth--; depth == 0 {
					out = append(out, [2]int{m[0], i})
					break
				}
			}
		}
	}
	return out
}

// outOfInline rewrites a path written inside depth inline modules so it's
// relative to the file's own module: the first depth super:: steps stay in
// the file, and self:: inside an inline module is this file too.
func outOfInline(p string, depth int) string {
	if depth == 0 {
		return p
	}
	segs := strings.Split(p, "::")
	k := 0
	for k < len(segs) && segs[k] == "super" {
		k++
	}
	switch {
	case k > depth:
		return strings.Join(segs[k-depth:], "::")
	case k > 0 || segs[0] == "self":
		return "self::" + strings.Join(segs[max(k, 1):], "::")
	}
	return p
}

// expandUse flattens a use tree (no spaces around :: { } ,) under prefix.
func expandUse(tree, prefix string) []string {
	join := func(a, b string) string {
		if a == "" {
			return b
		}
		return a + "::" + b
	}
	tree = strings.TrimPrefix(tree, "::")
	open := strings.IndexByte(tree, '{')
	if open < 0 {
		name, _, _ := strings.Cut(tree, " as ")
		switch {
		case name == "" || name == "self":
			return []string{prefix}
		case strings.HasSuffix(name, "::self"):
			return []string{join(prefix, strings.TrimSuffix(name, "::self"))}
		}
		return []string{join(prefix, name)}
	}
	head := strings.TrimSuffix(tree[:open], "::")
	body := tree[open+1:]
	if strings.HasSuffix(body, "}") {
		body = body[:len(body)-1]
	}
	var out []string
	depth, start := 0, 0
	for i := 0; i <= len(body); i++ {
		if i == len(body) || body[i] == ',' && depth == 0 {
			if part := body[start:i]; part != "" {
				out = append(out, expandUse(part, join(prefix, head))...)
			}
			start = i + 1
			continue
		}
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	return out
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// stripRustComments blanks // and nested /* */ comments and the contents of
// string and char literals, keeping newlines. A ' starts a char literal only
// when one closes it right after; otherwise it's a lifetime.
func stripRustComments(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	blank := func(i, j int) {
		for ; i < j && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			j := i
			for j < len(out) && out[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			depth, j := 1, i+2
			for j < len(out) && depth > 0 {
				switch {
				case out[j] == '/' && j+1 < len(out) && out[j+1] == '*':
					depth, j = depth+1, j+2
				case out[j] == '*' && j+1 < len(out) && out[j+1] == '/':
					depth, j = depth-1, j+2
				default:
					j++
				}
			}
			blank(i, j)
			i = j - 1
		case c == 'r' && i+1 < len(out) && (out[i+1] == '"' || out[i+1] == '#') && (i == 0 || !isIdent(out[i-1])):
			j := i + 1
			hashes := 0
			for j < len(out) && out[j] == '#' {
				hashes, j = hashes+1, j+1
			}
			if j >= len(out) || out[j] != '"' {
				continue // r#ident, a raw identifier
			}
			end := "\"" + strings.Repeat("#", hashes)
			k := strings.Index(string(out[j+1:]), end)
			if k < 0 {
				k = len(out) - j - 1
			}
			blank(j+1, j+1+k)
			i = j + k + len(end)
		case c == '"':
			j := i + 1
			for j < len(out) && out[j] != '"' {
				if out[j] == '\\' {
					j++
				}
				j++
			}
			blank(i+1, j)
			i = j
		case c == '\'':
			j := i + 1
			if j < len(out) && out[j] == '\\' {
				j += 2
				for j < len(out) && j < i+12 && out[j] != '\'' {
					j++
				}
			} else if j < len(out) {
				_, size := utf8.DecodeRune(out[j:])
				j += size
			}
			if j < len(out) && out[j] == '\'' {
				blank(i+1, j)
				i = j
			}
		}
	}
	return out
}
