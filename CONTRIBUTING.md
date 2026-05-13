# Contributing to mirava-core

Thanks for contributing. The direction is simple: **reliable `MirrorService` implementations for every `MirrorType` in [`pkg/type.go`](pkg/type.go)**—apt, yum, dnf, pacman, npm, go, cargo, pypi, nuget, docker, and composer—so callers can probe mirrors with one interface.

## What each backend must cover

1. **`CheckStatus`** — Does this base URL look like a healthy mirror for this ecosystem? Representative HTTP checks are enough; no need to mimic a full client install.
2. **`CheckSpeed`** — Pull a bounded, typical object (index or small binary), estimate MB/s, and optionally attach a small detail struct.
3. **`CheckPackage`** — Does the named package, image, module path, or equivalent appear to exist?

Match existing return conventions: primary `bool` or `float64`, optional `*interface{}` for JSON-friendly details, and `error`. Respect the generic parameters on `MirrorService[StatusInput, SpeedInput, PackageInput]`.

## Where code lives

This module is **not** “everything in one root package.” It ships **two Go packages**:

1. **`github.com/MiravaOrg/mirava-core/pkg`** — directory [`pkg/`](./pkg). This is where **mirror implementations**, **`MirrorType`**, **`MirrorService`**, **`MiravaService`**, and **`New…MirrorService` constructors** live. New backends belong here (for example `pkg/nuget.go`).
2. **`github.com/MiravaOrg/mirava-core`** — **repository root** (`package mirava_core`). This package is intentionally thin: today it mainly exposes [`mirava.go`](mirava.go) (`CreateMiravaService`) so apps can depend on a short import path for wiring. It does **not** replace `pkg/`; it imports and assembles types from `pkg`.

| Package | Directory | Responsibility |
|---------|-----------|----------------|
| `mirava_core` | Repo root (e.g. [`mirava.go`](mirava.go)) | Entrypoint: build and return `*pkg.MiravaService`. |
| `pkg` | [`pkg/`](./pkg) | Types, `MiravaService` fields, and per-ecosystem mirror code. |

When you add or change a backend:

- Put implementation code under **`pkg/`** (for example `pkg/nuget.go`), exporting `New…MirrorService()` with the same `MirrorService[…]` shape as peers.
- Keep shared contracts in **`pkg/type.go`** unless a type clearly belongs next to one backend only.
- When the constructor should ship in the default bundle, add a field on `pkg.MiravaService` in [`pkg/type.go`](pkg/type.go) and wire it in [`mirava.go`](mirava.go) (`CreateMiravaService`).

**References:** [`pkg/apt.go`](pkg/apt.go) for structure (timeouts, validation where needed, verbose logging). [`pkg/yum.go`](pkg/yum.go) for another full backend (`NewCentOSMirrorService`) that is not on the facade yet.

## Adding a mirror backend (checklist)

1. **Issue first** for non-trivial work: mirror layout, docs, example URLs, edge cases—avoids duplicate effort.
2. **`MirrorType`** — Only extend [`pkg/type.go`](pkg/type.go) with real package or registry mirror kinds.
3. **Implement `MirrorService`** in `pkg/` (dedicated file or shared core for yum/dnf-style overlap).
4. **Facade** — Add a field to `pkg.MiravaService` in [`pkg/type.go`](pkg/type.go) and assign it in root [`mirava.go`](mirava.go) inside `CreateMiravaService()` when ready.
5. **Tests** — Prefer `httptest.Server`. Tag live-network tests with `//go:build integration` and run with `go test -tags=integration ./...`; default `go test ./...` should not depend on the public internet.

## yum vs dnf

Many RPM mirrors serve metadata that works for both clients. Shared helpers between yum and dnf backends are fine; keep exported constructors and tests easy to follow.

## Style and pull requests

- Follow naming and formatting in neighboring `pkg/` files.
- Keep PRs focused: one ecosystem, one bug, or one mechanical change.
- Describe behavior, security, and performance tradeoffs; call out **download sizes and timeouts** used in speed checks.
- Run `go test ./...` for packages your change affects.

## Questions

Open an issue on [MiravaOrg/mirava-core](https://github.com/MiravaOrg/mirava-core) and name the `MirrorType` you are working on.
