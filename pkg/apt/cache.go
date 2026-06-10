package apt

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"net/http"
)

const defaultAptCacheTTL = 30 * time.Minute

type aptMirrorCache struct {
	mu              sync.RWMutex
	ttl             time.Duration
	disk            *aptDiskCache
	indexPaths      map[string]cachedAptIndexPaths
	packageVersions map[string]cachedAptPackageVersion
}

type cachedAptIndexPaths struct {
	Paths     []aptIndexPath `json:"paths"`
	ExpiresAt time.Time      `json:"expires_at"`
}

type cachedAptPackageVersion struct {
	Result    AptPackageVersionData `json:"result"`
	ExpiresAt time.Time             `json:"expires_at"`
}

func newAptMirrorCache(ttl time.Duration, disk *aptDiskCache) *aptMirrorCache {
	if ttl <= 0 {
		ttl = defaultAptCacheTTL
	}

	return &aptMirrorCache{
		ttl:             ttl,
		disk:            disk,
		indexPaths:      make(map[string]cachedAptIndexPaths),
		packageVersions: make(map[string]cachedAptPackageVersion),
	}
}

func (m *AptMirrorService) aptCacheDirectory() string {
	if m.CacheDir != "" {
		return m.CacheDir
	}
	return defaultAptCacheDirectory()
}

func defaultAptCacheDirectory() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mirava-core", "apt")
	}
	return filepath.Join(dir, "mirava-core", "apt")
}

// CacheDirectory returns the directory used for on-disk apt cache files.
func (m *AptMirrorService) CacheDirectory() string {
	return m.aptCacheDirectory()
}

func (m *AptMirrorService) aptMirrorCache() *aptMirrorCache {
	m.cacheOnce.Do(func() {
		var disk *aptDiskCache
		if !m.DisableDiskCache {
			cacheDir := m.aptCacheDirectory()
			if cache, err := newAptDiskCache(cacheDir, m.CacheTTL); err == nil {
				disk = cache
			}
		}
		m.aptCache = newAptMirrorCache(m.CacheTTL, disk)
	})
	return m.aptCache
}

func (c *aptMirrorCache) getIndexPaths(repositoryURL string) ([]aptIndexPath, bool) {
	c.mu.RLock()
	entry, ok := c.indexPaths[repositoryURL]
	c.mu.RUnlock()

	if ok && time.Now().Before(entry.ExpiresAt) {
		paths := make([]aptIndexPath, len(entry.Paths))
		copy(paths, entry.Paths)
		return paths, true
	}

	if c.disk == nil {
		return nil, false
	}

	paths, ok := c.disk.getIndexPaths(repositoryURL)
	if !ok {
		return nil, false
	}

	c.mu.Lock()
	c.indexPaths[repositoryURL] = cachedAptIndexPaths{
		Paths:     append([]aptIndexPath(nil), paths...),
		ExpiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	return paths, true
}

func (c *aptMirrorCache) setIndexPaths(repositoryURL string, paths []aptIndexPath) {
	stored := make([]aptIndexPath, len(paths))
	copy(stored, paths)

	c.mu.Lock()
	c.indexPaths[repositoryURL] = cachedAptIndexPaths{
		Paths:     stored,
		ExpiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	if c.disk != nil {
		_ = c.disk.setIndexPaths(repositoryURL, stored)
	}
}

func aptPackageCacheKey(repositoryURL, packageName string) string {
	return repositoryURL + "\x00" + packageName
}

func (c *aptMirrorCache) getPackageVersion(repositoryURL, packageName string) (*AptPackageVersionData, bool) {
	key := aptPackageCacheKey(repositoryURL, packageName)

	c.mu.RLock()
	entry, ok := c.packageVersions[key]
	c.mu.RUnlock()

	if ok && time.Now().Before(entry.ExpiresAt) {
		result := entry.Result
		return &result, true
	}

	if c.disk == nil {
		return nil, false
	}

	result, ok := c.disk.getPackageVersion(repositoryURL, packageName)
	if !ok {
		return nil, false
	}

	c.mu.Lock()
	c.packageVersions[key] = cachedAptPackageVersion{
		Result:    *result,
		ExpiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	return result, true
}

func (c *aptMirrorCache) setPackageVersion(repositoryURL, packageName string, result *AptPackageVersionData) {
	if result == nil {
		return
	}

	key := aptPackageCacheKey(repositoryURL, packageName)

	c.mu.Lock()
	c.packageVersions[key] = cachedAptPackageVersion{
		Result:    *result,
		ExpiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	if c.disk != nil {
		_ = c.disk.setPackageVersion(repositoryURL, packageName, result)
	}
}

func (c *aptMirrorCache) getListFile(rawURL string) ([]byte, bool) {
	if c.disk == nil {
		return nil, false
	}
	return c.disk.getListFile(rawURL)
}

func (c *aptMirrorCache) getListFileMeta(rawURL string) *aptDiskListMeta {
	if c.disk == nil {
		return nil
	}
	return c.disk.getListFileMeta(rawURL)
}

func (c *aptMirrorCache) setListFile(rawURL string, data []byte, header http.Header) {
	if c.disk == nil {
		return
	}
	_ = c.disk.setListFile(rawURL, data, header)
}
