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
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/MiravaOrg/mirava-core/internal/dns"

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
	RequestError    = core.RequestError
	StatusError     = core.StatusError
	ValidationError = core.ValidationError
	ParseError      = core.ParseError
	SpeedError      = core.SpeedError
)

// AptQueryOptions narrows an apt package lookup. It is an alias for
// apt.QueryOptions, re-exported because ecosystem-specific option structs
// live in internal/ packages that other modules cannot import: callers pass
// &AptQueryOptions{...} as PackageQuery.Options when querying apt mirrors.
type AptQueryOptions = apt.QueryOptions

// Version is the latest tagged release of mirava-core. Consumers display it
// (e.g. a CLI version banner); keep in sync with the git tag.
const Version = "0.4.3"

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
	HTTPClient   *http.Client
	Logger       *slog.Logger
	DNSProviders []dns.Provider
}

// WithHTTPClient overrides the shared HTTP client.
func WithHTTPClient(c *http.Client) func(*options) { return func(o *options) { o.HTTPClient = c } }

// WithLogger sets the diagnostic logger; diagnostics are discarded by default.
func WithLogger(l *slog.Logger) func(*options) { return func(o *options) { o.Logger = l } }

// WithDNSProviders sets the default providers used by Mirava.DNSProbe.
// Nil uses the dns package defaults (System, Cloudflare, Google, Quad9).
func WithDNSProviders(ps ...dns.Provider) func(*options) {
	return func(o *options) { o.DNSProviders = ps }
}

// Mirava bundles the ecosystem backends behind one configuration. Create it
// once with [New]; the zero value is not usable.
type Mirava struct {
	dnsProviders []dns.Provider

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
		dnsProviders: o.DNSProviders,
		Apt:          apt.New(cfg),
		AptDebian:    apt.New(cfg, apt.WithDistro(apt.DistroDebian)),
		AptUbuntu:    apt.New(cfg, apt.WithDistro(apt.DistroUbuntu)),
		Npm:          npm.New(cfg),
		PyPi:         pypi.New(cfg),
		Docker:       docker.New(cfg),
		Pacman:       pacman.New(cfg),
		Go:           gomod.New(cfg),
		Cargo:        cargo.New(cfg),
		Composer:     composer.New(cfg),
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

// DNS provider shorthands for the preset public resolvers.
var (
	DNSSystem        = dns.System{}
	DNSCloudflareUDP = dns.CloudflareUDP
	DNSCloudflareDoH = dns.CloudflareDoH
	DNSCloudflareDoT = dns.CloudflareDoT
	DNSGoogleUDP     = dns.GoogleUDP
	DNSGoogleDoH     = dns.GoogleDoH
	DNSQuad9UDP      = dns.Quad9UDP
	DNSQuad9DoH      = dns.Quad9DoH
	DNSOpenDNSUDP    = dns.OpenDNSUDP
	DNSAdGuardUDP    = dns.AdGuardUDP
	DNSAdGuardDoH    = dns.AdGuardDoH
)

// CustomDNS builds a custom UDP provider (e.g. a self-hosted resolver).
func CustomDNS(addr string) dns.Provider { return dns.UDP{Addr: addr} }

// CustomDNSDoH builds a custom DoH provider from a RFC 8484 endpoint URL.
func CustomDNSDoH(url string) dns.Provider { return dns.DoH{URL: url} }

// DNSProbe resolves the mirror's hostname through each configured DNS
// provider and checks TCP reachability to the mirror via each provider's
// IPs. Diverging answers across providers suggest poisoning, filtering or
// broken geo-DNS.
//
// Pass explicit providers to override the defaults (see WithDNSProviders).
func (m *Mirava) DNSProbe(ctx context.Context, mirror string, providers ...dns.Provider) (*dns.ProbeResult, error) {
	u, err := url.Parse(strings.TrimSpace(mirror))
	if err != nil || u.Host == "" {
		return nil, &ValidationError{Field: "mirror", Message: "invalid mirror URL"}
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	ps := providers
	if ps == nil && len(m.dnsProviders) > 0 {
		ps = m.dnsProviders
	}
	return dns.Probe(ctx, host, ps, port)
}
