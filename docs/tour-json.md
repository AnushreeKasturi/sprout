# Tour JSON, schema version 1

Run `sprout tour [local-directory] --json [--limit N]`. Flags can appear before
or after the directory. Output is one JSON object followed by a newline.
Ordering is deterministic for an unchanged visible tree and environment.
Paths use `/` and are relative to the selected directory. No absolute root,
timestamps, branch names, Git remotes or machine identifiers are generated.

| Field | Meaning |
|---|---|
| `schemaVersion`, `command` | `1`, `"tour"` |
| `project` | Selected directory's basename |
| `purpose` | `{path, text}` from the first recognized visible README, or `null` if no prose was readable |
| `projects` | Root manifest observations: `{manifest, language, packageManager}`; not verified frameworks or installed tools |
| `languages` | File counts by the existing statistics labels, including config/document formats |
| `files`, `directories` | Counts from the visible tree, including test files |
| `skipped` | Entries excluded by ignore rules or because they are links/special files; not a recursive count of everything below ignored directories |
| `layout` | Top-level directories `{path, files}` in path order, bounded by `--limit` |
| `readingOrder` | `{path, reason}` steps: README, likely entry filenames, then files ranked by non-test dependents, bounded by `--limit` |
| `omittedLayout`, `omittedReading` | Candidates left out by the requested output limit |
| `nextCommands` | Arrays of command arguments, intended to run from the selected directory |
| `caveats` | Human-readable descriptions of heuristic limits and unavailable evidence |

Empty collections are `[]` or `{}`, never `null`. The `purpose` field alone is
nullable. Recommendation ordering reuses `--entry`: entry candidates follow
the existing shallow filename heuristic (up to 10), and ranking ties break by
relative path. The README excerpt reuses `--ai`'s first-prose-line heuristic,
reads at most 64 KiB/40 opening lines, and keeps approximately 200 bytes.
It is repository-provided text, not an inferred statement of purpose. URL
userinfo, query strings and fragments are stripped from the excerpt; the
selected root and local home prefix are replaced. This is not a general secret
scanner: review repository prose before sharing reports.

For a reproducible example:

```sh
go run . tour testdata/tour --json
```

Its `readingOrder` is:

```json
[
  {"path":"README.md","reason":"project overview (README)"},
  {"path":"cmd/app/main.go","reason":"likely entry point (filename convention)"},
  {"path":"internal/greeting/greeting.go","reason":"used by 1 file"}
]
```

Use `nextCommands` as argument arrays, without concatenating them into a shell
command. The tour only reads files; it never executes these suggestions, project
scripts, package managers or language runtimes. Git may be queried for ignores.
Without Git, `.gitignore` is not interpreted and the fallback is reported.
No remote URLs, network requests or interactive input are used by tour.

The existing graph limit is 50,000 source candidates and 512 KiB per parsed
source. Source parse failures and unsupported imports may yield incomplete
ranking; tests and vendored/example files are excluded from ranking. The
filesystem walk itself remains proportional to repository size. The command
does not lock files against concurrent edits; rerun after a changing checkout
settles. Exit codes follow the CLI: 0 success, 1 runtime error, 2 usage error.
