package pkg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type PacmanMirrorService struct {
	HttpClient *http.Client
}

type PacmanCheckSpeedParams struct {
	Repository string
	Arch       string
	Package    string
}

type PacmanCheckSpeedData struct {
	DownloadMb      float64
	DurationSec     float64
	TimeoutSec      int
	SpeedMBps       float64
	SpeedRating     string
	BytesDownloaded int64
	ContentLength   int64
	Repository      string
	Arch            string
	Package         string
}

type PacmanCheckPackageData struct {
	Package string
	Version string
	Matches int
}

type PacmanCheckStatusData struct {
	Status     bool
	TestPath   string
	StatusCode int
}

func (m *PacmanMirrorService) CheckSpeed(
	mirrorURL string,
	timeout int,
	verbose bool,
) (float64, *PacmanCheckSpeedData, error) {

	repository := "core"
	arch := "x86_64"
	testPackage := "/"

	baseURL := strings.TrimSuffix(mirrorURL, "/")
	testURL := fmt.Sprintf(
		"%s/extra/os/x86_64/extra.db",
		baseURL,
	)

	if verbose {
		fmt.Printf(
			"Testing Pacman mirror speed with: %s (timeout: %d seconds)\n",
			testURL,
			timeout,
		)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeout)*time.Second,
	)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("User-Agent", USER_AGENT)

	start := time.Now()

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, &HttpRequestError{
				URL: testURL,
				Err: fmt.Errorf("timeout reached before connection established"),
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
			URL: testURL,
			Err: fmt.Errorf(
				"HTTP %d for speed test file (expected 200)",
				resp.StatusCode,
			),
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
		speedMBps := (float64(downloaded) / 1024 / 1024) / duration

		if verbose {
			fmt.Printf("Average speed: %.2f MB/s\n", speedMBps)
			fmt.Printf("Rating: %s\n", getPacmanSpeedRating(speedMBps))
		}

		info := PacmanCheckSpeedData{
			DownloadMb:      float64(downloaded) / 1024 / 1024,
			DurationSec:     duration,
			TimeoutSec:      timeout,
			SpeedMBps:       speedMBps,
			SpeedRating:     getPacmanSpeedRating(speedMBps),
			BytesDownloaded: downloaded,
			ContentLength:   contentLength,
			Repository:      repository,
			Arch:            arch,
			Package:         testPackage,
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &HttpRequestError{
		URL: testURL,
		Err: fmt.Errorf(
			"speed test failed (downloaded %d bytes in %.2fs)",
			downloaded,
			duration,
		),
	}
}

// CheckPackage checks if a package exists on a Pacman mirror
func (m *PacmanMirrorService) CheckPackage(
	mirrorURL,
	packageName string,
	verbose bool,
) (bool, *PacmanCheckPackageData, error) {

	baseURL := strings.TrimSuffix(mirrorURL, "/")

	repository := "core"
	arch := "x86_64"

	packageURL := fmt.Sprintf(
		"%s/%s/os/%s/",
		baseURL,
		repository,
		arch,
	)

	if verbose {
		fmt.Printf("Checking package '%s' in %s\n", packageName, packageURL)
	}

	req, err := http.NewRequest("GET", packageURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: fmt.Errorf(
				"HTTP %d from Pacman mirror",
				resp.StatusCode,
			),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: packageURL,
			Err: err,
		}
	}

	// Example:
	// bash-5.2.037-1-x86_64.pkg.tar.zst
	regex := regexp.MustCompile(
		fmt.Sprintf(
			`%s-([0-9][^"]+?)-x86_64\.pkg`,
			regexp.QuoteMeta(packageName),
		),
	)

	matches := regex.FindAllStringSubmatch(string(body), -1)

	if len(matches) == 0 {
		if verbose {
			fmt.Printf("Package '%s' not found\n", packageName)
		}

		return false, nil, nil
	}

	latestVersion := matches[0][1]

	if verbose {
		fmt.Printf(
			"Found package '%s' with version: %s\n",
			packageName,
			latestVersion,
		)
	}

	info := PacmanCheckPackageData{
		Package: packageName,
		Version: latestVersion,
		Matches: len(matches),
	}

	return true, &info, nil
}

// CheckStatus checks if a Pacman mirror is alive
func (m *PacmanMirrorService) CheckStatus(
	url string,
	verbose bool,
) (bool, *PacmanCheckStatusData, error) {

	testPaths := []string{
		"/core/os/x86_64/",
		"/extra/os/x86_64/",
	}

	baseURL := strings.TrimSuffix(url, "/")

	for _, path := range testPaths {
		testURL := strings.TrimSuffix(baseURL+path, "/")

		if verbose {
			fmt.Printf("Testing Pacman mirror endpoint: %s\n", testURL)
		}

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			continue
		}

		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			if verbose {
				fmt.Printf("Failed: %v\n", err)
			}

			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			if verbose {
				fmt.Printf(
					"Mirror responded successfully with status %d\n",
					resp.StatusCode,
				)
			}

			info := PacmanCheckStatusData{
				Status:     true,
				TestPath:   path,
				StatusCode: resp.StatusCode,
			}

			return true, &info, nil
		}
	}

	return false, nil, &HttpRequestError{
		URL: baseURL,
		Err: fmt.Errorf(
			"mirror does not appear to be a valid Pacman mirror",
		),
	}
}

func getPacmanSpeedRating(speedMBps float64) string {
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

// NewPacmanMirrorService creates a new Pacman mirror service instance
func NewPacmanMirrorService() *PacmanMirrorService {
	return &PacmanMirrorService{
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
