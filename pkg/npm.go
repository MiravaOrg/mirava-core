package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type NpmMirrorService struct {
	HttpClient *http.Client
}

type NpmCheckSpeedParams struct {
	PackageName string
}

type NpmCheckSpeedData struct {
	DownloadMb      float64
	DurationSec     float64
	ContentLength   int64
	TimeoutSec      int
	SpeedMbps       float64
	SpeedRating     string
	BytesDownloaded int64
}

type NpmCheckPackageData struct {
	Version  string
	distTags *map[string]interface{}
}

type NpmCheckStatusData struct {
	Status       bool
	TestPath     string
	StatusCode   int
	RedirectPath *string
}

func (m *NpmMirrorService) CheckSpeed(
	mirrorURL string,
	timeout int,
	verbose bool,
	params *NpmCheckSpeedParams,
) (float64, *NpmCheckSpeedData, error) {
	testPackage := "prisma"

	if params != nil && params.PackageName != "" {
		testPackage = params.PackageName
	}

	testURL := fmt.Sprintf(
		"%s/%s",
		strings.TrimSuffix(mirrorURL, "/"),
		testPackage,
	)

	if verbose {
		fmt.Printf("Testing NPM Mirror speed with: %s\n", testURL)
	}

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Accept", "application/json")

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeout)*time.Second,
	)
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

		return 0, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, &HttpRequestError{
			URL:        testURL,
			StatusCode: resp.StatusCode,
		}
	}

	contentLength := resp.ContentLength

	if contentLength > 0 && verbose {
		fmt.Printf(
			"Content-Length: %.2f MB\n",
			float64(contentLength)/1024/1024,
		)
	}

	var downloaded int64

	buf := make([]byte, 512*1024)

	lastProgress := time.Now()

	if verbose {
		fmt.Printf("Downloading for up to %d seconds...\n", timeout)
	}

	for {
		select {
		case <-ctx.Done():
			if verbose {
				fmt.Printf(
					"\nTimeout reached after %d seconds\n",
					timeout,
				)
			}

			goto calculateSpeed

		default:
			n, err := resp.Body.Read(buf)

			if n > 0 {
				downloaded += int64(n)

				if verbose && time.Since(lastProgress) > 500*time.Millisecond {
					elapsed := time.Since(start).Seconds()

					speedMBps :=
						(float64(downloaded) / 1024 / 1024) / elapsed

					if contentLength > 0 {
						percent :=
							float64(downloaded) /
								float64(contentLength) *
								100

						fmt.Printf(
							"\r[%ds] %.1f%% (%.2f/%.2f MB) - %.2f MB/s",
							int(elapsed),
							percent,
							float64(downloaded)/1024/1024,
							float64(contentLength)/1024/1024,
							speedMBps,
						)
					} else {
						fmt.Printf(
							"\r[%ds] Downloaded: %.2f MB - %.2f MB/s",
							int(elapsed),
							float64(downloaded)/1024/1024,
							speedMBps,
						)
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
		speedMBps :=
			(float64(downloaded) / 1024 / 1024) / duration

		if verbose {
			fmt.Printf("Average speed: %.2f MB/s\n", speedMBps)

			rating := getSpeedRating(speedMBps)

			fmt.Printf("Rating: %s\n", rating)
		}

		info := NpmCheckSpeedData{
			DownloadMb:      float64(downloaded) / 1024 / 1024,
			DurationSec:     duration,
			ContentLength:   contentLength,
			TimeoutSec:      timeout,
			SpeedMbps:       speedMBps,
			SpeedRating:     getSpeedRating(speedMBps),
			BytesDownloaded: downloaded,
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &SpeedTestError{
		URL: testURL,
		Err: fmt.Errorf("failed to speed test %v", testURL),
	}
}

// CheckPackage checks if a package exists on an NPM mirror
func (m *NpmMirrorService) CheckPackage(
	mirrorUrl,
	packageName string,
	verbose bool,
) (bool, *NpmCheckPackageData, error) {
	packageURL := fmt.Sprintf(
		"%s/%s",
		strings.TrimSuffix(mirrorUrl, "/"),
		packageName,
	)

	if verbose {
		fmt.Println("Checking package:", packageURL)
	}

	req, err := http.NewRequest("GET", packageURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Accept", "application/json")

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
			fmt.Printf(
				"Package '%s' not found on mirror\n",
				packageName,
			)
		}

		return false, nil, &PackageNotFoundError{
			Package: packageName,
		}
	}

	if resp.StatusCode != http.StatusOK {
		return false, nil, &HttpRequestError{
			URL:        packageURL,
			StatusCode: resp.StatusCode,
		}
	}

	var packageData map[string]interface{}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, &ResponseReadError{
			URL: packageURL,
			Err: err,
		}
	}

	if err := json.Unmarshal(body, &packageData); err != nil {
		return false, nil, &JsonParseError{
			URL: packageURL,
			Err: err,
		}
	}

	if distTags, ok := packageData["dist-tags"].(map[string]interface{}); ok {
		if latest, ok := distTags["latest"].(string); ok {
			if verbose {
				fmt.Printf(
					"Found package '%s' with latest version: %s\n",
					packageName,
					latest,
				)
			}

			info := NpmCheckPackageData{
				Version:  latest,
				distTags: &distTags,
			}

			return true, &info, nil
		}
	}

	if versions, ok := packageData["versions"].(map[string]interface{}); ok {
		if len(versions) > 0 {
			for version := range versions {
				if verbose {
					fmt.Printf(
						"Found package '%s' with version: %s\n",
						packageName,
						version,
					)
				}

				info := NpmCheckPackageData{
					Version: version,
				}

				return true, &info, nil
			}
		}
	}

	return false, nil, &PackageNotFoundError{
		Package: packageName,
	}
}

func (m *NpmMirrorService) CheckStatus(
	url string,
	verbose bool,
) (bool, *NpmCheckStatusData, error) {
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
			fmt.Printf(
				"Testing NPM mirror endpoint: %s\n",
				testURL,
			)
		}

		req, err := http.NewRequest(test.method, testURL, nil)
		if err != nil {
			continue
		}

		req.Header.Set(
			"Cache-Control",
			"no-cache, no-store, must-revalidate",
		)

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

		if resp.StatusCode == http.StatusOK {
			if verbose {
				fmt.Printf(
					"Mirror responded to %s with status %d\n",
					test.path,
					resp.StatusCode,
				)
			}

			info := NpmCheckStatusData{
				Status:       true,
				TestPath:     test.path,
				StatusCode:   resp.StatusCode,
				RedirectPath: nil,
			}

			return true, &info, nil
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")

			if verbose {
				fmt.Printf(
					"Mirror redirects to: %s\n",
					location,
				)
			}

			info := NpmCheckStatusData{
				Status:       true,
				TestPath:     test.path,
				StatusCode:   resp.StatusCode,
				RedirectPath: &location,
			}

			return true, &info, nil
		}
	}

	return false, nil, &InvalidMirrorError{
		URL: url,
	}
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
func NewNpmMirrorService() *NpmMirrorService {
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
