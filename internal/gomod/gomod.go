// Package gomod implements the Go module proxy (GOPROXY protocol) backend.
package gomod

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

// Detail is gomod's PackageResult.Detail payload.
type Detail struct {
	Versions []string `json:"versions,omitempty"`
	Info     *ModInfo `json:"info,omitempty"`
}

func (Detail) detailType() core.MirrorType { return core.TypeGo }

// ModInfo mirrors the GOPROXY .info document.
type ModInfo struct {
	Version string    `json:"Version"`
	Time    time.Time `json:"Time"`
}

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for GOPROXY-style mirrors.
type Service struct {
	cfg core.Config
}

// New builds a gomod Service.
func New(cfg core.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypeGo, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

// Status probes @v/list of a well-known module.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}
	candidates := []string{"github.com/gorilla/mux/@v/list", "golang.org/x/mod/@v/list"}
	for _, rel := range candidates {
		u, err := httpx.JoinURL(mirror, rel)
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
	res.Message = "Mirror not responding or not a valid Go module proxy"
	return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
}

// Speed measures throughput by downloading a module .zip (e.g.
// golang.org/x/mod's latest zip, a few hundred KB).
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}

	rel := o.TestFile
	if rel == "" {
		// Discover latest version of a small module and download its zip.
		const probe = "golang.org/x/mod"
		listURL, jerr := httpx.JoinURL(mirror, probe, "@v", "list")
		if jerr != nil {
			return res, &core.ValidationError{Field: "mirror", Message: jerr.Error()}
		}
		req, rerr := httpx.NewRequest(ctx, http.MethodGet, listURL)
		if rerr != nil {
			return res, &core.RequestError{URL: listURL, Err: rerr}
		}
		resp, derr := s.client().Do(req)
		if derr != nil {
			return res, &core.RequestError{URL: listURL, Err: derr}
		}
		body, berr := httpx.ReadAll(resp)
		_ = resp.Body.Close()
		if berr != nil {
			return res, &core.RequestError{URL: listURL, Err: berr}
		}
		if resp.StatusCode != http.StatusOK {
			return res, &core.StatusError{URL: listURL, StatusCode: resp.StatusCode}
		}
		versions := nonEmptyLines(string(body))
		if len(versions) == 0 {
			return res, &core.RequestError{URL: listURL, Err: core.ErrNotFound}
		}
		sort.Strings(versions)
		latest := versions[len(versions)-1]
		rel = probe + "/@v/" + latest + ".zip"
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
		res.Envelope.OK = false
		return res, &core.SpeedError{URL: u, Bytes: bytes, Elapsed: elapsed, Err: mErr}
	}
	res.SpeedMBps = mbps
	res.Rating = core.SpeedRating(mbps)
	res.Envelope.OK = true
	return res, nil
}

// Package lists versions and resolves the latest via @v/list + @v/latest.info.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}
	module := strings.TrimSpace(q.Name)
	if module == "" {
		return res, &core.ValidationError{Field: "name", Message: "module path is required"}
	}

	listURL, err := httpx.JoinURL(mirror, module, "@v", "list")
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	req, rerr := httpx.NewRequest(ctx, http.MethodGet, listURL)
	if rerr != nil {
		return res, &core.RequestError{URL: listURL, Err: rerr}
	}
	resp, derr := s.client().Do(req)
	if derr != nil {
		if ctx.Err() != nil {
			return res, core.AsError(ctx.Err())
		}
		return res, &core.RequestError{URL: listURL, Err: derr}
	}
	body, berr := httpx.ReadAll(resp)
	_ = resp.Body.Close()
	if berr != nil {
		return res, &core.RequestError{URL: listURL, Err: berr}
	}
	if resp.StatusCode == http.StatusNotFound {
		return res, nil // Found == false.
	}
	if resp.StatusCode != http.StatusOK {
		return res, &core.StatusError{URL: listURL, StatusCode: resp.StatusCode}
	}

	versions := nonEmptyLines(string(body))
	if len(versions) == 0 {
		return res, nil
	}
	sort.Strings(versions)

	latest := versions[len(versions)-1]
	if q.Version != "" {
		found := false
		for _, v := range versions {
			if v == q.Version {
				found = true
				break
			}
		}
		if !found {
			return res, nil
		}
		latest = q.Version
	}

	// Fetch .info for the chosen version (best-effort).
	var info *ModInfo
	infoURL, jerr := httpx.JoinURL(mirror, module, "@v", latest+".info")
	if jerr == nil {
		if iResp, derr2 := s.client().Get(infoURL); derr2 == nil {
			if iBody, berr2 := httpx.ReadAll(iResp); berr2 == nil && iResp.StatusCode == http.StatusOK {
				var mi ModInfo
				if json.Unmarshal(iBody, &mi) == nil {
					info = &mi
				}
			} else {
				_ = iResp.Body.Close()
			}
		}
	}

	res.Found = true
	res.Version = latest
	res.FoundPath = listURL
	res.Detail = Detail{Versions: versions, Info: info}
	return res, nil
}

// Resolve returns the direct .zip (or .mod with Options) URL.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "archive"}
	module := strings.TrimSpace(q.Name)
	if module == "" {
		return res, &core.ValidationError{Field: "name", Message: "module path is required"}
	}

	version := q.Version
	if version == "" {
		pr, err := s.Package(ctx, mirror, q)
		if err != nil {
			return res, err
		}
		if !pr.Found {
			return res, fmt.Errorf("%w: module %q", core.ErrNotFound, module)
		}
		version = pr.Version
	}

	kind := "archive"
	ext := ".zip"
	if opts, ok := q.Options.(*ResolveOptions); ok && opts != nil && opts.File == "mod" {
		kind = "mod"
		ext = ".mod"
	}

	u, err := httpx.JoinURL(mirror, module, "@v", version+ext)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	res.URL = u
	res.Kind = kind
	res.Filename = version + ext
	res.Envelope.OK = true
	return res, nil
}

// ResolveOptions selects which GOPROXY artifact Resolve returns.
type ResolveOptions struct {
	// File is "zip" (default) or "mod".
	File string
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
