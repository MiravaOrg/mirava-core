package pkg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

type CargoMirrorService struct {
	HttpClient *http.Client
}

type CargoCheckSpeedParams struct {
	PackageName string
}

type CargoCheckSpeedData struct {
	DownloadMb      float64
	DurationSec     float64
	ContentLength   int64
	TimeoutSec      int
	SpeedMbps       float64
	SpeedRating     string
	BytesDownloaded int64
	Crate           string
}

type CargoCheckPackageData struct {
	Version       string
	VersionsCount int
	AllVersions   []string
}

type CargoCheckStatusData struct {
	Status     bool
	TestPath   string
	StatusCode int
}

type cargoIndexEntry struct {
	Name   string `json:"name"`
	Vers   string `json:"vers"`
	Yanked bool   `json:"yanked"`
}

var cargoCrateNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// cargoSparseCratePath returns the relative path for a crate on a Cargo sparse HTTP index
// (same layout as crates.io — see https://doc.rust-lang.org/cargo/reference/registry-index.html).
func cargoSparseCratePath(crate string) (string, error) {
	name := strings.TrimSpace(crate)
	if name == "" {
		return "", fmt.Errorf("crate name is empty")
	}

	lower := strings.ToLower(name)
	if !cargoCrateNamePattern.MatchString(lower) {
		return "", fmt.Errorf(
			"crate name %q is not supported (use ASCII letters, digits, hyphen, underscore)",
			crate,
		)
	}

	switch len(lower) {
	case 1:
		return "1/" + lower, nil
	case 2:
		return "2/" + lower, nil
	case 3:
		return fmt.Sprintf("3/%s/%s", lower[:1], lower), nil
	default:
		return fmt.Sprintf("%s/%s/%s", lower[:2], lower[2:4], lower), nil
	}
}

func cargoCrateIndexURL(baseURL, crate string) (string, error) {
	rel, err := cargoSparseCratePath(crate)
	if err != nil {
		return "", err
	}

	return strings.TrimSuffix(baseURL, "/") + "/" + rel, nil
}

func parseCargoIndexBody(body io.Reader) ([]cargoIndexEntry, error) {
	sc := bufio.NewScanner(body)
	// Lines can be long (dependency metadata); allow generous buffer.
	const maxToken = 12 * 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxToken)

	var entries []cargoIndexEntry

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var e cargoIndexEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}

		if e.Vers != "" {
			entries = append(entries, e)
		}
	}

	if err := sc.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}

func latestNonYankedVersion(entries []cargoIndexEntry) (string, []string, int) {
	uniq := make(map[string]struct{})
	for _, e := range entries {
		uniq[e.Vers] = struct{}{}
	}

	all := make([]string, 0, len(uniq))
	for v := range uniq {
		all = append(all, v)
	}

	var best string
	var bestCanon string

	for _, e := range entries {
		if e.Yanked {
			continue
		}

		v := e.Vers
		cv := "v" + v
		if !semver.IsValid(cv) {
			if best == "" {
				best = v
			}

			continue
		}

		cc := semver.Canonical(cv)
		if best == "" {
			best = v
			bestCanon = cc

			continue
		}

		if bestCanon != "" && semver.Compare(cc, bestCanon) > 0 {
			best = v
			bestCanon = cc
		} else if bestCanon == "" {
			// Earlier best was non-semver; prefer first valid semver.
			best = v
			bestCanon = cc
		}
	}

	if best == "" {
		// All yanked — fall back to last line's version for existence check.
		if len(entries) > 0 {
			best = entries[len(entries)-1].Vers
		}
	}

	return best, all, len(uniq)
}

func (m *CargoMirrorService) CheckSpeed(
	mirrorURL string,
	timeout int,
	verbose bool,
	params *CargoCheckSpeedParams,
) (float64, *CargoCheckSpeedData, error) {
	testCrate := "serde"
	if params != nil && strings.TrimSpace(params.PackageName) != "" {
		testCrate = strings.TrimSpace(params.PackageName)
	}

	testURL, err := cargoCrateIndexURL(mirrorURL, testCrate)
	if err != nil {
		return 0, nil, &ValidationError{Field: "package", Message: err.Error()}
	}

	if verbose {
		fmt.Printf("Testing Cargo sparse index mirror with: %s\n", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", USER_AGENT)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeout)*time.Second,
	)
	defer cancel()

	req = req.WithContext(ctx)

	start := time.Now()

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, &TimeoutError{
				URL:     testURL,
				Timeout: timeout,
				Err:     ctx.Err(),
			}
		}

		return 0, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, &HttpRequestError{
			URL:        testURL,
			StatusCode: resp.StatusCode,
		}
	}

	contentLength := resp.ContentLength

	if contentLength > 0 && verbose {
		fmt.Printf(
			"Content-Length: %.2f MB\n",
			float64(contentLength)/1024/1024,
		)
	}

	var downloaded int64

	buf := make([]byte, 512*1024)

	lastProgress := time.Now()

	if verbose {
		fmt.Printf("Downloading for up to %d seconds...\n", timeout)
	}

	for {
		select {
		case <-ctx.Done():
			if verbose {
				fmt.Printf(
					"\nTimeout reached after %d seconds\n",
					timeout,
				)
			}

			goto calculateSpeed

		default:
			n, err := resp.Body.Read(buf)

			if n > 0 {
				downloaded += int64(n)

				if verbose && time.Since(lastProgress) > 500*time.Millisecond {
					elapsed := time.Since(start).Seconds()

					speedMBps :=
						(float64(downloaded) / 1024 / 1024) / elapsed

					if contentLength > 0 {
						percent :=
							float64(downloaded) /
								float64(contentLength) *
								100

						fmt.Printf(
							"\r[%ds] %.1f%% (%.2f/%.2f MB) - %.2f MB/s",
							int(elapsed),
							percent,
							float64(downloaded)/1024/1024,
							float64(contentLength)/1024/1024,
							speedMBps,
						)
					} else {
						fmt.Printf(
							"\r[%ds] Downloaded: %.2f MB - %.2f MB/s",
							int(elapsed),
							float64(downloaded)/1024/1024,
							speedMBps,
						)
					}

					lastProgress = time.Now()
				}
			}

			if err != nil {
				if err == io.EOF {
					if verbose {
						fmt.Println("\nReached end of file")
					}

					goto calculateSpeed
				}

				if ctx.Err() == context.DeadlineExceeded {
					goto calculateSpeed
				}

				return 0, nil, &HttpRequestError{
					URL: testURL,
					Err: err,
				}
			}
		}
	}

calculateSpeed:
	duration := time.Since(start).Seconds()

	if verbose {
		fmt.Printf(
			"\nDownloaded %.2f MB in %.2f seconds\n",
			float64(downloaded)/1024/1024,
			duration,
		)
	}

	if duration > 0 && downloaded > 0 {
		speedMBps :=
			(float64(downloaded) / 1024 / 1024) / duration

		if verbose {
			fmt.Printf("Average speed: %.2f MB/s\n", speedMBps)

			rating := getSpeedRating(speedMBps)

			fmt.Printf("Rating: %s\n", rating)
		}

		info := CargoCheckSpeedData{
			DownloadMb:      float64(downloaded) / 1024 / 1024,
			DurationSec:     duration,
			ContentLength:   contentLength,
			TimeoutSec:      timeout,
			SpeedMbps:       speedMBps,
			SpeedRating:     getSpeedRating(speedMBps),
			BytesDownloaded: downloaded,
			Crate:           testCrate,
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &SpeedTestError{
		URL: testURL,
		Err: fmt.Errorf("failed to speed test %v", testURL),
	}
}

// CheckPackage checks if a crate exists on a Cargo sparse HTTP index mirror.
func (m *CargoMirrorService) CheckPackage(
	mirrorURL,
	crateName string,
	verbose bool,
) (bool, *CargoCheckPackageData, error) {
	packageURL, err := cargoCrateIndexURL(mirrorURL, crateName)
	if err != nil {
		return false, nil, &ValidationError{Field: "package", Message: err.Error()}
	}

	if verbose {
		fmt.Println("Checking crate:", packageURL)
	}

	req, err := http.NewRequest("GET", packageURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", USER_AGENT)

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if verbose {
			fmt.Printf("Error checking crate: %v\n", err)
		}

		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if verbose {
			fmt.Printf("Crate '%s' not found on mirror\n", crateName)
		}

		return false, nil, &PackageNotFoundError{
			Package: crateName,
		}
	}

	if resp.StatusCode != http.StatusOK {
		return false, nil, &HttpRequestError{
			URL:        packageURL,
			StatusCode: resp.StatusCode,
		}
	}

	entries, err := parseCargoIndexBody(resp.Body)
	if err != nil {
		return false, nil, &JsonParseError{
			URL: packageURL,
			Err: err,
		}
	}

	if len(entries) == 0 {
		return false, nil, &PackageNotFoundError{
			Package: crateName,
		}
	}

	latest, allVersions, nVers := latestNonYankedVersion(entries)

	if verbose {
		fmt.Printf(
			"Found crate '%s' with latest version: %s (%d versions in index)\n",
			crateName,
			latest,
			nVers,
		)
	}

	info := CargoCheckPackageData{
		Version:       latest,
		VersionsCount: nVers,
		AllVersions:   allVersions,
	}

	return true, &info, nil
}

func (m *CargoMirrorService) CheckStatus(
	url string,
	verbose bool,
) (bool, *CargoCheckStatusData, error) {
	base := strings.TrimSuffix(url, "/")

	candidates := []struct {
		path string
	}{
		{"/config.json"},
		{"/li/bc/libc"},
		{"/se/rd/serde"},
	}

	for _, c := range candidates {
		testURL := base + c.path

		if verbose {
			fmt.Printf("Testing Cargo sparse index endpoint: %s\n", testURL)
		}

		req, err := http.NewRequest(http.MethodGet, testURL, nil)
		if err != nil {
			continue
		}

		req.Header.Set(
			"Cache-Control",
			"no-cache, no-store, must-revalidate",
		)

		if strings.HasSuffix(c.path, "config.json") {
			req.Header.Set("Accept", "application/json")
		} else {
			req.Header.Set("Accept", "text/plain")
		}

		req.Header.Set("User-Agent", USER_AGENT)

		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		req = req.WithContext(ctx)

		resp, err := m.HttpClient.Do(req)
		cancel()

		if err != nil {
			if verbose {
				fmt.Printf("Error checking endpoint: %v\n", err)
			}

			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()

			continue
		}

		if strings.HasSuffix(c.path, "config.json") {
			var stub map[string]interface{}

			dec := json.NewDecoder(io.LimitReader(resp.Body, 256*1024))
			if err := dec.Decode(&stub); err != nil {
				resp.Body.Close()

				continue
			}
		} else {
			if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024)); err != nil {
				resp.Body.Close()

				continue
			}
		}

		resp.Body.Close()

		if verbose {
			fmt.Printf(
				"Mirror responded to %s with status %d\n",
				c.path,
				resp.StatusCode,
			)
		}

		info := CargoCheckStatusData{
			Status:     true,
			TestPath:   c.path,
			StatusCode: resp.StatusCode,
		}

		return true, &info, nil
	}

	return false, nil, &InvalidMirrorError{
		URL: url,
	}
}

// NewCargoMirrorService creates a new Cargo sparse index mirror service instance.
func NewCargoMirrorService() *CargoMirrorService {
	return &CargoMirrorService{
		HttpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DisableCompression:  false,
				DisableKeepAlives:   false,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}
