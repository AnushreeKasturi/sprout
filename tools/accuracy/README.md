# Graph accuracy

Scores Sprout's dependency graph, the one behind `--entry` and `--ai`, against
each language's own tooling on real repositories. When Sprout says file A
depends on file B, is it right, and which real dependencies does it miss?

```sh
python3 tools/accuracy/run.py            # every repository in repos.json
python3 tools/accuracy/run.py ky click   # just these
```

Needs Go, Node and Python 3.11+. Repositories are fetched at the commits
pinned in `repos.json` into `tools/accuracy/.cache`, and every missed or wrong
link is written to `.cache/out/<repo>-<language>.txt`. Nothing here ships in
the `sprout` binary; the graph comes from `TestDumpGraph` in
`graphdump_test.go`, which is skipped in normal test runs.

## What the numbers mean

A link is one file depending on another file in the same repository. Imports
of outside packages (npm, PyPI, crates.io, Go modules) aren't part of Sprout's
graph and aren't counted.

- **In-repo imports found**: of the import statements that point at a file in
  the repository, how many Sprout's scanner reads. This isolates scanning.
- **False imports**: things Sprout read as imports that aren't, such as
  examples inside comments.
- **Links found**: of the real links, how many Sprout draws (recall). This is
  scanning and resolution together.
- **Links correct**: of the links Sprout draws, how many are real (precision).

Both link numbers matter: a missed link makes `impact` say nothing is affected,
and a wrong one adds noise.

## Where the right answers come from

| Language | Ground truth | Strength |
|---|---|---|
| TS/JS | The TypeScript compiler: `preProcessFile` for imports, `resolveModuleName` with the nearest `tsconfig.json`/`jsconfig.json`. Workspace packages are linked into `node_modules` first, as a package manager would. | Exact, except aliases defined only in bundler config (webpack, Vite), which the compiler can't see either. Without a configured strategy, resolution follows `bundler` rules. |
| Python | Python's `ast` for imports; a written rule for which file an import means (see `truth/py.py`): source roots are the repository root, every folder with a `pyproject.toml`/`setup.py`/`setup.cfg`, and their `src/`. | Imports are exact. Python has no static resolver, so the file mapping is the best static answer, not a compiler's. Code is never run. |
| Go | `go/parser` for imports; import paths map to the repository module with the longest matching path. | Checked per package: a link must land in the imported package's folder. Checking the exact file within the package would need a type checker. |
| Rust | Rust's module rules (see `truth/rs.py`): `mod` declarations from each crate root in `Cargo.toml`, expanded `use` trees, `self`/`super`/`crate`, workspace crates, and qualified paths in code. | Follows the compiler's rules without macros or `cfg`. |

## Current

After #34–#37 (TS/JS, Python, Rust and Go resolution):

| Repository | Language | Files | In-repo imports found | False imports | Links found | Links correct |
|---|---|---:|---:|---:|---:|---:|
| taxonomy | TS/JS | 131 | 100% (270/270) | 0 | 100% (270/270) | 100% (270/270) |
| ky | TS/JS | 87 | 100% (164/164) | 1 | 100% (157/157) | 100% (157/157) |
| create-t3-turbo | TS/JS | 77 | 100% (117/117) | 4 | 100% (113/113) | 99% (113/114) |
| click | Python | 77 | 100% (319/319) | 3 | 100% (151/151) | 99% (155/156) |
| fastapi-template | TS/JS | 109 | 100% (293/293) | 0 | 100% (292/292) | 100% (292/292) |
| fastapi-template | Python | 43 | 100% (75/75) | 0 | 100% (79/79) | 100% (90/90) |
| kubernetes | Go | 13,002 | n/a (go/parser) | – | 100% (56018/56137) | 100% (56018/56018) |
| anyhow | Rust | 29 | 100% (99/99) | 4 | 100% (53/53) | 100% (53/53) |
| ripgrep | Rust | 106 | 100% (683/686) | 7 | 99% (270/274) | 100% (270/271) |

Every remaining "wrong" link was checked by hand, and in each case Sprout is
right and the ground truth is too narrow:

- create-t3-turbo: `next.config.js` loads `./src/env` with `jiti.import(...)`,
  a real build-time dependency the TypeScript compiler doesn't count.
- click: `tests/test_arguments.py` does `from test_options import ...`; pytest
  puts the tests folder on the path. The Python rule only uses manifest roots.
- ripgrep: `crates/core/main.rs` calls `flags::parse()`, a function defined in
  `flags/parse.rs` and re-exported by `flags`; the module rules stop at `flags`.

Kubernetes' few Python scripts aren't shown: they have one link
(`hack/boilerplate/boilerplate_test.py` imports its neighbour, which is right).

## Baseline (before #34–#37)

Sprout `636d292`, 29 September 2026:

| Repository | Language | Files | In-repo imports found | False imports | Links found | Links correct |
|---|---|---:|---:|---:|---:|---:|
| taxonomy | TS/JS | 131 | 97% (262/270) | 3 | 0% (1/270) | 100% (1/1) |
| ky | TS/JS | 87 | 91% (150/164) | 48 | 92% (145/157) | 99% (145/146) |
| create-t3-turbo | TS/JS | 77 | 97% (113/117) | 8 | 21% (24/113) | 96% (24/25) |
| click | Python | 77 | 100% (319/319) | 2 | 100% (151/151) | 100% (155/155) |
| fastapi-template | TS/JS | 109 | 87% (254/293) | 0 | 29% (86/292) | 100% (86/86) |
| fastapi-template | Python | 43 | 100% (75/75) | 0 | 0% (0/79) | – |
| kubernetes | Go | 13,002 | n/a (go/parser) | – | 93% (52,443/56,137) | 100% (52,443/52,443) |
| anyhow | Rust | 29 | 43% (43/99) | 0 | 42% (22/53) | 100% (22/22) |
| ripgrep | Rust | 106 | 20% (136/686) | 0 | 26% (68/261) | 100% (68/68) |

When Sprout draws a link it's almost always right. The problem is the links it
doesn't draw:

- **TS/JS path aliases are dropped.** `@/lib/utils` is treated as an npm
  package, so an app using `tsconfig` `paths` gets almost no graph (taxonomy 0%).
- **TS/JS workspace packages aren't resolved** (create-t3-turbo 21%).
- **Python only looks in the repository root and `src/`.** An app rooted in
  `backend/` gets no links (fastapi-template 0%).
- **Rust resolves `crate::` from the repository's `src/`**, not the crate's own
  folder, so workspace crates don't resolve. Grouped `use crate::{a, b}`,
  `super::`, `self::` and cross-crate paths are missed too (ripgrep 26%).
- **The TS/JS scanner reads imports inside comments** (ky: 48) **and misses
  multi-line `import { … } from` and `export { … } from`.**
- **Go misses links to shared test helpers.** 96% of Go's misses point at
  packages under `test/` (`test/utils/ktesting`); files there, and Go files
  named `test_*.go`, are treated as tests, and nothing links to a test.
