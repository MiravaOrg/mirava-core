// Package docker implements the OCI / Docker registry mirror backend.
//
// Registries require a bearer token for pulls: GET /v2/ returns 401 with a
// WWW-Authenticate header pointing at the token endpoint. The backend
// fetches a token when challenged and passes it on every subsequent
// request. Resolve returns the blob URL together with the Authorization
// header needed to fetch it.
package docker

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

// Detail is docker's PackageResult.Detail payload.
type Detail struct {
	Tags       []string `json:"tags,omitempty"`
	TotalTags  int      `json:"total_tags,omitempty"`
	Digest     string   `json:"digest,omitempty"`
	LayerCount int      `json:"layer_count,omitempty"`
}

func (Detail) detailType() core.MirrorType { return core.TypeDocker }

// DefaultSpeedDuration bounds a speed-test download.
const DefaultSpeedDuration = 10 * time.Second

// Service implements core.Prober for OCI registries.
type Service struct {
	cfg core.Config
}

// New builds a docker Service.
func New(cfg core.Config) *Service { return &Service{cfg: cfg} }

func (s *Service) envelope(mirror string) core.Envelope {
	return core.Envelope{Type: core.TypeDocker, Mirror: mirror, CheckedAt: time.Now().UTC()}
}

func (s *Service) client() *http.Client { return s.cfg.Client() }

// token is a cached bearer token for one registry+scope.
type token struct {
	Value    string
	Registry string
	Scope    string
	Expires  time.Time
}

// authorize performs a GET and, on 401 with a WWW-Authenticate challenge,
// fetches a bearer token and retries once. Returns the response of the
// successful request.
func (s *Service) authorize(ctx context.Context, u string, accept string, headers http.Header) (*http.Response, error) {
	req, err := httpx.NewRequest(ctx, http.MethodGet, u)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	client := s.client()
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// Parse the WWW-Authenticate challenge.
	challenge := resp.Header.Get("WWW-Authenticate")
	_ = resp.Body.Close()
	if challenge == "" || !strings.HasPrefix(challenge, "Bearer") {
		return nil, &core.StatusError{URL: u, StatusCode: http.StatusUnauthorized}
	}
	realm, params := parseChallenge(challenge)
	if realm == "" {
		return nil, &core.StatusError{URL: u, StatusCode: http.StatusUnauthorized}
	}

	tokURL := realm + "?service=" + params["service"] + "&scope=" + params["scope"]
	treq, terr := httpx.NewRequest(ctx, http.MethodGet, tokURL)
	if terr != nil {
		return nil, terr
	}
	tresp, terr2 := client.Do(treq)
	if terr2 != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, terr2
	}
	tbody, terr3 := httpx.ReadAll(tresp)
	if terr3 != nil {
		return nil, terr3
	}
	if tresp.StatusCode != http.StatusOK {
		_ = tresp.Body.Close()
		return nil, &core.StatusError{URL: tokURL, StatusCode: tresp.StatusCode}
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tbody, &tok); err != nil {
		return nil, &core.ParseError{URL: tokURL, Kind: "json", Err: err}
	}
	bearer := tok.Token
	if bearer == "" {
		bearer = tok.AccessToken
	}

	// Retry the original request with the token.
	req2, err := httpx.NewRequest(ctx, http.MethodGet, u)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req2.Header.Set("Accept", accept)
	}
	for k, vs := range headers {
		for _, v := range vs {
			req2.Header.Add(k, v)
		}
	}
	req2.Header.Set("Authorization", "Bearer "+bearer)
	resp2, err := client.Do(req2)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return resp2, nil
}

// parseChallenge extracts realm and params from a Bearer challenge.
func parseChallenge(ch string) (string, map[string]string) {
	ch = strings.TrimPrefix(ch, "Bearer ")
	params := map[string]string{}
	var realm string
	for _, part := range strings.Split(ch, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		v := strings.Trim(kv[1], `"`)
		if kv[0] == "realm" {
			realm = v
		} else {
			params[kv[0]] = v
		}
	}
	return realm, params
}

const (
	manifestListAccept = "application/vnd.docker.distribution.manifest.list.v2+json," +
		"application/vnd.docker.distribution.manifest.v2+json," +
		"application/vnd.oci.image.manifest.v1+json," +
		"application/vnd.oci.image.index.v1+json"
	manifestAccept = "application/vnd.docker.distribution.manifest.v2+json," +
		"application/vnd.oci.image.manifest.v1+json"
)

type manifestList struct {
	Manifests []struct {
		Digest string `json:"digest"`
		Arch   string `json:"architecture,omitempty"`
		Size   int64  `json:"size"`
	} `json:"manifests"`
}

type imageManifest struct {
	Config struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"layers"`
}

// fetchManifest GETs a tag or digest manifest, following the index →
// platform-manifest chain when needed.
func (s *Service) fetchManifest(ctx context.Context, registry, image, ref string) (*imageManifest, string, error) {
	u, err := httpx.JoinURL(registry, "v2", image, "manifests", ref)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.authorize(ctx, u, manifestListAccept, nil)
	if err != nil {
		return nil, "", err
	}
	body, err := httpx.ReadAll(resp)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return nil, "", &core.StatusError{URL: u, StatusCode: code}
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	_ = resp.Body.Close()

	// Try as an index (manifest list) first.
	var ml manifestList
	if json.Unmarshal(body, &ml) == nil && len(ml.Manifests) > 0 {
		// Pick the amd64 (or first) platform manifest.
		digestRef := ml.Manifests[0].Digest
		for _, m := range ml.Manifests {
			if m.Arch == "amd64" {
				digestRef = m.Digest
				break
			}
		}
		return s.fetchDigestManifest(ctx, registry, image, digestRef)
	}

	var im imageManifest
	if err := json.Unmarshal(body, &im); err != nil {
		return nil, "", &core.ParseError{URL: u, Kind: "manifest", Err: err}
	}
	return &im, digest, nil
}

func (s *Service) fetchDigestManifest(ctx context.Context, registry, image, ref string) (*imageManifest, string, error) {
	u, err := httpx.JoinURL(registry, "v2", image, "manifests", ref)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.authorize(ctx, u, manifestAccept, nil)
	if err != nil {
		return nil, "", err
	}
	body, berr := httpx.ReadAll(resp)
	if berr != nil {
		return nil, "", berr
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return nil, "", &core.StatusError{URL: u, StatusCode: code}
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	_ = resp.Body.Close()
	var im imageManifest
	if err := json.Unmarshal(body, &im); err != nil {
		return nil, "", &core.ParseError{URL: u, Kind: "manifest", Err: err}
	}
	return &im, digest, nil
}

// normalizeImage adds library/ for official images without a namespace.
func normalizeImage(image string) string {
	if !strings.Contains(image, "/") {
		return "library/" + image
	}
	return image
}

// Status probes /v2/ (expect 200 or 401-challenge) and a tags list.
func (s *Service) Status(ctx context.Context, mirror string) (core.StatusResult, error) {
	res := core.StatusResult{Envelope: s.envelope(mirror), TestedPaths: []string{}}

	v2, err := httpx.JoinURL(mirror, "v2")
	if err == nil {
		res.TestedPaths = append(res.TestedPaths, v2)
		req, rerr := httpx.NewRequest(ctx, http.MethodGet, v2)
		if rerr == nil {
			start := time.Now()
			resp, derr := s.client().Do(req)
			if derr == nil {
				_, _ = httpx.ReadAll(resp)
				if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
					res.OK = true
					res.WorkingPath = v2
					res.StatusCode = resp.StatusCode
					res.Envelope.Latency = core.Duration(time.Since(start))
					res.Message = "Mirror is healthy and responding"
					return res, nil
				}
			} else if ctx.Err() != nil {
				return res, core.AsError(ctx.Err())
			}
		}
	}

	res.Message = "Mirror not responding or not a valid OCI registry"
	return res, &core.RequestError{URL: mirror, Err: core.ErrInvalidMirror}
}

// Speed measures throughput by downloading a layer blob.
func (s *Service) Speed(ctx context.Context, mirror string, o core.SpeedOptions) (core.SpeedResult, error) {
	res := core.SpeedResult{Envelope: s.envelope(mirror)}

	image := normalizeImage(o.Package)
	if o.Package == "" {
		image = "library/ubuntu"
	}
	tag := "latest"

	manifest, _, err := s.fetchManifest(ctx, mirror, image, tag)
	if err != nil {
		return res, err
	}
	if len(manifest.Layers) == 0 {
		return res, &core.RequestError{URL: mirror, Err: fmt.Errorf("no layers found in manifest")}
	}
	blobURL, err := httpx.JoinURL(mirror, "v2", image, "blobs", manifest.Layers[0].Digest)
	if err != nil {
		return res, err
	}
	res.TestURL = blobURL

	duration := o.Duration
	if duration <= 0 {
		duration = DefaultSpeedDuration
	}
	mbps, bytes, elapsed, _, mErr := httpx.MeasureSpeed(ctx, s.client(), blobURL, httpx.MeasureOptions{
		Duration:   duration,
		MinBytes:   o.MinBytes,
		OnProgress: nil,
	})
	res.Bytes = bytes
	res.DownloadedMB = float64(bytes) / 1024 / 1024
	res.DurationSec = elapsed.Seconds()
	res.Envelope.Latency = core.Duration(elapsed)
	if mErr != nil {
		if bytes >= 1024*1024 {
			res.SpeedMBps = mbps
			res.Rating = core.SpeedRating(mbps)
			res.Envelope.OK = true
			return res, nil
		}
		res.Envelope.OK = false
		return res, &core.SpeedError{URL: blobURL, Bytes: bytes, Elapsed: elapsed, Err: mErr}
	}
	res.SpeedMBps = mbps
	res.Rating = core.SpeedRating(mbps)
	res.Envelope.OK = true
	return res, nil
}

// Package lists tags for an image.
func (s *Service) Package(ctx context.Context, mirror string, q core.PackageQuery) (core.PackageResult, error) {
	res := core.PackageResult{Envelope: s.envelope(mirror), Name: q.Name}
	image := normalizeImage(strings.TrimSpace(q.Name))
	if image == "" {
		return res, &core.ValidationError{Field: "name", Message: "image name is required"}
	}

	tagsURL, err := httpx.JoinURL(mirror, "v2", image, "tags", "list")
	if err != nil {
		return res, &core.ValidationError{Field: "mirror", Message: err.Error()}
	}
	resp, err := s.authorize(ctx, tagsURL, "application/json", nil)
	if err != nil {
		return res, err
	}
	body, berr := httpx.ReadAll(resp)
	if berr != nil {
		return res, berr
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return res, nil // Found == false.
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return res, &core.StatusError{URL: tagsURL, StatusCode: code}
	}
	_ = resp.Body.Close()

	var tagsData struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &tagsData); err != nil {
		return res, &core.ParseError{URL: tagsURL, Kind: "json", Err: err}
	}
	sort.Strings(tagsData.Tags)
	if len(tagsData.Tags) == 0 {
		return res, nil
	}
	if q.Version != "" && !contains(tagsData.Tags, q.Version) {
		return res, nil // requested tag not present.
	}

	res.Found = true
	res.Version = q.Version
	if res.Version == "" && len(tagsData.Tags) > 0 {
		// Prefer the "latest" tag, else the lexicographically last tag.
		for _, t := range tagsData.Tags {
			if t == "latest" {
				res.Version = t
			}
		}
		if res.Version == "" {
			res.Version = tagsData.Tags[len(tagsData.Tags)-1]
		}
	}
	res.FoundPath = tagsURL
	res.Detail = Detail{Tags: tagsData.Tags, TotalTags: len(tagsData.Tags)}
	return res, nil
}

// Resolve returns the direct blob URL plus the Authorization header needed
// to fetch it.
func (s *Service) Resolve(ctx context.Context, mirror string, q core.PackageQuery) (core.DownloadResult, error) {
	res := core.DownloadResult{Envelope: s.envelope(mirror), Kind: "blob"}
	image := normalizeImage(strings.TrimSpace(q.Name))
	if image == "" {
		return res, &core.ValidationError{Field: "name", Message: "image name is required"}
	}
	tag := q.Version
	if tag == "" {
		tag = "latest"
	}

	manifest, digest, err := s.fetchManifest(ctx, mirror, image, tag)
	if err != nil {
		if se, ok := err.(*core.StatusError); ok && se.StatusCode == http.StatusNotFound {
			return res, fmt.Errorf("%w: image %q tag %q", core.ErrNotFound, image, tag)
		}
		return res, err
	}

	// Prefer a layer blob; fall back to the config blob.
	ref := ""
	size := int64(0)
	if len(manifest.Layers) > 0 {
		ref = manifest.Layers[0].Digest
		size = manifest.Layers[0].Size
	} else if manifest.Config.Digest != "" {
		ref = manifest.Config.Digest
		size = manifest.Config.Size
	} else {
		return res, fmt.Errorf("%w: manifest has no blobs for %q", core.ErrNotFound, image)
	}

	blobURL, err := httpx.JoinURL(mirror, "v2", image, "blobs", ref)
	if err != nil {
		return res, err
	}

	res.URL = blobURL
	res.SHA256 = strings.TrimPrefix(ref, "sha256:")
	res.Size = size
	res.Filename = strings.TrimPrefix(digest, "sha256:") + ".tar"
	res.Envelope.OK = true

	// Fetch a token for the caller's blob request (if the registry needs one).
	if tok, ok := s.blobToken(ctx, mirror, image); ok {
		res.Headers = map[string]string{"Authorization": "Bearer " + tok}
	}
	return res, nil
}

// blobToken obtains a pull token for the image scope, if challenged.
func (s *Service) blobToken(ctx context.Context, registry, image string) (string, bool) {
	v2, err := httpx.JoinURL(registry, "v2")
	if err != nil {
		return "", false
	}
	req, rerr := httpx.NewRequest(ctx, http.MethodGet, v2)
	if rerr != nil {
		return "", false
	}
	client := s.client()
	resp, derr := client.Do(req)
	if derr != nil {
		return "", false
	}
	if resp.StatusCode != http.StatusUnauthorized {
		_ = resp.Body.Close()
		return "", false
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	_ = resp.Body.Close()
	realm, params := parseChallenge(challenge)
	if realm == "" {
		return "", false
	}
	scope := "repository:" + image + ":pull"
	tokURL := realm + "?service=" + params["service"] + "&scope=" + scope
	treq, terr := httpx.NewRequest(ctx, http.MethodGet, tokURL)
	if terr != nil {
		return "", false
	}
	tresp, derr2 := client.Do(treq)
	if derr2 != nil {
		return "", false
	}
	tbody, berr := httpx.ReadAll(tresp)
	if berr != nil {
		_ = tresp.Body.Close()
		return "", false
	}
	_ = tresp.Body.Close()
	if tresp.StatusCode != http.StatusOK {
		return "", false
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(tbody, &tok) != nil {
		return "", false
	}
	if tok.Token != "" {
		return tok.Token, true
	}
	return tok.AccessToken, tok.AccessToken != ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
