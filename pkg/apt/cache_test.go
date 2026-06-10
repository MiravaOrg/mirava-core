package apt

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAptCacheListFilePaths(t *testing.T) {
	cache := &mirrorCache{root: "/cache"}

	dataPath, metaPath := cache.listFilePaths(
		"https://mirror.example.com/ubuntu/dists/noble/main/binary-amd64/Packages.gz",
	)

	wantPrefix := filepath.Join("/cache", "lists", "mirror.example.com", "ubuntu", "dists")
	if !strings.HasPrefix(dataPath, wantPrefix) {
		t.Fatalf("unexpected data path: %s", dataPath)
	}
	if !strings.HasSuffix(dataPath, filepath.Join("binary-amd64", "Packages.gz")) {
		t.Fatalf("unexpected data path suffix: %s", dataPath)
	}
	if metaPath != dataPath+".meta.json" {
		t.Fatalf("unexpected meta path: %s", metaPath)
	}
}

func TestDefaultAptCacheDirectoryUsesUserCacheDir(t *testing.T) {
	dir := defaultCacheDirectory()
	if !strings.Contains(dir, filepath.Join("mirava-core", "apt")) {
		t.Fatalf("unexpected cache dir: %s", dir)
	}
}
