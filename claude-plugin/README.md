# Sprout for Claude Code

Sprout maps your codebase for Claude: how files depend on each other, resolved the way each language does (Go, TypeScript/JavaScript, Python, Rust, Java, Kotlin), what a change could break, and which tests to run. One call answers what would take Claude many greps, and it finds what text search misses, such as tests that reach a file through other files.

## What's in the plugin

- **The Sprout MCP server**, with these tools: `project_map`, `tree`, `reading_order`, `diff_tree`, `dependents`, `deps`, `impact` and `context`.
- **A skill** that tells Claude when Sprout answers better than grep: `impact` before changing a function or file, `context` before editing one, `project_map` in an unfamiliar repository.

## Requirements

The plugin runs the `sprout` program on your PATH; install it first:

```sh
brew install sprout-devlabs/tap/sprout
```

Other ways to install (Scoop, Linux packages, `go install`, an install script) are on the [install page](https://sprout-devlabs.github.io/sprout-web/docs/install/). The plugin doesn't bundle or download the binary.

## What it runs and what it reads

- `sprout mcp` runs locally, as a stdio MCP server, in the project folder Claude Code has open.
- It reads the project's files, and runs read-only `git` commands (such as `status`, `diff` and `log`) for changes and history.
- It writes nothing, and it makes no network requests: every path a tool takes must already be inside the project.
- It refuses to serve your home folder or `/`, so a session started in `~` doesn't map private files.

## Example

Ask Claude which tests to run after changing a function. It calls `impact` with the file and the function's name, and gets the files that use it and a ready command, such as `go test . -run "^(TestSetContext|TestSetContextPreRun)$"`.

Source, documentation and issues: https://github.com/Sprout-DevLabs/sprout. MIT licensed.
