package pkg

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AptMirrorService implements the MirrorService interface for apt mirrors
type AptMirrorService struct {
	HttpClient *http.Client
}

// AptCheckStatusData contains detailed status check information
type AptCheckStatusData struct {
	Success     bool     `json:"success"`
	TestedPaths []string `json:"tested_paths"`
	WorkingPath string   `json:"working_path,omitempty"`
	StatusCode  int      `json:"status_code,omitempty"`
	Message     string   `json:"message,omitempty"`
}

// AptCheckSpeedData contains detailed speed test information
type AptCheckSpeedData struct {
	SpeedMBps       float64 `json:"speed_mbps"`
	DownloadedMB    float64 `json:"downloaded_mb"`
	DurationSec     float64 `json:"duration_sec"`
	TestURL         string  `json:"test_url"`
	BytesDownloaded int64   `json:"bytes_downloaded"`
	TargetBytes     int64   `json:"target_bytes"`
	Message         string  `json:"message"`
}

type AptCheckPackageParams struct {
	Release   string `validate:"required,oneof=stable oldstable testing focal jammy buster bullseye bookworm"`
	Component string `validate:"required,oneof=main universe contrib non-free"`
	Arch      string `validate:"required,oneof=amd64 arm64 i386 armhf ppc64el s390x"`
}

// AptCheckPackageData contains detailed package check information
type AptCheckPackageData struct {
	Exists       bool     `json:"exists"`
	PackageName  string   `json:"package_name"`
	Version      string   `json:"version,omitempty"`
	Release      string   `json:"release,omitempty"`
	Component    string   `json:"component,omitempty"`
	Arch         string   `json:"arch,omitempty"`
	CheckedPaths []string `json:"checked_paths"`
	FoundPath    string   `json:"found_path,omitempty"`
}

// CheckStatus implements MirrorService.CheckMirrorStatus
func (m *AptMirrorService) CheckStatus(mirrorURL string, verbose bool) (bool, *AptCheckStatusData, error) {
	testPaths := []string{
		"/ls-lR.gz",
	}

	statusInfo := AptCheckStatusData{
		Success:     false,
		TestedPaths: []string{},
	}

	for _, path := range testPaths {
		testURL := mirrorURL + path
		statusInfo.TestedPaths = append(statusInfo.TestedPaths, testURL)

		if verbose {
			fmt.Println("Testing apt Mirror Status With:", testURL)
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
				fmt.Printf("Error checking %s: %v\n", testURL, err)
			}
			continue
		}
		defer resp.Body.Close()

		// Check if we got a successful response
		if resp.StatusCode == http.StatusOK {
			statusInfo.Success = true
			statusInfo.WorkingPath = testURL
			statusInfo.StatusCode = resp.StatusCode
			statusInfo.Message = "Mirror is healthy and responding"

			return true, &statusInfo, nil
		}

		// Also consider redirects as valid (some mirrors redirect)
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			statusInfo.Success = true
			statusInfo.WorkingPath = testURL
			statusInfo.StatusCode = resp.StatusCode
			statusInfo.Message = fmt.Sprintf("Mirror redirects (HTTP %d)", resp.StatusCode)

			return true, &statusInfo, nil
		}
	}

	statusInfo.Message = "Mirror not responding or not a valid apt mirror"
	return false, &statusInfo, &InvalidMirrorError{URL: mirrorURL}
}

// CheckSpeed implements MirrorService.CheckMirrorSpeed
func (m *AptMirrorService) CheckSpeed(mirrorURL string, timeout int, verbose bool) (float64, *AptCheckSpeedData, error) {
	testURL := mirrorURL + "/ls-lR.gz"

	speedInfo := AptCheckSpeedData{
		TestURL:     testURL,
		TargetBytes: 1 * 1024 * 1024, // 1MB
	}

	if verbose {
		fmt.Println("Testing apt Mirror Speed with:", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, nil, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("User-Agent", USER_AGENT)

	start := time.Now()
	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return 0, nil, &HttpRequestError{URL: testURL, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, &HttpRequestError{StatusCode: resp.StatusCode, URL: testURL}
	}

	minBytes := int64(1 * 1024 * 1024) // 1MB minimum for accurate speed test
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
			return 0, nil, err
		}
	}

	duration := time.Since(start).Seconds()
	if duration > 0 && downloaded > 0 {
		speedMBps := (float64(downloaded) / 1024 / 1024) / duration

		// Fill speed info
		speedInfo.SpeedMBps = speedMBps
		speedInfo.DownloadedMB = float64(downloaded) / 1024 / 1024
		speedInfo.DurationSec = duration
		speedInfo.BytesDownloaded = downloaded

		return speedMBps, &speedInfo, nil
	}

	speedInfo.Message = fmt.Sprintf("Speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
	return 0, &speedInfo, fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
}

// CheckPackage implements MirrorService.CheckPackage
func (m *AptMirrorService) CheckPackage(mirrorURL, packageName string, verbose bool, params AptCheckPackageParams) (bool, *AptCheckPackageData, error) {
	packageInfo := AptCheckPackageData{
		Exists:       false,
		PackageName:  packageName,
		CheckedPaths: []string{},
		Arch:         params.Arch,
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	packagesURL := fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.gz",
		mirrorURL, params.Release, params.Component, params.Arch)

	packageInfo.CheckedPaths = append(packageInfo.CheckedPaths, packagesURL)

	if verbose {
		fmt.Println("Checking package in:", packagesURL)
	}

	exists, version, err := m.checkPackagesFile(client, packagesURL, packageName)
	if err != nil {
		if verbose {
			fmt.Printf("Error checking %s: %v\n", packagesURL, err)
		}
	}

	if exists {
		packageInfo.Exists = true
		packageInfo.Version = version
		packageInfo.Release = params.Release
		packageInfo.Component = params.Component
		packageInfo.FoundPath = packagesURL

		return true, &packageInfo, nil
	}

	return false, &packageInfo, nil
}

// checkPackagesFile is an internal helper to parse Packages.gz files
func (m *AptMirrorService) checkPackagesFile(client *http.Client, packagesURL, packageName string) (bool, string, error) {
	req, err := http.NewRequest("GET", packagesURL, nil)
	if err != nil {
		return false, "", err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("User-Agent", USER_AGENT)

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

func NewAptMirrorService() *AptMirrorService {
	return &AptMirrorService{
		HttpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DisableCompression: false, // Allow compression for speed
				DisableKeepAlives:  false,
				MaxIdleConns:       10,
				IdleConnTimeout:    30 * time.Second,
			},
		},
	}
}
