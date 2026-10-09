# Product evolution plan

## Phase 0: audit (2026-10-09)

Baseline: upstream `main` at `026c7be9346f39d9b820012e8239b088e6bd58a6`.
The existing `go test -race ./...` suite passes on macOS/arm64 with Go 1.26.5.
The module targets Go 1.22 and has no external dependencies. CI runs vet and
race tests on Linux, macOS and Windows. Keep that compatibility and test matrix.

The CLI uses the standard `flag` package and explicit dispatch in `run`.
Subcommands accept flags before or after arguments, use exit 2 for usage errors
and exit 1 for runtime failures. Graph subcommands do not load tree-view config.
JSON commands have independent versioned result structs. Completion metadata,
the man page and README must be updated together with each new command.

Reusable foundations:

| Need | Existing code |
|---|---|
| Filtered repository walk | `BuildTree`, `.sproutignore`, Git's ignore rules |
| Project purpose | `readmeSummary` in `ai.go` |
| Ecosystems and language counts | `DetectProject`, `collectStats`, `topLanguages` |
| Reading sequence | `readingOrder`, `keyFiles`, `Graph.Ranked` |
| Dependencies and explanations | `buildGraph`, `Graph`, `follow`, `explain` |
| Change impact and tests | `runImpact`, `impactResult`, `goTestPackages` |
| Editing context | `runContext`, `gatherContext`, including JSON added in #69 |
| Agent access | stdio MCP dispatch, `confine`, revision validation |
| Evaluation | `bench_test.go`, `tools/accuracy`, `tools/agent-eval` |

The code supports Go, JS/TS, Python, Rust, Java and Kotlin relationships, with
different levels of precision. Entry names are heuristics, not proof of runtime
entry. Ranking excludes tests, examples and vendored code. Source parsing is
capped at 50,000 files and 512 KiB per source file; walking itself is not capped.
Root manifest detection identifies ecosystems, not installed frameworks.

Open issues were checked: #54–57 concern language resolution, #58–59 require
measurements before caching/indexing, #60 concerns repeated agent evaluation,
and #46–52 cover focused CLI/docs improvements. Do not bundle these into tour.
There were no open PRs at audit time. Published user docs live in `sprout-web`.

Security review: analysis reads untrusted files. The new tour must exclude
symlinks and special files, respect ignored README/manifests, escape terminal
control characters, and keep generated paths relative. Config inheritance must
not read outside the selected root. Existing remote clone messages echo URLs;
credential redaction there warrants a separate focused security change before
expanding remote support. No remote cloning or MCP exposure is added in Phase 1.

## Reviewable phases

| Phase | Scope | Exit condition |
|---|---|---|
| 1 | `tour [path]`, text + JSON, bounded recommendations, docs and tests | Evidence-based offline reading plan; existing tests and platform CI pass |
| 2 | Additive Markdown format for local impact reports | Fixtures for empty/large diffs, renamed/deleted files and missing refs |
| 3 | GitHub Actions example using a job summary | Least-privilege permissions; fork-safe; repeated runs produce no comment spam |
| 4a | Versioned graph export from existing `Graph` | Stable path IDs, reasons, deterministic export, graph-limit tests |
| 4b | Offline interactive explorer | Keyboard + table fallback, bounded neighborhoods, no external assets |
| 5 | Read-only MCP tools, published docs and performance work | Confinement regressions, reproducible examples and measured costs |

One feature per PR, with tests, docs, design rationale and CI checks on its exact
commit. Do not merge automatically. Phase 1 ends with a report and a recommended
next step; later phases need a separate reviewable change.

For Phase 4, compare static self-contained HTML, a localhost server and a
standalone frontend. Prefer static HTML initially: it can explore a bounded
export offline without a server attack surface or a separate installation.
Revisit this choice in that phase's ADR if measured graph sizes require live
queries. No frontend runtime dependency is introduced now.
