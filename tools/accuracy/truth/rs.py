"""Ground truth for Rust, from the language's module rules.

There's no lightweight compiler tool to ask, so this follows the rules the
compiler uses, without macros or cfg:

- Every crate root (src/lib.rs, src/main.rs, paths set in Cargo.toml, and,
  unless autobins/autotests/... = false, src/bin/*, tests/*, benches/*,
  examples/*) starts a module tree. "mod x;"
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
    result, pos = [], 0
    while pos < len(src):
        m = TOKEN.match(src, pos)
        kind = m.lastgroup
        if kind == "block":  # nested block comments
            nesting_depth, idx = 1, m.end()
            while nesting_depth and idx < len(src):
                if src.startswith("/*", idx):
                    nesting_depth, idx = nesting_depth + 1, idx + 2
                elif src.startswith("*/", idx):
                    nesting_depth, idx = nesting_depth - 1, idx + 2
                else:
                    idx += 1
            pos = idx
            continue
        if kind in ("str", "raw"):
            result.append(("str", m.group()))
        elif kind == "ident":
            result.append(("id", m.group().removeprefix("r#")))
        elif kind in ("path", "p"):
            result.append(("p", m.group()))
        pos = m.end()
    return result


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
    roots = [j("src/main.rs")] + [j(t["path"]) for kind in ("bin", "test", "bench", "example") for t in cargo.get(kind, []) if "path" in t]
    pkg = cargo["package"]
    for sub, auto in (("src/bin", "autobins"), ("tests", "autotests"), ("benches", "autobenches"), ("examples", "autoexamples")):
        p = os.path.join(repo, j(sub))
        if os.path.isdir(p) and pkg.get(auto, True):
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


def load(rel_path):
    if rel_path not in parsed:
        try:
            parsed[rel_path] = tokens(open(os.path.join(repo, rel_path), encoding="utf-8", errors="replace").read())
        except OSError:
            parsed[rel_path] = []
    return parsed[rel_path]


def build(root, crate, rel_path, module_path, is_root):
    if rel_path in file_ctx:
        return
    file_ctx[rel_path] = (root, crate, module_path)
    module_file[(root, module_path)] = rel_path
    stem = os.path.splitext(os.path.basename(rel_path))[0]
    child_dir = os.path.dirname(rel_path) if is_root or stem == "mod" else norm(
        os.path.join(os.path.dirname(rel_path), stem)
    )
    tokens, stack_frames, brace_depth, attribute_path = load(rel_path), [], 0, None
    decls[rel_path] = []
    for idx, (token_type, token_value) in enumerate(tokens):
        if (token_type, token_value) == ("p", "{"):
            brace_depth += 1
        elif (token_type, token_value) == ("p", "}"):
            brace_depth -= 1
            if stack_frames and stack_frames[-1][1] > brace_depth:
                stack_frames.pop()
        elif (
            token_type == "id"
            and token_value == "path"
            and idx >= 2
            and tokens[idx - 1] == ("p", "[")
            and tokens[idx - 2] == ("p", "#")
            and idx + 2 < len(tokens)
            and tokens[idx + 2][0] == "str"
        ):
            attribute_path = tokens[idx + 2][1].strip('"')
        elif token_type == "id" and token_value == "mod" and idx + 2 < len(tokens) and tokens[idx + 1][0] == "id":
            mod_name = tokens[idx + 1][1]
            next_token = tokens[idx + 2]
            parent = tuple(name for name, _ in stack_frames)
            if next_token == ("p", "{"):
                stack_frames.append((mod_name, brace_depth + 1))
                module_file[(root, module_path + parent + (mod_name,))] = rel_path
            elif next_token == ("p", ";"):
                dir_path = (
                    norm(os.path.join(child_dir, *parent))
                    if parent
                    else child_dir
                )
                if attribute_path:
                    candidates = [
                        norm(os.path.join(os.path.dirname(rel_path) if not parent else dir_path, attribute_path))
                    ]
                else:
                    candidates = [
                        norm(os.path.join(dir_path, mod_name + ".rs")),
                        norm(os.path.join(dir_path, mod_name, "mod.rs")),
                    ]
                child_file = next((c for c in candidates if isfile(c)), None)
                decls[rel_path].append((parent, mod_name, child_file))
                if child_file:
                    build(root, crate, child_file, module_path + parent + (mod_name,), False)
            attribute_path = None


for name, root, _ in sorted(crates, key=lambda c: not c[2]):  # libraries first
    build(root, name, root, (), True)

# ---------- uses ----------


def use_trees(tokens, start):
    """Expand the use tree starting at tokens[start]; returns (result_paths, index after ';')."""
    result_paths, idx = [], start

    def tree(prefix):
        nonlocal idx
        segments = list(prefix)
        if tokens[idx] == ("p", "::"):
            idx += 1
            segments = ["::"]
        while idx < len(tokens):
            tok_type, tok_val = tokens[idx]
            if (tok_type, tok_val) == ("p", "{"):
                idx += 1
                while tokens[idx] != ("p", "}"):
                    tree(segments)
                    if tokens[idx] == ("p", ","):
                        idx += 1
                idx += 1
                return
            if (tok_type, tok_val) == ("p", "*"):
                result_paths.append(segments + ["*"])
                idx += 1
                return
            if tok_type == "id":
                idx += 1
                if tok_val == "self" and prefix and len(segments) == len(prefix):
                    if tokens[idx] == ("id", "as"):
                        idx += 2
                    result_paths.append(list(segments))  # a::{self} means a
                    return
                segments.append(tok_val)
                if tokens[idx] == ("p", "::"):
                    idx += 1
                    continue
                if tokens[idx] == ("id", "as"):
                    idx += 2
                result_paths.append(segments)
                return
            return

    try:
        tree([])
        while tokens[idx] != ("p", ";"):
            idx += 1
    except IndexError:
        pass
    return result_paths, idx + 1


def resolve(rel_path, inner_mod, segments):
    """The file a path refers to, from inside module `inner` of file rel."""
    global_crate_root, global_crate_name, module_path = file_ctx[rel_path]
    here = module_path + inner_mod
    if not segments or segments[0] == "::":
        return None
    first, rest_segments = segments[0], segments[1:]
    if first == "crate":
        root_dir, base_path = global_crate_root, ()
    elif first in ("self", "super"):
        root_dir, base_path = global_crate_root, here
        while segments and segments[0] in ("self", "super"):
            if segments[0] == "super":
                base_path = base_path[:-1]
            segments = segments[1:]
        rest_segments = segments
    elif (global_crate_root, here + (first,)) in module_file:
        root_dir, base_path, rest_segments = global_crate_root, here, segments
    elif first in libs and libs[first] != global_crate_root:  # another crate, or this package's library from a test or binary
        root_dir, base_path = libs[first], ()
    else:
        return None  # external crate or a name in scope
    rest_segments = [s for s in rest_segments if s != "*"]
    for idx in range(len(rest_segments), -1, -1):
        f = module_file.get((root_dir, base_path + tuple(rest_segments[:idx])))
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
