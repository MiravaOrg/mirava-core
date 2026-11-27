# mirava-core

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![CI](https://github.com/MiravaOrg/mirava-core/actions/workflows/ci.yml/badge.svg)](https://github.com/MiravaOrg/mirava-core/actions/workflows/ci.yml)

Go library for probing **package and container mirrors**: whether they respond, how fast they move bytes, whether a given package is present, and — for embedding in a website — a direct download URL.

It also resolves mirrors through **multiple DNS providers** (UDP, DoH, DoT, system) and flags diverging answers, a signal for poisoning, filtering or broken geo-DNS.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	mirava "github.com/MiravaOrg/mirava-core"
)

func main() {
	m := mirava.New()
	ctx := context.Background()

	// One interface for every ecosystem.
	p, err := m.Prober(mirava.TypeApt)
	if err != nil {
		log.Fatal(err)
	}

	status, err := p.Status(ctx, "https://archive.ubuntu.com/ubuntu/")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("status:", status.OK, status.WorkingPath)

	speed, err := p.Speed(ctx, "https://archive.ubuntu.com/ubuntu/", mirava.SpeedOptions{
		Duration: 5 * 1000 * 1000 * 1000, // 5s download window
		OnProgress: func(pr mirava.Progress) {
			fmt.Printf("\r%.1f MB/s", pr.SpeedMBs)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("speed: %.2f MB/s (%s)\n", speed.SpeedMBps, speed.Rating)

	pkg, err := p.Package(ctx, "https://archive.ubuntu.com/ubuntu/", mirava.PackageQuery{Name: "curl"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("curl:", pkg.Version, "found:", pkg.Found)

	// Direct download URL for a website client.
	dl, err := p.Resolve(ctx, "https://archive.ubuntu.com/ubuntu/", mirava.PackageQuery{Name: "curl"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("download:", dl.URL, dl.Size, dl.SHA256)

	// DNS-provider probing: how does each resolver see this mirror?
	probe, err := m.DNSProbe(ctx, "https://archive.ubuntu.com/ubuntu/",
		mirava.DNSSystem, mirava.DNSCloudflareUDP, mirava.DNSGoogleUDP)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("dns divergence:", probe.Divergence)
}
```

## The Prober interface

Every backend implements the same four probes. Results share a common
envelope (`type`, `mirror`, `checked_at`, `latency`, `ok`) with
ecosystem-specific detail payloads. A missing package is a **result**
(`Found == false`), not an error.

| Probe | Role |
|-------|------|
| `Status` | Mirror is reachable and behaves like the expected ecosystem. |
| `Speed` | Download throughput (`SpeedOptions.Duration` bounds the sample; `OnProgress` streams updates). |
| `Package` | Whether a package exists, with version + ecosystem detail. |
| `Resolve` | Direct download URL (issues any intermediate requests itself), plus size, SHA256 and required headers. |

Ecosystem-specific query options ride in `PackageQuery.Options`, e.g.
`*apt.QueryOptions{Suite, Component, Arch}`.

## Mirror types

| Type | Ecosystem | Backend |
|------|-----------|---------|
| `apt` | Debian/Ubuntu `.deb` repos | `internal/apt` |
| `pacman` | Arch Linux mirrors | `internal/pacman` |
| `npm` | npm registry mirrors | `internal/npm` |
| `go` | Go module proxies (GOPROXY) | `internal/gomod` |
| `cargo` | crates.io sparse index | `internal/cargo` |
| `pypi` | PEP 503 simple indexes | `internal/pypi` |
| `docker` | OCI / Docker registries | `internal/docker` |
| `composer` | Composer / Packagist | `internal/composer` |
| `yum`, `dnf`, `nuget` | — | not implemented yet |

`m.Apt`, `m.AptDebian` and `m.AptUbuntu` are distro-hinted variants of the
apt backend.

## DNS provider probing

```go
probe, err := m.DNSProbe(ctx, mirror,
	mirava.DNSSystem,
	mirava.DNSCloudflareUDP,
	mirava.DNSCloudflareDoH,
	mirava.CustomDNS("10.0.0.53"),
)
```

Each provider reports its resolved IPs, resolution RTT and TCP
reachability to the mirror. When providers disagree on IPs,
`ProbeResult.Divergence` says so.

## Caching

Backends share a two-tier cache: a bounded in-memory LRU over an atomic
disk store in the OS cache directory (e.g. `~/Library/Caches/mirava-core`).
Design points:

- **Content-addressed blobs.** apt indexes are stored by SHA256, so every
  mirror of the same distro shares one download.
- **Hash-verified downloads.** A checksum mismatch is reported, not served.
- **Parse once.** Each index is parsed a single time per blob; lookups
  afterwards are map lookups.
- **Atomic writes** (temp file + rename) — safe for concurrent processes.
- **Singleflight dedup** — concurrent lookups of the same index download once.
- **No TTL extension** on disk→memory promotion.

Bring your own store via a `cache.Store` implementation to share caches
across instances (Redis, S3, …).

## Development

```bash
make test        # unit tests (-race)
make integration # live-mirror tests (network required)
make cover       # coverage report
```

Unit tests are offline (`httptest` fixtures). Live-mirror tests live behind
the `integration` build tag:

```bash
go test -tags=integration ./...
```

## Layout

| Path | Role |
|------|------|
| `mirava.go` | `New()` — wiring, re-exported public API |
| `internal/core` | `Prober` interface, result envelopes, shared errors, backend `Config` |
| `internal/httpx` | URL joining, shared client, User-Agent, single speed-measure loop |
| `internal/cache` | `Store` interface, LRU, atomic disk layer, singleflight |
| `internal/apt` | InRelease-driven apt backend (reference implementation) |
| `internal/<eco>` | One backend package per ecosystem |
| `internal/dns` | DNS providers (UDP / DoH / DoT / system) and mirror probing |

## Adding a backend

1. Create `internal/<eco>` implementing the four `core.Prober` methods.
2. Add `QueryOptions` / `Detail` types in your package.
3. Wire it into `Mirava` and `Prober()` in `mirava.go`.
4. Add `httptest` fixtures under `internal/backends_test`.

See [CONTRIBUTING.md](CONTRIBUTING.md).

---

Maintained by [MiravaOrg](https://github.com/MiravaOrg).
