package apt

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/cache"
	"github.com/MiravaOrg/mirava-core/internal/core"
)

// testRepo builds an httptest server serving a minimal Debian-style mirror:
// dists/suite/InRelease + main/binary-amd64/Packages.gz, with request
// counters for cache-behavior assertions.
type testRepo struct {
	srv      *httptest.Server
	requests map[string]*atomic.Int64
	body     []byte // gzipped packages payload
}

func newTestRepo(t *testing.T, packages string) *testRepo {
	t.Helper()

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(packages))
	_ = zw.Close()
	pkgGZ := gz.Bytes()

	// SHA256 of the gz blob.
	sum := sha256HexStr(pkgGZ)

	inrelease := strings.Join([]string{
		"Origin: Ubuntu",
		"Suite: noble",
		"Codename: noble",
		"Components: main universe",
		"Architectures: amd64 arm64",
		"Date: Wed, 26 Nov 2026 00:00:00 UTC",
		"",
		"SHA256:",
		" " + sum + " " + fmt.Sprint(len(pkgGZ)) + " main/binary-amd64/Packages.gz",
		" " + sum + " " + fmt.Sprint(len(pkgGZ)) + " universe/binary-amd64/Packages.gz",
		"",
	}, "\n")

	r := &testRepo{requests: map[string]*atomic.Int64{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/dists/noble/InRelease", func(w http.ResponseWriter, req *http.Request) {
		r.count("inrelease").Add(1)
		_, _ = w.Write([]byte(inrelease))
	})
	mux.HandleFunc("/dists/noble/main/binary-amd64/Packages.gz", func(w http.ResponseWriter, req *http.Request) {
		r.count("main.gz").Add(1)
		_, _ = w.Write(pkgGZ)
	})
	mux.HandleFunc("/dists/noble/universe/binary-amd64/Packages.gz", func(w http.ResponseWriter, req *http.Request) {
		r.count("universe.gz").Add(1)
		_, _ = w.Write(pkgGZ)
	})
	mux.HandleFunc("/ls-lR.gz", func(w http.ResponseWriter, req *http.Request) {
		r.count("ls-lR").Add(1)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 3<<20))
	})
	// catch-all 404s
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		r.count("404").Add(1)
		http.NotFound(w, req)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *testRepo) count(k string) *atomic.Int64 {
	mu.Lock()
	defer mu.Unlock()
	if r.requests[k] == nil {
		r.requests[k] = &atomic.Int64{}
	}
	return r.requests[k]
}

var mu sync.Mutex

func sha256Bytes(b []byte) []byte {
	sum := sha256.Sum256(b)
	return []byte(hex.EncodeToString(sum[:]))
}

// sha256HexStr returns the hex digest as a string, matching what a Release
// file's SHA256 section advertises.
func sha256HexStr(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newTestService(t *testing.T, store cache.Store) *Service {
	t.Helper()
	return New(core.Config{Store: store, HTTPClient: &http.Client{Timeout: 5 * time.Second}})
}

func TestPackageFound(t *testing.T) {
	repo := newTestRepo(t, `Package: curl
Version: 8.5.0-2ubuntu2
Filename: pool/main/c/curl/curl_8.5.0-2ubuntu2_amd64.deb
SHA256: aaaa
Size: 400000

Package: zstd
Version: 1.5.5
Filename: pool/main/z/zstd/zstd_1.5.5_amd64.deb
SHA256: bbbb
Size: 500000
`)
	svc := newTestService(t, cache.NewLRU(0, 0))

	res, err := svc.Package(context.Background(), repo.srv.URL, core.PackageQuery{Name: "curl"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if !res.Found {
		t.Fatalf("expected found, got %+v", res)
	}
	if res.Version != "8.5.0-2ubuntu2" {
		t.Fatalf("version = %q", res.Version)
	}
	detail, ok := res.Detail.(PackageDetail)
	if !ok {
		t.Fatalf("detail type %T", res.Detail)
	}
	if detail.Filename != "pool/main/c/curl/curl_8.5.0-2ubuntu2_amd64.deb" {
		t.Fatalf("filename = %q", detail.Filename)
	}
}

func TestPackageNotFound(t *testing.T) {
	repo := newTestRepo(t, "Package: curl\nVersion: 1\n")
	svc := newTestService(t, cache.NewLRU(0, 0))

	res, err := svc.Package(context.Background(), repo.srv.URL, core.PackageQuery{Name: "nonexistent"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if res.Found {
		t.Fatalf("expected not found")
	}
}

func TestResolveURL(t *testing.T) {
	repo := newTestRepo(t, "Package: curl\nVersion: 1.0\nFilename: pool/main/c/curl/curl_1.0_amd64.deb\nSize: 400000\n")
	svc := newTestService(t, cache.NewLRU(0, 0))

	res, err := svc.Resolve(context.Background(), repo.srv.URL, core.PackageQuery{Name: "curl"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := repo.srv.URL + "/pool/main/c/curl/curl_1.0_amd64.deb"
	if res.URL != want {
		t.Fatalf("url = %q, want %q", res.URL, want)
	}
	if res.Size != 400000 {
		t.Fatalf("size = %d", res.Size)
	}
}

func TestSecondMirrorSharesBlob(t *testing.T) {
	repo := newTestRepo(t, "Package: curl\nVersion: 1.0\n")
	store := cache.NewLRU(0, 0)
	svc := newTestService(t, store)

	ctx := context.Background()
	if _, err := svc.Package(ctx, repo.srv.URL, core.PackageQuery{Name: "curl"}); err != nil {
		t.Fatalf("first Package: %v", err)
	}
	first := repo.count("main.gz").Load()

	// Same store, different service (e.g. the Ubuntu-flavored one): the
	// blob must already be cached by SHA256.
	svc2 := New(core.Config{Store: store, HTTPClient: svc.client()})
	if _, err := svc2.Package(ctx, repo.srv.URL, core.PackageQuery{Name: "curl"}); err != nil {
		t.Fatalf("second Package: %v", err)
	}
	if got := repo.count("main.gz").Load(); got != first {
		t.Fatalf("blob re-downloaded: first=%d now=%d", first, got)
	}
}

func TestSingleflightDeduplicates(t *testing.T) {
	repo := newTestRepo(t, "Package: curl\nVersion: 1.0\n")
	store := cache.NewLRU(0, 0)
	svc := newTestService(t, store)

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Package(ctx, repo.srv.URL, core.PackageQuery{Name: "curl"}); err != nil {
				t.Errorf("Package: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := repo.count("main.gz").Load(); got != 1 {
		t.Fatalf("expected exactly 1 index download, got %d", got)
	}
}

func TestStatus(t *testing.T) {
	repo := newTestRepo(t, "Package: curl\nVersion: 1.0\n")
	svc := newTestService(t, cache.NewLRU(0, 0))

	res, err := svc.Status(context.Background(), repo.srv.URL)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected OK, got %+v", res)
	}
	if !strings.Contains(res.WorkingPath, "/dists/") {
		t.Fatalf("working path = %q", res.WorkingPath)
	}

	res2, err := svc.Status(context.Background(), "http://127.0.0.1:1/")
	if err == nil {
		t.Fatalf("expected error for dead mirror")
	}
	_ = res2
}

func TestSpeed(t *testing.T) {
	repo := newTestRepo(t, "Package: curl\nVersion: 1.0\n")
	svc := newTestService(t, cache.NewLRU(0, 0))

	res, err := svc.Speed(context.Background(), repo.srv.URL, core.SpeedOptions{
		Duration: 2 * time.Second,
		MinBytes: 1,
	})
	if err != nil {
		t.Fatalf("Speed: %v", err)
	}
	if !res.OK || res.SpeedMBps <= 0 {
		t.Fatalf("bad speed result %+v", res)
	}
}

func TestURLJoinNoDoubleSlash(t *testing.T) {
	repo := newTestRepo(t, "Package: curl\nVersion: 1.0\nFilename: pool/main/c/curl/curl_1.0_amd64.deb\n")
	svc := newTestService(t, cache.NewLRU(0, 0))
	res, err := svc.Resolve(context.Background(), repo.srv.URL+"/", core.PackageQuery{Name: "curl"})
	if err != nil {
		t.Fatalf("Resolve with trailing slash: %v", err)
	}
	if strings.Contains(res.URL, "//pool") {
		t.Fatalf("double slash in %q", res.URL)
	}
}

func TestParseRelease(t *testing.T) {
	meta := parseRelease("Origin: Debian\nCodename: trixie\nSuite: stable\nComponents: main contrib\nArchitectures: amd64 arm64\n\nSHA256:\n abc 123 main/binary-amd64/Packages.gz\n")
	if meta.Codename != "trixie" || meta.Suite != "stable" {
		t.Fatalf("meta = %+v", meta)
	}
	if len(meta.Components) != 2 || len(meta.Architectures) != 2 {
		t.Fatalf("meta = %+v", meta)
	}
	sha, ok := meta.Checksums["main/binary-amd64/Packages.gz"]
	if !ok || sha != "abc" {
		t.Fatalf("checksums = %v", meta.Checksums)
	}
}

func TestDebVersionGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.3", "1.2.2", true},
		{"1.2.3-1", "1.2.3", true},
		{"1.2.3~rc1", "1.2.3", false},
		{"2.0", "1.9.9", true},
	}
	for _, c := range cases {
		if got := debVersionGreater(c.a, c.b); got != c.want {
			t.Errorf("debVersionGreater(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestJSONResultShape(t *testing.T) {
	// The website serializes results; ensure the envelope marshals.
	repo := newTestRepo(t, "Package: curl\nVersion: 1.0\n")
	svc := newTestService(t, cache.NewLRU(0, 0))
	res, err := svc.Package(context.Background(), repo.srv.URL, core.PackageQuery{Name: "curl"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"found":true`) {
		t.Fatalf("json = %s", data)
	}
}
