# Contributing to mirava-core

Thanks for contributing. mirava-core is the shared core behind the mirava
CLI, website and backends, so changes need to be reliable and keep the
public API stable.

**Read [AGENTS.md](AGENTS.md) first.** It holds the project rules for
humans and AI agents alike: layout, API conventions, HTTP rules, testing
requirements and the backend checklist. This file only covers the process.

## Before you start

- For anything non-trivial (a new ecosystem, an API change, a cache
  change), open an issue first describing the mirror layout, example URLs
  and edge cases.
- Name the `MirrorType` you're working on in the issue title.

## Making a change

1. Branch from `main`.
2. Follow the rules in [AGENTS.md](AGENTS.md). For a new backend, follow its
   "Adding a backend" checklist.
3. Run:

   ```bash
   make fmt
   make vet
   make test          # offline, must pass
   make integration   # live mirrors, for anything touching parsing or protocols
   ```

4. Keep the PR focused: one ecosystem, one bug, or one mechanical change.

## In the PR description

- What changed and why.
- For speed checks: download sizes and time bounds used.
- For anything that talks to real mirrors: which live mirrors you tested
  against and the result.
- Any public API change, and why it's needed.

## Questions

Open an issue on [MiravaOrg/mirava-core](https://github.com/MiravaOrg/mirava-core).
