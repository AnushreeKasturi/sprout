# Security policy

## Supported versions

Security fixes go into the latest release. Please upgrade to it before
reporting; `sprout --version` shows yours.

| Version | Supported |
|---|---|
| Latest minor (0.3.x) | Yes |
| Older | No |

## Reporting a vulnerability

Please **don't open a public issue.** Report it privately instead:

1. Go to the repository's [Security tab](https://github.com/Sprout-DevLabs/sprout/security).
2. Choose **Report a vulnerability**.

Include what you did, what happened, what you expected, and the Sprout version
and OS. A proof of concept helps a lot.

What to expect:

- An acknowledgement within 3 working days.
- An assessment and a plan within 10 working days.
- A fix and a release as soon as one is ready, with a GitHub security advisory
  that credits you, unless you'd rather stay anonymous.

Please give us a reasonable time to release a fix before you disclose the
problem publicly.

## What counts

Sprout reads your files and runs `git`; it doesn't run your code. The parts
where a problem would matter most:

- **The MCP server** (`sprout mcp`). Tool arguments come from an AI agent and
  are untrusted. Paths and file arguments must stay inside the served root
  (including through `..`, absolute paths and symlinks), and nothing may reach
  `git` as an option. Escaping the root, reading files outside it, or running
  anything other than read-only `git` is a vulnerability.
- **Remote repositories** (`sprout github.com/owner/repo`). Cloning, the
  temporary checkout, and the cloned repository's own config files, which must
  not be trusted.
- **Config files** (`.sproutrc`, `~/.config/sprout/config`) can only set flags.
- **Installers**: `install.sh` (checksum verification), the Homebrew cask and
  the Scoop manifest.
- **Release artifacts**: binaries, checksums and packages on the releases page.

Not vulnerabilities: Sprout showing the contents of files you pointed it at,
or a slow run on a very large repository (please do open a normal issue for
that).
