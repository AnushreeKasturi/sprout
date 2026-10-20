package main

import (
	"path"
	"regexp"
	"strings"
)

// Python imports, read across lines, and resolved from the source roots
// Python would use: the folders above the importing file that aren't
// packages (a script's own folder is on sys.path, and pytest adds a test's),
// each with its src/, then every folder holding a pyproject.toml, setup.py
// or setup.cfg.

var (
	pyFrom   = regexp.MustCompile(`(?m)^[ \t]*from[ \t]+(\.*[\w.]*)[ \t]+import[ \t]+(\([^)]*\)|[^\n#;]*)`)
	pyImport = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+([^\n#;]+)`)
)

// pyImports lists a file's imports. "from pkg import a, b" yields pkg and
// also pkg.a and pkg.b, since a and b may be submodules; names that aren't
// resolve to nothing.
func pyImports(src []byte) []string {
	var specs []string
	for _, m := range pyFrom.FindAllSubmatch(src, -1) {
		mod := string(m[1])
		specs = append(specs, mod)
		sep := "."
		if strings.Trim(mod, ".") == "" {
			sep = "" // from . import x -> .x
		}
		names, _ := pyNames(string(m[2]))
		for _, name := range names {
			if name != "*" {
				specs = append(specs, mod+sep+name)
			}
		}
	}
	for _, m := range pyImport.FindAllSubmatch(src, -1) {
		names, _ := pyNames(string(m[1]))
		specs = append(specs, names...)
	}
	return specs
}

// pyNames splits "a, b as c" or "(\n  a,\n  b,\n)" into what's imported,
// a, b, and the names that binds, a, c.
func pyNames(list string) (names, bound []string) {
	list = strings.NewReplacer("(", " ", ")", " ", "\\", " ").Replace(list)
	for _, part := range strings.Split(list, ",") {
		line, _, _ := strings.Cut(part, "#")
		if f := strings.Fields(line); len(f) > 0 {
			names = append(names, f[0])
			bound = append(bound, f[len(f)-1])
		}
	}
	return names, bound
}

func (r resolver) resolvePython(from, spec string) []string {
	mod, dir := spec, path.Dir(from)
	var bases []string
	if strings.HasPrefix(mod, ".") {
		up := len(mod) - len(strings.TrimLeft(mod, "."))
		base := dir
		for i := 1; i < up; i++ {
			base = path.Dir(base)
		}
		bases, mod = []string{base}, mod[up:]
	} else {
		for d := dir; ; d = path.Dir(d) {
			// A package's own folder isn't on sys.path: inside src/click,
			// "import types" is the standard library, not click/types.py.
			if !r.exists[path.Join(d, "__init__.py")] {
				bases = append(bases, d, path.Join(d, "src"))
			}
			if d == "." {
				break
			}
		}
		bases = append(bases, r.pyRoots...)
	}
	rel := strings.ReplaceAll(mod, ".", "/")
	for _, b := range bases {
		p := path.Join(b, rel)
		for _, cand := range []string{p + ".py", path.Join(p, "__init__.py")} {
			if r.exists[cand] {
				return []string{cand}
			}
		}
	}
	return r.pyReexported(from, spec)
}

// pyReexported resolves pkg.Name, for "from pkg import Name", when Name
// isn't a submodule: to the module pkg/__init__.py imports Name from, if it
// re-exports it from one of its own. "import pkg" still links __init__.py,
// which runs on import.
func (r resolver) pyReexported(from, spec string) []string {
	i := strings.LastIndex(spec, ".")
	if i < 0 || strings.Trim(spec[:i], ".") == "" {
		return nil
	}
	pkg, name := r.resolvePython(from, spec[:i]), spec[i+1:]
	for hops := 0; len(pkg) == 1 && hops < 4; hops++ { // a package can re-export what a subpackage re-exports
		export, ok := r.pyExports[pkg[0]][name]
		if !ok {
			return nil
		}
		if path.Base(export[0]) != "__init__.py" || export[0] == pkg[0] {
			return []string{export[0]}
		}
		pkg, name = []string{export[0]}, export[1]
	}
	return nil
}

// pyReexports reads what each __init__.py re-exports from inside its own
// package: from .user import User, or from app.models.user import User in
// app/models. Imports from elsewhere (from os import path) aren't
// re-exports of the package's code, and star imports name nothing.
func pyReexports(nodes []*Node, t *Tree, r resolver) map[string]map[string][2]string {
	exports := map[string]map[string][2]string{}
	for _, n := range nodes {
		if n.Name != "__init__.py" {
			continue
		}
		src, ok := readSource(n, t.FSPath(n))
		if !ok {
			continue
		}
		pkgDir := path.Dir(n.Rel)
		for _, m := range pyFrom.FindAllSubmatch(src, -1) {
			mod := r.resolvePython(n.Rel, string(m[1]))
			if len(mod) != 1 || !strings.HasPrefix(mod[0], pkgDir+"/") || mod[0] == n.Rel {
				continue
			}
			names, bound := pyNames(string(m[2]))
			for i, name := range names {
				if name == "*" {
					continue
				}
				if exports[n.Rel] == nil {
					exports[n.Rel] = map[string][2]string{}
				}
				exports[n.Rel][bound[i]] = [2]string{mod[0], name}
			}
		}
	}
	return exports
}
