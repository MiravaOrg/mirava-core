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

type ComposerMirrorService struct {
	HttpClient *http.Client
}

type ComposerCheckSpeedParams struct {
	Package string // Package to test speed with (e.g., "laravel/framework")
	Version string // Specific version, empty for latest
}

type ComposerCheckSpeedData struct {
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
	TestURL         string
}

type ComposerCheckPackageData struct {
	Package     string
	Version     string
	Description string
	Type        string
	License     []string
	Homepage    string
	Time        string
	Authors     []ComposerAuthor
	Require     map[string]string
	RequireDev  map[string]string
	Downloads   *ComposerDownloads
	Abandoned   bool
	AbandonedBy string
}

type ComposerAuthor struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Homepage string `json:"homepage"`
	Role     string `json:"role"`
}

type ComposerDownloads struct {
	Total   int `json:"total"`
	Monthly int `json:"monthly"`
	Daily   int `json:"daily"`
}

type ComposerCheckStatusData struct {
	Status       bool
	PackagesURL  string
	ProvidersURL string
	StatusCode   int
}

// PackageVersion represents a single package version from Composer API
type PackageVersion struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Type        string             `json:"type"`
	License     []string           `json:"license"`
	Homepage    string             `json:"homepage"`
	Time        string             `json:"time"`
	Authors     []ComposerAuthor   `json:"authors"`
	Require     map[string]string  `json:"require"`
	RequireDev  map[string]string  `json:"require-dev"`
	Downloads   *ComposerDownloads `json:"downloads"`
	Abandoned   interface{}        `json:"abandoned"`
	Version     string             `json:"version"`
	Dist        struct {
		URL string `json:"url"`
	} `json:"dist"`
}

func (m *ComposerMirrorService) CheckSpeed(
	mirrorURL string,
	timeout int,
	verbose bool,
	params ComposerCheckSpeedParams,
) (float64, *ComposerCheckSpeedData, error) {

	baseURL := strings.TrimSuffix(mirrorURL, "/")

	// Default test package if not specified
	packageName := params.Package
	if packageName == "" {
		packageName = "laravel/framework"
	}

	// First, get the latest version and its dist URL
	var packageVersion string
	var distURL string

	if params.Version != "" {
		packageVersion = params.Version
		// Construct dist URL for specific version
		distURL = fmt.Sprintf("%s/%s/%s/%s-%s-%s.zip",
			baseURL,
			strings.ReplaceAll(packageName, "/", "/"),
			packageVersion,
			strings.ReplaceAll(packageName, "/", "-"),
			packageVersion,
			strings.ReplaceAll(packageName, "/", "-"))
	} else {
		// Fetch package metadata to get latest version and dist URL
		apiURL := fmt.Sprintf("%s/p2/%s.json", baseURL, packageName)

		if verbose {
			fmt.Printf("Fetching package metadata from: %s\n", apiURL)
		}

		resp, err := m.HttpClient.Get(apiURL)
		if err != nil {
			return 0, nil, &HttpRequestError{
				URL: apiURL,
				Err: err,
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return 0, nil, &HttpRequestError{
				URL: apiURL,
				Err: fmt.Errorf("HTTP %d for package metadata", resp.StatusCode),
			}
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to read package metadata: %w", err)
		}

		// Parse the response to get versions and dist URLs
		var composerResponse struct {
			Packages map[string][]struct {
				Version string `json:"version"`
				Dist    struct {
					URL string `json:"url"`
				} `json:"dist"`
			} `json:"packages"`
		}

		if err := json.Unmarshal(body, &composerResponse); err != nil {
			return 0, nil, fmt.Errorf("failed to parse package metadata: %w", err)
		}

		versions, ok := composerResponse.Packages[packageName]
		if !ok || len(versions) == 0 {
			return 0, nil, fmt.Errorf("no versions found for package %s", packageName)
		}

		// Find the latest stable version with a valid dist URL
		var latestVersion string
		var latestDistURL string

		for _, v := range versions {
			// Skip dev versions
			isStable := !strings.Contains(v.Version, "dev") &&
				!strings.Contains(v.Version, "alpha") &&
				!strings.Contains(v.Version, "beta") &&
				!strings.Contains(v.Version, "rc")

			if (latestVersion == "" || (isStable && v.Version > latestVersion)) && v.Dist.URL != "" {
				latestVersion = v.Version
				latestDistURL = v.Dist.URL
			}
		}

		if latestVersion == "" {
			// Fallback to any version
			for _, v := range versions {
				if v.Dist.URL != "" {
					latestVersion = v.Version
					latestDistURL = v.Dist.URL
					break
				}
			}
		}

		if latestDistURL == "" {
			return 0, nil, fmt.Errorf("no valid dist URL found for package %s", packageName)
		}

		packageVersion = latestVersion
		distURL = latestDistURL

		if verbose {
			fmt.Printf("Latest stable version: %s\n", packageVersion)
			fmt.Printf("Dist URL: %s\n", distURL)
		}
	}

	if verbose {
		fmt.Printf("Testing Composer mirror speed with: %s (timeout: %d seconds)\n", distURL, timeout)
		fmt.Printf("Downloading package zip...\n")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeout)*time.Second,
	)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", distURL, nil)
	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: distURL,
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
				URL: distURL,
				Err: fmt.Errorf("timeout reached before connection established"),
			}
		}

		return 0, nil, &HttpRequestError{
			URL: distURL,
			Err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, &HttpRequestError{
			URL: distURL,
			Err: fmt.Errorf("HTTP %d for package zip", resp.StatusCode),
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
					URL: distURL,
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
			fmt.Printf("Rating: %s\n", getComposerSpeedRating(speedMBps))
		}

		info := ComposerCheckSpeedData{
			DownloadMb:      float64(downloaded) / 1024 / 1024,
			DurationSec:     duration,
			TimeoutSec:      timeout,
			SpeedMBps:       speedMBps,
			SpeedRating:     getComposerSpeedRating(speedMBps),
			BytesDownloaded: downloaded,
			ContentLength:   contentLength,
			Package:         packageName,
			Version:         packageVersion,
			TestURL:         distURL,
			MirrorURL:       baseURL,
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &HttpRequestError{
		URL: distURL,
		Err: fmt.Errorf("speed test failed (downloaded %d bytes in %.2fs)", downloaded, duration),
	}
}

func (m *ComposerMirrorService) CheckPackage(
	mirrorURL string,
	packageName string,
	verbose bool,
) (bool, *ComposerCheckPackageData, error) {

	baseURL := strings.TrimSuffix(mirrorURL, "/")

	// Try multiple possible URL patterns
	urlPatterns := []string{
		"%s/p2/%s.json", // Standard Composer V2
		"%s/%s.json",    // Alternative pattern
		"%s/p/%s.json",  // Composer V1 pattern
	}

	var resp *http.Response
	var apiURL string
	var err error

	for _, pattern := range urlPatterns {
		apiURL = fmt.Sprintf(pattern, baseURL, packageName)

		if verbose {
			fmt.Printf("Trying: %s\n", apiURL)
		}

		resp, err = m.HttpClient.Get(apiURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			if verbose {
				fmt.Printf("Found package at: %s\n", apiURL)
			}
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
	}

	if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("package not found: tried multiple patterns")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse versions from response
	versions, err := parseVersionsFromResponse(body, packageName)
	if err != nil {
		return false, nil, fmt.Errorf("failed to parse package metadata: %w", err)
	}

	if len(versions) == 0 {
		if verbose {
			fmt.Printf("No versions found for package '%s'\n", packageName)
		}
		return false, nil, nil
	}

	// Process versions to get latest
	return processVersions(packageName, versions, verbose)
}

// parseVersionsFromResponse attempts to parse Composer API response in various formats
func parseVersionsFromResponse(body []byte, packageName string) (map[string]PackageVersion, error) {
	versions := make(map[string]PackageVersion)

	// Try parsing as the standard Composer V2 format
	var composerResponse struct {
		Minified string `json:"minified"`
		Packages map[string][]struct {
			Name        string             `json:"name"`
			Description string             `json:"description"`
			Version     string             `json:"version"`
			Type        string             `json:"type"`
			License     []string           `json:"license"`
			Homepage    string             `json:"homepage"`
			Time        string             `json:"time"`
			Authors     []ComposerAuthor   `json:"authors"`
			Require     map[string]string  `json:"require"`
			RequireDev  map[string]string  `json:"require-dev"`
			Downloads   *ComposerDownloads `json:"downloads"`
			Abandoned   interface{}        `json:"abandoned"`
			Dist        struct {
				URL string `json:"url"`
			} `json:"dist"`
			Keywords []string `json:"keywords,omitempty"`
			Support  struct {
				Issues string `json:"issues,omitempty"`
				Source string `json:"source,omitempty"`
			} `json:"support,omitempty"`
			Funding []interface{} `json:"funding,omitempty"`
			Extra   struct {
				Laravel struct {
					DontDiscover []interface{} `json:"dont-discover,omitempty"`
				} `json:"laravel,omitempty"`
				BranchAlias struct {
					DevMaster string `json:"dev-master,omitempty"`
				} `json:"branch-alias,omitempty"`
			} `json:"extra,omitempty"`
			Autoload struct {
				Files []string          `json:"files,omitempty"`
				Psr4  map[string]string `json:"psr-4,omitempty"`
			} `json:"autoload,omitempty"`
		} `json:"packages"`
	}

	if err := json.Unmarshal(body, &composerResponse); err == nil {
		if versionArray, ok := composerResponse.Packages[packageName]; ok {
			for _, versionData := range versionArray {
				pv := PackageVersion{
					Name:        versionData.Name,
					Description: versionData.Description,
					Type:        versionData.Type,
					License:     versionData.License,
					Homepage:    versionData.Homepage,
					Time:        versionData.Time,
					Authors:     versionData.Authors,
					Require:     versionData.Require,
					RequireDev:  versionData.RequireDev,
					Downloads:   versionData.Downloads,
					Abandoned:   versionData.Abandoned,
					Dist:        versionData.Dist,
				}
				// Use the version field as the key
				versionKey := versionData.Version
				if versionKey == "" {
					versionKey = versionData.Name + "-" + versionData.Time
				}
				versions[versionKey] = pv
			}
			if len(versions) > 0 {
				return versions, nil
			}
		}
	}

	// Format 2: Standard Composer V2 with packages map of maps
	var v2MapResult struct {
		Packages map[string]map[string]PackageVersion `json:"packages"`
	}
	if err := json.Unmarshal(body, &v2MapResult); err == nil {
		if v2Versions, ok := v2MapResult.Packages[packageName]; ok {
			return v2Versions, nil
		}
	}

	// Format 3: Direct map of versions (Composer V1)
	var v1Result map[string]map[string]PackageVersion
	if err := json.Unmarshal(body, &v1Result); err == nil {
		if v1Versions, ok := v1Result[packageName]; ok {
			return v1Versions, nil
		}
	}

	return nil, fmt.Errorf("unable to parse response format - tried all known formats")
}

// processVersions processes versions map and creates package data
func processVersions(packageName string, versions map[string]PackageVersion, verbose bool) (bool, *ComposerCheckPackageData, error) {
	// Get the latest stable version
	var latestVersion string
	var latestVersionData PackageVersion

	for version, data := range versions {
		// Prefer stable versions (not dev or alpha/beta/rc)
		isStable := !strings.Contains(version, "dev") &&
			!strings.Contains(version, "alpha") &&
			!strings.Contains(version, "beta") &&
			!strings.Contains(version, "rc")

		if latestVersion == "" || (isStable && version > latestVersion) {
			latestVersion = version
			latestVersionData = data
		}
	}

	if latestVersion == "" {
		// Fallback to any version
		for version, data := range versions {
			latestVersion = version
			latestVersionData = data
			break
		}
	}

	if verbose {
		fmt.Printf("Found package '%s' with %d versions, latest: %s\n",
			packageName, len(versions), latestVersion)
		if latestVersionData.Description != "" {
			fmt.Printf("Description: %s\n", latestVersionData.Description)
		}
	}

	// Check if package is abandoned
	abandoned := false
	abandonedBy := ""
	if latestVersionData.Abandoned != nil {
		abandoned = true
		if abandonedStr, ok := latestVersionData.Abandoned.(string); ok {
			abandonedBy = abandonedStr
		}
	}

	info := &ComposerCheckPackageData{
		Package:     packageName,
		Version:     latestVersion,
		Description: latestVersionData.Description,
		Type:        latestVersionData.Type,
		License:     latestVersionData.License,
		Homepage:    latestVersionData.Homepage,
		Time:        latestVersionData.Time,
		Authors:     latestVersionData.Authors,
		Require:     latestVersionData.Require,
		RequireDev:  latestVersionData.RequireDev,
		Downloads:   latestVersionData.Downloads,
		Abandoned:   abandoned,
		AbandonedBy: abandonedBy,
	}

	return true, info, nil
}

func (m *ComposerMirrorService) CheckStatus(
	url string,
	verbose bool,
) (bool, *ComposerCheckStatusData, error) {

	baseURL := strings.TrimSuffix(url, "/")

	// Test endpoints for Composer mirror
	testPaths := []string{
		"/packages.json",             // Main packages index
		"/p2/laravel/framework.json", // Test specific package
	}

	for _, path := range testPaths {
		testURL := baseURL + path

		if verbose {
			fmt.Printf("Testing Composer mirror endpoint: %s\n", testURL)
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
				fmt.Printf("Failed: %v\n", err)
			}
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			if verbose {
				fmt.Printf("Mirror responded successfully with status %d\n", resp.StatusCode)
			}

			info := ComposerCheckStatusData{
				Status:      true,
				PackagesURL: testURL,
				StatusCode:  resp.StatusCode,
			}

			return true, &info, nil
		}
	}

	return false, nil, &HttpRequestError{
		URL: baseURL,
		Err: fmt.Errorf("mirror does not appear to be a valid Composer mirror"),
	}
}

func getComposerSpeedRating(speedMBps float64) string {
	switch {
	case speedMBps > 20:
		return "Excellent"
	case speedMBps > 10:
		return "Good"
	case speedMBps > 5:
		return "Average"
	case speedMBps > 2:
		return "Slow"
	default:
		return "Very Slow"
	}
}

// NewComposerMirrorService creates a new Composer mirror service instance
func NewComposerMirrorService() *ComposerMirrorService {
	return &ComposerMirrorService{
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
