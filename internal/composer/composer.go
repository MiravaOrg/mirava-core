// Package composer implements the Composer / Packagist mirror backend.
package composer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// Detail is composer's PackageResult.Detail payload.
type Detail struct {
	Versions      []string `json:"versions,omitempty"`
	VersionsCount int      `json:"versions_count,omitempty"`
}

func (Detail) detailType() core.MirrorType { return core.TypeComposer }

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for Composer mirrors.
type Service struct {
	cfg core.Config
}

// New builds a composer Service.
func New(cfg core.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypeComposer, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

type distInfo struct {
	URL    string `json:"url"`
	SHA256 string `json:"shasum"`
	Type   string `json:"type"`
}

type versionInfo struct {
	Version string   `json:"version"`
	Dist    distInfo `json:"dist"`
}

// fetchPackageDoc fetches package metadata, trying the Composer V2 p2
// layout first, then V1 p/.
func (s *Service) fetchPackageDoc(ctx context.Context, mirror, name string) ([]byte, string, error) {
	patterns := [][]string{{"p2"}, {"p"}}
	for _, dir := range patterns {
		u, err := httpx.JoinURL(mirror, dir[0], name+".json")
		if err != nil {
			continue
		}
		req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
		if rerr != nil {
			continue
		}
		resp, derr := s.client().Do(req)
		if derr != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}
		body, berr := httpx.ReadAll(resp)
		if berr != nil {
			continue
		}
		return body, u, nil
	}
	return nil, "", nil
}

// parseVersions extracts versions from p2 (minified) or p1 layouts.
func parseVersions(body []byte, name string) []versionInfo {
	// V2 p2: {"packages": {"vendor/name": [ {version...}, ... ]}}
	var v2 struct {
		Packages map[string][]versionInfo `json:"packages"`
	}
	if err := json.Unmarshal(body, &v2); err == nil && len(v2.Packages) > 0 {
		if vs, ok := v2.Packages[name]; ok {
			return vs
		}
		// minified providers form: map key may be the package itself
		for _, vs := range v2.Packages {
			return vs
		}
	}
	// V1 p/: {"packages": {"vendor/name": {"version": {dist...}}}}
	var v1 struct {
		Packages map[string]map[string]struct {
			Dist distInfo `json:"dist"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &v1); err == nil {
		pkg, ok := v1.Packages[name]
		if !ok {
			for _, p := range v1.Packages {
				pkg = p
				break
			}
		}
		var out []versionInfo
		for ver, data := range pkg {
			out = append(out, versionInfo{Version: ver, Dist: data.Dist})
		}
		return out
	}
	return nil
}

// Status probes packages.json and a known p2 package.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}
	candidates := [][]string{{"packages.json"}, {"p2", "laravel", "framework.json"}}
	for _, rel := range candidates {
		u, err := httpx.JoinURL(mirror, rel...)
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
	res.Message = "Mirror not responding or not a valid composer mirror"
	return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
}

// Speed measures throughput by fetching a package's metadata JSON
// (repeated within the window to accumulate a sample).
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}

	pkg := o.Package
	if pkg == "" {
		pkg = "laravel/framework"
	}
	rel := o.TestFile
	if rel == "" {
		rel = "p2/" + pkg + ".json"
	}
	u, err := httpx.JoinURL(mirror, rel)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	res.TestURL = u

	start := time.Now()
	req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
	if rerr != nil {
		return res, &core.RequestError{URL: u, Err: rerr}
	}
	resp, derr := s.client().Do(req)
	if derr != nil {
		return res, &core.RequestError{URL: u, Err: derr}
	}
	body, berr := httpx.ReadAll(resp)
	if berr != nil {
		return res, &core.RequestError{URL: u, Err: berr}
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return res, &core.StatusError{URL: u, StatusCode: resp.StatusCode}
	}

	duration := o.Duration
	if duration <= 0 {
		duration = DefaultSpeedDuration
	}
	total := int64(len(body))
	for time.Since(start) < duration {
		r2, derr2 := s.client().Do(req.Clone(ctx))
		if derr2 != nil {
			break
		}
		b2, _ := httpx.ReadAll(r2)
		total += int64(len(b2))
	}
	elapsed := time.Since(start)
	mbps := float64(total) / 1024 / 1024 / elapsed.Seconds()

	res.SpeedMBps = mbps
	res.Bytes = total
	res.DownloadedMB = float64(total) / 1024 / 1024
	res.DurationSec = elapsed.Seconds()
	res.Rating = core.SpeedRating(mbps)
	res.Envelope.OK = true
	res.Envelope.Latency = core.Duration(elapsed)
	return res, nil
}

// Package looks up versions for a composer package.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}
	name := strings.TrimSpace(q.Name)
	if name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}

	body, u, err := s.fetchPackageDoc(ctx, mirror, name)
	if err != nil {
		return res, err
	}
	if body == nil {
		return res, nil // Found == false.
	}

	versions := parseVersions(body, name)
	if len(versions) == 0 {
		return res, nil
	}

	var all []string
	latest := ""
	for _, v := range versions {
		if strings.HasPrefix(v.Version, "dev-") || v.Version == "" {
			continue
		}
		all = append(all, v.Version)
		if latest == "" || versionGreater(v.Version, latest) {
			latest = v.Version
		}
	}
	if latest == "" {
		return res, nil
	}
	sort.Strings(all)

	if q.Version != "" {
		found := false
		for _, v := range all {
			if v == q.Version {
				found = true
				break
			}
		}
		if !found {
			return res, nil
		}
	}

	res.Found = true
	res.Version = latest
	res.FoundPath = u
	res.Detail = Detail{Versions: all, VersionsCount: len(all)}
	return res, nil
}

// Resolve returns the dist URL for a version's zip/tarball.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "dist"}
	name := strings.TrimSpace(q.Name)
	if name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}

	body, _, err := s.fetchPackageDoc(ctx, mirror, name)
	if err != nil {
		return res, err
	}
	if body == nil {
		return res, fmt.Errorf("%w: package %q", core.ErrNotFound, name)
	}
	versions := parseVersions(body, name)

	// Pick the requested or latest stable version.
	var chosen *versionInfo
	for i := range versions {
		v := &versions[i]
		if q.Version != "" {
			if v.Version == q.Version {
				chosen = v
				break
			}
			continue
		}
		if strings.HasPrefix(v.Version, "dev-") || v.Version == "" {
			continue
		}
		if chosen == nil || versionGreater(v.Version, chosen.Version) {
			chosen = v
		}
	}
	if chosen == nil || chosen.Dist.URL == "" {
		return res, fmt.Errorf("%w: dist URL for %q@%q", core.ErrNotFound, name, q.Version)
	}

	res.URL = chosen.Dist.URL
	res.SHA256 = chosen.Dist.SHA256
	if chosen.Dist.Type != "" {
		res.Kind = chosen.Dist.Type // "zip", "tar", ...
	}
	res.Envelope.OK = true
	return res, nil
}

// versionGreater compares dotted versions segment by segment.
func versionGreater(a, b string) bool {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aok := num(as[i])
		bn, bok := num(bs[i])
		if aok && bok && an != bn {
			return an > bn
		}
		if as[i] != bs[i] {
			return as[i] > bs[i]
		}
	}
	return len(as) > len(bs)
}

func num(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, len(s) > 0
}
