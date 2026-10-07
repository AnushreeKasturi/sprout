"""Ground truth for Python.

Imports come from Python's own parser (ast), so they're exact. Python has
no static resolver, so which file an import means follows a written rule
instead of a compiler:

- Source roots are the repository root, every directory holding a
  pyproject.toml, setup.py or setup.cfg, and the src/ directory of each.
- "import a.b" means a/b.py or a/b/__init__.py under the first root that
  has it. Relative imports start from the importing file's package.
- "from a import b" means a/b.py (or a/b/__init__.py) when b is a
  submodule, and a itself otherwise.
- Importing a module also runs its parent packages' __init__.py, so an edge
  to one of those is accepted as correct but not required.

    python3 py.py <repo> <graph.json>   (prints {rel: {specs, altSpecs, localSpecs, targets, altTargets}})
"""

import ast
import json
import os
import sys

repo, graph_file = (os.path.abspath(p) for p in sys.argv[1:3])
graph = json.load(open(graph_file))["files"]
in_scope = {f["rel"] for f in graph}
exists = lambda rel: rel in in_scope or os.path.isfile(os.path.join(repo, rel))

roots = {""}
for d, dirs, names in os.walk(repo):
    dirs[:] = [x for x in dirs if not x.startswith(".") and x != "node_modules"]
    if {"pyproject.toml", "setup.py", "setup.cfg"} & set(names):
        r = os.path.relpath(d, repo).replace(os.sep, "/")
        roots.add("" if r == "." else r)
roots |= {(r + "/src").lstrip("/") for r in list(roots)}
roots = sorted(roots, key=lambda r: (r != "", len(r), r))  # repo root first, then nearest


def module_file(base_path, dotted):
    """The file for module `dotted` under directory `base_path`, or None."""
    p = "/".join(x for x in [base_path, dotted.replace(".", "/")] if x)
    for cand in (p + ".py", p + "/__init__.py"):
        if exists(cand):
            return cand
    return None


def parents(base_dir, dotted):
    """__init__.py of each package on the way to `dotted`."""
    parts, results = dotted.split(".") if dotted else [], []
    for i in range(1, len(parts)):
        file_path = "/".join(x for x in [base_dir, *parts[:i], "__init__.py"] if x)
        if exists(file_path):
            results.append(file_path)
    return results


def absolute(dotted):
    for current_root in roots:
        file_path = module_file(current_root, dotted)
        if file_path:
            return current_root, file_path
    return None, None


out = {}
for f in graph:
    if f["lang"] != "py":
        continue
    try:
        tree = ast.parse(open(os.path.join(repo, f["rel"]), "rb").read())
    except (SyntaxError, ValueError):
        continue  # Python 2 or broken source: no truth to compare against
    specs, alt_specs, local, targets, alt_targets = [], [], [], set(), set()
    pkg = os.path.dirname(f["rel"])
    for n in ast.walk(tree):
        if isinstance(n, ast.Import):
            for a in n.names:
                specs.append(a.name)
                root, t = absolute(a.name)
                if t:
                    local.append(a.name)
                    targets.add(t)
                    alt_targets.update(parents(root, a.name))
        elif isinstance(n, ast.ImportFrom):
            prefix = "." * n.level
            specs.append(prefix + (n.module or ""))
            for a in n.names:
                alt_specs.append(prefix + (n.module + "." if n.module else "") + a.name)
            mod = n.module or ""
            names = [a.name for a in n.names if a.name != "*"]
            full = lambda name: (mod + "." if mod else "") + name
            if n.level:
                base = pkg
                for _ in range(n.level - 1):
                    base = os.path.dirname(base)
                if base:
                    alt_targets.add(base + "/__init__.py")  # the importing package runs first
            else:
                # The first root that has the module, or one of the names as a
                # submodule (namespace packages have no __init__.py).
                base = next((r for r in roots if module_file(r, mod) or any(module_file(r, full(x)) for x in names)), None)
                if base is None:
                    continue  # not in the repository
            init = "/".join(x for x in [base, "__init__.py"] if x)
            mod_file = module_file(base, mod) if mod else (init if exists(init) else None)
            subs = [s for s in (module_file(base, full(x)) for x in names) if s]
            if subs or mod_file:
                local.append(prefix + mod)
            targets.update(subs)
            if mod_file:
                if len(subs) < len(names) or n.names[0].name == "*":
                    targets.add(mod_file)  # some names are attributes of the module itself
                else:
                    alt_targets.add(mod_file)
            alt_targets.update(parents(base, mod))
    targets.discard(f["rel"])
    targets &= in_scope
    alt_targets = (alt_targets & in_scope) - targets - {f["rel"]}
    out[f["rel"]] = {"specs": specs, "altSpecs": alt_specs, "localSpecs": local, "targets": sorted(targets), "altTargets": sorted(alt_targets)}

json.dump(out, sys.stdout)
