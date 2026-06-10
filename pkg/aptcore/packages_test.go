package aptcore

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gzipBytes(data string) []byte {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, _ = writer.Write([]byte(data))
	_ = writer.Close()
	return buf.Bytes()
}

const testReleaseBody = "Components: main\nArchitectures: amd64\n"

func TestFindLatestPackageInIndex(t *testing.T) {
	index := strings.Join([]string{
		"Package: nginx",
		"Version: 1.24.0-2ubuntu7",
		"Filename: pool/main/n/nginx/nginx_1.24.0-2ubuntu7_amd64.deb",
		"",
		"Package: nginx",
		"Version: 1.24.0-2ubuntu7.4",
		"Filename: pool/main/n/nginx/nginx_1.24.0-2ubuntu7.4_amd64.deb",
		"",
		"Package: curl",
		"Version: 8.5.0-2ubuntu10",
		"",
	}, "\n")

	candidate, err := findLatestPackageInIndex(strings.NewReader(index), "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if candidate == nil {
		t.Fatal("expected candidate")
	}
	if candidate.Version != "1.24.0-2ubuntu7.4" {
		t.Fatalf("expected latest nginx version, got %q", candidate.Version)
	}
}

func TestLookupPackageVersion(t *testing.T) {
	mainIndex := strings.Join([]string{
		"Package: nginx",
		"Version: 1.24.0-2ubuntu7",
		"Filename: pool/main/n/nginx/nginx_1.24.0-2ubuntu7_amd64.deb",
		"",
	}, "\n")

	securityIndex := strings.Join([]string{
		"Package: nginx",
		"Version: 1.24.0-2ubuntu7.4",
		"Filename: pool/main/n/nginx/nginx_1.24.0-2ubuntu7.4_amd64.deb",
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dists/noble/Release", "/dists/noble-security/Release":
			w.Write([]byte(testReleaseBody))
		case "/dists/noble/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(mainIndex))
		case "/dists/noble-security/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(securityIndex))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mirror := NewMirror(nil)
	mirror.DisableDiskCache = true
	result, err := mirror.LookupPackageVersion(server.URL, "nginx", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Version != "1.24.0-2ubuntu7.4" {
		t.Fatalf("expected latest version 1.24.0-2ubuntu7.4, got %q", result.Version)
	}
	if result.Suite != "noble-security" {
		t.Fatalf("expected suite noble-security, got %q", result.Suite)
	}
	if result.Arch != "amd64" {
		t.Fatalf("expected arch amd64, got %q", result.Arch)
	}
}

func TestLookupPackageVersionNotFound(t *testing.T) {
	index := "Package: curl\nVersion: 8.5.0-2ubuntu10\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dists/noble/Release":
			w.Write([]byte(testReleaseBody))
		case "/dists/noble/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(index))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mirror := NewMirror(nil)
	mirror.DisableDiskCache = true
	_, err := mirror.LookupPackageVersion(server.URL, "nginx", nil)
	if err == nil {
		t.Fatal("expected error for missing package")
	}

	if _, ok := err.(*PackageNotFoundError); !ok {
		t.Fatalf("expected PackageNotFoundError, got %T: %v", err, err)
	}
}

func TestLookupPackageVersionUsesCache(t *testing.T) {
	index := strings.Join([]string{
		"Package: nginx",
		"Version: 1.24.0-2ubuntu7.4",
		"Filename: pool/main/n/nginx/nginx_1.24.0-2ubuntu7.4_amd64.deb",
		"",
	}, "\n")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/dists/noble/Release":
			w.Write([]byte(testReleaseBody))
		case "/dists/noble/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(index))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mirror := NewMirror(nil)
	mirror.DisableDiskCache = true

	first, err := mirror.LookupPackageVersion(server.URL, "nginx", nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	firstRequests := requests

	second, err := mirror.LookupPackageVersion(server.URL, "nginx", nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if requests != firstRequests {
		t.Fatalf("expected cache hit with %d requests, got %d total", firstRequests, requests)
	}
	if second.Version != first.Version {
		t.Fatalf("cached version mismatch: %q vs %q", second.Version, first.Version)
	}
}

func TestLookupPackageVersionUsesDiskCache(t *testing.T) {
	index := strings.Join([]string{
		"Package: nginx",
		"Version: 1.24.0-2ubuntu7.4",
		"Filename: pool/main/n/nginx/nginx_1.24.0-2ubuntu7.4_amd64.deb",
		"",
	}, "\n")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/dists/noble/Release":
			w.Write([]byte(testReleaseBody))
		case "/dists/noble/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(index))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()

	firstMirror := NewMirror(nil)
	firstMirror.CacheDir = cacheDir
	_, err := firstMirror.LookupPackageVersion(server.URL, "nginx", nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	firstRequests := requests

	secondMirror := NewMirror(nil)
	secondMirror.CacheDir = cacheDir
	result, err := secondMirror.LookupPackageVersion(server.URL, "nginx", nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if requests != firstRequests {
		t.Fatalf("expected disk cache hit with %d requests, got %d total", firstRequests, requests)
	}
	if result.Version != "1.24.0-2ubuntu7.4" {
		t.Fatalf("unexpected version %q", result.Version)
	}
}

func TestFetchMirrorFileRevalidatesExpiredCache(t *testing.T) {
	index := gzipBytes("Package: nginx\nVersion: 1.0-1\n\n")
	etag := `"test-etag"`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	cache, err := newMirrorCache(time.Hour, cacheDir)
	if err != nil {
		t.Fatalf("newMirrorCache: %v", err)
	}

	rawURL := server.URL + "/dists/noble/main/binary-amd64/Packages.gz"
	cache.setListFile(rawURL, index, http.Header{
		"Etag": {etag},
	})

	_, metaPath := cache.listFilePaths(rawURL)
	_ = cache.writeJSON(metaPath, aptListMeta{
		ExpiresAt: time.Now().Add(-time.Hour),
		ETag:      etag,
	})

	mirror := NewMirror(server.Client())
	mirror.CacheDir = cacheDir

	body, err := mirror.FetchMirrorFile(server.Client(), rawURL)
	if err != nil {
		t.Fatalf("fetchMirrorFile: %v", err)
	}
	defer body.Close()

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, index) {
		t.Fatalf("unexpected body: %q", got)
	}

	if data, ok := cache.getListFile(rawURL); !ok || !bytes.Equal(data, index) {
		t.Fatal("expected cache entry to be refreshed after revalidation")
	}
}

func TestDebVersionGreaterThan(t *testing.T) {
	cases := []struct {
		left, right string
		want        bool
	}{
		{"1.24.0-2ubuntu7.4", "1.24.0-2ubuntu7", true},
		{"1.24.0-2ubuntu7", "1.24.0-2ubuntu7.4", false},
		{"2:1.0-1", "1:2.0-1", true},
	}

	for _, tc := range cases {
		if got := debVersionGreaterThan(tc.left, tc.right); got != tc.want {
			t.Fatalf("debVersionGreaterThan(%q, %q) = %v, want %v", tc.left, tc.right, got, tc.want)
		}
	}
}
