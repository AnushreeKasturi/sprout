# Agent evaluation

Does Sprout help a coding agent answer questions about how code fits
together? This runs Claude Code headless on questions whose answers come from
each language's own tooling (the ground truth in
[`tools/accuracy`](../accuracy/README.md), not from Sprout), with and without
Sprout's MCP server.

```sh
python3 tools/agent-eval/tasks.py        # questions and answers -> tasks.json (kept fixed once written)
python3 tools/agent-eval/run.py --sprout ./sprout [--runs 3]   # -> results.jsonl
python3 tools/agent-eval/summarize.py    # tables
```

Needs Go, Node, Python 3.11+ and a signed-in Claude Code (`claude`). Runs use
that account; a full pass of 14 questions in three arms costs about $2.

## The questions

Fourteen, on a Python app (FastAPI template backend), a TypeScript library
(ky) and a Rust workspace (ripgrep), each answered with a list of files:

- **importers**: which files directly depend on `FILE`?
- **tests**: which test files import `FILE`, directly or through other files?

Test files are what each language names as tests (`test_*.py`, `*.test.ts`,
`_test.go`, Rust's `tests/*.rs`); helpers in test folders don't count.

## The arms

The prompt is the same in each and never mentions Sprout. The agent only has
read tools.

| Arm | Tools |
|---|---|
| baseline | Read, Grep, Glob |
| sprout | the same, plus Sprout's MCP server |
| sprout-hint | the same, plus one line in the system prompt saying Sprout is there and what for, as a project's CLAUDE.md would |

Each run records the files listed, recall and precision against the answer,
which tools were called, tokens, turns, time and cost.

## Results

Sprout 0.3.0, Claude Sonnet, one run per question and arm (`results.jsonl`):

| Arm | Recall | Precision | All files right | Used Sprout | Time (median) | Cost |
|---|---:|---:|---:|---:|---:|---:|
| baseline | 100% | 94% | 12/14 | 0/14 | 12 s | $0.72 |
| sprout | 100% | 98% | 13/14 | 7/14 | 9 s | $0.58 |
| sprout-hint | 100% | 95% | 12/14 | 13/14 | 9 s | $0.62 |

On test questions, the unhinted agent used Sprout every time (6/6); on
importer questions, where one grep finds the answer, once (1/8).

What this does and doesn't show:

- **Agents use Sprout when it's the better tool, if the server says what it's
  for.** In the first pilot, before the server sent instructions (#42), the
  same unhinted setup never called a Sprout tool (0/14), even where grep
  missed tests reached through other files: on those questions grep alone
  found 83% of the answer, and Sprout 100%.
- **On these questions, a capable agent with grep gets there too.** Every arm
  found every file here. The differences are in precision, time (about a
  quarter faster with Sprout) and cost (about a fifth cheaper), from one run
  each, so they could be noise. Repeat with `--runs 3` or more before
  quoting them.
- **The pilot found two Sprout bugs that are now fixed**: agents copied
  `conftest.py` and test helpers from `impact` into lists of tests (#43), and
  never discovered the graph tools on their own (#42).

Fourteen small questions on three repositories is a pilot, not a benchmark.
Bigger repositories, questions where text search is genuinely hard (Go
packages imported by path, re-exports, aliases), and more runs per question
are the next steps.
