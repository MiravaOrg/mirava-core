// Package apt implements the apt mirror backend: Debian and Ubuntu
// repositories.
//
// The backend is built around repository metadata rather than TTL guesses.
// Each suite's small InRelease file lists the SHA256 and size of every
// Packages index, plus Codename, Suite, Components and Architectures.
// Indexes are stored in a content-addressed blob cache (keyed by SHA256),
// so healthy mirrors of the same distro share one download, and each index
// is parsed once into a compact lookup map.
package apt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// Distro identifies a distribution family for status/speed defaults and
// index discovery hints.
type Distro string

// Distro values.
const (
	DistroAny    Distro = ""
	DistroDebian Distro = "debian"
	DistroUbuntu Distro = "ubuntu"
)

// QueryOptions narrows an apt package lookup.
type QueryOptions struct {
	// Suite is a suite name ("noble", "bookworm-updates") or codename.
	// Empty scans all discovered suites and reports the latest codename.
	Suite     string
	Component string // "main", "universe", ... Empty = any (wave order).
	Arch      string // "amd64", "arm64", ... Empty = any arch listed.
}

// PackageDetail is apt's PackageResult.Detail payload.
type PackageDetail struct {
	Suite     string `json:"suite"`
	Component string `json:"component"`
	Arch      string `json:"arch"`
	Filename  string `json:"filename,omitempty"`
	IndexPath string `json:"index_path,omitempty"`
}

func (PackageDetail) detailType() core.MirrorType { return core.TypeApt }

// ConfigOption customizes a Service at construction.
type ConfigOption func(*Service)

// WithDistro sets the distro family hint.
func WithDistro(d Distro) ConfigOption { return func(s *Service) { s.distro = d } }

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for apt repositories.
type Service struct {
	distro Distro
	cfg    core.Config
	idx    *indexDB
}

// New builds an apt Service.
func New(cfg core.Config, opts ...ConfigOption) *Service {
	s := &Service{cfg: cfg}
	for _, o := range opts {
		o(s)
	}
	s.idx = newIndexDB(cfg.Store)
	return s
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

// envelope stamps the common fields onto a result.
func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypeApt, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

// Status checks the mirror by probing dists/<suite>/Release paths.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}

	suites := s.statusSuites()
	join := func(suite, file string) string {
		u, err := httpx.JoinURL(mirror, "dists", suite, file)
		if err != nil {
			return ""
		}
		return u
	}

	for _, suite := range suites {
		for _, file := range []string{"InRelease", "Release"} {
			u := join(suite, file)
			if u == "" {
				continue
			}
			res.TestedPaths = append(res.TestedPaths, u)

			startReq := time.Now()
			code, err := s.probeStatus(ctx, u)
			if err != nil && ctx.Err() != nil {
				return res, core.AsError(ctx.Err())
			}
			if err == nil && (code == 200 || (code >= 300 && code < 400)) {
				res.OK = true
				res.WorkingPath = u
				res.StatusCode = code
				res.Envelope.Latency = core.Duration(time.Since(startReq))
				res.Message = "Mirror is healthy and responding"
				return res, nil
			}
		}
	}

	res.Message = "Mirror not responding or not a valid apt mirror"
	return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror, StatusCode: res.StatusCode}
}

// statusSuites lists suites to try, most likely first.
func (s *Service) statusSuites() []string {
	switch s.distro {
	case DistroDebian:
		return []string{"stable", "trixie", "bookworm"}
	case DistroUbuntu:
		return []string{"noble", "jammy"}
	default:
		return []string{"noble", "stable", "trixie", "jammy"}
	}
}

func (s *Service) probeStatus(ctx context.Context, u string) (int, error) {
	req, err := httpx.NewRequest(ctx, http.MethodGet, u)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	// HEAD-equivalent: we only need the status code, so use HEAD first and
	// fall back to GET if a mirror mishandles it.
	resp, err := s.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close
	_, _ = io.Copy(io.Discard, resp.Body)    //nolint:errcheck // drain for keep-alive reuse
	return resp.StatusCode, nil
}

// Speed measures throughput by downloading the mirror's ls-lR.gz (a large
// file every Debian-style mirror ships).
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}

	rel := o.TestFile
	if rel == "" {
		rel = "ls-lR.gz"
	}
	u, err := httpx.JoinURL(mirror, rel)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	res.TestURL = u

	duration := o.Duration
	if duration <= 0 {
		duration = DefaultSpeedDuration
	}

	var progressFn func(httpx.Progress)
	if o.OnProgress != nil {
		progressFn = func(p httpx.Progress) { o.OnProgress(core.Progress(p)) }
	}
	mbps, bytes, elapsed, _, mErr := httpx.MeasureSpeed(ctx, s.client(), u, httpx.MeasureOptions{
		Duration:   duration,
		MinBytes:   o.MinBytes,
		OnProgress: progressFn,
	})
	res.Bytes = bytes
	res.DownloadedMB = float64(bytes) / 1024 / 1024
	res.DurationSec = elapsed.Seconds()
	res.Envelope.Latency = core.Duration(elapsed)

	if mErr != nil {
		if ctx.Err() != nil && elapsed >= duration {
			// Deadline reached mid-download: the sample is still usable.
			if bytes >= maxInt64(1, o.MinBytes) {
				res.SpeedMBps = mbps
				res.Rating = core.SpeedRating(mbps)
				res.Envelope.OK = true
				return res, nil
			}
		}
		res.Envelope.OK = false
		return res, &core.SpeedError{URL: u, Bytes: bytes, Elapsed: elapsed, Err: mErr}
	}

	res.SpeedMBps = mbps
	res.Rating = core.SpeedRating(mbps)
	res.Envelope.OK = true
	return res, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Package implements core.Prober.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	start := time.Now()
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}

	if strings.TrimSpace(q.Name) == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}

	opts, err := parseQueryOptions(q.Options)
	if err != nil {
		return res, err
	}

	cand, err := s.lookup(ctx, mirror, q.Name, q.Version, opts)
	if err != nil {
		return res, err
	}

	if cand == nil {
		res.Envelope.Latency = core.Duration(time.Since(start))
		return res, nil // Found == false, no error.
	}

	res.Found = true
	res.Version = cand.Version
	res.FoundPath = cand.IndexPath
	res.Detail = PackageDetail{
		Suite:     cand.Suite,
		Component: cand.Component,
		Arch:      cand.Arch,
		Filename:  cand.Filename,
		IndexPath: cand.IndexPath,
	}
	res.Envelope.Latency = core.Duration(time.Since(start))
	return res, nil
}

// Resolve implements core.Prober: return the direct .deb URL.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "deb"}

	if strings.TrimSpace(q.Name) == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}
	opts, err := parseQueryOptions(q.Options)
	if err != nil {
		return res, err
	}

	cand, err := s.lookup(ctx, mirror, q.Name, q.Version, opts)
	if err != nil {
		return res, err
	}
	if cand == nil || cand.Filename == "" {
		return res, fmt.Errorf("%w: no downloadable file for %q", core.ErrNotFound, q.Name)
	}

	poolURL, err := httpx.JoinURL(mirror, cand.Filename)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}

	res.URL = poolURL
	res.Filename = cand.Filename
	res.Size = cand.Size
	res.SHA256 = cand.SHA256
	res.Envelope.OK = true
	return res, nil
}

func parseQueryOptions(raw any) (*QueryOptions, error) {
	if raw == nil {
		return &QueryOptions{}, nil
	}
	switch v := raw.(type) {
	case *QueryOptions:
		if v == nil {
			return &QueryOptions{}, nil
		}
		return v, nil
	case QueryOptions:
		return &v, nil
	default:
		return nil, &core.ValidationError{Field: "options", Message: "expected *apt.QueryOptions"}
	}
}
