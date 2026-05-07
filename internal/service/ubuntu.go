package service

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UbuntuMirrorService implements the MirrorService interface for Ubuntu mirrors
type UbuntuMirrorService struct {
	HttpClient *http.Client
}

// StatusInfo contains detailed status check information
type StatusInfo struct {
	Success     bool     `json:"success"`
	TestedPaths []string `json:"tested_paths"`
	WorkingPath string   `json:"working_path,omitempty"`
	StatusCode  int      `json:"status_code,omitempty"`
	Message     string   `json:"message,omitempty"`
}

// SpeedInfo contains detailed speed test information
type SpeedInfo struct {
	SpeedMBps       float64 `json:"speed_mbps"`
	DownloadedMB    float64 `json:"downloaded_mb"`
	DurationSec     float64 `json:"duration_sec"`
	TestURL         string  `json:"test_url"`
	BytesDownloaded int64   `json:"bytes_downloaded"`
	TargetBytes     int64   `json:"target_bytes"`
	Message         string  `json:"message"`
}

// PackageCheckInfo contains detailed package check information
type PackageCheckInfo struct {
	Exists       bool     `json:"exists"`
	PackageName  string   `json:"package_name"`
	Version      string   `json:"version,omitempty"`
	Release      string   `json:"release,omitempty"`
	Component    string   `json:"component,omitempty"`
	Arch         string   `json:"arch,omitempty"`
	CheckedPaths []string `json:"checked_paths"`
	FoundPath    string   `json:"found_path,omitempty"`
}

// CheckMirrorStatus implements MirrorService.CheckMirrorStatus
func (m *UbuntuMirrorService) CheckMirrorStatus(mirrorURL string, verbose bool) (bool, *interface{}, error) {
	testPaths := []string{
		"/ubuntu/ls-lR.gz",
		"/ubuntu/dists/stable/Release",
		"/ubuntu/dists/noble/Release",
		"/ubuntu/",
	}

	statusInfo := StatusInfo{
		Success:     false,
		TestedPaths: []string{},
	}

	for _, path := range testPaths {
		testURL := mirrorURL + path
		statusInfo.TestedPaths = append(statusInfo.TestedPaths, testURL)

		if verbose {
			fmt.Println("Testing Ubuntu Mirror Status With:", testURL)
		}

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			continue
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

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

			// Return detailed info
			additionalData := interface{}(statusInfo)
			return true, &additionalData, nil
		}

		// Also consider redirects as valid (some mirrors redirect)
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			statusInfo.Success = true
			statusInfo.WorkingPath = testURL
			statusInfo.StatusCode = resp.StatusCode
			statusInfo.Message = fmt.Sprintf("Mirror redirects (HTTP %d)", resp.StatusCode)

			additionalData := interface{}(statusInfo)
			return true, &additionalData, nil
		}
	}

	statusInfo.Message = "Mirror not responding or not a valid Ubuntu mirror"
	additionalData := interface{}(statusInfo)
	return false, &additionalData, fmt.Errorf("mirror not responding or not a valid Ubuntu mirror")
}

// CheckMirrorSpeed implements MirrorService.CheckMirrorSpeed
func (m *UbuntuMirrorService) CheckMirrorSpeed(mirrorURL string, verbose bool) (float64, *interface{}, error) {
	testURL := mirrorURL + "/ubuntu/ls-lR.gz"

	speedInfo := SpeedInfo{
		TestURL:     testURL,
		TargetBytes: 1 * 1024 * 1024, // 1MB
	}

	if verbose {
		fmt.Println("Testing Ubuntu Mirror Speed with:", testURL)
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
		return 0, nil, fmt.Errorf("HTTP %d for test file", resp.StatusCode)
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

		// Return speed data in interface{} with detailed info
		speedData := interface{}(speedInfo)
		return speedMBps, &speedData, nil
	}

	speedInfo.Message = fmt.Sprintf("Speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
	speedData := interface{}(speedInfo)
	return 0, &speedData, fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
}

// CheckPackage implements MirrorService.CheckPackage
func (m *UbuntuMirrorService) CheckPackage(mirrorURL, packageName string, verbose bool) (bool, *interface{}, error) {
	releases := []string{"noble", "jammy", "focal", "bionic"}
	components := []string{"main", "universe"}
	arch := "amd64"

	packageInfo := PackageCheckInfo{
		Exists:       false,
		PackageName:  packageName,
		CheckedPaths: []string{},
		Arch:         arch,
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	for _, release := range releases {
		for _, component := range components {
			packagesURL := fmt.Sprintf("%s/ubuntu/dists/%s/%s/binary-%s/Packages.gz",
				mirrorURL, release, component, arch)

			packageInfo.CheckedPaths = append(packageInfo.CheckedPaths, packagesURL)

			if verbose {
				fmt.Println("Checking package in:", packagesURL)
			}

			exists, version, err := m.checkPackagesFile(client, packagesURL, packageName)
			if err != nil {
				if verbose {
					fmt.Printf("Error checking %s: %v\n", packagesURL, err)
				}
				continue
			}

			if exists {
				packageInfo.Exists = true
				packageInfo.Version = version
				packageInfo.Release = release
				packageInfo.Component = component
				packageInfo.FoundPath = packagesURL

				// Return package info in the interface{}
				packageData := interface{}(packageInfo)
				return true, &packageData, nil
			}
		}
	}

	// Package not found, return the check info anyway
	packageData := interface{}(packageInfo)
	return false, &packageData, nil
}

// checkPackagesFile is an internal helper to parse Packages.gz files
func (m *UbuntuMirrorService) checkPackagesFile(client *http.Client, packagesURL, packageName string) (bool, string, error) {
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

func NewUbuntuMirrorService() *UbuntuMirrorService {
	return &UbuntuMirrorService{
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
