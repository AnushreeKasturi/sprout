"""Ground truth for Rust, from the language's module rules.

There's no lightweight compiler tool to ask, so this follows the rules the
compiler uses, without macros or cfg:

- Every crate root (src/lib.rs, src/main.rs, src/bin/*, tests/*, benches/*,
  examples/*, or paths set in Cargo.toml) starts a module tree. "mod x;"
  means x.rs or x/mod.rs next to lib.rs, main.rs or mod.rs, and inside a
  folder named after any other file; #[path] overrides that. Inline
  "mod x { ... }" blocks nest modules inside a file.
- "use" trees are expanded ({a, b::c}, self, super, crate, globs). A path
  means the file of the longest prefix that is a module. Paths starting
  with a workspace crate's name go to that crate's library.
- Qualified paths in code (crate::x::y, super::z, other_crate::w) count
  as uses too.

    python3 rs.py <repo> <graph.json>   (prints {rel: {specs, localSpecs, targets}})
"""

import json
import os
import re
import sys
import tomllib

repo, graph_file = (os.path.abspath(p) for p in sys.argv[1:3])
graph = json.load(open(graph_file))["files"]
in_scope = {f["rel"] for f in graph}
isfile = lambda rel: os.path.isfile(os.path.join(repo, rel))
norm = lambda p: os.path.normpath(p).replace(os.sep, "/")

TOKEN = re.compile(
    r"""(?P<ws>\s+)|(?P<line>//[^\n]*)|(?P<block>/\*)"""
    r"""|(?P<raw>b?r(?P<h>\#*)"(?:.|\n)*?"(?P=h))"""
    r"""|(?P<str>b?"(?:\\.|[^"\\])*")"""
    r"""|(?P<char>b?'(?:\\(?:u\{[0-9a-fA-F]+\}|x[0-9a-fA-F]{2}|.)|[^'\\])')"""
    r"""|(?P<life>'[A-Za-z_]\w*)"""
    r"""|(?P<ident>r\#[A-Za-z_]\w*|[A-Za-z_]\w*)|(?P<path>::)|(?P<num>\d[\w.]*)|(?P<p>.)""",
    re.S,
)


def tokens(src):
    out, i = [], 0
    while i < len(src):
        m = TOKEN.match(src, i)
        kind = m.lastgroup
        if kind == "block":  # nested block comments
            depth, j = 1, m.end()
            while depth and j < len(src):
                if src.startswith("/*", j):
                    depth, j = depth + 1, j + 2
                elif src.startswith("*/", j):
                    depth, j = depth - 1, j + 2
                else:
                    j += 1
            i = j
            continue
        if kind in ("str", "raw"):
            out.append(("str", m.group()))
        elif kind == "ident":
            out.append(("id", m.group().removeprefix("r#")))
        elif kind in ("path", "p"):
            out.append(("p", m.group()))
        i = m.end()
    return out


# ---------- crates ----------

crates = []  # (crate name, root file, is_lib)
for d, dirs, names in os.walk(repo):
    dirs[:] = [x for x in dirs if not x.startswith(".") and x not in ("target", "node_modules")]
    if "Cargo.toml" not in names:
        continue
    try:
        cargo = tomllib.load(open(os.path.join(d, "Cargo.toml"), "rb"))
    except (tomllib.TOMLDecodeError, OSError):
        continue
    if "package" not in cargo:
        continue
    base = norm(os.path.relpath(d, repo))
    base = "" if base == "." else base
    j = lambda *p: norm(os.path.join(base, *p)) if base else norm(os.path.join(*p))
    name = cargo["package"].get("name", os.path.basename(d)).replace("-", "_")
    lib = cargo.get("lib", {})
    lib_path = j(lib["path"]) if "path" in lib else j("src/lib.rs")
    if isfile(lib_path):
        crates.append((lib.get("name", name).replace("-", "_"), lib_path, True))
    roots = [j("src/main.rs")] + [j(b["path"]) for b in cargo.get("bin", []) if "path" in b]
    for sub in ("src/bin", "tests", "benches", "examples"):
        p = os.path.join(repo, j(sub))
        if os.path.isdir(p):
            for e in sorted(os.listdir(p)):
                if e.endswith(".rs"):
                    roots.append(j(sub, e))
                elif isfile(j(sub, e, "main.rs")):
                    roots.append(j(sub, e, "main.rs"))
    crates += [(name, r, False) for r in roots if isfile(r)]
libs = {name: root for name, root, is_lib in crates if is_lib}

# ---------- module trees ----------

module_file = {}  # (crate root, module path tuple) -> file
file_ctx = {}  # file -> (crate root, crate name, module path of the file)
parsed = {}  # file -> tokens
decls = {}  # file -> [(module path inside file, child name, child file)]


def load(rel):
    if rel not in parsed:
        try:
            parsed[rel] = tokens(open(os.path.join(repo, rel), encoding="utf-8", errors="replace").read())
        except OSError:
            parsed[rel] = []
    return parsed[rel]


def build(crate_root, crate_name, rel, mpath, is_root):
    if rel in file_ctx:
        return
    file_ctx[rel] = (crate_root, crate_name, mpath)
    module_file[(crate_root, mpath)] = rel
    stem = os.path.splitext(os.path.basename(rel))[0]
    child_dir = os.path.dirname(rel) if is_root or stem == "mod" else norm(os.path.join(os.path.dirname(rel), stem))
    toks, stack, depth, attr_path = load(rel), [], 0, None
    decls[rel] = []
    for i, (k, v) in enumerate(toks):
        if (k, v) == ("p", "{"):
            depth += 1
        elif (k, v) == ("p", "}"):
            depth -= 1
            if stack and stack[-1][1] > depth:
                stack.pop()
        elif (k, v) == ("id", "path") and i >= 2 and toks[i - 1] == ("p", "[") and toks[i - 2] == ("p", "#") and i + 2 < len(toks) and toks[i + 2][0] == "str":
            attr_path = toks[i + 2][1].strip('"')
        elif (k, v) == ("id", "mod") and i + 2 < len(toks) and toks[i + 1][0] == "id":
            name, nxt = toks[i + 1][1], toks[i + 2]
            inner = tuple(s for s, _ in stack)
            if nxt == ("p", "{"):
                stack.append((name, depth + 1))
                module_file[(crate_root, mpath + inner + (name,))] = rel
            elif nxt == ("p", ";"):
                d = norm(os.path.join(child_dir, *inner)) if inner else child_dir
                if attr_path:
                    cands = [norm(os.path.join(os.path.dirname(rel) if not inner else d, attr_path))]
                else:
                    cands = [norm(os.path.join(d, name + ".rs")), norm(os.path.join(d, name, "mod.rs"))]
                child = next((c for c in cands if isfile(c)), None)
                decls[rel].append((inner, name, child))
                if child:
                    build(crate_root, crate_name, child, mpath + inner + (name,), False)
            attr_path = None


for name, root, _ in sorted(crates, key=lambda c: not c[2]):  # libraries first
    build(root, name, root, (), True)

# ---------- uses ----------


def use_trees(toks, start):
    """Expand the use tree starting at toks[start]; returns (paths, index after ';')."""
    paths, i = [], start

    def tree(prefix):
        nonlocal i
        segs = list(prefix)
        if toks[i] == ("p", "::"):
            i += 1
            segs = ["::"]
        while i < len(toks):
            k, v = toks[i]
            if (k, v) == ("p", "{"):
                i += 1
                while toks[i] != ("p", "}"):
                    tree(segs)
                    if toks[i] == ("p", ","):
                        i += 1
                i += 1
                return
            if (k, v) == ("p", "*"):
                paths.append(segs + ["*"])
                i += 1
                return
            if k == "id":
                i += 1
                if v == "self" and prefix and len(segs) == len(prefix):
                    if toks[i] == ("id", "as"):
                        i += 2
                    paths.append(list(segs))  # a::{self} means a
                    return
                segs.append(v)
                if toks[i] == ("p", "::"):
                    i += 1
                    continue
                if toks[i] == ("id", "as"):
                    i += 2
                paths.append(segs)
                return
            return

    try:
        tree([])
        while toks[i] != ("p", ";"):
            i += 1
    except IndexError:
        pass
    return paths, i + 1


def resolve(rel, inner, segs):
    """The file a path refers to, from inside module `inner` of file rel."""
    crate_root, crate_name, mpath = file_ctx[rel]
    here = mpath + inner
    if not segs or segs[0] == "::":
        return None
    first, rest = segs[0], segs[1:]
    if first == "crate":
        root, base = crate_root, ()
    elif first in ("self", "super"):
        root, base = crate_root, here
        while segs and segs[0] in ("self", "super"):
            if segs[0] == "super":
                base = base[:-1]
            segs = segs[1:]
        rest = segs
    elif (crate_root, here + (first,)) in module_file:
        root, base, rest = crate_root, here, segs
    elif first in libs and libs[first] != crate_root:  # another crate, or this package's library from a test or binary
        root, base = libs[first], ()
    else:
        return None  # external crate or a name in scope
    rest = [s for s in rest if s != "*"]
    for k in range(len(rest), -1, -1):
        f = module_file.get((root, base + tuple(rest[:k])))
        if f:
            return f
    return None


out = {}
for rel, (crate_root, crate_name, mpath) in file_ctx.items():
    if rel not in in_scope:
        continue
    toks, specs, local, targets = parsed[rel], [], [], set()
    for inner, name, child in decls[rel]:
        specs.append(name)
        if child:
            local.append(name)
            targets.add(child)
    stack, depth, i = [], 0, 0
    while i < len(toks):
        k, v = toks[i]
        if (k, v) == ("p", "{"):
            depth += 1
        elif (k, v) == ("p", "}"):
            depth -= 1
            if stack and stack[-1][1] > depth:
                stack.pop()
        elif (k, v) == ("id", "mod") and i + 2 < len(toks) and toks[i + 1][0] == "id" and toks[i + 2] == ("p", "{"):
            stack.append((toks[i + 1][1], depth + 1))
        elif (k, v) == ("id", "use") and (i == 0 or toks[i - 1] != ("p", "::")):
            paths, i = use_trees(toks, i + 1)
            for p in paths:
                spec = "::".join(s for s in p if s != "::")
                specs.append(spec)
                t = resolve(rel, tuple(s for s, _ in stack), p)
                if t:
                    local.append(spec)
                    if t != rel:
                        targets.add(t)
            continue
        elif k == "id" and i + 1 < len(toks) and toks[i + 1] == ("p", "::") and (i == 0 or toks[i - 1] != ("p", "::")):
            # a qualified path in code: crate::a::f(), super::x, other_crate::T
            segs, j = [v], i + 1
            while j + 1 < len(toks) and toks[j] == ("p", "::") and toks[j + 1][0] == "id":
                segs.append(toks[j + 1][1])
                j += 2
            if segs[0] in ("crate", "super", "self") or segs[0] in libs:
                t = resolve(rel, tuple(s for s, _ in stack), segs)
                if t and t != rel:
                    targets.add(t)
            i = j
            continue
        i += 1
    out[rel] = {"specs": specs, "localSpecs": local, "targets": sorted(targets & in_scope)}

json.dump(out, sys.stdout)
