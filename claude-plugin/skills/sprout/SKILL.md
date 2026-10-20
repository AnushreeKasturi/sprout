---
name: sprout
description: Use Sprout's MCP tools instead of grep for how files relate in this codebase. Before changing a function, type or file, call impact (with symbol for one declaration) to see what breaks and the tests to run; call context before editing a file you haven't read the neighbours of; dependents and deps for what imports a file and what it imports; project_map first in an unfamiliar repository.
---

# Sprout

Sprout resolves imports the way each language does (Go, TypeScript/JavaScript,
Python, Rust, Java, Kotlin), so it finds what text search misses: files that
use a name without mentioning the file, and tests that reach a change through
other files. One call answers what would take many greps.

| Question | Tool |
|---|---|
| New to this repository | `project_map` |
| About to edit a file | `context` with `file` |
| What breaks if I change this file | `impact` with `files` |
| What breaks if I change this function, method, class or type | `impact` with `files` (one file) and `symbol` |
| What did my edits affect, and which tests do I run | `impact` with no arguments (uncommitted changes) |
| Who imports this file / what does it import | `dependents` / `deps` with `file` |

- Run the test command `impact` prints. For Go it narrows `go test` with `-run`
  to the tests that reach the change.
- If a tool says the folder is the home folder or isn't a project, pass `root`
  with the project's absolute path.
- Grep is still right for finding text: a string, a log message, a TODO.
