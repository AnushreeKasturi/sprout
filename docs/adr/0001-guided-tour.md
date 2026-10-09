# ADR 0001: compose an offline tour from existing analysis

Status: accepted for Phase 1 implementation.

## Context

`--entry` already ranks reading candidates and `--ai` already summarizes the
repository. New contributors still have to discover several flags to combine
purpose, layout, entry points and next actions. Changing their existing output
would break scripts and agent prompts.

## Decision

Add `sprout tour [path] [--json] [--limit N]` as a non-interactive local command.
Reuse the existing tree, README extraction, ecosystem detection, language
counts and reading-order functions. Build one graph without test parsing or
symbol retention. Collect one result and render it as text or schema-version-1
JSON. The default limit is 8; accept 1–50. Report omitted items explicitly.

Reasons identify observed filenames, manifests and incoming dependencies.
Describe filename-based entries as *likely*, and state analysis limitations.
If evidence is missing, say so; do not invent architecture or call an LLM.
Keep next commands as argument arrays in JSON. Text suggestions are rendered
for the platform shell and run from the selected directory.

The command accepts a local directory (default current directory), requires no
Git repository, ignores tree-view config like existing graph subcommands, and
does not start a server, execute source code, or run suggested commands.
Git is used only for ignore discovery when available; the built-in exclusions
remain the fallback. Disable repository-configured fsmonitor hooks during
ignore discovery. Confine inherited JS/TS config reads to the canonical root
and regular files no larger than the source-size cap. These small shared
hardening changes are required to reuse the walker and graph safely; normal
in-root config inheritance retains its existing regression coverage.
Remote tours and a tour MCP tool are deferred.

## Alternatives and consequences

- Wrapping the output of `--ai` would parse presentation text and build analysis
  twice. Sharing the underlying functions gives one source of truth.
- Extending `--entry` in place would alter an established output contract.
- Interactive prompts add terminal state and make pipes/CI less useful.
- A persistent index is not needed for this feature. Follow the measurement
  requirements in issues #58–59 before adding caches.

Costs are one filtered walk, one graph build and ranking, matching `--entry`'s
main work, plus bounded presentation. A recommendation limit limits output,
not analysis. Existing graph caps and heuristics still apply. The tour does not
prove runtime behavior or infer frameworks from unverified names.

Tests cover a representative fixture, deterministic JSON, bounds, empty and
unsupported projects, ignored/symlinked input, absent Git, usage errors, CLI
dispatch, completions, and reuse of existing reading order. Measure a benchmark
against `--entry`; do not claim onboarding speed improvements without a human
study.
