package apt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// AptMirrorService implements the MirrorService interface for apt mirrors
type AptMirrorService struct {
	HttpClient *http.Client
	// CacheTTL controls caching for GetPackageVersion (memory + disk). Zero uses defaultAptCacheTTL.
	CacheTTL time.Duration
	// CacheDir overrides the on-disk cache location. Empty uses the OS app cache dir
	// (os.UserCacheDir()/mirava-core/apt, e.g. ~/Library/Caches/mirava-core/apt on macOS).
	CacheDir string
	// DisableDiskCache turns off persistent cache (tests only).
	DisableDiskCache bool

	cacheOnce sync.Once
	aptCache  *aptMirrorCache
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
	ContentLength   int64   `json:"content_length"`
	TimeoutSec      int     `json:"timeout_sec"`
	SpeedRating     string  `json:"speed_rating"`
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
		req.Header.Set("User-Agent", userAgent)

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

	speedInfo := &AptCheckSpeedData{
		TestURL:    testURL,
		TimeoutSec: timeout,
	}

	if verbose {
		fmt.Println("Testing apt Mirror Speed with:", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{URL: testURL, Err: err}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("User-Agent", userAgent)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
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
		return 0, nil, &HttpRequestError{URL: testURL, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, &HttpRequestError{StatusCode: resp.StatusCode, URL: testURL}
	}

	contentLength := resp.ContentLength
	speedInfo.ContentLength = contentLength

	if verbose && contentLength > 0 {
		fmt.Printf("Content-Length: %.2f MB\n", float64(contentLength)/1024/1024)
	}

	var downloaded int64
	buf := make([]byte, 512*1024) // 512KB buffer for faster downloads
	lastProgress := time.Now()

	if verbose {
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
					elapsed := time.Since(start).Seconds()
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

				return 0, nil, &HttpRequestError{URL: testURL, Err: err}
			}
		}
	}

calculateSpeed:
	duration := time.Since(start).Seconds()

	if verbose {
		fmt.Printf("\nDownloaded %.2f MB in %.2f seconds\n",
			float64(downloaded)/1024/1024,
			duration)
	}

	if duration > 0 && downloaded > 0 {
		speedMBps := (float64(downloaded) / 1024 / 1024) / duration

		if verbose {
			fmt.Printf("Average speed: %.2f MB/s\n", speedMBps)
			rating := getAptSpeedRating(speedMBps)
			fmt.Printf("Rating: %s\n", rating)
		}

		speedInfo.SpeedMBps = speedMBps
		speedInfo.DownloadedMB = float64(downloaded) / 1024 / 1024
		speedInfo.DurationSec = duration
		speedInfo.BytesDownloaded = downloaded
		speedInfo.SpeedRating = getAptSpeedRating(speedMBps)

		return speedMBps, speedInfo, nil
	}

	speedInfo.Message = fmt.Sprintf("Speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration)
	return 0, speedInfo, &SpeedTestError{
		URL: testURL,
		Err: fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration),
	}
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

// checkPackagesFile is an internal helper to parse Packages.gz files.
func (m *AptMirrorService) checkPackagesFile(client *http.Client, packagesURL, packageName string) (bool, string, error) {
	body, err := m.fetchMirrorFile(client, packagesURL)
	if err != nil {
		return false, "", err
	}

	reader, err := decompressAptIndex(body, "Packages.gz")
	if err != nil {
		return false, "", err
	}
	defer closeAptReader(reader)

	candidate, err := findLatestPackageInIndex(reader, packageName)
	if err != nil {
		return false, "", err
	}
	if candidate == nil {
		return false, "", nil
	}

	return true, candidate.Version, nil
}

func closeAptReader(reader io.Reader) {
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}
}

// getAptSpeedRating returns a rating based on download speed in MB/s
func getAptSpeedRating(speedMBps float64) string {
	switch {
	case speedMBps > 10:
		return "Excellent"
	case speedMBps > 5:
		return "Good"
	case speedMBps > 2:
		return "Average"
	default:
		return "Slow"
	}
}

// NewAptMirrorService creates a new APT mirror service instance
func NewAptMirrorService() *AptMirrorService {
	return &AptMirrorService{
		HttpClient: &http.Client{
			Timeout: 0,
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
