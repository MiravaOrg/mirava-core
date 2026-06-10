# mirava-core

Go library for probing **package and container mirrors**: whether they respond, how fast they move bytes, and whether a given name looks present on the mirror.

Higher-level apps get a **single pattern** via `MirrorService` implementations for apt, npm, PyPI, Docker, and more. There is no CLI here and no catalog file loader—only types and HTTP-based probe logic.

| Item | Value |
|------|-------|
| Module | `github.com/MiravaOrg/mirava-core` |
| Root package | `mirava_core` — entrypoint (`CreateMiravaService`) |
| Library package | [`pkg`](./pkg) — types, `MiravaService`, and per-ecosystem backends |
| Go | 1.26+ |

## Mirror kinds (`MirrorType`)

Constants and the `MirrorService` interface live in [`pkg/type.go`](pkg/type.go). Each value is an **artifact or index mirror** used by a package manager or registry—not a generic CDN URL.

| Type | Ecosystem | In this repo |
|------|-----------|--------------|
| **apt** | Debian/Ubuntu `.deb` repositories | Implemented (`pkg.NewAptMirrorService`) |
| **yum** | RPM-style mirrors (CentOS/EPEL-style paths) | Not implemented |
| **dnf** | DNF-compatible RPM mirrors | Not implemented (often shares yum layout) |
| **pacman** | Arch Linux mirrors | Not implemented |
| **npm** | npm registry mirrors | Implemented (`pkg.NewNpmMirrorService`) |
| **go** | Go module proxy / sumdb | Stub (`pkg/go.go`) |
| **cargo** | crates.io / sparse index | Stub (`pkg/cargo.go`) |
| **pypi** | PEP 503–style simple indexes | Implemented (`pkg.NewPyPIMirrorService`) |
| **nuget** | NuGet V3 feeds | Not implemented |
| **docker** | OCI / Docker registry mirrors | Implemented (`pkg.NewDockerMirrorService`) |
| **composer** | Composer / Packagist | Stub (`pkg/composer.go`) |

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

`CreateMiravaService` returns `*pkg.MiravaService` with **apt**, **npm**, **pypi**, and **docker** set. Other constructors (for example `pkg.NewCentOSMirrorService`) are available from `pkg` until they are added to the facade.

- npm speed checks: optional `*pkg.NpmCheckSpeedParams`
- Docker speed checks: `*pkg.DockerSpeedParams` where applicable  
- Pass `nil` for unused generic parameters.

## API surface

Every `MirrorService` provides:

| Method | Role |
|--------|------|
| `CheckStatus` | Mirror is reachable and behaves like the expected ecosystem. |
| `CheckSpeed` | Rough download throughput (MB/s) plus optional detail payload. |
| `CheckPackage` | Whether the given name appears present (exact meaning is backend-specific). |

`MirrorService` is generic over optional per-method inputs; many backends use `*interface{}` or dedicated param structs.

## Development

```bash
go test ./...
```

Prefer **offline** tests (`httptest`, small fixtures). Network-heavy checks should use a build tag such as `integration` and be documented next to the test (`go test -tags=integration ./...`).

## Layout

| Path | Role |
|------|------|
| [`mirava.go`](mirava.go) | `CreateMiravaService()` — wires `pkg.MiravaService` |
| [`pkg/type.go`](pkg/type.go) | `MirrorType`, `MirrorService`, `MiravaService` |
| [`pkg/apt/`](pkg/apt/), [`pkg/npm.go`](pkg/npm.go), … | Mirror probe implementations |
| [`pkg/yum.go`](pkg/yum.go) | RPM-style mirror probing (`NewCentOSMirrorService`) |
| `error.go`, `utils.go` | Root-package helpers (placeholders today) |

---

Maintained by [MiravaOrg](https://github.com/MiravaOrg). Pull requests that extend or harden backends are welcome—see [CONTRIBUTING.md](CONTRIBUTING.md).
