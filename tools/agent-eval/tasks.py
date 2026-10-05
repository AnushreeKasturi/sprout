"""Generate agent-eval tasks whose answers come from each language's own
tooling (tools/accuracy's ground truth), not from Sprout.

    python3 tools/agent-eval/tasks.py   -> tools/agent-eval/tasks.json

Two kinds of question, each answered with a list of files:

- importers: which files directly depend on FILE (import it, or use
  something it declares)?
- tests: which test files import FILE, directly or through other files?
  Test files are named as their language expects (test_*.py, *.test.ts,
  _test.go, Rust's tests/*.rs); helpers in test folders don't count.

Files are picked so answers have 3-10 entries for importers and 2-12 for
tests: big enough to need real search, small enough to list. Once
tasks.json exists, its questions are kept and only the answers are
recomputed, so results stay comparable when the ground truth is refined.
"""

import json
import os
import random
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ACC = os.path.join(os.path.dirname(HERE), "accuracy")
CACHE = os.path.join(ACC, ".cache")
sys.path.insert(0, ACC)
import run as accuracy  # noqa: E402  (tools/accuracy/run.py)

# repository, language, how many tasks of each kind
PLAN = [("fastapi-template", "py", 3), ("ky", "js", 2), ("ripgrep", "rs", 3)]
# Module roots and index files are left out: "what depends on lib.rs" mixes
# up the crate with the file.
ROOTS = {"lib.rs", "main.rs", "mod.rs", "__init__.py", "index.ts", "index.js"}


def is_test_file(rel):
    """A test by its language's naming convention: helpers and fixtures
    living in a tests folder (conftest.py, tests/utils/...) aren't tests."""
    base, parts = os.path.basename(rel), rel.split("/")
    if base.endswith(".py"):
        return base.startswith("test_") or base.endswith("_test.py")
    if base.endswith(".go"):
        return base.endswith("_test.go")
    if base.endswith(".rs"):
        return len(parts) >= 2 and parts[-2] == "tests"  # integration tests
    return ".test." in base or ".spec." in base or "__tests__" in parts or (len(parts) >= 2 and parts[-2] == "test")


def truth_graph(repo, lang):
    """The repository's folder, file -> files it depends on (from the
    language's own tooling), and its test files."""
    repos = {r["name"]: r for r in json.load(open(os.path.join(ACC, "repos.json")))}
    d = accuracy.fetch(repos[repo])
    graph_file = os.path.join(CACHE, "out", repo + ".graph.json")
    if not os.path.exists(graph_file):
        os.makedirs(os.path.dirname(graph_file), exist_ok=True)
        env = dict(os.environ, SPROUT_GRAPH_DIR=d, SPROUT_GRAPH_OUT=graph_file)
        subprocess.run(["go", "test", "-run", "^TestDumpGraph$", "-count=1", "."], cwd=accuracy.ROOT, env=env, check=True, capture_output=True)
    t = accuracy.truth(lang, d, graph_file)
    files = json.load(open(graph_file))["files"]  # only for the list of files in scope
    tests = {f["rel"] for f in files if is_test_file(f["rel"])}
    deps = {rel: set(v["targets"]) for rel, v in t.items()}
    return d, deps, tests


def main():
    rng = random.Random(7)
    path = os.path.join(HERE, "tasks.json")
    fixed = {t["id"] for t in json.load(open(path))} if os.path.exists(path) else None
    tasks = []
    for repo, lang, n in PLAN:
        d, deps, tests = truth_graph(repo, lang)
        users = {}
        for f, ds in deps.items():
            for x in ds:
                users.setdefault(x, set()).add(f)

        def reaching_tests(f):
            seen, todo = {f}, [f]
            while todo:
                for u in users.get(todo.pop(), ()):
                    if u not in seen:
                        seen.add(u)
                        todo.append(u)
            return sorted((seen - {f}) & tests)

        # Questions are about application code: not tests, not test helpers.
        code = sorted(f for f in deps if f not in tests and os.path.basename(f) not in ROOTS
                      and not {"test", "tests", "__tests__"} & set(f.split("/")))
        imp = [f for f in code if 3 <= len(users.get(f, ())) <= 10]
        tst = [f for f in code if 2 <= len(reaching_tests(f)) <= 12]
        if fixed is not None:
            imp = [f for f in code if f"{repo}:importers:{f}" in fixed]
            tst = [f for f in code if f"{repo}:tests:{f}" in fixed]
            n = len(code)
        for f in rng.sample(imp, min(n, len(imp))):
            tasks.append({"id": f"{repo}:importers:{f}", "repo": repo, "kind": "importers", "file": f,
                          "answer": sorted(users[f])})
        for f in rng.sample(tst, min(n, len(tst))):
            tasks.append({"id": f"{repo}:tests:{f}", "repo": repo, "kind": "tests", "file": f,
                          "answer": reaching_tests(f)})
    json.dump(tasks, open(path, "w"), indent=1)
    for t in tasks:
        print(f"{t['id']}: {len(t['answer'])} files")


if __name__ == "__main__":
    main()
