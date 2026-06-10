package aptcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultAptCacheTTL = 30 * time.Minute

type aptListMeta struct {
	ExpiresAt    time.Time `json:"expires_at"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
}

type cachedAptIndexPaths struct {
	Paths     []aptIndexPath `json:"paths"`
	ExpiresAt time.Time      `json:"expires_at"`
}

type cachedAptPackageVersion struct {
	Result    PackageLookupResult `json:"result"`
	ExpiresAt time.Time              `json:"expires_at"`
}

// mirrorCache stores apt metadata in memory and, when root is set, on disk.
type mirrorCache struct {
	mu              sync.RWMutex
	ttl             time.Duration
	root            string
	indexPaths      map[string]cachedAptIndexPaths
	packageVersions map[string]cachedAptPackageVersion
}

func newMirrorCache(ttl time.Duration, root string) (*mirrorCache, error) {
	if ttl <= 0 {
		ttl = defaultAptCacheTTL
	}

	if root != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}

	return &mirrorCache{
		ttl:             ttl,
		root:            root,
		indexPaths:      make(map[string]cachedAptIndexPaths),
		packageVersions: make(map[string]cachedAptPackageVersion),
	}, nil
}

func defaultCacheDirectory() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mirava-core", "apt")
	}
	return filepath.Join(dir, "mirava-core", "apt")
}

func (c *mirrorCache) diskEnabled() bool {
	return c.root != ""
}

func aptPackageCacheKey(repositoryURL, packageName string) string {
	return repositoryURL + "\x00" + packageName
}

func aptPackageFileKey(repositoryURL, packageName string) string {
	sum := sha256.Sum256([]byte(aptPackageCacheKey(repositoryURL, packageName)))
	return hex.EncodeToString(sum[:8])
}

func (c *mirrorCache) repoKey(repositoryURL string) string {
	sum := sha256.Sum256([]byte(repositoryURL))
	return hex.EncodeToString(sum[:8])
}

func (c *mirrorCache) repositoryDir(repositoryURL string) string {
	return filepath.Join(c.root, "repositories", c.repoKey(repositoryURL))
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

func (c *mirrorCache) listFilePaths(rawURL string) (dataPath, metaPath string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		sum := sha256.Sum256([]byte(rawURL))
		dataPath = filepath.Join(c.root, "lists", "_invalid", hex.EncodeToString(sum[:]))
		return dataPath, dataPath + ".meta.json"
	}

	segments := []string{c.root, "lists", aptCachePathSegment(parsed.Host)}
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

func (c *mirrorCache) readJSON(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func (c *mirrorCache) writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (c *mirrorCache) getIndexPaths(repositoryURL string) ([]aptIndexPath, bool) {
	c.mu.RLock()
	entry, ok := c.indexPaths[repositoryURL]
	c.mu.RUnlock()

	if ok && time.Now().Before(entry.ExpiresAt) {
		paths := make([]aptIndexPath, len(entry.Paths))
		copy(paths, entry.Paths)
		return paths, true
	}

	if !c.diskEnabled() {
		return nil, false
	}

	path := filepath.Join(c.repositoryDir(repositoryURL), "index-paths.json")
	var diskEntry cachedAptIndexPaths
	if err := c.readJSON(path, &diskEntry); err != nil || time.Now().After(diskEntry.ExpiresAt) {
		return nil, false
	}

	paths := make([]aptIndexPath, len(diskEntry.Paths))
	copy(paths, diskEntry.Paths)

	c.mu.Lock()
	c.indexPaths[repositoryURL] = cachedAptIndexPaths{
		Paths:     append([]aptIndexPath(nil), paths...),
		ExpiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	return paths, true
}

func (c *mirrorCache) setIndexPaths(repositoryURL string, paths []aptIndexPath) {
	stored := make([]aptIndexPath, len(paths))
	copy(stored, paths)

	entry := cachedAptIndexPaths{
		Paths:     stored,
		ExpiresAt: time.Now().Add(c.ttl),
	}

	c.mu.Lock()
	c.indexPaths[repositoryURL] = entry
	c.mu.Unlock()

	if !c.diskEnabled() {
		return
	}

	_ = c.writeJSON(filepath.Join(c.repositoryDir(repositoryURL), "index-paths.json"), entry)
}

func (c *mirrorCache) getPackageVersion(repositoryURL, packageName string) (*PackageLookupResult, bool) {
	key := aptPackageCacheKey(repositoryURL, packageName)

	c.mu.RLock()
	entry, ok := c.packageVersions[key]
	c.mu.RUnlock()

	if ok && time.Now().Before(entry.ExpiresAt) {
		result := entry.Result
		return &result, true
	}

	if !c.diskEnabled() {
		return nil, false
	}

	path := filepath.Join(
		c.repositoryDir(repositoryURL),
		"packages",
		aptPackageFileKey(repositoryURL, packageName)+".json",
	)

	var diskEntry cachedAptPackageVersion
	if err := c.readJSON(path, &diskEntry); err != nil || time.Now().After(diskEntry.ExpiresAt) {
		return nil, false
	}

	result := diskEntry.Result

	c.mu.Lock()
	c.packageVersions[key] = cachedAptPackageVersion{
		Result:    result,
		ExpiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	return &result, true
}

func (c *mirrorCache) setPackageVersion(repositoryURL, packageName string, result *PackageLookupResult) {
	if result == nil {
		return
	}

	key := aptPackageCacheKey(repositoryURL, packageName)
	entry := cachedAptPackageVersion{
		Result:    *result,
		ExpiresAt: time.Now().Add(c.ttl),
	}

	c.mu.Lock()
	c.packageVersions[key] = entry
	c.mu.Unlock()

	if !c.diskEnabled() {
		return
	}

	path := filepath.Join(
		c.repositoryDir(repositoryURL),
		"packages",
		aptPackageFileKey(repositoryURL, packageName)+".json",
	)
	_ = c.writeJSON(path, entry)
}

func (c *mirrorCache) readListFileData(rawURL string) ([]byte, bool) {
	if !c.diskEnabled() {
		return nil, false
	}

	dataPath, _ := c.listFilePaths(rawURL)
	data, err := os.ReadFile(dataPath)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (c *mirrorCache) touchListFileMeta(rawURL string) {
	if !c.diskEnabled() {
		return
	}

	_, metaPath := c.listFilePaths(rawURL)
	meta := c.getListFileMeta(rawURL)
	if meta == nil {
		meta = &aptListMeta{}
	}
	meta.ExpiresAt = time.Now().Add(c.ttl)
	_ = c.writeJSON(metaPath, meta)
}

func (c *mirrorCache) getListFile(rawURL string) ([]byte, bool) {
	if !c.diskEnabled() {
		return nil, false
	}

	_, metaPath := c.listFilePaths(rawURL)
	var meta aptListMeta
	if err := c.readJSON(metaPath, &meta); err != nil || time.Now().After(meta.ExpiresAt) {
		return nil, false
	}

	dataPath, _ := c.listFilePaths(rawURL)
	data, err := os.ReadFile(dataPath)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (c *mirrorCache) getListFileMeta(rawURL string) *aptListMeta {
	if !c.diskEnabled() {
		return nil
	}

	_, metaPath := c.listFilePaths(rawURL)
	var meta aptListMeta
	if err := c.readJSON(metaPath, &meta); err != nil {
		return nil
	}
	return &meta
}

func (c *mirrorCache) setListFile(rawURL string, data []byte, header http.Header) {
	if !c.diskEnabled() {
		return
	}

	dataPath, metaPath := c.listFilePaths(rawURL)
	meta := aptListMeta{
		ExpiresAt:    time.Now().Add(c.ttl),
		ETag:         strings.TrimSpace(header.Get("ETag")),
		LastModified: strings.TrimSpace(header.Get("Last-Modified")),
	}

	if err := os.MkdirAll(filepath.Dir(dataPath), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(dataPath, data, 0o644); err != nil {
		return
	}
	_ = c.writeJSON(metaPath, meta)
}

func readHTTPBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
