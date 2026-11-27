// Package httpx provides shared HTTP plumbing for mirava-core backends:
// URL joining, the shared default client, User-Agent construction, and a
// single speed-measurement loop used by every backend's Speed probe.
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout bounds every individual HTTP request made by the default
// client. Backends accept an injected client; when none is provided this one
// is used. Callers that want per-probe budgets pass context deadlines instead.
const DefaultTimeout = 60 * time.Second

// userAgent is computed once. -X httpx.userAgent=... via ldflags overrides
// the version detected from build info.
var (
	userAgentOnce sync.Once
	userAgent     string
)

// SetUserAgentVersion overrides the version reported in the User-Agent
// header. Intended for builds where build info is unavailable (e.g. go run
// with -ldflags "-X github.com/.../internal/httpx.userAgentVersion=v0.4.0").
func SetUserAgentVersion(v string) { userAgentOnce = sync.Once{}; userAgentVersion = v }

var userAgentVersion = "devel"

// UserAgent returns the User-Agent value used for all probe requests, e.g.
// "mirava-core/0.4.0 (https://github.com/MiravaOrg/mirava-core)".
func UserAgent() string {
	userAgentOnce.Do(func() {
		v := userAgentVersion
		if v == "devel" {
			if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
				v = info.Main.Version
			}
		}
		userAgent = "mirava-core/" + v + " (+" + ModulePath + ")"
	})
	return userAgent
}

// ModulePath is this module's import path.
const ModulePath = "github.com/MiravaOrg/mirava-core"

var (
	defaultClientOnce sync.Once
	defaultClient     *http.Client
)

// DefaultClient returns the process-wide client used when a backend is not
// given one. It is shared so connection pools are reused across backends.
func DefaultClient() *http.Client {
	defaultClientOnce.Do(func() {
		defaultClient = &http.Client{
			Timeout: DefaultTimeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				ForceAttemptHTTP2:   true,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		}
	})
	return defaultClient
}

// ClientOr returns c if it is non-nil, else the shared default client. All
// backends call this so a zero-value service never panics on a nil client.
func ClientOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return DefaultClient()
}

// NewRequest builds a GET request with the mirava User-Agent set. Returns an
// error suitable for *RequestError wrapping by the caller.
func NewRequest(ctx context.Context, method, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent())
	return req, nil
}

// Do sends req via c, wrapping transport errors. On non-2xx responses the
// body is drained and closed by the caller via resp.Body.
func Do(ctx context.Context, c *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return resp, nil
}

// ReadAll reads and closes resp.Body.
func ReadAll(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// JoinURL joins base with path segments, normalizing slashes so both
// "https://m/ubuntu/" and "https://m/ubuntu" produce the same result. This is
// the only way backends combine a mirror base URL with a path.
func JoinURL(base string, paths ...string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("invalid mirror URL %q: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("mirror URL %q must use http or https", base)
	}
	return u.JoinPath(paths...).String(), nil
}

// joinURLWithHost rebuilds a URL from host and path segments. Kept for
// backends that need to swap the host (e.g. Docker registry blob redirects).
func joinURLWithHost(scheme, host string, paths ...string) string {
	u := &url.URL{Scheme: scheme, Host: host}
	return u.JoinPath(paths...).String()
}

// Progress describes incremental speed-test progress. OnProgress is invoked
// at most every ProgressInterval (implementations decide).
type Progress struct {
	Bytes    int64
	Total    int64 // 0 when unknown
	Elapsed  time.Duration
	SpeedMBs float64
}

// MeasureOptions configures MeasureSpeed.
type MeasureOptions struct {
	// Duration bounds the download. Zero means unbounded (rely on ctx).
	Duration time.Duration
	// MinBytes is the smallest sample considered valid. Shorter downloads
	// produce a measurement error instead of a wildly inflated speed.
	MinBytes int64
	// OnProgress, when non-nil, receives incremental progress.
	OnProgress func(Progress)
	// Headers added to the request (e.g. Docker registry auth).
	Headers http.Header
}

// MeasureSpeed downloads url for at most opts.Duration (bounded by ctx) and
// returns the average throughput in MB/s, total bytes, and elapsed time.
// It is the single implementation of the speed-test loop every backend uses.
func MeasureSpeed(ctx context.Context, client *http.Client, rawURL string, opts MeasureOptions) (mbs float64, bytes int64, elapsed time.Duration, finalURL string, err error) {
	if opts.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Duration)
		defer cancel()
	}

	req, err := NewRequest(ctx, http.MethodGet, rawURL)
	if err != nil {
		return 0, 0, 0, "", err
	}
	for k, vs := range opts.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, 0, rawURL, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	start := time.Now()
	buf := make([]byte, 256*1024)
	var downloaded int64
	last := time.Now()

	for {
		select {
		case <-ctx.Done():
			goto done
		default:
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				downloaded += int64(n)
				now := time.Now()
				if opts.OnProgress != nil && now.Sub(last) >= 200*time.Millisecond {
					el := now.Sub(start)
					opts.OnProgress(Progress{
						Bytes:    downloaded,
						Total:    resp.ContentLength,
						Elapsed:  el,
						SpeedMBs: mbps(downloaded, el),
					})
					last = now
				}
			}
			if rerr == io.EOF {
				goto done
			}
			if rerr != nil {
				if ctx.Err() != nil {
					goto done
				}
				return 0, downloaded, time.Since(start), resp.Request.URL.String(), rerr
			}
		}
	}

done:
	elapsed = time.Since(start)
	if downloaded < opts.MinBytes {
		return 0, downloaded, elapsed, resp.Request.URL.String(), fmt.Errorf("insufficient sample: %d bytes in %s", downloaded, elapsed)
	}
	if downloaded == 0 || elapsed <= 0 {
		return 0, downloaded, elapsed, resp.Request.URL.String(), fmt.Errorf("no data transferred")
	}
	return mbps(downloaded, elapsed), downloaded, elapsed, resp.Request.URL.String(), nil
}

func mbps(bytes int64, el time.Duration) float64 {
	if el <= 0 {
		return 0
	}
	return (float64(bytes) / 1024 / 1024) / el.Seconds()
}
