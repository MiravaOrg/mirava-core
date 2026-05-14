package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type GoMirrorService struct {
	HttpClient *http.Client
}

type GoCheckSpeedParams struct {
	Module  string // Module to test speed with (e.g., "github.com/gorilla/mux")
	Version string // Specific version, empty for latest
}

type GoCheckSpeedData struct {
	DownloadMb      float64
	DurationSec     float64
	TimeoutSec      int
	SpeedMBps       float64
	SpeedRating     string
	BytesDownloaded int64
	ContentLength   int64
	Module          string
	Version         string
	ProxyURL        string
}

type GoCheckPackageData struct {
	Module   string
	Version  string
	Info     *GoModuleInfo
	Versions []string
	Latest   string
	IsCached bool
}

type GoCheckStatusData struct {
	Status       bool
	ProxyVersion string
	Ready        bool
	TestPath     string
}

// GoModuleInfo represents the module information from GOPROXY
type GoModuleInfo struct {
	Version string `json:"Version"`
	Time    string `json:"Time"`
	Origin  *struct {
		VCS  string `json:"vcs"`
		URL  string `json:"url"`
		Hash string `json:"hash"`
	} `json:"Origin,omitempty"`
}

func (m *GoMirrorService) CheckSpeed(
	mirrorURL string,
	timeout int,
	verbose bool,
	params GoCheckSpeedParams,
) (float64, *GoCheckSpeedData, error) {

	baseURL := strings.TrimSuffix(mirrorURL, "/")

	// Default test module if not specified
	module := params.Module
	if module == "" {
		module = "github.com/kubernetes/kubernetes"
	}

	// First, get the latest version if not specified
	var moduleVersion string
	if params.Version != "" {
		moduleVersion = params.Version
	} else {
		// Fetch version list to get latest
		listURL := fmt.Sprintf("%s/%s/@v/list", baseURL, module)

		if verbose {
			fmt.Printf("Fetching version list from: %s\n", listURL)
		}

		resp, err := m.HttpClient.Get(listURL)
		if err != nil {
			return 0, nil, &HttpRequestError{
				URL: listURL,
				Err: err,
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return 0, nil, &HttpRequestError{
				URL: listURL,
				Err: fmt.Errorf("HTTP %d for version list", resp.StatusCode),
			}
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to read version list: %w", err)
		}

		versions := strings.Split(strings.TrimSpace(string(body)), "\n")
		if len(versions) == 0 {
			return 0, nil, fmt.Errorf("no versions found for module %s", module)
		}

		moduleVersion = versions[0] // Latest version is first

		if verbose {
			fmt.Printf("Latest version: %s\n", moduleVersion)
		}
	}

	// Now download the actual module zip file for speed test
	zipURL := fmt.Sprintf("%s/%s/@v/%s.zip", baseURL, module, moduleVersion)

	if verbose {
		fmt.Printf("Testing Go proxy speed with: %s (timeout: %d seconds)\n", zipURL, timeout)
		fmt.Printf("Downloading module zip...\n")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeout)*time.Second,
	)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", zipURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: zipURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("User-Agent", USER_AGENT)

	startZip := time.Now()

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, &HttpRequestError{
				URL: zipURL,
				Err: fmt.Errorf("timeout reached before connection established"),
			}
		}

		return 0, nil, &HttpRequestError{
			URL: zipURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// If zip is not found, it might need to be cached. Try to trigger cache by HEAD request
		if resp.StatusCode == http.StatusNotFound {
			if verbose {
				fmt.Printf("Module zip not cached. Attempting to trigger cache...\n")
			}

			// Make a HEAD request to potentially trigger caching
			headReq, _ := http.NewRequestWithContext(ctx, "HEAD", zipURL, nil)
			headReq.Header.Set("User-Agent", USER_AGENT)
			headResp, _ := m.HttpClient.Do(headReq)
			if headResp != nil {
				headResp.Body.Close()
			}

			return 0, nil, &HttpRequestError{
				URL: zipURL,
				Err: fmt.Errorf("module zip not cached yet. Please request it once to trigger caching: curl -I %s", zipURL),
			}
		}

		return 0, nil, &HttpRequestError{
			URL: zipURL,
			Err: fmt.Errorf("HTTP %d for module zip", resp.StatusCode),
		}
	}

	contentLength := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 512*1024)
	lastProgress := time.Now()

	if verbose {
		if contentLength > 0 {
			fmt.Printf("Module size: %.2f MB\n", float64(contentLength)/1024/1024)
		}
		fmt.Printf("Downloading for up to %d seconds...\n", timeout)
	}

	for {
		select {
		case <-ctx.Done():
			if verbose {
				fmt.Printf("\nTimeout reached after %d seconds\n", timeout)
			}
			goto calculateSpeed
		default:
			n, err := resp.Body.Read(buf)
			if n > 0 {
				downloaded += int64(n)

				if verbose && time.Since(lastProgress) > 500*time.Millisecond {
					elapsed := time.Since(startZip).Seconds()
					speedMBps := (float64(downloaded) / 1024 / 1024) / elapsed

					if contentLength > 0 {
						percent := float64(downloaded) / float64(contentLength) * 100
						fmt.Printf("\r[%ds] %.1f%% (%.2f/%.2f MB) - %.2f MB/s",
							int(elapsed), percent,
							float64(downloaded)/1024/1024,
							float64(contentLength)/1024/1024,
							speedMBps)
					} else {
						fmt.Printf("\r[%ds] Downloaded: %.2f MB - %.2f MB/s",
							int(elapsed),
							float64(downloaded)/1024/1024,
							speedMBps)
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
					URL: zipURL,
					Err: err,
				}
			}
		}
	}

calculateSpeed:
	duration := time.Since(startZip).Seconds()

	if verbose {
		fmt.Printf("\nDownloaded %.2f MB in %.2f seconds\n",
			float64(downloaded)/1024/1024, duration)
	}

	if duration > 0 && downloaded > 0 {
		speedMBps := (float64(downloaded) / 1024 / 1024) / duration

		if verbose {
			fmt.Printf("Average speed: %.2f MB/s\n", speedMBps)
			fmt.Printf("Rating: %s\n", getGoSpeedRating(speedMBps))
		}

		info := GoCheckSpeedData{
			DownloadMb:      float64(downloaded) / 1024 / 1024,
			DurationSec:     duration,
			TimeoutSec:      timeout,
			SpeedMBps:       speedMBps,
			SpeedRating:     getGoSpeedRating(speedMBps),
			BytesDownloaded: downloaded,
			ContentLength:   contentLength,
			Module:          module,
			Version:         moduleVersion,
			ProxyURL:        baseURL,
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &HttpRequestError{
		URL: zipURL,
		Err: fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration),
	}
}

func (m *GoMirrorService) CheckPackage(
	mirrorURL string,
	packageName string,
	verbose bool,
) (bool, *GoCheckPackageData, error) {

	baseURL := strings.TrimSuffix(mirrorURL, "/")

	// GOPROXY protocol: GET /{module}/@v/list returns all versions
	listURL := fmt.Sprintf("%s/%s/@v/list", baseURL, packageName)

	if verbose {
		fmt.Printf("Fetching version list from: %s\n", listURL)
	}

	resp, err := m.HttpClient.Get(listURL)
	if err != nil {
		return false, nil, fmt.Errorf("failed to fetch version list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("module not found: HTTP %d", resp.StatusCode)
	}

	// Read version list (one version per line)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("failed to read response: %w", err)
	}

	versions := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(versions) == 0 || (len(versions) == 1 && versions[0] == "") {
		if verbose {
			fmt.Printf("No versions found for module '%s'\n", packageName)
		}
		return false, nil, nil
	}

	// Get the latest version (first in list is typically latest)
	latestVersion := versions[0]

	if verbose {
		fmt.Printf("Found module '%s' with %d versions, latest: %s\n",
			packageName, len(versions), latestVersion)
	}

	// Fetch module info for the latest version
	infoURL := fmt.Sprintf("%s/%s/@v/%s.info", baseURL, packageName, latestVersion)

	infoResp, err := m.HttpClient.Get(infoURL)
	if err != nil {
		return false, nil, fmt.Errorf("failed to fetch module info: %w", err)
	}
	defer infoResp.Body.Close()

	if infoResp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("module info not found: HTTP %d", infoResp.StatusCode)
	}

	infoBody, err := io.ReadAll(infoResp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("failed to read module info: %w", err)
	}

	var moduleInfo GoModuleInfo
	if err := json.Unmarshal(infoBody, &moduleInfo); err != nil {
		return false, nil, fmt.Errorf("failed to parse module info: %w", err)
	}

	info := &GoCheckPackageData{
		Module:   packageName,
		Version:  latestVersion,
		Info:     &moduleInfo,
		Versions: versions,
		Latest:   latestVersion,
	}

	return true, info, nil
}

func (m *GoMirrorService) CheckStatus(
	url string,
	verbose bool,
) (bool, *GoCheckStatusData, error) {

	baseURL := strings.TrimSuffix(url, "/")

	// Test endpoints for GOPROXY
	testPaths := []string{
		"/",                               // Root should return 200 OK
		"/github.com/gorilla/mux/@v/list", // Test module list
	}

	for _, path := range testPaths {
		testURL := baseURL + path

		if verbose {
			fmt.Printf("Testing Go proxy endpoint: %s\n", testURL)
		}

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			continue
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		req.Header.Set("User-Agent", USER_AGENT)

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			if verbose {
				fmt.Printf("Failed: %v\n", err)
			}
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			if verbose {
				fmt.Printf("Mirror responded successfully with status %d\n", resp.StatusCode)
			}

			info := GoCheckStatusData{
				Status:       true,
				ProxyVersion: "go-proxy",
				Ready:        true,
				TestPath:     path,
			}

			return true, &info, nil
		}
	}

	return false, nil, &HttpRequestError{
		URL: baseURL,
		Err: fmt.Errorf("mirror does not appear to be a valid Go module proxy"),
	}
}

func getGoSpeedRating(speedMBps float64) string {
	switch {
	case speedMBps > 50:
		return "Excellent"
	case speedMBps > 20:
		return "Good"
	case speedMBps > 10:
		return "Average"
	case speedMBps > 5:
		return "Slow"
	default:
		return "Very Slow"
	}
}

// NewGoMirrorService creates a new Go module proxy service instance
func NewGoMirrorService() *GoMirrorService {
	return &GoMirrorService{
		HttpClient: &http.Client{
			Timeout: 60 * time.Second,
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
