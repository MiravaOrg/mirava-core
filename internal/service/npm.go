package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core"
)

type NpmMirrorService struct {
	HttpClient *http.Client
}

func (m *NpmMirrorService) CheckMirrorSpeed(mirrorURL string, verbose bool) (float64, *interface{}, error) {
	testURL := strings.TrimSuffix(mirrorURL, "/") + "/prisma"

	if verbose {
		fmt.Printf("Testing NPM Mirror speed with: %s\n", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, nil, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("HTTP %d for speed test file (expected 200)", resp.StatusCode)
	}

	// Get content length if available
	contentLength := resp.ContentLength
	if contentLength > 0 && verbose {
		fmt.Printf("Content-Length: %.2f MB\n", float64(contentLength)/1024/1024)
	}

	minBytes := int64(5 * 1024 * 1024) // Download at least 5MB
	var downloaded int64
	buf := make([]byte, 512*1024) // 512KB buffer for optimal performance

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
				percent := float64(downloaded) / float64(contentLength) * 100
				if contentLength > 0 {
					fmt.Printf("\rDownloading: %.1f%% (%.2f/%.2f MB)",
						percent,
						float64(downloaded)/1024/1024,
						float64(contentLength)/1024/1024)
				} else {
					fmt.Printf("\rDownloaded: %.2f MB", float64(downloaded)/1024/1024)
				}
				lastProgress = time.Now()
			}
		}
		if err != nil {
			if err == io.EOF {
				if verbose {
					fmt.Println() // New line after progress
				}
				break
			}
			return 0, nil, err
		}
	}

	if verbose {
		fmt.Println() // New line after progress
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
			"downloaded_mb":  float64(downloaded) / 1024 / 1024,
			"duration_sec":   duration,
			"content_length": contentLength,
			"speed_rating":   getSpeedRating(speedMBps),
		}
		var iface interface{} = info
		return speedMBps, &iface, nil
	}

	return 0, nil, fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
}

// CheckPackage checks if a package exists on an NPM mirror
// Returns: (exists, version, error)
func (m *NpmMirrorService) CheckPackage(mirrorUrl, packageName string, verbose bool) (bool, *interface{}, error) {
	// NPM registry API endpoint for package metadata
	packageURL := fmt.Sprintf("%s/%s", strings.TrimSuffix(mirrorUrl, "/"), packageName)

	if verbose {
		fmt.Println("Checking package: ", packageURL)
	}

	req, err := http.NewRequest("GET", packageURL, nil)
	if err != nil {
		return false, nil, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Accept", "application/json")

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
		return false, nil, fmt.Errorf("HTTP %d from registry", resp.StatusCode)
	}

	// Parse JSON response
	var packageData map[string]interface{}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, err
	}

	if err := json.Unmarshal(body, &packageData); err != nil {
		return false, nil, err
	}

	// Get the latest version
	if distTags, ok := packageData["dist-tags"].(map[string]interface{}); ok {
		if latest, ok := distTags["latest"].(string); ok {
			if verbose {
				fmt.Printf("Found package '%s' with latest version: %s\n", packageName, latest)
			}

			// Store package info
			info := map[string]interface{}{
				"version":   latest,
				"dist_tags": distTags,
			}
			var iface interface{} = info
			return true, &iface, nil
		}
	}

	// Alternative: get versions
	if versions, ok := packageData["versions"].(map[string]interface{}); ok {
		if len(versions) > 0 {
			// Get the first version as fallback
			for version := range versions {
				if verbose {
					fmt.Printf("Found package '%s' with version: %s\n", packageName, version)
				}

				info := map[string]interface{}{
					"version": version,
				}
				var iface interface{} = info
				return true, &iface, nil
			}
		}
	}

	return false, nil, nil
}

func (m *NpmMirrorService) CheckMirrorStatus(url string, verbose bool) (bool, *interface{}, error) {
	// Test multiple endpoints for NPM mirror
	testPaths := []struct {
		path   string
		method string
	}{
		{"/-/v1/search?text=test&size=1", "GET"},
		{"/react", "GET"},
	}

	for _, test := range testPaths {
		testURL := strings.TrimSuffix(url, "/") + test.path

		if verbose {
			fmt.Printf("Testing NPM mirror endpoint: %s\n", testURL)
		}

		req, err := http.NewRequest(test.method, testURL, nil)
		if err != nil {
			continue
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		if test.path == "/-/v1/search?text=test&size=1" {
			req.Header.Set("Accept", "application/json")
		}

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			if verbose {
				fmt.Printf("Error checking endpoint: %v\n", err)
			}
			continue
		}
		defer resp.Body.Close()

		// Check if we got a successful response
		if resp.StatusCode == http.StatusOK {
			if verbose {
				fmt.Printf("Mirror responded to %s with status %d\n", test.path, resp.StatusCode)
			}

			info := map[string]interface{}{
				"status":      "active",
				"tested_path": test.path,
				"status_code": resp.StatusCode,
				"mirror_type": "npm",
			}
			var iface interface{} = info
			return true, &iface, nil
		}

		// Some mirrors redirect to the official registry
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			if verbose {
				fmt.Printf("Mirror redirects to: %s\n", location)
			}
			// If it redirects, it's still functional
			info := map[string]interface{}{
				"status":      "redirect",
				"tested_path": test.path,
				"status_code": resp.StatusCode,
				"redirect_to": location,
				"mirror_type": "npm",
			}
			var iface interface{} = info
			return true, &iface, nil
		}
	}

	return false, nil, fmt.Errorf("mirror does not appear to be a valid NPM registry")
}

// Helper function to get speed rating
func getSpeedRating(speedMBps float64) string {
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

// NewNpmMirrorService creates a new NPM mirror service instance
func NewNpmMirrorService() mirava_core.MirrorService {
	return &NpmMirrorService{
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
