package pkg

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// npmPackageDoc builds a minimal npm registry document for a package.
func npmPackageDoc(name, latest string) map[string]any {
	return map[string]any{
		"name": name,
		"dist-tags": map[string]any{
			"latest": latest,
		},
		"versions": map[string]any{
			latest: map[string]any{"version": latest},
		},
	}
}

func TestNpmCheckPackage_Found(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prisma" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(npmPackageDoc("prisma", "5.22.0"))
	}))
	defer server.Close()

	service := NewNpmMirrorService()

	ok, info, err := service.CheckPackage(server.URL, "prisma", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected package to exist")
	}
	if info == nil || info.Version != "5.22.0" {
		t.Fatalf("expected latest 5.22.0, got %+v", info)
	}
}

func TestNpmCheckPackage_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewNpmMirrorService()

	ok, info, err := service.CheckPackage(server.URL, "definitely-not-a-real-package", false)
	if err == nil {
		t.Fatal("expected error for missing package")
	}
	if _, okType := err.(*PackageNotFoundError); !okType {
		t.Fatalf("expected PackageNotFoundError, got %T: %v", err, err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if info != nil {
		t.Fatal("expected nil info on not found")
	}
}

func TestNpmCheckStatus_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The status check probes either the search endpoint or a known package.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"objects":[],"total":0}`))
	}))
	defer server.Close()

	service := NewNpmMirrorService()

	alive, info, err := service.CheckStatus(server.URL, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alive {
		t.Fatal("expected mirror to be alive")
	}
	if info == nil || !info.Status || info.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status info: %+v", info)
	}
}

func TestNpmCheckStatus_Unreachable(t *testing.T) {
	// Use a reserved port with nothing listening.
	service := NewNpmMirrorService()
	service.HttpClient = &http.Client{Timeout: 500 * time.Millisecond}

	ok, _, err := service.CheckStatus("http://127.0.0.1:1/", false)
	if ok {
		t.Fatal("expected ok=false for unreachable mirror")
	}
	if err == nil {
		t.Fatal("expected error for unreachable mirror")
	}
	if _, okType := err.(*InvalidMirrorError); !okType {
		t.Fatalf("expected InvalidMirrorError, got %T: %v", err, err)
	}
}

func TestNpmCheckSpeed(t *testing.T) {
	payload := strings.Repeat("a", 2*1024*1024) // 2 MB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prisma" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, payload)
	}))
	defer server.Close()

	service := NewNpmMirrorService()

	mbps, info, err := service.CheckSpeed(server.URL, 5, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mbps <= 0 {
		t.Fatalf("expected positive speed, got %f", mbps)
	}
	if info == nil || info.SpeedMbps != mbps {
		t.Fatalf("expected info.SpeedMbps == %f, got %+v", mbps, info)
	}
}

func TestNpmCheckSpeed_Timeout(t *testing.T) {
	// Handler blocks until the client cancels the request (client timeout = 1s).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	service := NewNpmMirrorService()

	mbps, _, err := service.CheckSpeed(server.URL, 1, false, nil)
	if err == nil {
		t.Fatalf("expected timeout error, got speed %f", mbps)
	}
	if _, okType := err.(*TimeoutError); !okType {
		t.Fatalf("expected TimeoutError, got %T: %v", err, err)
	}
}

func TestGetSpeedRating(t *testing.T) {
	tests := []struct {
		speed float64
		want  string
	}{
		{25, "Excellent"},
		{15, "Good"},
		{7, "Average"},
		{1, "Slow"},
	}
	for _, tt := range tests {
		if got := getSpeedRating(tt.speed); got != tt.want {
			t.Errorf("getSpeedRating(%v) = %q, want %q", tt.speed, got, tt.want)
		}
	}
}
