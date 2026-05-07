package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/model"
)

type DockerMirrorService struct {
	HttpClient *http.Client
}

// ManifestList Docker manifest structures
type ManifestList struct {
	SchemaVersion int        `json:"schemaVersion"`
	MediaType     string     `json:"mediaType"`
	Manifests     []Manifest `json:"manifests"`
}

type Manifest struct {
	Digest    string   `json:"digest"`
	MediaType string   `json:"mediaType"`
	Size      int64    `json:"size"`
	Platform  Platform `json:"platform"`
}
type Platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

type DigestManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        ConfigDescriptor  `json:"config"`
	Layers        []LayerDescriptor `json:"layers"`
}

type ConfigDescriptor struct {
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
}

type LayerDescriptor struct {
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
}

func (m *DockerMirrorService) CheckMirrorSpeed(mirrorURL string, verbose bool) (float64, error) {
	registryURL := strings.TrimSuffix(mirrorURL, "/")
	imageName := "library/ubuntu"
	tag := "latest"

	if verbose {
		fmt.Printf("Testing registry: %s with image: %s:%s\n", registryURL, imageName, tag)
	}

	// Step 1: Get the layer digest using the manifest approach
	layerDigest, layerSize, err := m.getFirstLayerDigest(registryURL, imageName, tag, verbose)
	if err != nil {
		return 0, fmt.Errorf("failed to get layer digest: %w", err)
	}

	if verbose {
		fmt.Printf("Got layer digest: %s, size: %d bytes (%.2f MB)\n",
			layerDigest[:19], layerSize, float64(layerSize)/1024/1024)
	}

	// Step 2: Download the layer blob for speed testing
	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, imageName, layerDigest)

	if verbose {
		fmt.Printf("Downloading blob from: %s\n", blobURL)
	}

	// Download with timeout (15 seconds max)
	maxDuration := 15 * time.Second
	downloadedBytes, err := m.downloadWithTimeout(blobURL, maxDuration, verbose)
	if err != nil {
		return 0, fmt.Errorf("download failed: %w", err)
	}

	if downloadedBytes == 0 {
		return 0, fmt.Errorf("no data downloaded")
	}

	speed := (float64(downloadedBytes) / 1024 / 1024) / maxDuration.Seconds()

	if verbose {
		fmt.Printf("Downloaded %d bytes (%.2f MB) in %.2f seconds\n",
			downloadedBytes, float64(downloadedBytes)/1024/1024, maxDuration.Seconds())
		fmt.Printf("Average speed: %.2f MB/s\n", speed)

		// Speed rating
		switch {
		case speed > 20:
			fmt.Println("Rating: Excellent ⚡⚡⚡")
		case speed > 10:
			fmt.Println("Rating: Good ⚡⚡")
		case speed > 5:
			fmt.Println("Rating: Average ⚡")
		default:
			fmt.Println("Rating: Slow ⚠")
		}
	}

	return speed, nil
}

func (m *DockerMirrorService) getFirstLayerDigest(registryURL, imageName, tag string, verbose bool) (string, int64, error) {
	if verbose {
		fmt.Printf("Fetching tag manifest for %s:%s\n", imageName, tag)
	}

	// Step 1: Fetch tag manifest
	manifestList, err := m.fetchTagManifest(registryURL, imageName, tag)
	if err != nil {
		return "", 0, fmt.Errorf("failed to fetch tag manifest: %w", err)
	}

	var digestManifest *DigestManifest

	// Check if we have a manifest list or a single manifest
	if manifestList != nil && len(manifestList.Manifests) > 0 {
		// Step 2: Get first manifest digest
		firstManifestDigest := manifestList.Manifests[0].Digest
		if verbose {
			fmt.Printf("First manifest digest: %s\n", firstManifestDigest[:19])
		}

		// Step 3: Fetch digest manifest
		digestManifest, err = m.fetchDigestManifest(registryURL, imageName, firstManifestDigest)
		if err != nil {
			return "", 0, fmt.Errorf("failed to fetch digest manifest: %w", err)
		}
	} else {
		// Try direct manifest fetch as fallback
		if verbose {
			fmt.Println("No manifests in list, trying direct manifest fetch")
		}
		digestManifest, err = m.fetchDigestManifest(registryURL, imageName, tag)
		if err != nil {
			return "", 0, fmt.Errorf("failed to fetch direct manifest: %w", err)
		}
	}

	if len(digestManifest.Layers) == 0 {
		return "", 0, fmt.Errorf("no layers found in manifest")
	}

	// Step 4: Get first layer digest
	layerDigest := digestManifest.Layers[0].Digest
	layerSize := digestManifest.Layers[0].Size

	// If first layer is very small (< 1MB) and there are more layers, try to find a larger one
	if layerSize < 1024*1024 && len(digestManifest.Layers) > 1 {
		for _, layer := range digestManifest.Layers[1:] {
			if layer.Size > layerSize {
				if verbose {
					fmt.Printf("Using larger layer instead: %s, size: %d bytes (%.2f MB)\n",
						layer.Digest[:19], layer.Size, float64(layer.Size)/1024/1024)
				}
				return layer.Digest, layer.Size, nil
			}
		}
	}

	if verbose {
		fmt.Printf("First layer digest: %s, size: %d bytes (%.2f MB)\n",
			layerDigest[:19], layerSize, float64(layerSize)/1024/1024)
	}

	return layerDigest, layerSize, nil
}

func (m *DockerMirrorService) fetchTagManifest(registryURL, imageName, tag string) (*ManifestList, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, imageName, tag)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for manifest", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Try to parse as ManifestList first
	var manifestList ManifestList
	if err := json.Unmarshal(body, &manifestList); err == nil && len(manifestList.Manifests) > 0 {
		return &manifestList, nil
	}

	// If that fails, return empty manifest list (signals to try direct fetch)
	return &ManifestList{Manifests: []Manifest{}}, nil
}

func (m *DockerMirrorService) fetchDigestManifest(registryURL, imageName, digest string) (*DigestManifest, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, imageName, digest)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for digest manifest", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var digestManifest DigestManifest
	if err := json.Unmarshal(body, &digestManifest); err != nil {
		return nil, err
	}

	return &digestManifest, nil
}

func (m *DockerMirrorService) downloadWithTimeout(url string, maxDuration time.Duration, verbose bool) (int64, error) {
	startTime := time.Now()

	if verbose {
		fmt.Printf("Starting download from: %s\n", url)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	buffer := make([]byte, 8192)
	var totalBytes int64
	lastLogTime := startTime

	for {
		// Check timeout
		elapsed := time.Since(startTime)
		if elapsed >= maxDuration {
			if verbose {
				fmt.Printf("Download timeout reached after %.2f seconds, downloaded %d bytes\n",
					elapsed.Seconds(), totalBytes)
			}
			break
		}

		// Set read deadline
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			totalBytes += int64(n)

			// Log progress every second
			if verbose && time.Since(lastLogTime) >= time.Second {
				speedMbps := (float64(totalBytes) * 8) / (elapsed.Seconds() * 1_000_000)
				fmt.Printf("Downloaded %d bytes in %.1fs, speed: %.2f Mbps\n",
					totalBytes, elapsed.Seconds(), speedMbps)
				lastLogTime = time.Now()
			}
		}

		if err != nil {
			if err == io.EOF {
				// Completed successfully
				break
			}
			// If we downloaded some data before the error, consider it a success
			if totalBytes > 0 {
				if verbose {
					fmt.Printf("Download interrupted after downloading %d bytes: %v\n", totalBytes, err)
				}
				break
			}
			return 0, err
		}
	}

	finalElapsed := time.Since(startTime)
	finalSpeedMbps := 0.0
	if finalElapsed.Seconds() > 0 {
		finalSpeedMbps = (float64(totalBytes) * 8) / (finalElapsed.Seconds() * 1_000_000)
	}

	if verbose {
		fmt.Printf("Download completed: %d bytes in %.2fs, final speed: %.2f Mbps\n",
			totalBytes, finalElapsed.Seconds(), finalSpeedMbps)
	}

	return totalBytes, nil
}

// CheckPackage checks if an image exists on a Docker registry mirror
// For Docker, "package" means an image repository
// Returns: (exists, latest_tag, error)
func (m *DockerMirrorService) CheckPackage(mirrorUrl, imageName string, verbose bool) (bool, string, error) {
	// Docker registry v2 API endpoint for image tags
	baseURL := strings.TrimSuffix(mirrorUrl, "/")
	tagsURL := fmt.Sprintf("%s/v2/%s/tags/list", baseURL, imageName)

	if verbose {
		fmt.Println("Checking image: ", tagsURL)
	}

	req, err := http.NewRequest("GET", tagsURL, nil)
	if err != nil {
		return false, "", err
	}

	req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	req.Header.Set("Accept", "application/json")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		if verbose {
			fmt.Printf("Error checking image: %v\n", err)
		}
		return false, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if verbose {
			fmt.Printf("Image '%s' not found on mirror\n", imageName)
		}
		return false, "", nil
	}

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("HTTP %d from Docker registry", resp.StatusCode)
	}

	// Parse JSON response
	var tagsData struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", err
	}

	if err := json.Unmarshal(body, &tagsData); err != nil {
		return false, "", err
	}

	if len(tagsData.Tags) > 0 {
		// Find the latest tag (usually "latest" or the highest version)
		latestTag := "latest"
		for _, tag := range tagsData.Tags {
			if tag == "latest" {
				latestTag = tag
				break
			}
			// Check for version tags (e.g., "1.2.3", "v1.2.3")
			if len(tag) > 0 && (tag[0] >= '0' && tag[0] <= '9' || tag[0] == 'v') {
				latestTag = tag
			}
		}

		if verbose {
			fmt.Printf("Found image '%s' with %d tags, latest: %s\n", imageName, len(tagsData.Tags), latestTag)
		}
		return true, latestTag, nil
	}

	if verbose {
		fmt.Printf("Image '%s' found but no tags available\n", imageName)
	}

	return true, "unknown", nil
}

// CheckMirrorStatus checks if a Docker registry mirror is alive and responding
func (m *DockerMirrorService) CheckMirrorStatus(url string, verbose bool) (bool, error) {
	baseURL := strings.TrimSuffix(url, "/")

	// Test multiple endpoints with different characteristics
	type EndpointTest struct {
		path           string
		description    string
		expectedStatus []int
	}

	endpoints := []EndpointTest{
		{
			path:           "/v2/",
			description:    "Registry API v2 endpoint",
			expectedStatus: []int{200, 401},
		},
		{
			path:           "/v2/_catalog?n=1",
			description:    "Catalog endpoint (often restricted)",
			expectedStatus: []int{200, 401, 403},
		},
		{
			path:           "/v2/library/alpine/manifests/latest",
			description:    "Known image manifest",
			expectedStatus: []int{200, 401},
		},
	}

	var lastErr error
	registryInfo := &RegistryInfo{
		URL:               baseURL,
		IsAlive:           false,
		AuthRequired:      false,
		CatalogAccessible: false,
		APIVersion:        "unknown",
	}

	for _, endpoint := range endpoints {
		testURL := baseURL + endpoint.path

		if verbose {
			fmt.Printf("Testing endpoint: %s (%s)\n", testURL, endpoint.description)
		}

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			lastErr = err
			continue
		}

		// Add appropriate headers
		req.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		if strings.Contains(endpoint.path, "manifests") {
			req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
		}

		resp, err := m.HttpClient.Do(req)
		if err != nil {
			if verbose {
				fmt.Printf("Failed: %v\n", err)
			}
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		// Check if status is expected
		isValid := false
		for _, expected := range endpoint.expectedStatus {
			if resp.StatusCode == expected {
				isValid = true
				break
			}
		}

		if isValid {
			registryInfo.IsAlive = true
			registryInfo.APIVersion = "v2"

			switch resp.StatusCode {
			case 200:
				if verbose {
					fmt.Printf("OK (status %d)\n", resp.StatusCode)
				}
				if endpoint.path == "/v2/_catalog?n=1" {
					registryInfo.CatalogAccessible = true
				}
			case 401:
				if verbose {
					fmt.Printf("Requires authentication (status %d)\n", resp.StatusCode)
				}
				registryInfo.AuthRequired = true

				// Try to get auth info from header
				authHeader := resp.Header.Get("WWW-Authenticate")
				if authHeader != "" && verbose {
					fmt.Printf("Auth info: %s\n", authHeader)
				}
			case 403:
				if verbose {
					fmt.Printf("Access forbidden (status %d)\n", resp.StatusCode)
				}
			}
		} else {
			if verbose {
				fmt.Printf("Unexpected status %d\n", resp.StatusCode)
			}
		}
	}

	// Try to get registry version info
	if registryInfo.IsAlive {
		m.getRegistryInfo(baseURL, registryInfo, verbose)
	}

	// Display summary
	if verbose {
		fmt.Println("\nRegistry Summary:")
		fmt.Printf("URL: %s\n", registryInfo.URL)
		fmt.Printf("Status: %s\n", map[bool]string{true: "Online", false: "Offline"}[registryInfo.IsAlive])
		fmt.Printf("API Version: %s\n", registryInfo.APIVersion)
		fmt.Printf("Authentication: %s\n", map[bool]string{true: "Required", false: "Not required"}[registryInfo.AuthRequired])
		fmt.Printf("Catalog Access: %s\n", map[bool]string{true: "Accessible", false: "Restricted"}[registryInfo.CatalogAccessible])

		if registryInfo.AuthRequired {
			fmt.Println("\nNote: This registry requires authentication")
			fmt.Println("   Most operations will need valid credentials")
		}

		if !registryInfo.CatalogAccessible && registryInfo.IsAlive {
			fmt.Println("\nNote: Catalog endpoint is restricted (normal for production registries)")
			fmt.Println("   You can still pull images if you know their names")
		}
	}

	if !registryInfo.IsAlive {
		return false, fmt.Errorf("registry is not responding properly: %w", lastErr)
	}

	return true, nil
}

// RegistryInfo holds detailed information about a Docker registry
type RegistryInfo struct {
	URL               string
	IsAlive           bool
	AuthRequired      bool
	CatalogAccessible bool
	APIVersion        string
}

// getRegistryInfo attempts to gather additional registry information
func (m *DockerMirrorService) getRegistryInfo(baseURL string, info *RegistryInfo, verbose bool) {
	// Try to get registry version info (not all registries support this)
	versionURL := baseURL + "/v2/"

	req, err := http.NewRequest("GET", versionURL, nil)
	if err != nil {
		return
	}

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Check for Docker-Distribution-API-Version header
	if apiVersion := resp.Header.Get("Docker-Distribution-API-Version"); apiVersion != "" {
		info.APIVersion = apiVersion
		if verbose {
			fmt.Printf("   API Version Header: %s\n", apiVersion)
		}
	}

	// Check for rate limiting info
	rateLimit := resp.Header.Get("RateLimit-Limit")
	if rateLimit != "" && verbose {
		fmt.Printf("   Rate Limit: %s\n", rateLimit)
	}

	rateRemaining := resp.Header.Get("RateLimit-Remaining")
	if rateRemaining != "" && verbose {
		fmt.Printf("   Rate Remaining: %s\n", rateRemaining)
	}
}

// Alternative simpler version that returns a friendly string
func (m *DockerMirrorService) CheckMirrorStatusSimple(url string, verbose bool) (string, error) {
	baseURL := strings.TrimSuffix(url, "/")
	testURL := baseURL + "/v2/"

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 200:
		return "Registry is online and accessible", nil
	case 401:
		authHeader := resp.Header.Get("WWW-Authenticate")
		if authHeader != "" {
			return fmt.Sprintf("Registry requires authentication\n   Auth: %s", authHeader), nil
		}
		return "Registry requires authentication", nil
	case 403:
		return "Registry access forbidden (check permissions or IP restrictions)", nil
	default:
		return fmt.Sprintf("⚠️ Registry returned unexpected status: %d", resp.StatusCode), nil
	}
}

// CreateDockerMirrorService creates a new Docker registry mirror service instance
func CreateDockerMirrorService() model.MirrorService {
	return &DockerMirrorService{
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
