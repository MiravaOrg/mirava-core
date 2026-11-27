# mirava-core

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![CI](https://github.com/MiravaOrg/mirava-core/actions/workflows/ci.yml/badge.svg)](https://github.com/MiravaOrg/mirava-core/actions/workflows/ci.yml)

Go library for probing **package and container mirrors**: whether they respond, how fast they move bytes, and whether a given name looks present on the mirror.

Higher-level apps get a **single pattern** via `MirrorService` implementations for apt, npm, PyPI, Docker, and more. There is no CLI here and no catalog file loader—only types and HTTP-based probe logic.

| Item | Value |
|------|-------|
| Module | `github.com/MiravaOrg/mirava-core` |
| Root package | `mirava` — entrypoint (`CreateMiravaService`) |
| Library package | [`pkg`](./pkg) — types, `MiravaService`, and per-ecosystem backends |
| Go | 1.26+ |

## Mirror kinds (`MirrorType`)

Constants and the `MirrorService` interface live in [`pkg/type.go`](pkg/type.go). Each value is an **artifact or index mirror** used by a package manager or registry—not a generic CDN URL.

| Type | Ecosystem | In this repo |
|------|-----------|--------------|
| **apt-debian** | Debian `.deb` repositories | Implemented (`pkg.NewAptDebianMirrorService`) |
| **apt-ubuntu** | Ubuntu `.deb` repositories | Implemented (`pkg.NewAptUbuntuMirrorService`) |
| **apt** | Generic Debian/Ubuntu `.deb` repositories | Implemented (`pkg.NewAptMirrorService`) |
| **yum** | RPM-style mirrors (CentOS/EPEL-style paths) | Not implemented |
| **dnf** | DNF-compatible RPM mirrors | Not implemented (often shares yum layout) |
| **pacman** | Arch Linux mirrors | Implemented (`pkg.NewPacmanMirrorService`) |
| **npm** | npm registry mirrors | Implemented (`pkg.NewNpmMirrorService`) |
| **go** | Go module proxy / sumdb | Implemented (`pkg.NewGoMirrorService`) |
| **cargo** | crates.io / sparse index | Implemented (`pkg.NewCargoMirrorService`) |
| **pypi** | PEP 503–style simple indexes | Implemented (`pkg.NewPyPIMirrorService`) |
| **nuget** | NuGet V3 feeds | Not implemented |
| **docker** | OCI / Docker registry mirrors | Implemented (`pkg.NewDockerMirrorService`) |
| **composer** | Composer / Packagist | Implemented (`pkg.NewComposerMirrorService`) |

See [CONTRIBUTING.md](CONTRIBUTING.md) to add or finish a backend.

## Install

```bash
go get github.com/MiravaOrg/mirava-core@latest
```

## Quick start

Use the root package for the default wiring. Import [`pkg`](./pkg) for parameter types and for constructing services directly when you do not use the facade.

```go
package main

import (
	"fmt"
	"log"

	mirava "github.com/MiravaOrg/mirava-core"
	"github.com/MiravaOrg/mirava-core/pkg"
)

func main() {
	m := mirava.CreateMiravaService()

	mbps, info, err := m.Apt.CheckSpeed("https://archive.ubuntu.com/ubuntu/", 15, false)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Speed: %.2f MB/s\n", mbps)
	_ = info

	ok, pkgInfo, err := m.Apt.CheckPackage(
		"https://archive.ubuntu.com/ubuntu/",
		"curl",
		false,
		pkg.AptCheckPackageParams{
			Release:   "jammy",
			Component: "main",
			Arch:      "amd64",
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("package found:", ok)
	_ = pkgInfo
}
```

`CreateMiravaService` returns `*pkg.MiravaService` with **apt**, **apt-debian**, **apt-ubuntu**, **npm**, **pypi**, **docker**, **pacman**, **go**, **cargo**, and **composer** set. Backends for **yum**, **dnf**, and **nuget** are not implemented yet—see [CONTRIBUTING.md](CONTRIBUTING.md) to add one.

- npm speed checks: optional `*pkg.NpmCheckSpeedParams`
- Docker speed checks: `*pkg.DockerSpeedParams` where applicable  
- Pass `nil` for unused generic parameters.

## API surface

Every backend service (`pkg.*MirrorService`) provides the same three methods; signatures vary per ecosystem where a param struct is useful (for example `pkg.AptCheckPackageParams`):

| Method | Role |
|--------|------|
| `CheckStatus` | Mirror is reachable and behaves like the expected ecosystem. |
| `CheckSpeed` | Rough download throughput (MB/s) plus optional detail payload. |
| `CheckPackage` | Whether the given name appears present (exact meaning is backend-specific). |

## Development

```bash
go test ./...
```

Prefer **offline** tests (`httptest`, small fixtures). Network-heavy checks should use a build tag such as `integration` and be documented next to the test (`go test -tags=integration ./...`).

## Layout

| Path | Role |
|------|------|
| [`mirava.go`](mirava.go) | `CreateMiravaService()` — wires `pkg.MiravaService` |
| [`pkg/type.go`](pkg/type.go) | `MirrorType` constants and `MiravaService` bundle |
| [`pkg/apt/`](pkg/apt/), [`pkg/npm.go`](pkg/npm.go), … | Mirror probe implementations |
| [`pkg/error.go`](pkg/error.go) | Shared error types (aliases into `pkg/apt`) |

---

Maintained by [MiravaOrg](https://github.com/MiravaOrg). Pull requests that extend or harden backends are welcome—see [CONTRIBUTING.md](CONTRIBUTING.md).
