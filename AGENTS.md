# AGENTS.md — rules for working on mirava-core

This file is for AI coding agents and humans alike. Read it before changing
code. If something here conflicts with what you see in the code, the code
may be stale or this file may be — say so instead of guessing.

## What this project is

`mirava-core` is a Go library: a **toolbox for package and container
mirrors**. It answers four questions about a mirror URL:

1. Is it a healthy mirror of the expected kind? (`Status`)
2. How fast does it move bytes? (`Speed`)
3. Does it have package X, and which version? (`Package`)
4. What is the direct download URL for package X? (`Resolve`)

Plus DNS-provider probing (`DNSProbe`): resolve a mirror through several
resolvers (UDP, DoH, DoT, system) and flag diverging answers.

It is a **core**: a CLI, a website and backend services all depend on it.
Every design decision follows from that:

- It never prints, never exits, never reads stdin/env for behaviour.
- Every public call takes a `context.Context` first and honours it.
- Results are plain structs with stable JSON tags — callers serialize them.
- Breaking the public API needs a deliberate decision, a version bump and a
  note in the README.

## Layout

| Path | Role |
|------|------|
| `mirava.go` | Public entrypoint: `New(opts...)`, `Mirava` bundle, `Prober(type)`, `DNSProbe`, re-exported types/errors/constants |
| `internal/core` | `Prober` interface, result envelopes, `PackageQuery`, `SpeedOptions`, sentinel + typed errors, backend `Config` |
| `internal/httpx` | The only place for HTTP plumbing: `JoinURL`, shared default client, User-Agent, `MeasureSpeed` |
| `internal/cache` | `Store` interface, bounded LRU, atomic disk store, layered store, singleflight |
| `internal/apt` | apt backend — the **reference implementation**; copy its patterns |
| `internal/{npm,pypi,cargo,gomod,composer,docker,pacman}` | One package per ecosystem |
| `internal/dns` | DNS providers and mirror probing |
| `internal/backends_test` | `httptest` fixtures for the registry backends |
| `integration_test.go` | Live-network tests, build tag `integration` |
| `cmd/mirrorscan` | Dev tool: runs all probes against a parsed mirror catalog |
| `pkg/`, `internal/constants` | **Legacy v0.3 code, pending deletion.** Do not import, extend or fix it. |

Backends import `internal/core`, never the root package (the root imports
the backends — the reverse is a cycle).

## Hard rules

These come from bugs we actually shipped or nearly shipped. Do not break
them.

### API shape
- Every backend implements `core.Prober`: `Status`, `Speed`, `Package`,
  `Resolve`, all `ctx`-first. Ecosystem-specific inputs go in
  `PackageQuery.Options` as a typed struct (e.g. `*apt.QueryOptions`), and
  ecosystem-specific outputs go in a `Detail` struct.
- **A missing package is a result, not an error**: return
  `PackageResult{Found: false}`, `nil`. Errors mean "could not check".
  (`Resolve` is the exception: no URL to return → wrap `core.ErrNotFound`.)
- Return errors that work with `errors.Is` against the sentinels in
  `internal/core/errors.go` (`ErrNotFound`, `ErrTimeout`, `ErrInvalidMirror`,
  `ErrUnreachable`, `ErrNotImplemented`). Wrap context errors with
  `core.AsError`. Every error type needs `Unwrap`.
- Fill the `Envelope` (`Type`, `Mirror`, `CheckedAt`, `Latency`, `OK`) on
  every result, success or failure.
- No `verbose bool`, no `fmt.Print*`, no `log.*`. Diagnostics go to the
  configured `*slog.Logger`; progress goes to `SpeedOptions.OnProgress`.
- Durations are `time.Duration` (or `core.Duration` in results), never
  integer seconds.

### HTTP
- Build every URL with `httpx.JoinURL`. Never concatenate `mirror + "/path"`
  (that produced `//ls-lR.gz` for URLs with a trailing slash).
  `JoinURL` escapes `?`: append query strings **after** joining.
- Build requests with `httpx.NewRequest` (sets the User-Agent) and get the
  client from `cfg.Client()` — never a nil `*http.Client`, never a new
  `http.Transport` per backend.
- Always close and drain response bodies, including on error paths.
  Never `defer resp.Body.Close()` inside a loop.
- Speed tests use `httpx.MeasureSpeed`. Do not write another download loop.

### Status must validate content, not just status codes
Many real mirrors sit behind SPA frontends that answer **every** path with
`200 OK` and an HTML landing page. A 200 proves nothing. Each Status probe
must check the body looks like the ecosystem's real format:
apt → parses as Release metadata; npm/composer/cargo `config.json` → valid
JSON; go `@v/list` → not HTML; pypi `/simple/` → PEP 503 anchors;
docker `/v2/` → `Docker-Distribution-API-Version` header or a 401 challenge.
`TestStatusRejectsHTMLCatchAll` enforces this — extend it for new backends.

### Mirror URLs
- The caller passes the **full repository URL**, including any distro or
  repo subpath (`https://mirror.example/ubuntu/`, `…/archlinux/`,
  `…/repository/npm-group/`). The core does not guess subpaths.
  Helpful error messages or hints are fine; silent rewriting is not.
- Don't default to the host machine's properties. Pacman once probed
  `aarch64` because tests ran on Apple Silicon. Defaults describe what
  mirrors serve (`x86_64`, `amd64`), not where the code runs.

### apt and caching
- Freshness comes from repository metadata (`InRelease`/`Release`), not
  guessed TTLs. Suite discovery reads `Codename`/`Suite` from the files;
  the hint lists in `release.go` are only candidate URLs.
- Index blobs are content-addressed by the SHA256 from `InRelease` and
  verified on download. **The checksum must belong to the exact file you
  fetch** — pairing the `.xz` hash with the `.gz` path broke every live
  mirror while unit tests passed.
- Cache writes go through `cache.Store`. Disk writes are atomic (temp file
  + rename). Promotion from disk to memory must not extend expiry.
- Wrap concurrent fetches of the same key in singleflight.

## Testing

```bash
make test         # go test -race ./...  (offline, must always pass)
make vet          # go vet ./...
make fmt          # gofmt
make integration  # live mirrors, needs network
```

- Default tests are **offline**: `httptest.Server` fixtures only. No public
  internet in `go test ./...`.
- Live tests use `//go:build integration` and live in `integration_test.go`
  (or `*_integration_test.go`).
- Unit tests passing is not enough for anything touching parsing or real
  protocol formats. Fixtures you write encode your own assumptions; run the
  integration tests (or `cmd/mirrorscan`) against real mirrors before
  calling such a change done. Several bugs here were invisible until a live
  run.
- When a live mirror fails, find out **whose** fault it is before changing
  code: reproduce with `curl`, then classify as our logic, mirror-side
  (TLS, 503, SPA catch-all, hijacked redirect), network (geo-blocked,
  timeout) or bad input (wrong URL/subpath). Only the first is a code fix.
- Don't weaken an assertion to make a test pass. If a test is flaky, find
  the real cause (e.g. a fixture where two indexes share one hash).
- Remove debug test files before committing.

CI runs `gofmt -l`, `go vet ./...` and `go test -race -cover ./...`. All
three must be clean.

## Adding a backend

1. Create `internal/<eco>/` with a `Service` implementing `core.Prober`,
   constructed by `New(cfg core.Config)`.
2. Define `QueryOptions` (inputs) and `Detail` (outputs) in that package
   if the ecosystem needs them.
3. Status validates content (see above). Speed uses `httpx.MeasureSpeed`.
   Resolve performs every intermediate request needed and returns the
   final URL plus any required headers (see docker's bearer token).
4. Add the `MirrorType` constant in `internal/core/api.go`, update
   `Implemented()`, re-export it in `mirava.go`, add a field to `Mirava`,
   wire it in `New` and `Prober`.
5. Add `httptest` fixtures to `internal/backends_test`, including the HTML
   catch-all case, and a live test in `integration_test.go`.
6. Update the mirror-type table in `readme.md`.

## Style

- `gofmt`, standard Go naming, no new dependencies without a reason
  (current: `golang.org/x/sync`, `golang.org/x/net`, `go-deb-version`).
- Doc comments on every exported identifier; explain *why* in comments
  where a rule above applies (e.g. why a body is validated).
- Keep each backend self-contained; shared code belongs in `internal/httpx`
  or `internal/core`, not copied between backends.
- Prefer small, focused changes. Don't refactor unrelated code in the same
  change.

## Git and PRs

- Never commit to `main` directly; work on a branch.
- Commit only when asked. Conventional-style messages
  (`feat:`, `fix:`, `test:`, `docs:`; `!` for breaking changes) with a body
  explaining why.
- Don't push, open PRs, tag releases or delete files/branches without the
  maintainer's go-ahead.
- Never commit secrets, local caches, `coverage.out` or built binaries.
