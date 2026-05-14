package pkg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

type NuGetMirrorService struct {
	HttpClient *http.Client
}

type NuGetCheckSpeedParams struct {
	Package string // Package to test speed with (e.g., "microsoft.aspnetcore.app.runtime.win-x64")
	Version string // Specific version, empty for latest
}

type NuGetCheckSpeedData struct {
	DownloadMb      float64
	DurationSec     float64
	TimeoutSec      int
	SpeedMBps       float64
	SpeedRating     string
	BytesDownloaded int64
	ContentLength   int64
	Package         string
	Version         string
	MirrorURL       string
}

type NuGetCheckPackageData struct {
	Package       string
	Version       string
	Description   string
	Versions      []string
	LatestVersion string
}

type NuGetCheckStatusData struct {
	Status     bool
	Repository string
	StatusCode int
}

func (m *NuGetMirrorService) CheckSpeed(
	mirrorURL string,
	timeout int,
	verbose bool,
	params NuGetCheckSpeedParams,
) (float64, *NuGetCheckSpeedData, error) {

	baseURL := strings.TrimSuffix(mirrorURL, "/")

	// Default test package if not specified
	packageName := params.Package
	if packageName == "" {
		packageName = "microsoft.aspnetcore.app.runtime.win-x64"
	}

	// Determine the version to download
	var packageVersion string
	var downloadURL string

	if params.Version != "" {
		packageVersion = params.Version
		// Construct direct download URL for specific version
		downloadURL = fmt.Sprintf("%s/repository/nuget/%s/%s", baseURL, packageName, packageVersion)
	} else {
		// Fetch the directory listing to find the latest version
		browseURL := fmt.Sprintf("%s/service/rest/repository/browse/nuget/%s", baseURL, packageName)

		if verbose {
			fmt.Printf("Fetching version list from: %s\n", browseURL)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "GET", browseURL, nil)
		if err != nil {
			return 0, nil, &HttpRequestError{
				URL: browseURL,
				Err: err,
			}
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		req.Header.Set("User-Agent", USER_AGENT)

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			return 0, nil, &HttpRequestError{
				URL: browseURL,
				Err: err,
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return 0, nil, &HttpRequestError{
				URL: browseURL,
				Err: fmt.Errorf("HTTP %d for version list", resp.StatusCode),
			}
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to read version list: %w", err)
		}

		// Parse HTML to find version directories
		// Looking for patterns like: <a href="8.0.23/">8.0.23</a>
		versionRegex := regexp.MustCompile(`<a href="([0-9]+\.[0-9]+\.[0-9]+)/">`)
		matches := versionRegex.FindAllStringSubmatch(string(body), -1)

		if len(matches) == 0 {
			return 0, nil, fmt.Errorf("no versions found for package %s", packageName)
		}

		// Collect all versions
		var versions []string
		for _, match := range matches {
			if len(match) > 1 {
				versions = append(versions, match[1])
			}
		}

		if len(versions) == 0 {
			return 0, nil, fmt.Errorf("no valid versions found for package %s", packageName)
		}

		// Sort versions (as strings - works for semantic versioning)
		sort.Slice(versions, func(i, j int) bool {
			return versions[i] > versions[j]
		})

		packageVersion = versions[0] // Latest version
		downloadURL = fmt.Sprintf("%s/repository/nuget/%s/%s", baseURL, packageName, packageVersion)

		if verbose {
			fmt.Printf("Latest version found: %s\n", packageVersion)
			fmt.Printf("Total versions available: %d\n", len(versions))
		}
	}

	if verbose {
		fmt.Printf("Testing NuGet mirror speed with: %s (timeout: %d seconds)\n", downloadURL, timeout)
		fmt.Printf("Downloading package nupkg...\n")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeout)*time.Second,
	)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: downloadURL,
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
				URL: downloadURL,
				Err: fmt.Errorf("timeout reached before connection established"),
			}
		}

		return 0, nil, &HttpRequestError{
			URL: downloadURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, &HttpRequestError{
			URL: downloadURL,
			Err: fmt.Errorf("HTTP %d for package download", resp.StatusCode),
		}
	}

	contentLength := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 512*1024)
	lastProgress := time.Now()

	if verbose {
		if contentLength > 0 {
			fmt.Printf("Package size: %.2f MB\n", float64(contentLength)/1024/1024)
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
					URL: downloadURL,
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
			fmt.Printf("Rating: %s\n", getNuGetSpeedRating(speedMBps))
		}

		info := NuGetCheckSpeedData{
			DownloadMb:      float64(downloaded) / 1024 / 1024,
			DurationSec:     duration,
			TimeoutSec:      timeout,
			SpeedMBps:       speedMBps,
			SpeedRating:     getNuGetSpeedRating(speedMBps),
			BytesDownloaded: downloaded,
			ContentLength:   contentLength,
			Package:         packageName,
			Version:         packageVersion,
			MirrorURL:       baseURL,
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &HttpRequestError{
		URL: downloadURL,
		Err: fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration),
	}
}

func (m *NuGetMirrorService) CheckPackage(
	mirrorURL string,
	packageName string,
	verbose bool,
) (bool, *NuGetCheckPackageData, error) {

	baseURL := strings.TrimSuffix(mirrorURL, "/")

	// Fetch the directory listing to find versions
	browseURL := fmt.Sprintf("%s/service/rest/repository/browse/nuget/%s/", baseURL, packageName)

	if verbose {
		fmt.Printf("Fetching package versions from: %s\n", browseURL)
	}

	resp, err := m.HttpClient.Get(browseURL)
	if err != nil {
		return false, nil, fmt.Errorf("failed to fetch package listing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("package not found: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("failed to read package listing: %w", err)
	}

	// Parse HTML to find version directories
	// Looking for patterns like: <a href="8.0.23/">8.0.23</a>
	versionRegex := regexp.MustCompile(`<a href="([0-9]+\.[0-9]+\.[0-9]+)/">`)
	matches := versionRegex.FindAllStringSubmatch(string(body), -1)

	if len(matches) == 0 {
		if verbose {
			fmt.Printf("No versions found for package '%s'\n", packageName)
		}
		return false, nil, nil
	}

	// Collect all versions
	var versions []string
	for _, match := range matches {
		if len(match) > 1 {
			versions = append(versions, match[1])
		}
	}

	if len(versions) == 0 {
		return false, nil, nil
	}

	// Sort versions (newest first)
	sort.Slice(versions, func(i, j int) bool {
		return versions[i] > versions[j]
	})

	latestVersion := versions[0]

	if verbose {
		fmt.Printf("Found package '%s' with %d versions, latest: %s\n",
			packageName, len(versions), latestVersion)
	}

	info := &NuGetCheckPackageData{
		Package:       packageName,
		Version:       latestVersion,
		Versions:      versions,
		LatestVersion: latestVersion,
	}

	return true, info, nil
}

func (m *NuGetMirrorService) CheckStatus(
	url string,
	verbose bool,
) (bool, *NuGetCheckStatusData, error) {

	baseURL := strings.TrimSuffix(url, "/")

	// Test if the repository is accessible
	testURL := fmt.Sprintf("%s/service/rest/repository/browse/nuget/", baseURL)

	if verbose {
		fmt.Printf("Testing NuGet mirror endpoint: %s\n", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("User-Agent", USER_AGENT)

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if verbose {
			fmt.Printf("Failed: %v\n", err)
		}
		return false, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if verbose {
			fmt.Printf("HTTP %d from NuGet mirror\n", resp.StatusCode)
		}
		return false, nil, &HttpRequestError{
			URL: testURL,
			Err: fmt.Errorf("HTTP %d for repository browse", resp.StatusCode),
		}
	}

	if verbose {
		fmt.Printf("Mirror responded successfully with status %d\n", resp.StatusCode)
	}

	info := NuGetCheckStatusData{
		Status:     true,
		Repository: testURL,
		StatusCode: resp.StatusCode,
	}

	return true, &info, nil
}

func getNuGetSpeedRating(speedMBps float64) string {
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

// NewNuGetMirrorService creates a new NuGet mirror service instance
func NewNuGetMirrorService() *NuGetMirrorService {
	return &NuGetMirrorService{
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
