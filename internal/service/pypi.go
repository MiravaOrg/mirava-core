package service

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core"
)

type PyPIMirrorService struct {
	HttpClient *http.Client
}

func (m *PyPIMirrorService) CheckMirrorSpeed(mirrorURL string, verbose bool) (float64, *interface{}, error) {
	// Use the simple index for speed testing (can be large with many packages)
	baseURL := strings.TrimSuffix(mirrorURL, "/")
	testURL := baseURL + "/simple/"

	if verbose {
		fmt.Printf("Testing PyPI Mirror speed with: %s\n", testURL)
		fmt.Printf("Fetching package index for speed test\n")
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, nil, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	start := time.Now()
	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("HTTP %d for speed test (expected 200)", resp.StatusCode)
	}

	// Download at least 5MB for accurate speed test
	minBytes := int64(5 * 1024 * 1024)
	var downloaded int64
	buf := make([]byte, 512*1024)

	// Show progress bar in verbose mode
	if verbose {
		fmt.Print("Downloading: ")
	}

	lastProgress := time.Now()
	for downloaded < minBytes && time.Since(start) < 15*time.Second {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)

			// Show progress every 500ms
			if verbose && time.Since(lastProgress) > 500*time.Millisecond {
				fmt.Printf("\rDownloaded: %.2f MB", float64(downloaded)/1024/1024)
				lastProgress = time.Now()
			}
		}
		if err != nil {
			if err == io.EOF {
				if verbose {
					fmt.Println()
				}
				break
			}
			return 0, nil, err
		}
	}

	if verbose {
		fmt.Println()
	}

	duration := time.Since(start).Seconds()
	if duration > 0 && downloaded > 0 {
		speedMBps := (float64(downloaded) / 1024 / 1024) / duration

		if verbose {
			fmt.Printf("Downloaded %.2f MB in %.2f seconds\n", float64(downloaded)/1024/1024, duration)
			fmt.Printf("Average speed: %.2f MB/s\n", speedMBps)

			// Provide a speed rating
			switch {
			case speedMBps > 20:
				fmt.Println("Rating: Excellent ⚡⚡⚡")
			case speedMBps > 10:
				fmt.Println("Rating: Good ⚡⚡")
			case speedMBps > 5:
				fmt.Println("Rating: Average ⚡")
			default:
				fmt.Println("Rating: Slow ⚠")
			}
		}

		// Store speed test info
		info := map[string]interface{}{
			"downloaded_mb": float64(downloaded) / 1024 / 1024,
			"duration_sec":  duration,
			"speed_rating":  getPyPISpeedRating(speedMBps),
		}
		var iface interface{} = info
		return speedMBps, &iface, nil
	}

	return 0, nil, fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
}

// CheckPackage checks if a package exists on a PyPI mirror using the simple API
// Returns: (exists, version, error)
func (m *PyPIMirrorService) CheckPackage(mirrorUrl, packageName string, verbose bool) (bool, *interface{}, error) {
	// Use the simple API format: {mirror_url}/simple/{package_name}/
	baseURL := strings.TrimSuffix(mirrorUrl, "/")
	packageURL := fmt.Sprintf("%s/simple/%s/", baseURL, packageName)

	if verbose {
		fmt.Println("Checking package: ", packageURL)
	}

	req, err := http.NewRequest("GET", packageURL, nil)
	if err != nil {
		return false, nil, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if verbose {
			fmt.Printf("Error checking package: %v\n", err)
		}
		return false, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if verbose {
			fmt.Printf("Package '%s' not found on mirror\n", packageName)
		}
		return false, nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("HTTP %d from PyPI mirror", resp.StatusCode)
	}

	// Parse the HTML response to find the latest version
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, err
	}

	// Extract version numbers from the HTML
	// The simple index contains links like: <a href="fastapi-0.104.1.tar.gz">fastapi-0.104.1.tar.gz</a>
	// or <a href="../../packages/.../fastapi-0.104.1-py3-none-any.whl">fastapi-0.104.1-py3-none-any.whl</a>

	// Regex to find version numbers in package files
	versionRegex := regexp.MustCompile(fmt.Sprintf(`%s-([0-9]+(?:\.[0-9]+)*(?:[a-z]?[0-9]*)?)`, regexp.QuoteMeta(packageName)))

	versions := make(map[string]bool)
	matches := versionRegex.FindAllStringSubmatch(string(body), -1)

	for _, match := range matches {
		if len(match) > 1 {
			versions[match[1]] = true
		}
	}

	if len(versions) > 0 {
		// Find the latest version (simple string comparison works for semantic versioning)
		var latestVersion string
		for version := range versions {
			if latestVersion == "" || version > latestVersion {
				latestVersion = version
			}
		}

		if verbose {
			fmt.Printf("Found package '%s' with latest version: %s (%d versions available)\n",
				packageName, latestVersion, len(versions))
		}

		// Store package info
		info := map[string]interface{}{
			"version":        latestVersion,
			"versions_count": len(versions),
			"all_versions":   getVersionList(versions),
		}
		var iface interface{} = info
		return true, &iface, nil
	}

	if verbose {
		fmt.Printf("Package '%s' found but no version could be extracted\n", packageName)
	}

	// Package exists but we couldn't extract version
	info := map[string]interface{}{
		"version": "unknown",
		"exists":  true,
	}
	var iface interface{} = info
	return true, &iface, nil
}

// CheckMirrorStatus checks if a PyPI mirror is alive and responding
func (m *PyPIMirrorService) CheckMirrorStatus(url string, verbose bool) (bool, *interface{}, error) {
	// Test the simple endpoint for PyPI mirror
	testURL := strings.TrimSuffix(url, "/") + "/simple/"

	if verbose {
		fmt.Printf("Testing PyPI mirror endpoint: %s\n", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return false, nil, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return false, nil, err
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
			"mirror_type": "pypi",
		}
		var iface interface{} = info
		return true, &iface, nil
	}

	return false, nil, fmt.Errorf("mirror does not appear to be a valid PyPI mirror (simple endpoint returned %d)", resp.StatusCode)
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
func NewPyPIMirrorService() mirava_core.MirrorService {
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
