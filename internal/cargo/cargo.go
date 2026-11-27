// Package cargo implements the Cargo sparse-index mirror backend.
package cargo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// Detail is cargo's PackageResult.Detail payload.
type Detail struct {
	Versions      []string `json:"versions,omitempty"`
	VersionsCount int      `json:"versions_count,omitempty"`
}

func (Detail) detailType() core.MirrorType { return core.TypeCargo }

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for Cargo sparse HTTP indexes.
type Service struct {
	cfg core.Config
}

// New builds a cargo Service.
func New(cfg core.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypeCargo, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

var crateNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// cratePath returns the sparse-index relative path for a crate (crates.io
// layout; see the cargo registry-index reference).
func cratePath(crate string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(crate))
	if name == "" {
		return "", fmt.Errorf("crate name is empty")
	}
	if !crateNamePattern.MatchString(name) {
		return "", fmt.Errorf("crate name %q is not supported (use ASCII letters, digits, hyphen, underscore)", crate)
	}
	switch len(name) {
	case 1:
		return "1/" + name, nil
	case 2:
		return "2/" + name, nil
	case 3:
		return "3/" + name[:1] + "/" + name, nil
	default:
		return name[:2] + "/" + name[2:4] + "/" + name, nil
	}
}

type indexEntry struct {
	Name   string `json:"name"`
	Vers   string `json:"vers"`
	Yanked bool   `json:"yanked"`
	CKSum  string `json:"cksum"`
}

// fetchIndex fetches and parses a crate's index lines.
func (s *Service) fetchIndex(ctx context.Context, mirror, crate string) ([]indexEntry, string, error) {
	rel, err := cratePath(crate)
	if err != nil {
		return nil, "", &core.ValidationError{Field: "name", Message: err.Error()}
	}
	u, err := httpx.JoinURL(mirror, rel)
	if err != nil {
		return nil, "", &core.ValidationError{Field: "mirror", Message: err.Error()}
	}

	req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
	if rerr != nil {
		return nil, "", &core.RequestError{URL: u, Err: rerr}
	}
	req.Header.Set("Accept", "text/plain")
	resp, derr := s.client().Do(req)
	if derr != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", &core.RequestError{URL: u, Err: derr}
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, u, nil
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return nil, u, &core.StatusError{URL: u, StatusCode: code}
	}

	var entries []indexEntry
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 12*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e indexEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			_ = resp.Body.Close()
			return nil, u, &core.ParseError{URL: u, Kind: "index", Err: err}
		}
		if e.Vers != "" {
			entries = append(entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		_ = resp.Body.Close()
		return nil, u, &core.ParseError{URL: u, Kind: "index", Err: err}
	}
	_ = resp.Body.Close()
	return entries, u, nil
}

// Status probes config.json and a couple of known crate paths.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}
	candidates := []string{"config.json", "se/rd/serde"}
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
		if resp.StatusCode != http.StatusOK {
			_, _ = httpx.ReadAll(resp)
			continue
		}
		// Content validation: config.json must parse as JSON; an index
		// path must answer plain-text lines, never an HTML landing page.
		body, berr := httpx.ReadAll(resp)
		if berr != nil {
			continue
		}
		if strings.HasSuffix(rel, "config.json") {
			var stub map[string]any
			if json.Unmarshal(body, &stub) != nil {
				continue
			}
		} else if looksLikeHTML(body) || !strings.Contains(string(body), "{") {
			continue
		}
		res.OK = true
		res.WorkingPath = u
		res.StatusCode = resp.StatusCode
		res.Envelope.Latency = core.Duration(time.Since(start))
		res.Message = "Mirror is healthy and responding"
		return res, nil
	}
	res.Message = "Mirror not responding or not a valid cargo sparse index"
	return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
}

// Speed measures throughput by fetching a crate index (configurable).
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}

	crate := o.Package
	if crate == "" {
		crate = "serde"
	}
	rel := o.TestFile
	if rel == "" {
		rel, _ = cratePath(crate)
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
	req.Header.Set("Accept", "text/plain")
	resp, derr := s.client().Do(req)
	if derr != nil {
		return res, &core.RequestError{URL: u, Err: derr}
	}
	body, berr := httpx.ReadAll(resp)
	elapsed := time.Since(start)
	_ = resp.Body.Close()
	if berr != nil {
		return res, &core.RequestError{URL: u, Err: berr}
	}
	if resp.StatusCode != http.StatusOK {
		return res, &core.StatusError{URL: u, StatusCode: resp.StatusCode}
	}

	// Index lines are tiny; loop downloads within the duration window to
	// accumulate a usable sample.
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
		_ = r2.Body.Close()
		total += int64(len(b2))
		if resp.StatusCode != http.StatusOK {
			break
		}
	}
	elapsed = time.Since(start)
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

// Package looks up a crate in the sparse index.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}
	entries, u, err := s.fetchIndex(ctx, mirror, q.Name)
	if err != nil {
		return res, err
	}
	if len(entries) == 0 {
		return res, nil // Found == false.
	}

	var all []string
	latest := ""
	for _, e := range entries {
		if e.Yanked {
			continue
		}
		all = append(all, e.Vers)
		if latest == "" || semverGreater(e.Vers, latest) {
			latest = e.Vers
		}
	}
	if latest == "" {
		return res, nil // only yanked versions.
	}

	if q.Version != "" {
		found := false
		for _, e := range entries {
			if e.Vers == q.Version {
				found = true
				break
			}
		}
		if !found {
			res.Found = false
			return res, nil
		}
	}

	res.Found = true
	res.Version = latest
	res.FoundPath = u
	res.Detail = Detail{Versions: all, VersionsCount: len(all)}
	return res, nil
}

// Resolve returns the direct .crate download URL, honoring the mirror's
// config.json "dl" template when present.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "crate"}
	if q.Name == "" {
		return res, &core.ValidationError{Field: "name", Message: "crate name is required"}
	}

	entries, _, err := s.fetchIndex(ctx, mirror, q.Name)
	if err != nil {
		if se, ok := err.(*core.StatusError); ok && se.StatusCode == http.StatusNotFound {
			return res, fmt.Errorf("%w: crate %q", core.ErrNotFound, q.Name)
		}
		return res, err
	}
	latest := q.Version
	if latest == "" {
		for _, e := range entries {
			if !e.Yanked && (latest == "" || semverGreater(e.Vers, latest)) {
				latest = e.Vers
			}
		}
	}
	if latest == "" {
		return res, fmt.Errorf("%w: crate %q", core.ErrNotFound, q.Name)
	}

	// config.json's "dl" template overrides the default download host.
	dl := ""
	{
		u, jerr := httpx.JoinURL(mirror, "config.json")
		if jerr == nil {
			req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
			if rerr == nil {
				if resp, derr := s.client().Do(req); derr == nil {
					if resp.StatusCode == http.StatusOK {
						var cfg struct {
							DL string `json:"dl"`
						}
						body, berr := httpx.ReadAll(resp)
						if berr == nil && json.Unmarshal(body, &cfg) == nil {
							dl = cfg.DL
						}
					} else {
						_ = resp.Body.Close()
					}
				}
			}
		}
	}

	name := strings.ToLower(q.Name)
	filename := name + "-" + latest + ".crate"
	var url string
	switch {
	case dl == "":
		rel, perr := cratePath(name)
		if perr != nil {
			return res, &core.ValidationError{Field: "name", Message: perr.Error()}
		}
		url = strings.TrimSuffix(mirror, "/") + "/" + rel + "/" + filename
	case strings.Contains(dl, "{crate}"):
		url = strings.ReplaceAll(dl, "{crate}", name)
	case strings.Contains(dl, "{version}"):
		url = strings.ReplaceAll(dl, "{version}", latest)
		if !strings.Contains(url, "{crate}") && !strings.Contains(url, "/") {
			url = mirror + "/" + url
		}
	default:
		url = strings.TrimSuffix(dl, "/") + "/" + name + "/" + latest + "/download"
	}

	res.URL = url
	res.Filename = filename
	res.Envelope.OK = true
	return res, nil
}

// semverGreater compares dotted versions with pre-release awareness
// (1.2.3 > 1.2.3-rc.1). Falls back to lexicographic.
func semverGreater(a, b string) bool {
	return compareSemver(a, b) > 0
}

func compareSemver(a, b string) int {
	av, apr := splitPre(a)
	bv, bpr := splitPre(b)
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			if av[i] > bv[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case apr == "" && bpr == "":
		return 0
	case apr == "":
		return 1
	case bpr == "":
		return -1
	}
	if apr > bpr {
		return 1
	}
	if apr < bpr {
		return -1
	}
	return 0
}

func splitPre(v string) ([3]int, string) {
	var nums [3]int
	pre := ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		pre = v[i:]
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		n := 0
		for _, r := range parts[i] {
			if r < '0' || r > '9' {
				n = -1
				break
			}
			n = n*10 + int(r-'0')
		}
		nums[i] = n
	}
	return nums, pre
}

// looksLikeHTML reports whether b starts like an HTML document.
func looksLikeHTML(b []byte) bool {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return false
	}
	head := strings.ToLower(string(b[:min(len(b), 256)]))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") ||
		strings.Contains(head, "<head") || strings.Contains(head, "<body")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
