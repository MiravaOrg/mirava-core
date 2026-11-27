// Package npm implements the npm registry mirror backend.
package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// Detail is npm's PackageResult.Detail payload.
type Detail struct {
	Latest   string            `json:"latest,omitempty"`
	DistTags map[string]string `json:"dist_tags,omitempty"`
	Versions []string          `json:"versions,omitempty"`
}

func (Detail) detailType() core.MirrorType { return core.TypeNpm }

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for npm registries.
type Service struct {
	cfg core.Config
}

// New builds an npm Service.
func New(cfg core.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypeNpm, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

func (s *Service) getJSON(ctx context.Context, u string, dest any) (*http.Response, error) {
	req, err := httpx.NewRequest(ctx, http.MethodGet, u)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &core.RequestError{URL: u, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return nil, &core.StatusError{URL: u, StatusCode: code}
	}
	body, err := httpx.ReadAll(resp)
	if err != nil {
		return nil, &core.ParseError{URL: u, Kind: "response", Err: err}
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return nil, &core.ParseError{URL: u, Kind: "json", Err: err}
	}
	return resp, nil
}

// Status probes well-known registry endpoints. The search endpoint must
// return parseable JSON; /react is the fallback reachability check.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}

	searchURL, err := httpx.JoinURL(mirror, "-/v1/search")
	if err == nil {
		// Query string appended after JoinURL: JoinPath escapes "?".
		searchURL += "?text=test&size=1"
	}
	tests := []string{searchURL, joinPathOrEmpty(mirror, "react")}
	for _, u := range tests {
		if u == "" {
			continue
		}
		res.TestedPaths = append(res.TestedPaths, u)

		req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
		if rerr != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")
		start := time.Now()
		resp, derr := s.client().Do(req)
		if derr != nil {
			if ctx.Err() != nil {
				return res, core.AsError(ctx.Err())
			}
			continue
		}
		if resp.StatusCode != http.StatusOK && !(resp.StatusCode >= 300 && resp.StatusCode < 400) {
			_, _ = httpx.ReadAll(resp)
			continue
		}

		// Content validation: every npm registry endpoint answers JSON.
		// SPA catch-all fronts answer every path with an HTML page.
		body, berr := httpx.ReadAll(resp)
		if berr != nil || !json.Valid(bytes.TrimSpace(body)) {
			continue
		}

		res.OK = true
		res.WorkingPath = u
		res.StatusCode = resp.StatusCode
		res.Envelope.Latency = core.Duration(time.Since(start))
		res.Message = "Mirror is healthy and responding"
		return res, nil
	}

	res.Message = "Mirror not responding or not a valid npm registry mirror"
	return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
}

// joinPathOrEmpty joins mirror and elements, returning "" on error.
func joinPathOrEmpty(mirror string, elem ...string) string {
	u, err := httpx.JoinURL(mirror, elem...)
	if err != nil {
		return ""
	}
	return u
}

// Speed measures throughput by fetching a package's registry document
// (configurable via SpeedOptions.Package or TestFile).
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}

	testPackage := "prisma"
	if o.Package != "" {
		testPackage = o.Package
	}
	rel := o.TestFile
	if rel == "" {
		rel = testPackage
	}
	u, err := httpx.JoinURL(mirror, rel)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	res.TestURL = u

	// A registry doc is small; loop over it only if the caller wants a long
	// window. npm docs are ~1MB for prisma, enough for a rough estimate.
	duration := o.Duration
	if duration <= 0 {
		duration = DefaultSpeedDuration
	}

	start := time.Now()
	var total int64
	req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
	if rerr != nil {
		return res, &core.RequestError{URL: u, Err: rerr}
	}
	req.Header.Set("Accept", "application/json")
	resp, derr := s.client().Do(req)
	if derr != nil {
		return res, &core.RequestError{URL: u, Err: derr}
	}
	cl := resp.ContentLength
	_, rerr = httpx.ReadAll(resp)
	elapsed := time.Since(start)
	if rerr != nil {
		return res, &core.RequestError{URL: u, Err: rerr}
	}
	_ = resp.Body.Close()

	if cl > 0 {
		total = cl
	} else {
		total = 1 // unknown; single-shot measurement
	}

	// Download repeatedly until duration elapses to accumulate a sample.
	if elapsed < duration {
		rounds := int(duration / elapsed)
		for i := 0; i < rounds; i++ {
			r2, derr2 := s.client().Do(req.Clone(ctx))
			if derr2 != nil {
				break
			}
			n, _ := httpx.ReadAll(r2)
			total += int64(len(n))
		}
	}

	mbps := float64(total) / 1024 / 1024 / elapsed.Seconds()
	res.SpeedMBps = mbps
	res.Bytes = total
	res.DownloadedMB = float64(total) / 1024 / 1024
	res.DurationSec = elapsed.Seconds()
	res.ContentLength = cl
	res.Rating = core.SpeedRating(mbps)
	res.Envelope.OK = true
	res.Envelope.Latency = core.Duration(elapsed)
	return res, nil
}

type registryDoc struct {
	DistTags map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Dist struct {
			Tarball string `json:"tarball"`
			Shasum  string `json:"shasum"`
		} `json:"dist"`
	} `json:"versions"`
}

// Package looks up a package in the registry document.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}
	if q.Name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}

	u, err := httpx.JoinURL(mirror, q.Name)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}

	var doc registryDoc
	if _, err := s.getJSON(ctx, u, &doc); err != nil {
		if se, ok := err.(*core.StatusError); ok && se.StatusCode == http.StatusNotFound {
			return res, nil // Found == false.
		}
		return res, err
	}

	res.Found = true
	res.FoundPath = u

	detail := Detail{DistTags: doc.DistTags}
	if latest, ok := doc.DistTags["latest"]; ok {
		res.Version = latest
		detail.Latest = latest
	} else {
		for v := range doc.Versions {
			res.Version = v
			break
		}
	}
	for v := range doc.Versions {
		detail.Versions = append(detail.Versions, v)
	}
	if q.Version != "" {
		if _, ok := doc.Versions[q.Version]; !ok {
			// Named version not published: report found=false for that
			// version but keep latest in detail.
			res.Found = false
			res.Version = ""
		}
	}
	res.Detail = detail
	return res, nil
}

// Resolve returns the direct tarball URL.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "tarball"}
	if q.Name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}

	u, err := httpx.JoinURL(mirror, q.Name)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}

	var doc registryDoc
	if _, err := s.getJSON(ctx, u, &doc); err != nil {
		if se, ok := err.(*core.StatusError); ok && se.StatusCode == http.StatusNotFound {
			return res, fmt.Errorf("%w: package %q", core.ErrNotFound, q.Name)
		}
		return res, err
	}

	version := q.Version
	if version == "" {
		version = doc.DistTags["latest"]
	}
	v, ok := doc.Versions[version]
	if !ok {
		return res, fmt.Errorf("%w: version %q of %q", core.ErrNotFound, version, q.Name)
	}
	res.URL = v.Dist.Tarball
	res.SHA256 = v.Dist.Shasum
	res.Filename = tarballName(q.Name, version)
	res.Envelope.OK = true
	return res, nil
}

// tarballName derives the conventional npm tarball filename for a
// scoped ("@scope/name") or plain package.
func tarballName(name, version string) string {
	base := name
	if strings.HasPrefix(base, "@") {
		if i := strings.Index(base, "/"); i >= 0 {
			base = base[i+1:]
		}
	}
	return base + "-" + version + ".tgz"
}
