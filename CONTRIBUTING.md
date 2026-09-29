# Contributing to Sprout

Thanks for helping. A few things keep Sprout small and pleasant to work on.

## Setup

```bash
git clone https://github.com/Sprout-DevLabs/sprout && cd sprout
go test ./...
go run . --help
```

You need Go (see `go.mod`) and git. There are no other dependencies, and
we'd like to keep it that way.

## Ground rules

- **Standard library only.** Open an issue before adding a dependency.
- **One change per PR,** with a test that fails without it. Tests that need
  git create real throwaway repos (`setupGitRepo` in `tree_test.go`).
- **Every mode goes through `run()`.** CLI flags, `--json` and the MCP tools
  share one code path; don't fork logic for one of them.
- **Nothing hidden silently.** If a feature filters output, the summary must
  say so.
- **Untrusted input stays contained.** Anything reachable from MCP arguments
  must stay inside the root and must never reach git as an option.
- `gofmt` and `go vet` clean; CI runs tests with `-race` on Linux, macOS and
  Windows.

## Layout

| File | What |
|---|---|
| `main.go` | Flags, usage, mode dispatch |
| `config.go` | `~/.config/sprout/config` and `.sproutrc` |
| `tree.go` | Walk, `Node`, tree printer |
| `filter.go` | gitignore-style matcher, `.sproutignore`, `.gitignore` via git |
| `sort.go` | `--sort`, `--size` units, `--max-files`, `--changed-within` |
| `git.go`, `churn.go` | `--git`, `--diff`, `--churn` |
| `codegraph.go` | Declarations, imports and ranking for `--ai` and `--entry` |
| `ai.go`, `entry.go` | `--ai` map, `--entry` reading order |
| `remote.go` | Cloning `github.com/owner/repo` and other URLs |
| `mcp.go` | `sprout mcp` server |
| `completion.go` | `--completion` and `--man`, generated from the flags |
| `output_json.go`, `stats.go`, `project.go`, `color.go` | `--json`, `--stats`, stack detection, terminal output |

## Performance

`bench_test.go` runs every mode against a real repository:

```bash
git clone --depth 1 https://github.com/kubernetes/kubernetes /tmp/k8s
SPROUT_BENCH_DIR=/tmp/k8s go test -run '^$' -bench .
```

Please include before/after numbers in performance PRs.

## Graph accuracy

`tools/accuracy` scores the dependency graph against each language's own
tooling (the TypeScript compiler, Python's `ast`, `go/parser`, Rust's module
rules) on pinned repositories:

```bash
python3 tools/accuracy/run.py
```

Please include before/after numbers in PRs that change import scanning or
resolution. [tools/accuracy/README.md](tools/accuracy/README.md) explains the
numbers and has the current baseline.

## Releasing (maintainers)

Tag and push; the Release workflow runs GoReleaser and updates the Homebrew
tap:

```bash
git tag v0.3.0 && git push origin v0.3.0
```
