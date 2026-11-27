package pkg

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDockerCheckStatus_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
			return
		}
		// Registries commonly answer /v2/ with 200 or 401; both count as alive.
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := NewDockerMirrorService()

	alive, info, err := service.CheckStatus(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alive {
		t.Fatal("expected registry to be alive")
	}
	if info == nil || info.Status != "active" || info.ApiVersion != "v2" {
		t.Fatalf("unexpected status info: %+v", info)
	}
}

func TestDockerCheckStatus_UnauthorizedStillAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	service := NewDockerMirrorService()

	alive, _, err := service.CheckStatus(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alive {
		t.Fatal("expected 401 on /v2/ to count as alive")
	}
}

func TestDockerCheckStatus_NotRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewDockerMirrorService()

	alive, _, err := service.CheckStatus(server.URL)
	if alive {
		t.Fatal("expected alive=false for non-registry")
	}
	if _, okType := err.(*InvalidMirrorError); !okType {
		t.Fatalf("expected InvalidMirrorError, got %T: %v", err, err)
	}
}

func TestDockerCheckPackage_Found(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/library/alpine/tags/list" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "library/alpine",
			"tags": []string{"3.20", "3.21", "latest"},
		})
	}))
	defer server.Close()

	service := NewDockerMirrorService()

	ok, info, err := service.CheckPackage(server.URL, "library/alpine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected image to exist")
	}
	if info == nil || info.TotalTags != 3 {
		t.Fatalf("expected 3 tags, got %+v", info)
	}
}

func TestDockerCheckPackage_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewDockerMirrorService()

	ok, info, err := service.CheckPackage(server.URL, "definitely/not-an-image")
	if err == nil {
		t.Fatal("expected error for missing image")
	}
	if _, okType := err.(*PackageNotFoundError); !okType {
		t.Fatalf("expected PackageNotFoundError, got %T: %v", err, err)
	}
	if ok || info != nil {
		t.Fatal("expected ok=false and nil info")
	}
}

func TestDockerGetSpeedRating(t *testing.T) {
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
		if got := NewDockerMirrorService().getDockerSpeedRating(tt.speed); got != tt.want {
			t.Errorf("getDockerSpeedRating(%v) = %q, want %q", tt.speed, got, tt.want)
		}
	}
}
