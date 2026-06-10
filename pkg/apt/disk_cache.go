package apt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type aptDiskListMeta struct {
	ExpiresAt    time.Time `json:"expires_at"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
}

type aptDiskCache struct {
	root string
	ttl  time.Duration
}

func newAptDiskCache(root string, ttl time.Duration) (*aptDiskCache, error) {
	if root == "" {
		return nil, fmt.Errorf("disk cache root is required")
	}
	if ttl <= 0 {
		ttl = defaultAptCacheTTL
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}

	return &aptDiskCache{root: root, ttl: ttl}, nil
}

func aptCachePathSegment(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func (dc *aptDiskCache) listFilePaths(rawURL string) (dataPath, metaPath string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		sum := sha256.Sum256([]byte(rawURL))
		dataPath = filepath.Join(dc.root, "lists", "_invalid", hex.EncodeToString(sum[:]))
		return dataPath, dataPath + ".meta.json"
	}

	segments := []string{dc.root, "lists", aptCachePathSegment(parsed.Host)}
	for _, part := range strings.Split(strings.Trim(parsed.Path, "/"), "/") {
		if part == "" {
			continue
		}
		segments = append(segments, aptCachePathSegment(part))
	}
	if len(segments) == 3 {
		segments = append(segments, "index")
	}

	dataPath = filepath.Join(segments...)
	return dataPath, dataPath + ".meta.json"
}

func (dc *aptDiskCache) repositoryDir(repositoryURL string) string {
	return filepath.Join(dc.root, "repositories", dc.repoKey(repositoryURL))
}

func (dc *aptDiskCache) repoKey(repositoryURL string) string {
	sum := sha256.Sum256([]byte(repositoryURL))
	return hex.EncodeToString(sum[:8])
}

func (dc *aptDiskCache) listDataPath(rawURL string) string {
	dataPath, _ := dc.listFilePaths(rawURL)
	return dataPath
}

func (dc *aptDiskCache) listMetaPath(rawURL string) string {
	_, metaPath := dc.listFilePaths(rawURL)
	return metaPath
}

func (dc *aptDiskCache) readJSON(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func (dc *aptDiskCache) writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (dc *aptDiskCache) getListFile(rawURL string) ([]byte, bool) {
	metaPath := dc.listMetaPath(rawURL)
	var meta aptDiskListMeta
	if err := dc.readJSON(metaPath, &meta); err != nil || time.Now().After(meta.ExpiresAt) {
		return nil, false
	}

	data, err := os.ReadFile(dc.listDataPath(rawURL))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (dc *aptDiskCache) getListFileMeta(rawURL string) *aptDiskListMeta {
	metaPath := dc.listMetaPath(rawURL)
	var meta aptDiskListMeta
	if err := dc.readJSON(metaPath, &meta); err != nil {
		return nil
	}
	return &meta
}

func (dc *aptDiskCache) setListFile(rawURL string, data []byte, header http.Header) error {
	dataPath, metaPath := dc.listFilePaths(rawURL)

	meta := aptDiskListMeta{
		ExpiresAt:    time.Now().Add(dc.ttl),
		ETag:         strings.TrimSpace(header.Get("ETag")),
		LastModified: strings.TrimSpace(header.Get("Last-Modified")),
	}

	if err := os.MkdirAll(filepath.Dir(dataPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dataPath, data, 0o644); err != nil {
		return err
	}
	return dc.writeJSON(metaPath, meta)
}

func (dc *aptDiskCache) getIndexPaths(repositoryURL string) ([]aptIndexPath, bool) {
	path := filepath.Join(dc.repositoryDir(repositoryURL), "index-paths.json")

	var entry cachedAptIndexPaths
	if err := dc.readJSON(path, &entry); err != nil || time.Now().After(entry.ExpiresAt) {
		return nil, false
	}

	paths := make([]aptIndexPath, len(entry.Paths))
	copy(paths, entry.Paths)
	return paths, true
}

func (dc *aptDiskCache) setIndexPaths(repositoryURL string, paths []aptIndexPath) error {
	stored := make([]aptIndexPath, len(paths))
	copy(stored, paths)

	entry := cachedAptIndexPaths{
		Paths:     stored,
		ExpiresAt: time.Now().Add(dc.ttl),
	}
	return dc.writeJSON(filepath.Join(dc.repositoryDir(repositoryURL), "index-paths.json"), entry)
}

func aptPackageFileKey(repositoryURL, packageName string) string {
	sum := sha256.Sum256([]byte(aptPackageCacheKey(repositoryURL, packageName)))
	return hex.EncodeToString(sum[:8])
}

func (dc *aptDiskCache) getPackageVersion(repositoryURL, packageName string) (*AptPackageVersionData, bool) {
	path := filepath.Join(
		dc.repositoryDir(repositoryURL),
		"packages",
		aptPackageFileKey(repositoryURL, packageName)+".json",
	)

	var entry cachedAptPackageVersion
	if err := dc.readJSON(path, &entry); err != nil || time.Now().After(entry.ExpiresAt) {
		return nil, false
	}

	result := entry.Result
	return &result, true
}

func (dc *aptDiskCache) setPackageVersion(repositoryURL, packageName string, result *AptPackageVersionData) error {
	if result == nil {
		return nil
	}

	entry := cachedAptPackageVersion{
		Result:    *result,
		ExpiresAt: time.Now().Add(dc.ttl),
	}

	path := filepath.Join(
		dc.repositoryDir(repositoryURL),
		"packages",
		aptPackageFileKey(repositoryURL, packageName)+".json",
	)
	return dc.writeJSON(path, entry)
}

func readHTTPBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
