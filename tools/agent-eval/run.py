"""Does Sprout help a coding agent? Runs each task in tasks.json with Claude
Code in headless mode, in three arms:

- baseline: only its read tools (Read, Grep, Glob);
- sprout: the same tools plus Sprout's MCP server, never mentioned;
- sprout-hint: the same, plus one line saying Sprout is there and what for,
  as a project's CLAUDE.md would.

The task prompt is the same in every arm.

    python3 tools/agent-eval/run.py --sprout /path/to/sprout [--runs N] [--model sonnet]
        [--kind tests] [--arms baseline sprout] [--only ID ...] [--label NOTE]

Each run is scored on the files it lists against the ground truth (recall,
precision), with the tool calls, tokens, turns, time and cost Claude Code
reports. Results append to results.jsonl; summarize.py makes the tables.
The agent only gets read tools; nothing in the repositories is changed.
Runs use your Claude Code account.
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ACC = os.path.join(os.path.dirname(HERE), "accuracy")
READ_TOOLS = ["Read", "Grep", "Glob"]
ARMS = ["baseline", "sprout", "sprout-hint"]
# What a project's CLAUDE.md might say once Sprout is set up.
HINT = ("This project has the Sprout MCP server. For how files depend on each other (what imports a file, "
        "what a change affects, which tests cover it), use its tools.")

QUESTIONS = {
    "importers": "List every file in this repository that directly depends on `{file}`: it imports that file, "
    "or uses something declared in it.",
    "tests": "List every test file in this repository that imports `{file}`, directly or through a chain of "
    "imports of other files.",
}
FORMAT = (
    "\n\nAnswer with paths relative to the repository root, one per line, inside a single ```text block, and "
    "nothing else in that block. Don't change any files."
)


def prompt(task):
    return QUESTIONS[task["kind"]].format(file=task["file"]) + FORMAT


def parse_answer(text):
    blocks = re.findall(r"```(?:text)?\n(.*?)```", text or "", re.S)
    if not blocks:
        return []
    out = []
    for line in blocks[-1].splitlines():
        line = line.strip().strip("`").strip().removeprefix("./")
        if line and not line.startswith("#"):
            out.append(line)
    return sorted(set(out))


def score(got, want):
    got, want = set(got), set(want)
    hit = len(got & want)
    return {
        "recall": hit / len(want) if want else 1.0,
        "precision": hit / len(got) if got else 0.0,
        "exact": got == want,
    }


def run_one(task, arm, sprout, model, max_turns):
    tools = list(READ_TOOLS)
    args = [shutil.which("claude") or sys.exit("claude (Claude Code) not found on PATH"), "-p", prompt(task), "--output-format", "stream-json", "--verbose", "--model", model,
            "--max-turns", str(max_turns), "--no-session-persistence", "--setting-sources", "", "--strict-mcp-config",
            "--permission-mode", "default"]
    cfg = None
    if arm.startswith("sprout"):
        cfg = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False)
        json.dump({"mcpServers": {"sprout": {"command": sprout, "args": ["mcp", task["dir"]]}}}, cfg)
        cfg.close()
        args += ["--mcp-config", cfg.name]
        tools.append("mcp__sprout")
    if arm == "sprout-hint":
        args += ["--append-system-prompt", HINT]
    args += ["--allowedTools", *tools]
    start = time.time()
    p = subprocess.run(args, cwd=task["dir"], capture_output=True, text=True, timeout=900)
    wall = time.time() - start
    if cfg:
        os.unlink(cfg.name)

    # The stream has the session's setup (which MCP servers connected), every
    # tool call, and the final result with usage and cost.
    r, servers, calls = None, {}, {}
    for line in p.stdout.splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        if ev.get("type") == "system" and ev.get("subtype") == "init":
            servers = {s["name"]: s.get("status") for s in ev.get("mcp_servers", [])}
        elif ev.get("type") == "assistant":
            for c in ev.get("message", {}).get("content", []):
                if c.get("type") == "tool_use":
                    calls[c["name"]] = calls.get(c["name"], 0) + 1
        elif ev.get("type") == "result":
            r = ev
    if r is None:
        return {"error": (p.stderr or p.stdout)[-500:], "wall": round(wall, 1)}
    if r.get("is_error"):
        return {"error": str(r.get("result"))[:500], "wall": round(wall, 1)}
    if arm.startswith("sprout") and servers.get("sprout") != "connected":
        return {"error": f"sprout MCP server not connected: {servers}", "wall": round(wall, 1)}
    usage = r.get("usage") or {}
    got = parse_answer(r.get("result", ""))
    return {
        "answer": got,
        **score(got, task["answer"]),
        "turns": r.get("num_turns"),
        "cost": r.get("total_cost_usd"),
        "input_tokens": usage.get("input_tokens", 0) + usage.get("cache_read_input_tokens", 0) + usage.get("cache_creation_input_tokens", 0),
        "output_tokens": usage.get("output_tokens", 0),
        "wall": round(wall, 1),
        "tool_calls": calls,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--sprout", required=True, help="path to the sprout binary to serve over MCP")
    ap.add_argument("--runs", type=int, default=1)
    ap.add_argument("--model", default="sonnet")
    ap.add_argument("--max-turns", type=int, default=40)
    ap.add_argument("--only", nargs="*", help="task ids to run")
    ap.add_argument("--kind", choices=["importers", "tests"], help="only tasks of this kind")
    ap.add_argument("--arms", nargs="*", choices=ARMS, default=ARMS)
    ap.add_argument("--label", default="", help="a note stored with each result, e.g. the sprout version")
    ap.add_argument("--out", default=os.path.join(HERE, "results.jsonl"))
    a = ap.parse_args()
    tasks = json.load(open(os.path.join(HERE, "tasks.json")))
    if a.only:
        tasks = [t for t in tasks if t["id"] in a.only]
    if a.kind:
        tasks = [t for t in tasks if t["kind"] == a.kind]
    sprout = os.path.abspath(a.sprout)
    # Repositories are fetched at their pinned commits, as tools/accuracy does.
    sys.path.insert(0, ACC)
    import run as accuracy  # tools/accuracy/run.py

    pinned = {r["name"]: r for r in json.load(open(os.path.join(ACC, "repos.json")))}
    for t in tasks:
        t["dir"] = accuracy.fetch(pinned[t["repo"]])
    with open(a.out, "a") as out:
        for i in range(a.runs):
            for n, t in enumerate(tasks):
                # Rotate which arm goes first, so none always runs on a warmer cache.
                k = (i + n) % len(a.arms)
                for arm in a.arms[k:] + a.arms[:k]:
                    res = run_one(t, arm, sprout, a.model, a.max_turns)
                    rec = {"task": t["id"], "kind": t["kind"], "arm": arm, "run": i, "model": a.model, "label": a.label, **res}
                    out.write(json.dumps(rec) + "\n")
                    out.flush()
                    print(f"{t['id']} [{arm}] recall {res.get('recall', 0):.2f} precision {res.get('precision', 0):.2f} "
                          f"turns {res.get('turns')} cost ${res.get('cost') or 0:.3f} {res.get('error', '')[:80]}", flush=True)


if __name__ == "__main__":
    main()
