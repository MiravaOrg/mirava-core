package pkg

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type CentOSMirrorService struct {
	HttpClient *http.Client
}

func (m *CentOSMirrorService) CheckSpeed(mirrorURL string, timeout int, verbose bool, params *interface{}) (float64, *interface{}, error) {
	// CentOS mirrors typically have a repomd.xml file in the base repository
	baseURL := strings.TrimSuffix(mirrorURL, "/")

	// Try to find a reasonably sized file for speed testing
	testPaths := []string{
		"/epel/8/Everything/x86_64/repodata/repomd.xml",
		"/centos/8-stream/BaseOS/x86_64/os/repodata/repomd.xml",
		"/centos/7/os/x86_64/repodata/repomd.xml",
		"/centos/9-stream/BaseOS/x86_64/os/repodata/repomd.xml",
	}

	var testURL string
	var found bool

	for _, path := range testPaths {
		url := baseURL + path
		if verbose {
			fmt.Printf("Checking for test file: %s\n", url)
		}

		req, err := http.NewRequest("HEAD", url, nil)
		if err != nil {
			continue
		}

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			testURL = url
			found = true
			if verbose {
				fmt.Printf("Found test file: %s\n", url)
			}
			break
		}
	}

	if !found {
		// Fallback to a simple GET request to the root
		testURL = baseURL + "/"
		if verbose {
			fmt.Printf("Using fallback test URL: %s\n", testURL)
		}
	}

	if verbose {
		fmt.Printf("Testing CentOS Mirror speed with: %s\n", testURL)
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
		return 0, nil, fmt.Errorf("HTTP %d for speed test", resp.StatusCode)
	}

	contentLength := resp.ContentLength
	if contentLength > 0 && verbose {
		fmt.Printf("Content-Length: %.2f MB\n", float64(contentLength)/1024/1024)
	}

	// Download at least 5MB for accurate speed test
	minBytes := int64(5 * 1024 * 1024)
	var downloaded int64
	buf := make([]byte, 512*1024) // 512KB buffer
	lastProgress := time.Now()

	if verbose {
		fmt.Print("Downloading: ")
	}

	for downloaded < minBytes && time.Since(start) < 15*time.Second {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)

			// Show progress every 500ms
			if verbose && time.Since(lastProgress) > 500*time.Millisecond {
				if contentLength > 0 {
					percent := float64(downloaded) / float64(contentLength) * 100
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
			"downloaded_mb":  float64(downloaded) / 1024 / 1024,
			"duration_sec":   duration,
			"content_length": contentLength,
			"test_url":       testURL,
			"speed_rating":   m.getCentOSSpeedRating(speedMBps),
		}
		var iface interface{} = info
		return speedMBps, &iface, nil
	}

	return 0, nil, fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
}

// CheckPackage checks if a package exists on a CentOS mirror
// Returns: (exists, version, error)
func (m *CentOSMirrorService) CheckPackage(mirrorUrl, packageName string, verbose bool, params *interface{}) (bool, *interface{}, error) {
	baseURL := strings.TrimSuffix(mirrorUrl, "/")

	// Common CentOS/RHEL versions and repositories
	releases := []string{
		"7", "8-stream", "8", "9-stream", "9",
	}

	repos := []string{
		"BaseOS", "AppStream", "extras", "epel", "centosplus",
	}

	architectures := []string{"x86_64", "aarch64", "noarch"}

	for _, release := range releases {
		for _, repo := range repos {
			for _, arch := range architectures {
				// Try different package index locations
				packagePaths := []string{
					fmt.Sprintf("/centos/%s/%s/%s/os/repodata/primary.xml.gz", release, repo, arch),
					fmt.Sprintf("/centos/%s/%s/%s/os/repodata/primary.sqlite.bz2", release, repo, arch),
					fmt.Sprintf("/epel/%s/%s/repodata/primary.xml.gz", release, arch),
				}

				for _, packagePath := range packagePaths {
					packagesURL := baseURL + packagePath

					if verbose {
						fmt.Printf("Checking package in: %s\n", packagesURL)
					}

					exists, version, err := m.checkPackagesFile(packagesURL, packageName)
					if err != nil {
						if verbose {
							fmt.Printf("Error checking packages file: %v\n", err)
						}
						continue
					}

					if exists {
						info := map[string]interface{}{
							"version":      version,
							"release":      release,
							"repo":         repo,
							"architecture": arch,
							"mirror_url":   mirrorUrl,
						}
						var iface interface{} = info
						return true, &iface, nil
					}
				}
			}
		}
	}

	return false, nil, nil
}

// CheckStatus checks if a CentOS mirror is alive and responding
func (m *CentOSMirrorService) CheckStatus(url string, verbose bool, params *interface{}) (bool, *interface{}, error) {
	baseURL := strings.TrimSuffix(url, "/")

	// Test multiple endpoints for CentOS mirror
	testPaths := []string{
		"/centos/",
		"/centos/8-stream/BaseOS/x86_64/os/repodata/repomd.xml",
		"/centos/7/os/x86_64/repodata/repomd.xml",
		"/epel/",
	}

	var lastErr error
	var successfulPath string

	for _, testPath := range testPaths {
		testURL := baseURL + testPath

		if verbose {
			fmt.Printf("Testing CentOS mirror endpoint: %s\n", testURL)
		}

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			if verbose {
				fmt.Printf("Error checking endpoint: %v\n", err)
			}
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			if verbose {
				fmt.Printf("Mirror responded to %s with status %d\n", testPath, resp.StatusCode)
			}
			successfulPath = testPath
			break
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			if verbose {
				fmt.Printf("Mirror redirects to: %s\n", location)
			}
			successfulPath = testPath
			break
		}

		lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, testPath)
	}

	if successfulPath != "" {
		// Try to determine CentOS version information
		versionInfo := m.getCentOSVersionInfo(baseURL, verbose)

		info := map[string]interface{}{
			"status":       "active",
			"tested_path":  successfulPath,
			"version_info": versionInfo,
			"mirror_type":  "centos",
		}
		var iface interface{} = info
		return true, &iface, nil
	}

	return false, nil, fmt.Errorf("mirror does not appear to be a valid CentOS mirror: %w", lastErr)
}

// Helper method to check packages file
func (m *CentOSMirrorService) checkPackagesFile(packagesURL, packageName string) (bool, string, error) {
	req, err := http.NewRequest("GET", packagesURL, nil)
	if err != nil {
		return false, "", err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Check if it's gzipped
	var reader io.ReadCloser
	if strings.HasSuffix(packagesURL, ".gz") {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return false, "", err
		}
		defer gzReader.Close()
		reader = gzReader
	} else {
		reader = resp.Body
	}

	scanner := bufio.NewScanner(reader)

	// For XML-based repodata (primary.xml.gz)
	// Look for package name in XML format: <name>package-name</name>
	pattern := fmt.Sprintf("<name>%s</name>", regexp.QuoteMeta(packageName))
	re := regexp.MustCompile(pattern)

	// Also look for version pattern
	versionPattern := regexp.MustCompile(`<version epoch="[^"]*" ver="([^"]+)" rel="[^"]*"/>`)

	for scanner.Scan() {
		line := scanner.Text()

		if re.MatchString(line) {
			// Found the package, now look for version info
			// In a real implementation, you'd parse the XML properly
			// This is a simplified approach
			for i := 0; i < 20 && scanner.Scan(); i++ {
				versionLine := scanner.Text()
				if matches := versionPattern.FindStringSubmatch(versionLine); len(matches) > 1 {
					return true, matches[1], nil
				}
			}
			return true, "unknown", nil
		}
	}

	if err := scanner.Err(); err != nil {
		return false, "", err
	}

	return false, "", nil
}

// Helper function to get CentOS version information from mirror
func (m *CentOSMirrorService) getCentOSVersionInfo(baseURL string, verbose bool) map[string]interface{} {
	versionInfo := make(map[string]interface{})

	// Try to fetch available versions
	versionsURL := baseURL + "/centos/"

	req, err := http.NewRequest("GET", versionsURL, nil)
	if err != nil {
		return versionInfo
	}

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return versionInfo
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			// Look for version directories (7, 8, 8-stream, 9, 9-stream)
			versionPattern := regexp.MustCompile(`>(\d+(?:-stream)?)/<`)
			matches := versionPattern.FindAllStringSubmatch(string(body), -1)

			versions := make([]string, 0)
			for _, match := range matches {
				if len(match) > 1 {
					versions = append(versions, match[1])
				}
			}

			if len(versions) > 0 {
				versionInfo["available_versions"] = versions
			}
		}
	}

	return versionInfo
}

// Helper function to get CentOS speed rating
func (m *CentOSMirrorService) getCentOSSpeedRating(speedMBps float64) string {
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

// NewCentOSMirrorService creates a new CentOS mirror service instance
func NewCentOSMirrorService() MirrorService[*interface{}, *interface{}, *interface{}] {
	return &CentOSMirrorService{
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
