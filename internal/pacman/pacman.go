// Package pacman implements the Arch Linux mirror backend.
//
// Arch mirrors serve repo databases at <mirror>/<repo>/os/<arch>/<repo>.db
// — a gzip'd tar of per-package desc entries. The backend scans core and
// extra by default; QueryOptions.Repository selects another repo.
package pacman

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// Detail is pacman's PackageResult.Detail payload.
type Detail struct {
	Repository string `json:"repository"`
	Arch       string `json:"arch"`
}

func (Detail) detailType() core.MirrorType { return core.TypePacman }

// ResolveOptions customizes pacman Resolve.
type ResolveOptions struct {
	// Repository is the repo to resolve in (core, extra, multilib...).
	Repository string
	// Arch is the machine architecture (x86_64 by default).
	Arch string
}

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for pacman mirrors.
type Service struct {
	cfg core.Config
}

// New builds a pacman Service.
func New(cfg core.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypePacman, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

func defaultArch() string {
	// Arch Linux calls amd64 "x86_64" and almost every mirror hosts the
	// x86_64 tree; aarch64 mirrors are rare. Default to x86_64 regardless
	// of the host GOARCH — a Mac probing an Arch mirror should query what
	// the mirror serves, not what machine it runs on.
	return "x86_64"
}

// Status probes <repo>/os/<arch>/ directory listings.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}
	for _, path := range []string{"core/os/x86_64", "extra/os/x86_64"} {
		u, err := httpx.JoinURL(mirror, path, "")
		if err != nil {
			continue
		}
		res.TestedPaths = append(res.TestedPaths, u)
		req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
		if rerr != nil {
			continue
		}
		start := time.Now()
		resp, derr := s.client().Do(req)
		if derr != nil {
			if ctx.Err() != nil {
				return res, core.AsError(ctx.Err())
			}
			continue
		}
		_, _ = httpx.ReadAll(resp)
		if resp.StatusCode == http.StatusOK || (resp.StatusCode >= 300 && resp.StatusCode < 400) {
			res.OK = true
			res.WorkingPath = u
			res.StatusCode = resp.StatusCode
			res.Envelope.Latency = core.Duration(time.Since(start))
			res.Message = "Mirror is healthy and responding"
			return res, nil
		}
	}
	res.Message = "Mirror not responding or not a valid pacman mirror"
	return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
}

// Speed measures throughput by downloading the extra repo database.
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}
	rel := o.TestFile
	if rel == "" {
		rel = "extra/os/x86_64/extra.db"
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
	mbps, bytes, elapsed, _, mErr := httpx.MeasureSpeed(ctx, s.client(), u, httpx.MeasureOptions{
		Duration:   duration,
		MinBytes:   o.MinBytes,
		OnProgress: nil,
	})
	res.Bytes = bytes
	res.DownloadedMB = float64(bytes) / 1024 / 1024
	res.DurationSec = elapsed.Seconds()
	res.Envelope.Latency = core.Duration(elapsed)
	if mErr != nil {
		// Repo DBs are 5–50 MB; a timeout-shortened sample is still usable.
		if bytes >= 1024*1024 {
			res.SpeedMBps = mbps
			res.Rating = core.SpeedRating(mbps)
			res.Envelope.OK = true
			return res, nil
		}
		res.Envelope.OK = false
		return res, &core.SpeedError{URL: u, Bytes: bytes, Elapsed: elapsed, Err: mErr}
	}
	res.SpeedMBps = mbps
	res.Rating = core.SpeedRating(mbps)
	res.Envelope.OK = true
	return res, nil
}

// QueryOptions customizes pacman Package lookups.
type QueryOptions struct {
	Repository string // default "core"
	Arch       string // default: GOARCH mapping
}

// fetchDB downloads and decompresses a repo database.
func (s *Service) fetchDB(ctx context.Context, mirror, repo, arch string) ([]byte, error) {
	u, err := httpx.JoinURL(mirror, repo, "os", arch, repo+".db")
	if err != nil {
		return nil, err
	}
	req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
	if rerr != nil {
		return nil, &core.RequestError{URL: u, Err: rerr}
	}
	resp, derr := s.client().Do(req)
	if derr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &core.RequestError{URL: u, Err: derr}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &core.StatusError{URL: u, StatusCode: resp.StatusCode}
	}
	gz, gerr := gzip.NewReader(resp.Body)
	if gerr != nil {
		return nil, &core.ParseError{URL: u, Kind: "gzip", Err: gerr}
	}
	defer func() { _ = gz.Close() }()
	data, terr := io.ReadAll(gz)
	if terr != nil {
		return nil, &core.ParseError{URL: u, Kind: "tar", Err: terr}
	}
	return data, nil
}

// Package scans the repo database for a package's desc entry.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}
	name := strings.TrimSpace(q.Name)
	if name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}
	opts := QueryOptions{}
	if q.Options != nil {
		if o, ok := q.Options.(*QueryOptions); ok && o != nil {
			opts = *o
		}
	}
	repo := opts.Repository
	if repo == "" {
		repo = "core"
	}
	arch := opts.Arch
	if arch == "" {
		arch = defaultArch()
	}

	data, err := s.fetchDB(ctx, mirror, repo, arch)
	if err != nil {
		if se, ok := err.(*core.StatusError); ok && se.StatusCode == http.StatusNotFound {
			return res, nil // repo not hosted here: treat as not found
		}
		return res, err
	}

	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `-([^/]+)/desc$`)
	tr := tar.NewReader(strings.NewReader(string(data))) //nolint:all // data is plain bytes
	version := ""
	filename := ""
	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return res, &core.ParseError{URL: mirror, Kind: "tar", Err: terr}
		}
		if m := pattern.FindStringSubmatch(hdr.Name); len(m) > 1 {
			version = m[1]
			// desc's %FILENAME% line gives the package file name.
			raw, rerr := io.ReadAll(tr)
			if rerr == nil {
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.TrimSpace(line) == "%FILENAME%" {
						continue
					}
					if strings.HasPrefix(line, "%") {
						break
					}
					if line != "" && filename == "" {
						filename = strings.TrimSpace(line)
						break
					}
				}
			}
			break
		}
	}

	if version == "" {
		return res, nil // Found == false.
	}
	res.Found = true
	res.Version = version
	res.FoundPath = mirror + "/" + repo + "/os/" + arch + "/" + repo + ".db"
	res.Detail = Detail{Repository: repo, Arch: arch}
	return res, nil
}

// Resolve returns the direct .pkg.tar.zst URL.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "package"}
	name := strings.TrimSpace(q.Name)
	if name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}
	opts := QueryOptions{}
	if q.Options != nil {
		if o, ok := q.Options.(*QueryOptions); ok && o != nil {
			opts = *o
		}
	}
	repo := opts.Repository
	if repo == "" {
		repo = "core"
	}
	arch := opts.Arch
	if arch == "" {
		arch = defaultArch()
	}

	pr, err := s.Package(ctx, mirror, core.PackageQuery{Name: name, Version: q.Version, Options: &opts})
	if err != nil {
		return res, err
	}
	if !pr.Found {
		return res, fmt.Errorf("%w: package %q in %s", core.ErrNotFound, name, repo)
	}

	// Arch package files embed the version: name-version-arch.pkg.tar.zst.
	// Without the full release suffix we cannot guess exactly, so probe
	// the directory listing for name-version-*.
	dirURL, jerr := httpx.JoinURL(mirror, repo, "os", arch, "")
	if jerr != nil {
		return res, &core.ValidationError{Field: "mirror", Message: jerr.Error()}
	}
	req, rerr := httpx.NewRequest(ctx, http.MethodGet, dirURL)
	if rerr != nil {
		return res, &core.RequestError{URL: dirURL, Err: rerr}
	}
	resp, derr := s.client().Do(req)
	if derr != nil {
		return res, &core.RequestError{URL: dirURL, Err: derr}
	}
	body, berr := httpx.ReadAll(resp)
	_ = resp.Body.Close()
	if berr != nil {
		return res, &core.RequestError{URL: dirURL, Err: berr}
	}
	if resp.StatusCode != http.StatusOK {
		return res, &core.StatusError{URL: dirURL, StatusCode: resp.StatusCode}
	}

	prefix := name + "-" + pr.Version + "-"
	fileRe := regexp.MustCompile(regexp.QuoteMeta(prefix) + `[^"<> ]+\.pkg\.tar\.(?:zst|xz|gz)`)
	m := fileRe.FindString(string(body))
	if m == "" {
		return res, fmt.Errorf("%w: package file for %q not listed on %s", core.ErrNotFound, name, mirror)
	}

	u, jerr := httpx.JoinURL(mirror, repo, "os", arch, m)
	if jerr != nil {
		return res, &core.ValidationError{Field: "mirror", Message: jerr.Error()}
	}
	res.URL = u
	res.Filename = m
	res.Envelope.OK = true
	return res, nil
}
