# Phase 1 validation

Local environment: Apple M4, macOS/arm64, Go 1.26.5. The project still targets
Go 1.22; GitHub Actions uses `go.mod` on Linux, macOS and Windows.

Checks:

- Baseline `go test -race ./...` passed before editing.
- `gofmt -l .` produced no paths; `git diff --check` passed.
- `go vet ./...` and `go test -race ./...` passed after the feature.
- A coverage run measured 88.9% of statements in the main package; new tour
  functions were between 90% and 100% in that run.
- `go run . tour testdata/tour` and its `--json` variant reproduce the README
  reading sequence. The same command was exercised against Sprout and Gleam.
- `golangci-lint` is not installed in this environment. The repository's own
  required checks are gofmt, vet and race tests; no new lint dependency was added.

Regression cases include flag positions and exit codes, `./tour` as a directory,
config independence, missing evidence/Git, stable JSON and relative paths,
output limits, Git/.sproutignore exclusions, symlink confinement, credential URL
redaction, terminal controls and shell quoting, graph cycles, inherited config
escape attempts, fsmonitor execution prevention, and completion/help visibility.
The existing graph/query/impact/context/MCP tests remain in the full suite.

## Local performance sample

Median of three benchmark samples, with five iterations per sample on Sprout
and three on Gleam, warm local filesystem cache. These measurements include
walking and graph construction, but exclude binary startup. `B/op` measures
total allocations, **not peak or retained memory**.

| Checkout | Visible files in tour | `--entry` | `tour --json` | Tour allocated bytes/op |
|---|---:|---:|---:|---:|
| Sprout during development | 93 | 6.64 ms | 6.69 ms | 3.19 MB |
| Gleam local checkout | 5,371 | 138 ms | 159 ms | 135 MB |

Reproduce against your checkout:

```sh
SPROUT_BENCH_DIR=. go test -run '^$' -bench 'Benchmark(Tour|Entry)$' -benchmem -benchtime=5x -count=3
```

Tour does additional regular-file checks and gathers layout metadata; the
larger sample shows about 15% overhead versus `--entry`. The reading limit
does not reduce analysis cost. No onboarding-time, accuracy or universal
performance improvement is claimed. Large production-repository evaluation
and human onboarding studies remain future work.
