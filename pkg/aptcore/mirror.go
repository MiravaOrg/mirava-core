// Package aptcore implements APT mirror internals. Use pkg.AptMirrorService instead.
package aptcore

import (
	"net/http"
	"sync"
	"time"
)

// Mirror holds HTTP and cache state for apt index lookups.
type Mirror struct {
	HttpClient       *http.Client
	CacheTTL         time.Duration
	CacheDir         string
	DisableDiskCache bool

	cacheOnce sync.Once
	aptCache  *mirrorCache
}

// NewMirror creates an apt mirror backend with the given settings.
func NewMirror(httpClient *http.Client) *Mirror {
	return &Mirror{HttpClient: httpClient}
}

// CacheDirectory returns the directory used for on-disk apt cache files.
func (m *Mirror) CacheDirectory() string {
	return m.cacheDirectory()
}

func (m *Mirror) cacheDirectory() string {
	if m.CacheDir != "" {
		return m.CacheDir
	}
	return defaultCacheDirectory()
}

func (m *Mirror) mirrorCache() *mirrorCache {
	m.cacheOnce.Do(func() {
		root := ""
		if !m.DisableDiskCache {
			root = m.cacheDirectory()
		}

		cache, err := newMirrorCache(m.CacheTTL, root)
		if err != nil {
			cache, _ = newMirrorCache(m.CacheTTL, "")
		}
		m.aptCache = cache
	})
	return m.aptCache
}

func (m *Mirror) httpClient() *http.Client {
	if m.HttpClient != nil {
		return m.HttpClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
