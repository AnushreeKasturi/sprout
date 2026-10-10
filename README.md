# 🌱 Sprout

**Map your codebase, for you and your AI agent.**

[![CI](https://github.com/Sprout-DevLabs/sprout/actions/workflows/go.yml/badge.svg)](https://github.com/Sprout-DevLabs/sprout/actions/workflows/go.yml)
[![Release](https://img.shields.io/github/v/release/Sprout-DevLabs/sprout)](https://github.com/Sprout-DevLabs/sprout/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![DeepSource](https://app.deepsource.com/gh/Sprout-DevLabs/sprout.svg/?label=active+issues&show_trend=true)](https://app.deepsource.com/gh/Sprout-DevLabs/sprout/)
[![Go version](https://img.shields.io/github/go-mod/go-version/Sprout-DevLabs/sprout)](go.mod)
[![Docs](https://img.shields.io/badge/docs-sprout--web-blue)](https://sprout-devlabs.github.io/sprout-web/docs/)
[![Good first issues](https://img.shields.io/github/issues/Sprout-DevLabs/sprout/good%20first%20issue?label=good%20first%20issues&color=7057ff)](https://github.com/Sprout-DevLabs/sprout/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
[![Discussions](https://img.shields.io/github/discussions/Sprout-DevLabs/sprout)](https://github.com/Sprout-DevLabs/sprout/discussions)

<p align="center">
  <img src=".github/assets/sprout-demo.gif" width="800" alt="Sprout demo: sprout maps a repository as a tree, lists where to start reading with sprout --entry, and condenses the project into a 1,484-token map for AI agents with sprout --ai.">
</p>

Sprout is a fast, single-binary directory explorer built around how developers
read projects:

- It respects `.gitignore` and counts what it hides.
- It shows git changes, commit hotspots and sizes in the tree, and renders a
  pull request as a tree.
- It suggests where to start reading.
- It gives LLMs a compact project map that includes function and type
  signatures.
- It runs as an MCP server, so coding agents can use it directly.

```bash
sprout --ai | pbcopy                     # project context for any chat, ~2k tokens
sprout --entry                           # where to start reading
sprout tour                              # purpose, layout, reading plan and next commands
sprout impact --diff main...HEAD         # what this branch could break, and the tests to run
sprout --diff main...HEAD -L 2           # what this branch touched, as a tree
sprout github.com/owner/repo --ai        # the same, for a repo you haven't cloned
```

📖 **Docs:** https://sprout-devlabs.github.io/sprout-web/docs.html

## Install

| Platform | Command |
|---|---|
| macOS, Linux | `brew install sprout-devlabs/tap/sprout` |
| Windows | `scoop bucket add sprout https://github.com/Sprout-DevLabs/scoop-bucket` then `scoop install sprout` |
| Debian, Ubuntu, Fedora, Alpine, Arch | `.deb`, `.rpm`, `.apk` and `.pkg.tar.zst` from [Releases](https://github.com/Sprout-DevLabs/sprout/releases) |
| Any Unix | `curl -fsSL https://sprout-devlabs.github.io/sprout-web/install.sh \| sh` (checks the SHA-256 before installing) |
| Go 1.22+ | `go install github.com/Sprout-DevLabs/sprout@latest` |

Homebrew and the Linux packages include shell completions and a man page. For
other installs, add completions yourself:

```bash
sprout --completion zsh > "${fpath[1]}/_sprout"       # or bash, fish, powershell
```

## A tree that knows what matters

Inside a git repo, **`.gitignore` decides** what's shown. Sprout asks git
itself, so nested ignores, negations and global excludes all work. Outside git,
`node_modules`, `.venv`, `dist` and similar folders are hidden instead. The last
line always says how much was left out.

| Flag | |
|---|---|
| `-L, --depth N` | Limit depth |
| `--ignore PATTERNS` | Hide matches, in gitignore syntax: `'*.log'`, `'src/gen/'`, `'docs/**/*.png'`, `'!keep.log'` |
| `--only PATTERNS` | Show only matching files and prune emptied folders: `'*.go'`, `'web/src/**/*.tsx'` |
| `--changed-within AGE` | Only files modified recently: `30m`, `12h`, `7d`, `2w` |
| `--max-files N` | At most N files per folder, then `… 37 more files` |
| `--size` | File sizes and **true** folder totals, even past `--depth` |
| `--sort name\|size\|time` | Largest or newest first. `-r` reverses; `--dirs-first` lists folders first; `--si` uses powers of 1000 |
| `-a, --all` | Show hidden and ignored entries |
| `--hyperlink` | Make names clickable in terminals that support links |

A `.sproutignore` in the project applies the same patterns on every run.

## `--ai`: context for LLMs

A structure-first map of the project, sized to a token budget (`--budget`,
default 2000). It contains:

- the README's opening line
- the detected stack and languages
- entry points and the config/CI files
- uncommitted work and the 90-day hotspots
- the layout, opened breadth-first
- the **key files**: the files the rest of the code uses most, with their
  function and type signatures

It never includes file bodies. Here's the key-files section for
[sindresorhus/ky](https://github.com/sindresorhus/ky):

```
## key files (most used first)
source/types/options.ts (used by 12): export type SearchParamsInit; export type SearchParamsOption; …
source/core/constants.ts (used by 8): export const supportsRequestStreams; export const supportsAbortController; …
source/errors/KyError.ts (used by 7): export class KyError extends Error
source/types/request.ts (used by 5): export type KyRequest<T = unknown>
```

Signatures come from Go's own parser. For TypeScript/JavaScript, Python,
Rust, Java and Kotlin, Sprout reads declarations and imports line by line.
Files are ranked by how many other files import or reference them, and tests
don't count.

## `--entry`: where to start reading

```
$ sprout github.com/charmbracelet/bubbletea --entry
Reading order for bubbletea

  1. README.md                   what the project is
  2. tutorials/basics/main.go    entry point
  3. tutorials/commands/main.go  entry point
  4. tea.go                      used by 25 files
  5. mouse.go                    used by 7 files
```

## `tour`: get oriented in one command

`sprout tour` is `--entry` with context: the README's opening line, the
ecosystems and languages, the top-level layout, why each file is on the reading
list, and what to run next.

```text
$ sprout tour testdata/tour
Tour of tour

Purpose (README.md excerpt): A small command that prints a greeting.
...
Start here:
  1. README.md — project overview (README)
  2. cmd/app/main.go — likely entry point (filename convention)
  3. internal/greeting/greeting.go — used by 1 file

Next (run from the directory you toured; POSIX shell or PowerShell):
  sprout context './cmd/app/main.go'
```

`--limit N` (default 8, up to 50) sets how many reading steps and directories
are shown; anything left out is counted. `--json` prints a stable,
versioned object, documented in [docs/tour-json.md](docs/tour-json.md). Tour is
offline and read-only: it respects ignore rules, skips symlinks, and never runs
the commands it suggests. To map a folder named `tour`, write `sprout ./tour`.

## `deps` and `dependents`: what a file touches

`sprout deps FILE` lists what a file depends on, and `sprout dependents FILE` what
depends on it, tests included. Each line says why: the import, or for Go, the
names it actually uses. `--depth N` follows more hops (`-1` for all), `--no-tests`
skips test files, and `--json` is for scripts.

```
$ sprout dependents graph.go --depth 2
11 files (6 tests) depend on graph.go

  ai.go              uses Graph
  codegraph.go       uses FileID, Graph, GraphFile +2 more
  entry.go           uses FileID, Graph
  query.go           uses FileID, Graph
  codegraph_test.go  test · uses Graph
  graph_test.go      test · uses FileID, Graph, GraphFile +1 more
  graphdump_test.go  test · uses FileID
  main.go            via ai.go · uses AIMap
  ai_test.go         test · via ai.go · uses estimateTokens
  bench_test.go      test · via codegraph.go · uses buildGraph
  query_test.go      test · via query.go · uses queryResult
```

Go is resolved to the file that declares what's used. Other languages are
resolved from their imports: tsconfig `paths` and workspace packages for
TypeScript, real source roots for Python, and crates, `use` trees and workspace
crates for Rust. `tools/accuracy` checks this against each language's own
tooling. To map a folder named `deps` or `dependents`, write `./deps`.

## `impact`: what a change could break

`sprout impact` traces a change through the graph: every file that depends on
it, directly or through others, and every test that reaches it, with a command
to run them: `go test` for Go, `pytest` for Python, and `vitest`, `jest`, `ava`
or `mocha` for TypeScript and JavaScript when the project uses one. With no
arguments it uses your uncommitted changes;
`--staged`, `--diff main...HEAD` and `--commit HEAD` take them from git, or name
the files. `--all` lists every affected file and `--json` is for scripts and CI.

```
$ sprout impact pkg/controller/garbagecollector/graph_builder.go   # in kubernetes
Impact of 1 file

  pkg/controller/garbagecollector/graph_builder.go

Affected: 67 files, 8 directly
  cmd/kube-controller-manager/app/controllermanager.go          uses garbagecollector.GraphBuilder, garbagecollector.NewDependencyGraphBuilder
  cmd/kube-controller-manager/app/options/options.go            uses garbagecollector.DefaultIgnoredResources
  pkg/controller/garbagecollector/dump.go                       uses beingDeleted
  pkg/controller/garbagecollector/garbagecollector.go           uses GraphBuilder, NewDependencyGraphBuilder, hasDeleteDependentsFinalizer +2 more
  pkg/controller/garbagecollector/graph.go                      uses beingDeleted
  pkg/controller/garbagecollector/patch.go                      uses monitors
  pkg/controller/storageversionmigrator/storageversionmigrator.go  uses garbagecollector.GraphBuilder, garbagecollector.Monitor
  test/integration/util/util.go                                 uses garbagecollector.DefaultIgnoredResources
  … and 59 more files through them (--all lists them)

Tests: 88 files, 67 Go packages
  go test ./cmd/kube-controller-manager/app ./cmd/kube-controller-manager/app/options ./pkg/controller/garbagecollector ./pkg/controller/storageversionmigrator … +63 more (--all)
```

## `context`: before you edit a file

`sprout context FILE` is what to know before changing one file, fitted to a
token budget (`--budget`, default 1500): the signatures it actually uses from
each dependency, the files that use it and why, the tests that reach it, then
what it declares. Agents get it as the `context` MCP tool.

```
$ sprout context pkg/controller/garbagecollector/garbagecollector.go --budget 700   # in kubernetes
# pkg/controller/garbagecollector/garbagecollector.go
27 dependencies · 5 direct users · 60 affected through them · 83 tests reach it

## depends on (and what it uses from each)
pkg/controller/controller_ref_manager.go — uses c.GenerateDeleteOwnerRefStrategicMergeBytes
  func GenerateDeleteOwnerRefStrategicMergeBytes(dependentUID types.UID, ownerUIDs []types.UID, finalizers ...string) ([]byte, error)
pkg/controller/garbagecollector/errors.go — uses restMappingError
  func (r *restMappingError) Error() string
  type restMappingError struct
pkg/controller/garbagecollector/graph.go — uses node, objectReference, ownerReferenceCoordinates
  type objectReference struct
  type node struct
  func (n *node) isBeingDeleted() bool
  func (n *node) isObserved() bool
…
## used by
cmd/kube-controller-manager/app/core.go — uses garbagecollector.GarbageCollector, garbagecollector.NewComposedGarbageCollector
pkg/controller/garbagecollector/dump.go — uses GarbageCollector
pkg/controller/garbagecollector/operations.go — uses GarbageCollector, namespacedOwnerOfClusterScopedObjectErr
pkg/controller/garbagecollector/patch.go — uses GarbageCollector
test/integration/util/util.go — uses garbagecollector.NewGarbageCollector

```

## `--diff`: a pull request as a tree

```
$ sprout --diff main...HEAD -L 1
. main...HEAD
├── .github/  (2 changed)  +13 -28
├── cursed_renderer.go  M  +67 -17
├── examples/  (3 changed)  +5 -5
├── tea.go  M  +45 -20
└── testdata/  (13 changed)  +13 -13

34 files changed, +469 -98
```

It takes any git revision: `main...HEAD`, `HEAD~3`, `v1.0..v1.1`.

## `--git` and `--churn`

`--git` marks `M` modified, `A` added, `D` deleted (shown where the file was),
`R` renamed, `?` untracked and `U` conflicted, and counts changes on folders.
`--churn` draws how many commits touched each path. `--since '90 days ago'`
narrows the window.

## Remote repositories

```bash
sprout github.com/owner/repo --ai
sprout git@github.com:acme/private.git --churn -L 2
```

Any git URL, or `host/owner/repo` shorthand, is cloned into a temporary folder,
mapped, and deleted afterwards. Private repos work through your git
credentials. A cloned repo's own `.sproutrc` is ignored.

## For coding agents: `sprout mcp`

```bash
claude mcp add sprout -- sprout mcp
```

```json
{ "mcpServers": { "sprout": { "command": "sprout", "args": ["mcp"] } } }
```

| Tool | Does |
|---|---|
| `project_map` | The `--ai` map, key files included. Agents are told to call it first |
| `reading_order` | The `--entry` list |
| `tree` | A tree of any folder, with optional git status or churn |
| `diff_tree` | `--diff` for a revision like `main...HEAD` |
| `dependents` | What depends on a file, and why |
| `deps` | What a file depends on, and why |
| `impact` | What a change could break, and the tests to run: given files, uncommitted, staged, a range or a commit |
| `context` | What to know before editing a file: signatures it uses, its users, its tests |

The server is read-only. Paths and file arguments are confined to the project,
symlinks included, git arguments can't carry options, and it never clones.

## Config

Put default flags in `~/.config/sprout/config` (or `$SPROUT_CONFIG`), or in a
`.sproutrc` for one project. Use one flag per line:

```
--depth 3
--hyperlink
--ignore=*.snap,fixtures/
```

The command line always wins. `--no-config` skips both files.

## Scripting

`--json` emits the tree, stack and stats, with a `schemaVersion`. Adding
fields isn't a breaking change. Every other flag applies, so
`sprout --diff main...HEAD --json` can feed a PR bot directly.

## Speed

Sprout reads only what the output needs and parses sources in parallel. On
Kubernetes (31k files, warm cache):

| Command | Time |
|---|---|
| `sprout` | 0.47 s, about the same as `find` |
| `sprout --entry` | 1.0 s |
| `sprout --ai` | 1.1 s |

`bench_test.go` reproduces these numbers.

## Design principles

- **Useful by default.** `sprout` with no flags should usually be enough.
- **Transparent.** Anything hidden automatically is counted and can be shown.
- **Humans and machines.** Color only on terminals (`NO_COLOR` respected), plain text in pipes, JSON when asked.
- **Small.** One static binary, standard library only. git is needed only for git features.

## Contributing

New to open source? Issues labelled
[`good first issue`](https://github.com/Sprout-DevLabs/sprout/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
are small, say where to look and how to test, and are a good place to start.
[CONTRIBUTING.md](CONTRIBUTING.md) walks you through it. Questions and ideas:
[Discussions](https://github.com/Sprout-DevLabs/sprout/discussions). Bugs:
[open an issue](https://github.com/Sprout-DevLabs/sprout/issues). Security
problems: see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
