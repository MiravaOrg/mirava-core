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

// RegistryInfo holds detailed information about a Docker registry
type RegistryInfo struct {
	URL               string
	IsAlive           bool
	AuthRequired      bool
	CatalogAccessible bool
	APIVersion        string
}

type DockerSpeedParams struct {
	ImageName string
}

type DockerCheckSpeedData struct {
	DownloadMb      float64
	DurationSec     float64
	TimeoutSec      int
	LayerDigest     string
	LayerSize       float64
	Image           string
	SpeedMbps       float64
	SpeedRating     string
	BytesDownloaded int64
	ContentLength   int64
}

type DockerImageData struct {
	ImageName string
	Tags      []string
	TotalTags int
}

type DockerCheckStatusData struct {
	Status     string
	ApiVersion string
	Tags       string
	Url        string
}

func (m *DockerMirrorService) CheckSpeed(
	mirrorURL string,
	timeout int,
	verbose bool,
	params *DockerSpeedParams,
) (float64, *DockerCheckSpeedData, error) {

	imageName := "library/ubuntu"

	if params != nil {
		imageName = strings.Replace(params.ImageName, "library/", "", 1)
		imageName = fmt.Sprintf("library/%s", imageName)
	}

	registryURL := strings.TrimSuffix(mirrorURL, "/")
	tag := "latest"

	if verbose {
		fmt.Printf(
			"Testing registry: %s with image: %s:%s (timeout: %d seconds)\n",
			registryURL,
			imageName,
			tag,
			timeout,
		)
	}

	layerDigest, layerSize, err := m.getFirstLayerDigest(
		registryURL,
		imageName,
		tag,
		verbose,
	)

	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: mirrorURL,
			Err: err,
		}
	}

	blobURL := fmt.Sprintf(
		"%s/v2/%s/blobs/%s",
		registryURL,
		imageName,
		layerDigest,
	)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(timeout)*time.Second,
	)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		"GET",
		blobURL,
		nil,
	)

	if err != nil {
		return 0, nil, &HttpRequestError{
			URL: blobURL,
			Err: err,
		}
	}

	req.Header.Set(
		"Cache-Control",
		"no-cache, no-store, must-revalidate",
	)

	start := time.Now()

	resp, err := m.HttpClient.Do(req)
	if err != nil {

		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, &HttpRequestError{
				URL: blobURL,
				Err: context.DeadlineExceeded,
			}
		}

		return 0, nil, &HttpRequestError{
			URL: blobURL,
			Err: err,
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, nil, &HttpRequestError{
			URL:        blobURL,
			StatusCode: resp.StatusCode,
		}
	}

	var downloaded int64
	buf := make([]byte, 512*1024)

	for {
		select {
		case <-ctx.Done():
			goto calculateSpeed

		default:
			n, err := resp.Body.Read(buf)

			if n > 0 {
				downloaded += int64(n)
			}

			if err != nil {

				if err == io.EOF {
					goto calculateSpeed
				}

				if ctx.Err() == context.DeadlineExceeded {
					goto calculateSpeed
				}

				return 0, nil, &HttpRequestError{
					URL: blobURL,
					Err: err,
				}
			}
		}
	}

calculateSpeed:

	duration := time.Since(start).Seconds()

	if duration > 0 && downloaded > 0 {

		speedMBps := (float64(downloaded) / 1024 / 1024) / duration

		info := DockerCheckSpeedData{
			DownloadMb:      float64(downloaded) / 1024 / 1024,
			DurationSec:     duration,
			TimeoutSec:      timeout,
			LayerDigest:     layerDigest,
			LayerSize:       float64(layerSize) / 1024 / 1024,
			Image:           fmt.Sprintf("%s:%s", imageName, tag),
			SpeedMbps:       speedMBps,
			SpeedRating:     m.getDockerSpeedRating(speedMBps),
			BytesDownloaded: downloaded,
			ContentLength:   resp.ContentLength,
		}

		return speedMBps, &info, nil
	}

	return 0, nil, &HttpRequestError{
		URL: blobURL,
		Err: fmt.Errorf(
			"speed test failed (downloaded %d bytes in %.2fs)",
			downloaded,
			duration,
		),
	}
}

func (m *DockerMirrorService) getFirstLayerDigest(
	registryURL,
	imageName,
	tag string,
	verbose bool,
) (string, int64, error) {

	manifestList, err := m.fetchTagManifest(
		registryURL,
		imageName,
		tag,
	)

	if err != nil {
		return "", 0, &HttpRequestError{
			URL: registryURL,
			Err: err,
		}
	}

	var digestManifest *DigestManifest

	if manifestList != nil && len(manifestList.Manifests) > 0 {

		firstManifestDigest := manifestList.Manifests[0].Digest

		digestManifest, err = m.fetchDigestManifest(
			registryURL,
			imageName,
			firstManifestDigest,
		)

		if err != nil {
			return "", 0, &HttpRequestError{
				URL: registryURL,
				Err: err,
			}
		}

	} else {

		digestManifest, err = m.fetchDigestManifest(
			registryURL,
			imageName,
			tag,
		)

		if err != nil {
			return "", 0, &HttpRequestError{
				URL: registryURL,
				Err: err,
			}
		}
	}

	if len(digestManifest.Layers) == 0 {
		return "", 0, &InvalidMirrorError{
			URL: registryURL,
			Err: fmt.Errorf("no layers found in manifest"),
		}
	}

	layerDigest := digestManifest.Layers[0].Digest
	layerSize := digestManifest.Layers[0].Size

	return layerDigest, layerSize, nil
}

func (m *DockerMirrorService) fetchTagManifest(
	registryURL,
	imageName,
	tag string,
) (*ManifestList, error) {

	url := fmt.Sprintf(
		"%s/v2/%s/manifests/%s",
		registryURL,
		imageName,
		tag,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, &HttpRequestError{
			URL: url,
			Err: err,
		}
	}

	req.Header.Set(
		"Accept",
		"application/vnd.docker.distribution.manifest.list.v2+json,"+
			"application/vnd.docker.distribution.manifest.v2+json",
	)

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return nil, &HttpRequestError{
			URL: url,
			Err: err,
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &HttpRequestError{
			URL:        url,
			StatusCode: resp.StatusCode,
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &HttpRequestError{
			URL: url,
			Err: err,
		}
	}

	var manifestList ManifestList

	if err := json.Unmarshal(body, &manifestList); err == nil &&
		len(manifestList.Manifests) > 0 {

		return &manifestList, nil
	}

	return &ManifestList{
		Manifests: []Manifest{},
	}, nil
}

func (m *DockerMirrorService) fetchDigestManifest(
	registryURL,
	imageName,
	digest string,
) (*DigestManifest, error) {

	url := fmt.Sprintf(
		"%s/v2/%s/manifests/%s",
		registryURL,
		imageName,
		digest,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, &HttpRequestError{
			URL: url,
			Err: err,
		}
	}

	req.Header.Set(
		"Accept",
		"application/vnd.docker.distribution.manifest.v2+json",
	)

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return nil, &HttpRequestError{
			URL: url,
			Err: err,
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &HttpRequestError{
			URL:        url,
			StatusCode: resp.StatusCode,
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &HttpRequestError{
			URL: url,
			Err: err,
		}
	}

	var digestManifest DigestManifest

	if err := json.Unmarshal(body, &digestManifest); err != nil {
		return nil, &HttpRequestError{
			URL: url,
			Err: err,
		}
	}

	return &digestManifest, nil
}

func (m *DockerMirrorService) CheckPackage(
	mirrorUrl,
	imageName string,
) (bool, *DockerImageData, error) {

	baseURL := strings.TrimSuffix(mirrorUrl, "/")

	tagsURL := fmt.Sprintf(
		"%s/v2/%s/tags/list",
		baseURL,
		imageName,
	)

	req, err := http.NewRequest("GET", tagsURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: tagsURL,
			Err: err,
		}
	}

	req.Header.Set(
		"Cache-Control",
		"no-cache, no-store, must-revalidate",
	)

	req.Header.Set("Accept", "application/json")

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: tagsURL,
			Err: err,
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil, &PackageNotFoundError{
			Package: imageName,
		}
	}

	if resp.StatusCode != http.StatusOK {
		return false, nil, &HttpRequestError{
			URL:        tagsURL,
			StatusCode: resp.StatusCode,
		}
	}

	var tagsData struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: tagsURL,
			Err: err,
		}
	}

	if err := json.Unmarshal(body, &tagsData); err != nil {
		return false, nil, &HttpRequestError{
			URL: tagsURL,
			Err: err,
		}
	}

	info := DockerImageData{
		ImageName: imageName,
		Tags:      tagsData.Tags,
		TotalTags: len(tagsData.Tags),
	}

	return true, &info, nil
}

func (m *DockerMirrorService) CheckStatus(
	url string,
) (bool, *DockerCheckStatusData, error) {

	baseURL := strings.TrimSuffix(url, "/")

	testURL := baseURL + "/v2/"

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return false, nil, &HttpRequestError{
			URL: testURL,
			Err: err,
		}
	}

	resp, err := m.HttpClient.Do(req)
	if err != nil {
		return false, nil, &InvalidMirrorError{
			URL: testURL,
			Err: err,
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusUnauthorized {

		return false, nil, &InvalidMirrorError{
			URL: testURL,
			Err: fmt.Errorf(
				"unexpected status code %d",
				resp.StatusCode,
			),
		}
	}

	info := DockerCheckStatusData{
		Status:     "active",
		ApiVersion: "v2",
		Url:        baseURL,
	}

	return true, &info, nil
}

func (m *DockerMirrorService) getDockerSpeedRating(
	speedMBps float64,
) string {

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

func NewDockerMirrorService() *DockerMirrorService {

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
