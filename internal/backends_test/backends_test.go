// Package backends_test contains httptest-fixture tests for the simpler
// registry backends (npm, pypi, cargo, gomod, composer, pacman, docker).
package backends_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/cargo"
	"github.com/MiravaOrg/mirava-core/internal/composer"
	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/docker"
	"github.com/MiravaOrg/mirava-core/internal/gomod"
	"github.com/MiravaOrg/mirava-core/internal/npm"
	"github.com/MiravaOrg/mirava-core/internal/pypi"
)

func cfg() core.Config {
	return core.Config{HTTPClient: &http.Client{Timeout: 5 * time.Second}}
}

func TestNpmPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/react" {
			doc := map[string]any{
				"dist-tags": map[string]string{"latest": "18.2.0"},
				"versions": map[string]any{
					"18.2.0": map[string]any{"dist": map[string]any{"tarball": srv0(r) + "/react/-/react-18.2.0.tgz", "shasum": "abc"}},
				},
			}
			_ = json.NewEncoder(w).Encode(doc)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := npm.New(cfg())
	res, err := s.Package(context.Background(), srv.URL, core.PackageQuery{Name: "react"})
	if err != nil || !res.Found || res.Version != "18.2.0" {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	dl, err := s.Resolve(context.Background(), srv.URL, core.PackageQuery{Name: "react"})
	if err != nil || dl.URL == "" || dl.SHA256 != "abc" {
		t.Fatalf("dl=%+v err=%v", dl, err)
	}

	res2, err := s.Package(context.Background(), srv.URL, core.PackageQuery{Name: "missing"})
	if err != nil || res2.Found {
		t.Fatalf("missing package should be found=false, err=%v", err)
	}
}

func srv0(r *http.Request) string { return "http://" + r.Host }

func TestPypiPackageAndResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/simple/"):
			_, _ = w.Write([]byte("<a href='/simple/requests/'>requests</a>"))
		case strings.Contains(r.URL.Path, "/simple/requests"):
			body := `<a href="../../packages/requests-2.31.0-py3-none-any.whl#sha256=deadbeef">requests-2.31.0</a><br/>` +
				`<a href="../../packages/requests-2.30.0.tar.gz">requests-2.30.0</a>`
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := pypi.New(cfg())
	res, err := s.Package(context.Background(), srv.URL, core.PackageQuery{Name: "requests"})
	if err != nil || !res.Found {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if res.Version != "2.31.0" {
		t.Fatalf("version=%q", res.Version)
	}

	dl, err := s.Resolve(context.Background(), srv.URL, core.PackageQuery{Name: "requests"})
	if err != nil || !strings.HasSuffix(dl.URL, "requests-2.31.0-py3-none-any.whl") {
		t.Fatalf("dl=%+v err=%v", dl, err)
	}
	if dl.SHA256 != "deadbeef" {
		t.Fatalf("sha=%q", dl.SHA256)
	}
}

func TestCargoPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/se/rd/serde":
			_, _ = w.Write([]byte(`{"name":"serde","vers":"1.0.200","yanked":false,"cksum":"x"}` + "\n" +
				`{"name":"serde","vers":"1.0.199","yanked":false,"cksum":"y"}` + "\n"))
		case "/config.json":
			_, _ = w.Write([]byte(`{"dl":"https://crates.io/api/v1/crates"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := cargo.New(cfg())
	res, err := s.Package(context.Background(), srv.URL, core.PackageQuery{Name: "serde"})
	if err != nil || !res.Found || res.Version != "1.0.200" {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	dl, err := s.Resolve(context.Background(), srv.URL, core.PackageQuery{Name: "serde"})
	if err != nil || dl.URL == "" || !strings.Contains(dl.URL, "serde") {
		t.Fatalf("dl=%+v err=%v", dl, err)
	}
}

func TestGoPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "@v/list"):
			_, _ = w.Write([]byte("v0.16.0\nv0.17.0\n"))
		case strings.HasSuffix(r.URL.Path, "@v/v0.17.0.info"):
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": "v0.17.0", "Time": "2024-01-01T00:00:00Z"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := gomod.New(cfg())
	res, err := s.Package(context.Background(), srv.URL, core.PackageQuery{Name: "golang.org/x/mod"})
	if err != nil || !res.Found || res.Version != "v0.17.0" {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	dl, err := s.Resolve(context.Background(), srv.URL, core.PackageQuery{Name: "golang.org/x/mod"})
	if err != nil || !strings.HasSuffix(dl.URL, "/@v/v0.17.0.zip") {
		t.Fatalf("dl=%+v err=%v", dl, err)
	}
}

func TestComposerPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p2/laravel/framework.json" {
			doc := map[string]any{
				"packages": map[string]any{
					"laravel/framework": []map[string]any{
						{"version": "v11.0.0", "dist": map[string]any{"url": "https://example.com/laravel.zip", "type": "zip", "shasum": "f00d"}},
						{"version": "v10.0.0", "dist": map[string]any{"url": "https://example.com/laravel10.zip"}},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(doc)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := composer.New(cfg())
	res, err := s.Package(context.Background(), srv.URL, core.PackageQuery{Name: "laravel/framework"})
	if err != nil || !res.Found || res.Version != "v11.0.0" {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	dl, err := s.Resolve(context.Background(), srv.URL, core.PackageQuery{Name: "laravel/framework"})
	if err != nil || dl.URL != "https://example.com/laravel.zip" {
		t.Fatalf("dl=%+v err=%v", dl, err)
	}
}

func TestDockerStatusAndPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tags/list") {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "library/alpine", "tags": []string{"3.19", "latest"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := docker.New(cfg())
	res, err := s.Status(context.Background(), srv.URL)
	if err != nil || !res.OK {
		t.Fatalf("status=%+v err=%v", res, err)
	}

	pkg, err := s.Package(context.Background(), srv.URL, core.PackageQuery{Name: "alpine"})
	if err != nil || !pkg.Found || pkg.Detail == nil {
		t.Fatalf("pkg=%+v err=%v", pkg, err)
	}
}

func TestDockerResolveWithToken(t *testing.T) {
	tokenIssued := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/v2/":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srvURL(r)+`/token",service="registry",scope="repository:library/alpine:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/token":
			tokenIssued = true
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok123"})
		case strings.HasSuffix(r.URL.Path, "/manifests/latest"):
			if auth != "Bearer tok123" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srvURL(r)+`/token",service="registry",scope="repository:library/alpine:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"sha256:cfg","size":100},"layers":[{"digest":"sha256:layer0","size":1024}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := docker.New(cfg())
	dl, err := s.Resolve(context.Background(), srv.URL, core.PackageQuery{Name: "alpine"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !tokenIssued {
		t.Fatal("expected token flow to run")
	}
	if dl.Headers == nil || dl.Headers["Authorization"] != "Bearer tok123" {
		t.Fatalf("headers=%v", dl.Headers)
	}
	if !strings.Contains(dl.URL, "/blobs/sha256:layer0") {
		t.Fatalf("url=%q", dl.URL)
	}
}

func srvURL(r *http.Request) string { return "http://" + r.Host }

func TestStatusDeadMirrorTimesOut(t *testing.T) {
	// A server that never responds: ctx deadline must produce an error
	// promptly rather than hanging.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.Close()

	s := npm.New(cfg())
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Status(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("status took %s; ctx deadline not honored", time.Since(start))
	}
	if !strings.Contains(fmt.Sprint(err), "context") && !strings.Contains(fmt.Sprint(err), "timed out") {
		t.Logf("err = %v (should wrap ctx error)", err)
	}
}
