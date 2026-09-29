# 🌱 Sprout

**Map your codebase, for you and your AI agent.**

[![CI](https://github.com/Sprout-DevLabs/sprout/actions/workflows/go.yml/badge.svg)](https://github.com/Sprout-DevLabs/sprout/actions/workflows/go.yml)
[![Release](https://img.shields.io/github/v/release/Sprout-DevLabs/sprout)](https://github.com/Sprout-DevLabs/sprout/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

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

The server is read-only. Paths are confined to the project, symlinks included,
git arguments can't carry options, and it never clones.

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

See [CONTRIBUTING.md](CONTRIBUTING.md). Ideas and bugs:
[open an issue](https://github.com/Sprout-DevLabs/sprout/issues).

## License

[MIT](LICENSE)
