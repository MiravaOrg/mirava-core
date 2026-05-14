package pkg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type PyPIMirrorService struct {
	HttpClient *http.Client
}

type PyPiCheckSpeedData struct {
	DownloadedMB float64
	DurationSec  float64
	TimeoutSec   int
	SpeedMbps    float64
	SpeedRating  string
}

type PyPiCheckPackageData struct {
	Version       string
	VersionsCount int
	AllVersions   []string
}

type PyPiCheckStatusData struct {
	Status     bool
	TestPath   string
	StatusCode int
}

func (m *PyPIMirrorService) CheckSpeed(mirrorURL string, timeout int, verbose bool) (float64, *PyPiCheckSpeedData, error) {
	baseURL := strings.TrimSuffix(mirrorURL, "/")
	testURL := baseURL + "/simple/"

	if verbose {
		fmt.Printf("Testing PyPI Mirror speed with: %s (timeout: %d seconds)\n", testURL, timeout)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	start := time.Now()
	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, &HttpRequestError{
				URL: testURL,
				Err: fmt.Errorf("timeout reached before connection established"),
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
			URL: testURL,
			Err: fmt.Errorf("HTTP %d for speed test (expected 200)", resp.StatusCode),
		}
	}

	var downloaded int64
	buf := make([]byte, 512*1024)
	lastProgress := time.Now()

	if verbose {
		fmt.Printf("Downloading for up to %d seconds...\n", timeout)
	}

	// Download until timeout
	for {
		select {
		case <-ctx.Done():
			// Timeout reached
			goto calculateSpeed

		default:
			n, err := resp.Body.Read(buf)

			if n > 0 {
				downloaded += int64(n)

				if verbose && time.Since(lastProgress) > 500*time.Millisecond {
					elapsed := time.Since(start).Seconds()
					speedMBps := (float64(downloaded) / 1024 / 1024) / elapsed

					fmt.Printf(
						"\r[%ds] Downloaded: %.2f MB - %.2f MB/s",
						int(elapsed),
						float64(downloaded)/1024/1024,
						speedMBps,
					)

					lastProgress = time.Now()
				}
			}

			if err != nil {
				if err == io.EOF {
					if verbose {
						fmt.Println()
					}

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
		speedMBps := (float64(downloaded) / 1024 / 1024) / duration

		if verbose {
			fmt.Printf("Average speed: %.2f MB/s\n", speedMBps)
			fmt.Printf("Rating: %s\n", getPyPISpeedRating(speedMBps))
		}

		info := PyPiCheckSpeedData{
			DownloadedMB: float64(downloaded) / 1024 / 1024,
			DurationSec:  duration,
			TimeoutSec:   timeout,
			SpeedMbps:    speedMBps,
			SpeedRating:  getPyPISpeedRating(speedMBps),
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &HttpRequestError{
		URL: testURL,
		Err: fmt.Errorf(
			"speed test failed (downloaded %d bytes in %.2fs)",
			downloaded,
			duration,
		),
	}
}

// CheckPackage checks if a package exists on a PyPI mirror using the simple API
// Returns: (exists, version, error)
func (m *PyPIMirrorService) CheckPackage(mirrorUrl, packageName string, verbose bool) (bool, *PyPiCheckPackageData, error) {
	// Use the simple API format: {mirror_url}/simple/{package_name}/
	baseURL := strings.TrimSuffix(mirrorUrl, "/")
	packageURL := fmt.Sprintf("%s/simple/%s/", baseURL, packageName)

	if verbose {
		fmt.Println("Checking package: ", packageURL)
	}

	req, err := http.NewRequest("GET", packageURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if verbose {
			fmt.Printf("Error checking package: %v\n", err)
		}

		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if verbose {
			fmt.Printf("Package '%s' not found on mirror\n", packageName)
		}

		return false, nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: fmt.Errorf("HTTP %d from PyPI mirror", resp.StatusCode),
		}
	}

	// Parse the HTML response to find the latest version
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}

	// Extract version numbers from the HTML
	versionRegex := regexp.MustCompile(
		fmt.Sprintf(
			`%s-([0-9]+(?:\.[0-9]+)*(?:[a-z]?[0-9]*)?)`,
			regexp.QuoteMeta(packageName),
		),
	)

	versions := make(map[string]bool)
	matches := versionRegex.FindAllStringSubmatch(string(body), -1)

	for _, match := range matches {
		if len(match) > 1 {
			versions[match[1]] = true
		}
	}

	if len(versions) > 0 {
		// Find the latest version
		var latestVersion string

		for version := range versions {
			if latestVersion == "" || version > latestVersion {
				latestVersion = version
			}
		}

		if verbose {
			fmt.Printf(
				"Found package '%s' with latest version: %s (%d versions available)\n",
				packageName,
				latestVersion,
				len(versions),
			)
		}

		// Store package info
		info := PyPiCheckPackageData{
			Version:       latestVersion,
			VersionsCount: len(versions),
			AllVersions:   getVersionList(versions),
		}

		return true, &info, nil
	}

	if verbose {
		fmt.Printf("Package '%s' found but no version could be extracted\n", packageName)
	}

	// Package exists but we couldn't extract version
	info := PyPiCheckPackageData{
		Version: "unknown",
	}

	return true, &info, nil
}

// CheckStatus checks if a PyPI mirror is alive and responding
func (m *PyPIMirrorService) CheckStatus(url string, verbose bool) (bool, *interface{}, error) {
	// Test the simple endpoint for PyPI mirror
	testURL := strings.TrimSuffix(url, "/") + "/simple/"

	if verbose {
		fmt.Printf("Testing PyPI mirror endpoint: %s\n", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		if verbose {
			fmt.Printf("Mirror responded to /simple/ with status %d\n", resp.StatusCode)
		}

		info := map[string]interface{}{
			"status":      "active",
			"tested_path": "/simple/",
			"status_code": resp.StatusCode,
		}

		var iface interface{} = info
		return true, &iface, nil
	}

	return false, nil, &HttpRequestError{
		URL: testURL,
		Err: fmt.Errorf(
			"mirror does not appear to be a valid PyPI mirror (simple endpoint returned %d)",
			resp.StatusCode,
		),
	}
}

// Helper function to get PyPI speed rating
func getPyPISpeedRating(speedMBps float64) string {
	switch {
	case speedMBps > 20:
		return "Excellent"
	case speedMBps > 10:
		return "Good"
	case speedMBps > 5:
		return "Average"
	default:
		return "Slow"
	}
}

// Helper function to convert version map to slice
func getVersionList(versions map[string]bool) []string {
	versionList := make([]string, 0, len(versions))

	for version := range versions {
		versionList = append(versionList, version)
	}

	return versionList
}

// NewPyPIMirrorService creates a new PyPI mirror service instance
func NewPyPIMirrorService() *PyPIMirrorService {
	return &PyPIMirrorService{
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
