// Package pypi implements the PyPI (PEP 503 simple index) mirror backend.
package pypi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// Detail is pypi's PackageResult.Detail payload.
type Detail struct {
	Versions      []string `json:"versions,omitempty"`
	VersionsCount int      `json:"versions_count,omitempty"`
}

func (Detail) detailType() core.MirrorType { return core.TypePypi }

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for PEP 503 simple-index mirrors.
type Service struct {
	cfg core.Config
}

// New builds a pypi Service.
func New(cfg core.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypePypi, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

// Status probes /simple/.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}
	u, err := simpleIndexURL(mirror)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	res.TestedPaths = append(res.TestedPaths, u)

	req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
	if rerr != nil {
		return res, &core.RequestError{URL: u, Err: rerr}
	}
	start := time.Now()
	resp, derr := s.client().Do(req)
	if derr != nil {
		if ctx.Err() != nil {
			return res, core.AsError(ctx.Err())
		}
		res.Message = "Mirror not responding or not a valid pypi mirror"
		return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = httpx.ReadAll(resp)
		res.Message = "Mirror not responding or not a valid pypi mirror"
		return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror, StatusCode: resp.StatusCode}
	}
	// Content validation: /simple/ answers PEP 503 HTML with project
	// anchors — never a marketing page. SPA catch-all fronts answer every
	// path with HTML 200 and no anchors; reject those.
	body, berr := httpx.ReadAll(resp)
	if berr != nil || !strings.Contains(string(body), "<a ") {
		res.Message = "Response from /simple/ is not a PEP 503 index"
		return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
	}
	res.OK = true
	res.WorkingPath = u
	res.StatusCode = resp.StatusCode
	res.Envelope.Latency = core.Duration(time.Since(start))
	res.Message = "Mirror is healthy and responding"
	return res, nil
}

// Speed measures throughput by downloading the simple index (a large HTML
// document on real mirrors).
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}
	rel := o.TestFile
	if rel == "" {
		// Speed against the index URL itself, not "simple" appended again —
		// mirrors whose base already ends in /simple would otherwise be
		// measured against a tiny 301 stub.
		u, uerr := simpleIndexURL(mirror)
		if uerr != nil {
			return res, &core.ValidationError{Field: "mirror", Message: uerr.Error()}
		}
		rel = u
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
		// /simple/ is a few MB; a short download is still a usable sample.
		if bytes >= 512*1024 {
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

// PEP 503 project page anchors look like
// <a href=".../package-1.2.3-py3-none-any.whl#sha256=...">package-1.2.3...</a>
// The version component ends at wheel/platform tags (py3, cp312, none-any,
// tar.gz, ...): capture only numeric segments plus a/b/rc, post, dev.
var versionRe = regexp.MustCompile(
	`(?:^|[/#])` + `([A-Za-z0-9._]+)-` +
		`([0-9]+(?:\.[0-9]+)*(?:(?:(?:a|b|rc)\d+)|(?:\.post\d+)|(?:\.dev\d+))*)`,
)

// Package looks up a project's simple index page.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}
	name := strings.TrimSpace(q.Name)
	if name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}

	base, err := simpleIndexURL(mirror)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	// PEP 503 project URLs are canonical with a trailing slash
	// (.../simple/<name>/). Some Artifactory-backed mirrors 404 on the
	// slashless form while serving 200 on the canonical one.
	u, err := httpx.JoinURL(base, normalize(name))
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}

	req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
	if rerr != nil {
		return res, &core.RequestError{URL: u, Err: rerr}
	}
	resp, derr := s.client().Do(req)
	if derr != nil {
		if ctx.Err() != nil {
			return res, core.AsError(ctx.Err())
		}
		return res, &core.RequestError{URL: u, Err: derr}
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return res, nil // Found == false.
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return res, &core.StatusError{URL: u, StatusCode: code}
	}
	body, berr := httpx.ReadAll(resp)
	if berr != nil {
		return res, &core.ParseError{URL: u, Kind: "response", Err: berr}
	}

	versions := map[string]bool{}
	for _, m := range versionRe.FindAllStringSubmatch(string(body), -1) {
		if len(m) > 2 && strings.EqualFold(strings.ReplaceAll(strings.ToLower(m[1]), "_", "-"), strings.ReplaceAll(strings.ToLower(name), "_", "-")) {
			versions[m[2]] = true
		}
	}
	if len(versions) == 0 {
		res.Found = true // project exists, no parseable versions
		res.Version = "unknown"
		res.FoundPath = u
		res.Detail = Detail{VersionsCount: 0}
		return res, nil
	}

	list := make([]string, 0, len(versions))
	for v := range versions {
		list = append(list, v)
	}
	sort.Strings(list)
	latest := latestPEP440(list)

	res.Found = true
	res.Version = latest
	res.FoundPath = u
	res.Detail = Detail{Versions: list, VersionsCount: len(list)}
	return res, nil
}

// Resolve returns a direct download URL for a wheel or sdist.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "wheel"}
	name := strings.TrimSpace(q.Name)
	if name == "" {
		return res, &core.ValidationError{Field: "name", Message: "package name is required"}
	}

	base, err := simpleIndexURL(mirror)
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	// Same trailing-slash rule as Package: canonical PEP 503 form.
	u, err := httpx.JoinURL(base, normalize(name))
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}

	req, rerr := httpx.NewRequest(ctx, http.MethodGet, u)
	if rerr != nil {
		return res, &core.RequestError{URL: u, Err: rerr}
	}
	resp, derr := s.client().Do(req)
	if derr != nil {
		if ctx.Err() != nil {
			return res, core.AsError(ctx.Err())
		}
		return res, &core.RequestError{URL: u, Err: derr}
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return res, fmt.Errorf("%w: project %q", core.ErrNotFound, name)
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return res, &core.StatusError{URL: u, StatusCode: code}
	}
	body, berr := httpx.ReadAll(resp)
	if berr != nil {
		return res, &core.ParseError{URL: u, Kind: "response", Err: berr}
	}

	// Collect (filename, url, sha256, version) tuples from anchors.
	type fileRef struct {
		name, url, sha, version string
	}
	var files []fileRef
	re := regexp.MustCompile(`<a[^>]+href="([^"]+)"[^>]*>([^<]+)</a>`)
	for _, m := range re.FindAllStringSubmatch(string(body), -1) {
		if len(m) < 3 {
			continue
		}
		href, fname := m[1], m[2]
		sha := ""
		if i := strings.Index(href, "#sha256="); i >= 0 {
			sha = href[i+len("#sha256="):]
			href = href[:i]
		}
		if !strings.Contains(href, "://") {
			abs, jerr := httpx.JoinURL(u, href)
			if jerr != nil {
				continue
			}
			href = abs
		}
		ver := ""
		if vm := versionRe.FindStringSubmatch(fname); len(vm) > 2 {
			ver = vm[2]
		}
		files = append(files, fileRef{name: fname, url: href, sha: sha, version: ver})
	}

	// Pick a file: requested version if given, else latest; wheel before sdist.
	var chosen *fileRef
	for i := range files {
		f := &files[i]
		if q.Version != "" && f.version != q.Version {
			continue
		}
		if chosen == nil {
			chosen = f
			continue
		}
		betterWheel := strings.HasSuffix(f.name, ".whl") && !strings.HasSuffix(chosen.name, ".whl")
		sameKind := strings.HasSuffix(f.name, ".whl") == strings.HasSuffix(chosen.name, ".whl")
		betterVersion := q.Version == "" && pep440Greater(f.version, chosen.version)
		if betterWheel || (sameKind && betterVersion) {
			chosen = f
		}
	}
	if chosen == nil {
		return res, fmt.Errorf("%w: no file for %q on %q", core.ErrNotFound, name, mirror)
	}

	kind := "sdist"
	if strings.HasSuffix(chosen.name, ".whl") {
		kind = "wheel"
	}
	res.Kind = kind
	res.URL = chosen.url
	res.SHA256 = chosen.sha
	res.Filename = chosen.name
	res.Envelope.OK = true
	return res, nil
}

// normalize applies PEP 503 name normalization.
func normalize(name string) string {
	name = strings.ToLower(name)
	name = regexp.MustCompile(`[-_.]+`).ReplaceAllString(name, "-")
	return name
}

// simpleIndexURL returns the mirror's simple-index URL. The caller passes
// the full repository URL, which for pypi mirrors usually already ends in
// "/simple" (e.g. https://pypi.tuna.tsinghua.edu.cn/simple/). Appending
// "simple" unconditionally produced /simple/simple — a 301 to an empty
// "Links for simple" stub page that failed content validation on every
// real mirror of that shape. If the path already ends in the index
// segment, use it as-is; otherwise append it once.
func simpleIndexURL(mirror string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(mirror), "/")
	if strings.HasSuffix(strings.ToLower(trimmed), "/simple") {
		return trimmed + "/", nil
	}
	u, err := httpx.JoinURL(mirror, "simple")
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u, nil
}

// latestPEP440 picks the greatest version lexicographically by segments.
func latestPEP440(versions []string) string {
	sort.Slice(versions, func(i, j int) bool { return pep440Greater(versions[i], versions[j]) })
	if len(versions) == 0 {
		return ""
	}
	return versions[0]
}

// pep440Greater compares dotted numeric versions segment by segment.
func pep440Greater(a, b string) bool {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, an := segNum(as[i])
		bi, bn := segNum(bs[i])
		if an && bn && ai != bi {
			return ai > bi
		}
		if as[i] != bs[i] {
			return as[i] > bs[i]
		}
	}
	return len(as) > len(bs)
}

func segNum(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, len(s) > 0
}
