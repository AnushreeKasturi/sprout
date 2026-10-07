# Contributing to Sprout

Thanks for helping. Sprout is a small Go codebase with no dependencies, and
first-time contributors are welcome: you don't need to know it, or Go, well
to start.

## Your first contribution

1. **Pick an issue.** [`good first issue`](https://github.com/Sprout-DevLabs/sprout/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
   ones are small and say which files to look at and how to test.
   [`help wanted`](https://github.com/Sprout-DevLabs/sprout/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22)
   ones are bigger.
2. **Say you're on it.** Comment on the issue so no one else starts the same
   work. If you go quiet for two weeks, someone else may pick it up; that's
   fine, you can always come back.
3. **Ask early.** Questions on the issue are welcome at any point, including
   "where do I start?". General questions go in
   [Discussions](https://github.com/Sprout-DevLabs/sprout/discussions).
4. **Open a draft pull request** as soon as something works, even partly. It's
   easier to help with code we can see.

Want to change something without an issue? For a small fix, just open the pull
request. For anything bigger (a new flag, a new command, a new dependency),
open an issue first so we can agree on the shape before you spend time on it.

## Setup

```bash
git clone https://github.com/Sprout-DevLabs/sprout && cd sprout
go test ./...
go run . --help
go run . deps main.go    # try a command on Sprout itself
```

You need Go (the version is in `go.mod`) and git. There are no other
dependencies for the CLI. `tools/accuracy` and `tools/agent-eval` also use
Node and Python; you only need those if you work on them.

## Ground rules

- **Standard library only.** Open an issue before adding a dependency.
- **One change per pull request,** with a test that fails without it. Tests
  that need git create real throwaway repositories (`setupGitRepo` in
  `tree_test.go`, `impactRepo` in `impact_test.go`).
- **The CLI and MCP share code.** MCP tools call the same functions as the
  commands (`run`, `runQuery`, `runImpact`, `runContext`); don't fork logic
  for one of them.
- **Nothing hidden silently.** If a feature filters output, the output says so.
- **Untrusted input stays contained.** Anything reachable from MCP arguments
  must stay inside the root and must never reach git as an option.
- **Claims are measured.** Performance changes come with before/after numbers
  (`bench_test.go`); changes to import scanning or resolution come with
  before/after accuracy (`tools/accuracy`).
- `gofmt` and `go vet` clean. CI runs the tests with `-race` on Linux, macOS
  and Windows.

## Where things are

| File | What |
|---|---|
| `main.go` | Flags, usage, dispatch to modes and subcommands |
| `config.go` | `~/.config/sprout/config` and `.sproutrc` |
| `tree.go` | Walk, `Node`, tree printer |
| `filter.go` | gitignore-style matcher, `.sproutignore`, `.gitignore` via git |
| `sort.go` | `--sort`, `--size` units, `--max-files`, `--changed-within` |
| `git.go`, `churn.go` | `--git`, `--diff`, `--churn`, and the changed files for `impact` |
| `graph.go` | The dependency graph: file IDs, edges both ways, tests |
| `codegraph.go` | Builds the graph: reads source files, Go analysis, the resolver |
| `jsresolve.go`, `pyresolve.go`, `rsresolve.go` | Import scanning and resolution for TypeScript/JavaScript, Python and Rust |
| `query.go` | `sprout deps` and `sprout dependents` |
| `impact.go` | `sprout impact` |
| `context.go` | `sprout context` |
| `ai.go`, `entry.go` | `--ai` map, `--entry` reading order |
| `remote.go` | Cloning `github.com/owner/repo` and other URLs |
| `mcp.go` | `sprout mcp` server |
| `completion.go` | `--completion` and `--man`, generated from the flags |
| `output_json.go`, `stats.go`, `project.go`, `color.go` | `--json`, `--stats`, stack detection, terminal output |
| `tools/accuracy` | Scores the graph against each language's own tooling |
| `tools/agent-eval` | Measures whether Sprout helps a coding agent |

The website and user docs are in
[Sprout-DevLabs/sprout-web](https://github.com/Sprout-DevLabs/sprout-web).

## Before you open a pull request

```bash
gofmt -l .        # prints nothing
go vet ./...
go test -race ./...
```

- Say what changed and why in the description, and link the issue
  (`Fixes #123`).
- If output changed, paste a before/after example.
- Update `--help` (`main.go`), the man page (`completion.go`) and the README
  when you add or change a flag or command.
- Keep commits focused; we squash or merge as they are, so write messages you'd
  want to read in `git log`.

A maintainer reviews within a few days. Reviews are about the code, not you:
expect questions and suggestions, and feel free to push back.

## Performance

`bench_test.go` runs every mode against a real repository:

```bash
git clone --depth 1 https://github.com/kubernetes/kubernetes /tmp/k8s
SPROUT_BENCH_DIR=/tmp/k8s go test -run '^$' -bench .
```

## Graph accuracy

`tools/accuracy` scores the dependency graph against each language's own
tooling (the TypeScript compiler, Python's `ast`, `go/parser`, Rust's module
rules) on pinned repositories:

```bash
python3 tools/accuracy/run.py
```

[tools/accuracy/README.md](tools/accuracy/README.md) explains the numbers and
has the current results.

## Conduct and security

Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).
Please report security problems privately, as described in
[SECURITY.md](SECURITY.md), not in a public issue.

## Releasing (maintainers)

Tag and push; the Release workflow runs GoReleaser and updates the Homebrew
tap and the Scoop bucket:

```bash
git tag v0.4.0 && git push origin v0.4.0
```
