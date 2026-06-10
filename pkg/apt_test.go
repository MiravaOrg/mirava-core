package pkg

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckPackage(t *testing.T) {
	mainIndex := strings.Join([]string{
		"Package: nginx",
		"Version: 1.24.0-2ubuntu7.4",
		"Filename: pool/main/n/nginx/nginx_1.24.0-2ubuntu7.4_amd64.deb",
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dists/noble/Release":
			w.Write([]byte("Components: main\nArchitectures: amd64\n"))
		case "/dists/noble/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(mainIndex))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewAptMirrorService()
	service.DisableDiskCache = true

	ok, info, err := service.CheckPackage(server.URL, "nginx", false, AptCheckPackageParams{
		Release:   "noble",
		Component: "main",
		Arch:      "amd64",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected package to exist")
	}
	if info.Version != "1.24.0-2ubuntu7.4" {
		t.Fatalf("unexpected version %q", info.Version)
	}
	if info.FoundPath == "" {
		t.Fatal("expected found path")
	}
}

func TestCheckPackageNotFound(t *testing.T) {
	mainIndex := strings.Join([]string{
		"Package: curl",
		"Version: 8.5.0-2ubuntu10",
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dists/noble/Release":
			w.Write([]byte("Components: main\nArchitectures: amd64\n"))
		case "/dists/noble/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(mainIndex))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewAptMirrorService()
	service.DisableDiskCache = true

	ok, info, err := service.CheckPackage(server.URL, "nginx", false, AptCheckPackageParams{
		Release:   "noble",
		Component: "main",
		Arch:      "amd64",
	})
	if err == nil {
		t.Fatal("expected error for missing package")
	}
	if _, ok := err.(*PackageNotFoundError); !ok {
		t.Fatalf("expected PackageNotFoundError, got %T: %v", err, err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if info != nil {
		t.Fatal("expected nil info on not found")
	}
}

func TestCheckPackageEmptyRelease(t *testing.T) {
	jammyIndex := strings.Join([]string{
		"Package: nginx",
		"Version: 1.18.0-6ubuntu14.7",
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dists/noble/Release", "/dists/jammy/Release", "/dists/focal/Release":
			w.Write([]byte("Components: main\nArchitectures: amd64\n"))
		case "/dists/jammy/main/binary-amd64/Packages.gz":
			w.Write(gzipBytes(jammyIndex))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewAptMirrorService()
	service.DisableDiskCache = true

	ok, info, err := service.CheckPackage(server.URL, "nginx", false, AptCheckPackageParams{
		Component: "main",
		Arch:      "amd64",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || info.Release != "jammy" {
		t.Fatalf("expected nginx on jammy, got ok=%v info=%+v", ok, info)
	}
}

func gzipBytes(data string) []byte {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, _ = writer.Write([]byte(data))
	_ = writer.Close()
	return buf.Bytes()
}
