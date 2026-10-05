"""Summarize results.jsonl as Markdown tables: per arm, then per arm and
question. Runs are rescored against the current tasks.json, so refining the
ground truth rescores old answers.

    python3 tools/agent-eval/summarize.py [results.jsonl]
"""

import collections
import json
import os
import statistics
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ARMS = ["baseline", "sprout", "sprout-hint"]


def table(rows, key):
    groups = collections.defaultdict(list)
    for r in rows:
        groups[key(r)].append(r)
    out = ["| Arm | Runs | Recall | Precision | All files right | Used Sprout | Input tokens (median) | Turns (median) | Time (median) | Cost |",
           "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|"]
    for k in sorted(groups, key=lambda k: (ARMS.index(k[0]) if k[0] in ARMS else 9, k[1:])):
        g = [r for r in groups[k] if "error" not in r]
        failed = len(groups[k]) - len(g)
        if not g:
            continue
        used = sum(1 for r in g if any(n.startswith("mcp__sprout") for n in r.get("tool_calls", {})))
        out.append(
            f"| {' · '.join(k)} | {len(g)}{f' (+{failed} failed)' if failed else ''} "
            f"| {statistics.mean(r['recall'] for r in g):.0%} | {statistics.mean(r['precision'] for r in g):.0%} "
            f"| {sum(r['exact'] for r in g)}/{len(g)} | {used}/{len(g)} "
            f"| {statistics.median(r['input_tokens'] for r in g):,.0f} | {statistics.median(r['turns'] or 0 for r in g):.0f} "
            f"| {statistics.median(r['wall'] for r in g):.0f} s | ${sum(r['cost'] or 0 for r in g):.2f} |"
        )
    return "\n".join(out)


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else os.path.join(HERE, "results.jsonl")
    rows = [json.loads(l) for l in open(path) if l.strip()]
    truth = {t["id"]: set(t["answer"]) for t in json.load(open(os.path.join(HERE, "tasks.json")))}
    for r in rows:
        if "answer" in r and r["task"] in truth:
            got, want = set(r["answer"]), truth[r["task"]]
            r["recall"] = len(got & want) / len(want) if want else 1.0
            r["precision"] = len(got & want) / len(got) if got else 0.0
            r["exact"] = got == want
    print("## By arm\n")
    print(table(rows, lambda r: (r["arm"],)))
    print("\n## By arm and question\n")
    print(table(rows, lambda r: (r["arm"], r["kind"])))


if __name__ == "__main__":
    main()
