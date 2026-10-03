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
		for _, name := range pyNames(string(m[2])) {
			if name != "*" {
				specs = append(specs, mod+sep+name)
			}
		}
	}
	for _, m := range pyImport.FindAllSubmatch(src, -1) {
		specs = append(specs, pyNames(string(m[1]))...)
	}
	return specs
}

// pyNames splits "a, b as c" or "(\n  a,\n  b,\n)" into a, b.
func pyNames(list string) []string {
	list = strings.NewReplacer("(", " ", ")", " ", "\\", " ").Replace(list)
	var out []string
	for _, part := range strings.Split(list, ",") {
		line, _, _ := strings.Cut(part, "#")
		if f := strings.Fields(line); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
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
	return nil
}
