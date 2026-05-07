package service

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/model"
)

type DebianMirrorService struct {
	HttpClient *http.Client
}

func (m *DebianMirrorService) CheckMirrorSpeed(mirrorURL string, verbose bool) (float64, error) {
	// Debian mirrors typically have a ls-lR.gz file in the root
	testURL := mirrorURL + "/ls-lR.gz"

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	start := time.Now()
	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if verbose {
			fmt.Println("Error checking debian mirror speed in ", time.Since(start))
		}
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d for test file", resp.StatusCode)
	}

	minBytes := int64(1 * 1024 * 1024) // Download at least 1MB
	var downloaded int64
	buf := make([]byte, 32*1024)

	for downloaded < minBytes && time.Since(start) < 5*time.Second {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, err
		}
	}

	duration := time.Since(start).Seconds()
	if duration > 0 && downloaded > 0 {
		speedMBps := (float64(downloaded) / 1024 / 1024) / duration
		return speedMBps, nil
	}

	return 0, fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
}

// CheckPackage checks if a package exists on a Debian mirror
// Returns: (exists, version, error)
func (m *DebianMirrorService) CheckPackage(mirrorUrl, packageName string, verbose bool) (bool, string, error) {
	// Debian releases (stable, testing, unstable, and specific versions)
	releases := []string{
		"bookworm", // Debian 12 (current stable)
		"bullseye", // Debian 11 (old stable)
		"buster",   // Debian 10 (oldoldstable)
		"stable",
		"testing",
		"unstable",
	}

	// Debian components (main, contrib, non-free)
	components := []string{"main", "contrib", "non-free"}

	// Architectures to check
	architectures := []string{"amd64", "arm64", "i386", "armhf"}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	for _, release := range releases {
		for _, component := range components {
			for _, arch := range architectures {
				// Debian package index path format
				packagesURL := fmt.Sprintf("%s/debian/dists/%s/%s/binary-%s/Packages.gz",
					mirrorUrl, release, component, arch)

				if verbose {
					fmt.Println("Testing Debian Mirror with: ", packagesURL)
				}

				exists, version, err := m.checkPackagesFile(client, packagesURL, packageName)
				if err != nil {
					if verbose {
						fmt.Println("Error checking package file: ", err)
					}
					continue
				}

				if exists {
					return true, version, nil
				}
			}
		}
	}

	return false, "", nil
}

// CheckPackageInRelease checks for a package in a specific Debian release
func (m *DebianMirrorService) CheckPackageInRelease(mirrorUrl, packageName, release string) (bool, string, error) {
	components := []string{"main", "contrib", "non-free"}
	architectures := []string{"amd64", "arm64", "i386", "armhf"}

	for _, component := range components {
		for _, arch := range architectures {
			packagesURL := fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.gz",
				mirrorUrl, release, component, arch)

			exists, version, err := m.checkPackagesFile(m.HttpClient, packagesURL, packageName)
			if err != nil {
				continue
			}

			if exists {
				return true, version, nil
			}
		}
	}

	return false, "", nil
}

// CheckPackageInComponent checks for a package in a specific component
func (m *DebianMirrorService) CheckPackageInComponent(mirrorUrl, packageName, component string) (bool, string, error) {
	releases := []string{"bookworm", "bullseye", "stable", "testing"}
	architectures := []string{"amd64", "arm64"}

	for _, release := range releases {
		for _, arch := range architectures {
			packagesURL := fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.gz",
				mirrorUrl, release, component, arch)

			exists, version, err := m.checkPackagesFile(m.HttpClient, packagesURL, packageName)
			if err != nil {
				continue
			}

			if exists {
				return true, version, nil
			}
		}
	}

	return false, "", nil
}

func (m *DebianMirrorService) checkPackagesFile(client *http.Client, packagesURL, packageName string) (bool, string, error) {
	req, err := http.NewRequest("GET", packagesURL, nil)
	if err != nil {
		return false, "", err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return false, "", err
	}
	defer gzReader.Close()

	scanner := bufio.NewScanner(gzReader)

	var currentPackage string
	var currentVersion string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "Package: ") {
			currentPackage = strings.TrimPrefix(line, "Package: ")
		}

		if strings.HasPrefix(line, "Version: ") && currentPackage == packageName {
			currentVersion = strings.TrimPrefix(line, "Version: ")
			return true, currentVersion, nil
		}

		// Empty line marks end of package section
		if line == "" {
			currentPackage = ""
			currentVersion = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return false, "", err
	}

	return false, "", nil
}

func (m *DebianMirrorService) CheckMirrorStatus(url string, verbose bool) (bool, error) {
	testPaths := []string{
		"/debian/ls-lR.gz",
		"/debian/dists/stable/Release",
		"/debian/dists/stable/InRelease",
		"/debian/dists/",
	}

	var lastErr error

	for _, test := range testPaths {
		testURL := strings.TrimSuffix(url, "/") + test
		fmt.Println(testURL)

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		// Check if we got a successful response
		if resp.StatusCode == http.StatusOK {
			return true, nil
		}

		// Also consider redirects as valid (some mirrors redirect)
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return true, nil
		}

		lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, test)
	}

	// If HEAD requests all fail, try GET for the main Release file
	testURL := strings.TrimSuffix(url, "/") + "/dists/stable/Release"
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return false, lastErr
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("mirror unreachable: %w", lastErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			if strings.Contains(string(body), "Debian") ||
				strings.Contains(string(body), "Suite:") ||
				strings.Contains(string(body), "Codename:") {
				return true, nil
			}
		}
		return true, nil
	}

	return false, fmt.Errorf("mirror does not appear to be a valid Debian mirror")
}

// GetAvailableReleases returns available Debian releases from the mirror
func (m *DebianMirrorService) GetAvailableReleases(mirrorUrl string) ([]string, error) {
	// Try to fetch the dists directory listing
	distsURL := mirrorUrl + "/dists/"
	resp, err := m.HttpClient.Get(distsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch dists directory")
	}

	// Parse HTML or directory listing to find available releases
	// This is a simplified version - in production you might want to parse the actual directory listing
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Simple parsing for directory listing (adjust based on actual server response)
	releases := []string{}
	knownReleases := []string{"bookworm", "bullseye", "buster", "stable", "testing", "unstable"}

	for _, release := range knownReleases {
		if strings.Contains(string(body), release) {
			releases = append(releases, release)
		}
	}

	return releases, nil
}

// CreateDebianMirrorService creates a new Debian mirror service instance
func CreateDebianMirrorService() model.MirrorService {
	return &DebianMirrorService{
		HttpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DisableCompression: true,
				DisableKeepAlives:  true,
				MaxIdleConns:       10,
				IdleConnTimeout:    30 * time.Second,
			},
		},
	}
}
