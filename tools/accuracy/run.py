"""Score Sprout's dependency graph against each language's own tooling.

    python3 tools/accuracy/run.py              # every repository in repos.json
    python3 tools/accuracy/run.py ky click     # just these

Repositories are fetched at their pinned commit into tools/accuracy/.cache.
Each missed and wrong link is written to .cache/out/<repo>-<lang>.txt.
Needs Go, Node and Python 3.11+; see README.md for what each number means.
"""

import collections
import re
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))  # the sprout module
CACHE = os.environ.get("SPROUT_ACCURACY_CACHE", os.path.join(HERE, ".cache"))
LANGS = {"js": "TS/JS", "py": "Python", "go": "Go", "rs": "Rust"}


def sh(*cmd, cwd=None, env=None, out=None):
    return subprocess.run(cmd, cwd=cwd, env=env, check=True, stdout=out or subprocess.DEVNULL, stderr=subprocess.PIPE if out else None, text=True)


def fetch(repo):
    d = os.path.join(CACHE, "repos", repo["name"])
    head = os.path.join(d, ".git", "HEAD")
    if os.path.exists(head) and subprocess.run(["/usr/bin/git", "rev-parse", "HEAD"], cwd=d, capture_output=True, text=True).stdout.strip() == repo["commit"]:
        return d
    os.makedirs(d, exist_ok=True)
    if not os.path.exists(head):
        sh("/usr/bin/git", "init", "-q", cwd=d)
    sh("/usr/bin/git", "fetch", "-q", "--depth", "1", repo["url"], repo["commit"], cwd=d)
    sh("/usr/bin/git", "checkout", "-q", "--force", "FETCH_HEAD", cwd=d)
    return d


def truth(lang, repo_dir, graph_file):
    cmd = {
        "js": ["node", os.path.join(HERE, "truth", "ts.mjs")],
        "py": [sys.executable, os.path.join(HERE, "truth", "py.py")],
        "rs": [sys.executable, os.path.join(HERE, "truth", "rs.py")],
        "go": ["go", "run", "./tools/accuracy/truth/gotruth"],
    }[lang]
    return json.loads(sh(*cmd, repo_dir, graph_file, cwd=ROOT, out=subprocess.PIPE).stdout)


def spec_match(lang, sprout, want):
    if lang == "rs":
        # Inside an inline module Sprout writes super::x as the equivalent
        # self::x, relative to the file; compare paths without that prefix.
        bare = lambda p: re.sub(r"^((self|super)::)+", "", p.rstrip(":"))
        s, w = bare(sprout), bare(want)
        return w == s or w.startswith(s + "::")
    return sprout == want


def score(lang, files, t):
    """Totals for one language, plus the per-file mismatches."""
    n = collections.Counter()
    report = []
    for f in files:
        if f["rel"] not in t:
            continue  # no ground truth for this file (unparsable, or outside every Rust crate)
        tr, n["files"] = t[f["rel"]], n["files"] + 1
        if lang == "go":
            here = os.path.dirname(f["rel"])
            want = set(tr["dirs"]) - {here}
            got = {os.path.dirname(d) for d in f["deps"]} - {here}
            ok = want
        else:
            want = set(tr["targets"])
            got = set(f["deps"])
            ok = want | set(tr.get("altTargets", []))
            local, specs = list(tr["localSpecs"]), f["specs"]
            n["imports"] += len(local)
            unmatched = list(specs)
            for w in local:
                i = next((i for i, s in enumerate(unmatched) if spec_match(lang, s, w)), None)
                if i is not None:
                    n["found"] += 1
                    unmatched.pop(i)
            everything = tr["specs"] + tr.get("altSpecs", [])
            n["false"] += sum(1 for s in unmatched if not any(spec_match(lang, s, w) for w in everything))
        missed, wrong = sorted(want - got), sorted(got - ok)
        n["want"] += len(want)
        n["hit"] += len(want & got)
        n["got"] += len(got)
        n["right"] += len(got & ok)
        if missed or wrong:
            report.append(f"{f['rel']}\n" + "".join(f"  missed {m}\n" for m in missed) + "".join(f"  wrong  {w}\n" for w in wrong))
    return n, report


def pct(a, b):
    return f"{100 * a / b:.0f}% ({a}/{b})" if b else "–"


def main():
    repos = json.load(open(os.path.join(HERE, "repos.json")))
    if sys.argv[1:]:
        repos = [r for r in repos if r["name"] in sys.argv[1:]]
    if not os.path.exists(os.path.join(HERE, "node_modules", "typescript")):
        sh("npm", "ci", "--no-audit", "--no-fund", "--silent", cwd=HERE)
    os.makedirs(os.path.join(CACHE, "out"), exist_ok=True)

    rows = ["| Repository | Language | Files | In-repo imports found | False imports | Links found | Links correct |", "|---|---|---:|---:|---:|---:|---:|"]
    for repo in repos:
        print(f"{repo['name']}: fetching", file=sys.stderr)
        d = fetch(repo)
        graph_file = os.path.join(CACHE, "out", repo["name"] + ".graph.json")
        # Dump the graph before the TypeScript truth links workspace packages into node_modules.
        env = dict(os.environ, SPROUT_GRAPH_DIR=d, SPROUT_GRAPH_OUT=graph_file)
        print(f"{repo['name']}: building Sprout's graph", file=sys.stderr)
        sh("go", "test", "-run", "^TestDumpGraph$", "-count=1", ".", cwd=ROOT, env=env)
        files = json.load(open(graph_file))["files"]
        for lang in LANGS:
            mine = [f for f in files if f["lang"] == lang]
            if not mine:
                continue
            print(f"{repo['name']}: {LANGS[lang]} ground truth", file=sys.stderr)
            n, report = score(lang, mine, truth(lang, d, graph_file))
            if not n["want"] and not n["got"] and not n["imports"]:
                continue  # nothing links here: stray scripts in another language's repository
            with open(os.path.join(CACHE, "out", f"{repo['name']}-{lang}.txt"), "w") as out:
                out.writelines(report)
            found = "n/a (go/parser)" if lang == "go" else pct(n["found"], n["imports"])
            false = "–" if lang == "go" else str(n["false"])
            rows.append(f"| {repo['name']} | {LANGS[lang]} | {n['files']:,} | {found} | {false} | {pct(n['hit'], n['want'])} | {pct(n['right'], n['got'])} |")
    print("\n".join(rows))


if __name__ == "__main__":
    main()
