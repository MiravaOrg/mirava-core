package core

import (
	"context"
	"encoding/json"
	"time"
)

// MirrorType identifies an ecosystem. Values match the string names used
// across mirava's CLI, website and API.
type MirrorType string

// Supported mirror ecosystems.
const (
	TypeApt      MirrorType = "apt"      // Debian/Ubuntu .deb repositories
	TypeYum      MirrorType = "yum"      // RHEL-family RPM repositories (unimplemented)
	TypeDnf      MirrorType = "dnf"      // DNF-compatible RPM mirrors (unimplemented)
	TypePacman   MirrorType = "pacman"   // Arch Linux mirrors
	TypeNpm      MirrorType = "npm"      // npm registry mirrors
	TypeGo       MirrorType = "go"       // Go module proxy / sumdb mirrors
	TypeCargo    MirrorType = "cargo"    // crates.io sparse index mirrors
	TypePypi     MirrorType = "pypi"     // PEP 503 simple index mirrors
	TypeNuget    MirrorType = "nuget"    // NuGet V3 feeds (unimplemented)
	TypeDocker   MirrorType = "docker"   // OCI / Docker registry mirrors
	TypeComposer MirrorType = "composer" // Composer / Packagist mirrors
)

// Implemented reports whether the mirror type has a backend in this module.
func (t MirrorType) Implemented() bool {
	switch t {
	case TypeApt, TypePacman, TypeNpm, TypeGo, TypeCargo, TypePypi, TypeDocker, TypeComposer:
		return true
	}
	return false
}

// Prober is the uniform contract every backend implements. One interface,
// four probes, ecosystem-specific details carried in Options payloads.
type Prober interface {
	// Status checks that the mirror is reachable and behaves like the
	// expected ecosystem.
	Status(ctx context.Context, mirror string) (StatusResult, error)
	// Speed measures download throughput from the mirror.
	Speed(ctx context.Context, mirror string, o SpeedOptions) (SpeedResult, error)
	// Package looks up a package on the mirror. A missing package is a
	// result (Found == false), not an error.
	Package(ctx context.Context, mirror string, q PackageQuery) (PackageResult, error)
	// Resolve returns a direct download URL for the package, issuing any
	// intermediate requests required (index lookups, registry tokens).
	Resolve(ctx context.Context, mirror string, q PackageQuery) (DownloadResult, error)
}

// PackageQuery names what to look up. Options carries an ecosystem-specific
// struct, e.g. *apt.QueryOptions{Suite, Component, Arch} — see each backend
// package for its type.
type PackageQuery struct {
	Name    string
	Version string // optional; exact version or ecosystem constraint
	Options any
}

// SpeedOptions configures a speed probe.
type SpeedOptions struct {
	// Duration bounds the download window. Zero uses the backend default
	// (10s). The overall deadline still comes from ctx.
	Duration time.Duration
	// MinBytes is the smallest sample considered valid; shorter downloads
	// yield an error rather than an inflated speed.
	MinBytes int64
	// TestFile overrides the backend's default test file (path relative to
	// the mirror URL). Empty uses the backend default.
	TestFile string
	// OnProgress receives incremental progress at most every ~200ms.
	OnProgress func(Progress)
	// Package, when non-empty, measures speed by downloading a file for
	// that specific package instead of the backend's generic test file.
	Package string
}

// Progress reports incremental speed-test state.
type Progress struct {
	Bytes    int64
	Total    int64 // 0 when unknown
	Elapsed  time.Duration
	SpeedMBs float64
}

// Envelope is embedded in every result so consumers get common metadata
// without type switches.
type Envelope struct {
	Type      MirrorType   `json:"type"`
	Mirror    string       `json:"mirror"`
	CheckedAt time.Time    `json:"checked_at"`
	Latency   Duration     `json:"latency"`
	OK        bool         `json:"ok"`
	Detail    ResultDetail `json:"detail,omitempty"`
}

// Duration is time.Duration with JSON string encoding ("150ms") instead of
// a bare integer, so website clients can read it directly.
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// ResultDetail is implemented by backend-specific payloads so a result can
// be re-expanded by consumers that know the ecosystem.
type ResultDetail interface{ detailType() MirrorType }

// StatusResult reports mirror reachability.
type StatusResult struct {
	Envelope
	// WorkingPath is the first URL that answered successfully.
	WorkingPath string   `json:"working_path,omitempty"`
	StatusCode  int      `json:"status_code,omitempty"`
	Message     string   `json:"message,omitempty"`
	TestedPaths []string `json:"tested_paths"`
}

// SpeedResult reports measured throughput.
type SpeedResult struct {
	Envelope
	SpeedMBps     float64 `json:"speed_mbps"`
	Rating        string  `json:"rating"` // Slow / Average / Good / Excellent
	Bytes         int64   `json:"bytes"`
	DownloadedMB  float64 `json:"downloaded_mb"`
	DurationSec   float64 `json:"duration_sec"`
	TestURL       string  `json:"test_url"`
	ContentLength int64   `json:"content_length"`
}

// PackageResult reports whether a package exists, with its metadata.
type PackageResult struct {
	Envelope
	Found        bool     `json:"found"`
	Name         string   `json:"name"`
	Version      string   `json:"version,omitempty"`
	Detail       any      `json:"detail,omitempty"` // ecosystem payload, e.g. apt.PackageDetail
	CheckedPaths []string `json:"checked_paths,omitempty"`
	FoundPath    string   `json:"found_path,omitempty"`
}

// DownloadResult is a direct download URL plus everything a client needs to
// fetch it. Some ecosystems (Docker) require headers on the blob request.
type DownloadResult struct {
	Envelope
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
	Size     int64             `json:"size,omitempty"`
	SHA256   string            `json:"sha256,omitempty"`
	Kind     string            `json:"kind,omitempty"` // blob, tarball, archive, ...
	Filename string            `json:"filename,omitempty"`
}

// SpeedRating maps MB/s to a coarse label.
func SpeedRating(mbps float64) string {
	switch {
	case mbps > 10:
		return "Excellent"
	case mbps > 5:
		return "Good"
	case mbps > 2:
		return "Average"
	default:
		return "Slow"
	}
}
