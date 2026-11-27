// Package mirava is the entrypoint for mirava-core: a library for probing
// package and container mirrors — whether they respond, how fast they move
// bytes, whether a package is present, and (for the website) a direct
// download URL.
//
// Construct once with [New], then use [Mirava.Prober] for the uniform
// per-ecosystem interface:
//
//	m := mirava.New()
//	p, _ := m.Prober(mirava.TypeApt)
//	res, err := p.Status(ctx, "https://archive.ubuntu.com/ubuntu/")
package mirava

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/MiravaOrg/mirava-core/internal/apt"
	"github.com/MiravaOrg/mirava-core/internal/cargo"
	"github.com/MiravaOrg/mirava-core/internal/composer"
	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/docker"
	"github.com/MiravaOrg/mirava-core/internal/gomod"
	"github.com/MiravaOrg/mirava-core/internal/npm"
	"github.com/MiravaOrg/mirava-core/internal/pacman"
	"github.com/MiravaOrg/mirava-core/internal/pypi"
)

// Re-exported API. The interface, result types and errors live in
// internal/core and are re-exported here as the public surface.
type (
	Prober          = core.Prober
	PackageQuery    = core.PackageQuery
	SpeedOptions    = core.SpeedOptions
	Progress        = core.Progress
	Envelope        = core.Envelope
	Duration        = core.Duration
	ResultDetail    = core.ResultDetail
	StatusResult    = core.StatusResult
	SpeedResult     = core.SpeedResult
	PackageResult   = core.PackageResult
	DownloadResult  = core.DownloadResult
	MirrorType      = core.MirrorType
	Options         = core.Options
	RequestError    = core.RequestError
	StatusError     = core.StatusError
	ValidationError = core.ValidationError
	ParseError      = core.ParseError
	SpeedError      = core.SpeedError
)

// Mirror type constants.
const (
	TypeApt      = core.TypeApt
	TypeYum      = core.TypeYum
	TypeDnf      = core.TypeDnf
	TypePacman   = core.TypePacman
	TypeNpm      = core.TypeNpm
	TypeGo       = core.TypeGo
	TypeCargo    = core.TypeCargo
	TypePypi     = core.TypePypi
	TypeNuget    = core.TypeNuget
	TypeDocker   = core.TypeDocker
	TypeComposer = core.TypeComposer
)

// Sentinel errors.
var (
	ErrNotFound       = core.ErrNotFound
	ErrTimeout        = core.ErrTimeout
	ErrInvalidMirror  = core.ErrInvalidMirror
	ErrUnreachable    = core.ErrUnreachable
	ErrNotImplemented = core.ErrNotImplemented
)

// SpeedRating maps MB/s to a coarse label (Slow/Average/Good/Excellent).
var SpeedRating = core.SpeedRating

// Options configures a Mirava instance.
type options struct {
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// WithHTTPClient overrides the shared HTTP client.
func WithHTTPClient(c *http.Client) func(*options) { return func(o *options) { o.HTTPClient = c } }

// WithLogger sets the diagnostic logger; diagnostics are discarded by default.
func WithLogger(l *slog.Logger) func(*options) { return func(o *options) { o.Logger = l } }

// Mirava bundles the ecosystem backends behind one configuration. Create it
// once with [New]; the zero value is not usable.
type Mirava struct {
	Apt       *apt.Service
	AptDebian *apt.Service
	AptUbuntu *apt.Service
	Npm       *npm.Service
	PyPi      *pypi.Service
	Docker    *docker.Service
	Pacman    *pacman.Service
	Go        *gomod.Service
	Cargo     *cargo.Service
	Composer  *composer.Service
}

// New builds a Mirava instance with defaults (shared HTTP client, silent
// logger, memory+disk cache) overridden by opts.
func New(opts ...func(*options)) *Mirava {
	o := options{}
	for _, fn := range opts {
		fn(&o)
	}
	cfg := core.Config{HTTPClient: o.HTTPClient, Logger: o.Logger}

	return &Mirava{
		Apt:       apt.New(cfg),
		AptDebian: apt.New(cfg, apt.WithDistro(apt.DistroDebian)),
		AptUbuntu: apt.New(cfg, apt.WithDistro(apt.DistroUbuntu)),
		Npm:       npm.New(cfg),
		PyPi:      pypi.New(cfg),
		Docker:    docker.New(cfg),
		Pacman:    pacman.New(cfg),
		Go:        gomod.New(cfg),
		Cargo:     cargo.New(cfg),
		Composer:  composer.New(cfg),
	}
}

// Prober returns the backend for a mirror type. Unimplemented types return
// an error wrapping ErrNotImplemented.
func (m *Mirava) Prober(t MirrorType) (Prober, error) {
	switch t {
	case TypeApt:
		return m.Apt, nil
	case TypeNpm:
		return m.Npm, nil
	case TypePypi:
		return m.PyPi, nil
	case TypeDocker:
		return m.Docker, nil
	case TypePacman:
		return m.Pacman, nil
	case TypeGo:
		return m.Go, nil
	case TypeCargo:
		return m.Cargo, nil
	case TypeComposer:
		return m.Composer, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrNotImplemented, t)
	}
}
