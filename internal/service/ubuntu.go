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

type UbuntuMirrorService struct {
	HttpClient *http.Client
}

func (m *UbuntuMirrorService) CheckMirrorSpeed(mirrorURL string, verbose bool) (float64, error) {
	testURL := mirrorURL + "/ubuntu/ls-lR.gz"
	if verbose {
		fmt.Println("Testing Ubuntu Mirror with: ", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	start := time.Now()
	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d for test file", resp.StatusCode)
	}

	minBytes := int64(1 * 1024 * 1024)
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

// PackageInfo holds information about a found package
type PackageInfo struct {
	Name         string
	Version      string
	Filename     string
	Size         string
	Architecture string
	Component    string
	Release      string
}

// CheckPackage checks if a package exists on an Ubuntu mirror
// Returns: (exists, version, error)
func (m *UbuntuMirrorService) CheckPackage(mirrorUrl, packageName string, verbose bool) (bool, string, error) {
	releases := []string{"noble", "jammy", "focal", "bionic"}

	components := []string{"main", "universe"}

	arch := "amd64"

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	for _, release := range releases {
		for _, component := range components {
			packagesURL := fmt.Sprintf("%s/ubuntu/dists/%s/%s/binary-%s/Packages.gz",
				mirrorUrl, release, component, arch)
			if verbose {
				fmt.Println("Checking package: ", packagesURL)
			}

			exists, version, err := checkPackagesFile(client, packagesURL, packageName)
			if err != nil {
				if verbose {
					fmt.Println("Error checking package: ", packagesURL)
					fmt.Println(err.Error())
				}
				continue
			}

			if exists {
				return true, version, nil
			}
		}
	}

	return false, "", nil
}

func checkPackagesFile(client *http.Client, packagesURL, packageName string) (bool, string, error) {
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

func (m *UbuntuMirrorService) CheckMirrorStatus(url string, verbose bool) (bool, error) {
	testPaths := []string{
		"/ubuntu/ls-lR.gz",
		"/ubuntu/dists/stable/Release",
		"/ubuntu/dists/noble/Release",
		"/ubuntu/",
	}

	// Use the shared HTTP client from the service
	for _, path := range testPaths {
		testURL := url + path

		if verbose {
			fmt.Println("Testing Ubuntu Mirror Speed With:", testURL)
		}

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			continue
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			if verbose {
				fmt.Println("Error checking mirror status: ", url)
				fmt.Println(err.Error())
			}
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
	}

	return false, fmt.Errorf("mirror not responding or not a valid Ubuntu mirror")
}

func CreateUbuntuMirrorService() model.MirrorService {
	return &UbuntuMirrorService{
		HttpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DisableCompression: true,
				DisableKeepAlives:  true,
			},
		},
	}
}
