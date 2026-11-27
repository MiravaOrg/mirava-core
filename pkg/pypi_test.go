package pkg

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPypiCheckPackage_Found(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/simple/requests/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Links for requests</title></head><body>
<h1>Links for requests</h1>
<a href="../../packages/requests-2.31.0.tar.gz#sha256=abc">requests-2.31.0.tar.gz</a><br/>
<a href="../../packages/requests-2.32.3-py3-none-any.whl#sha256=def">requests-2.32.3-py3-none-any.whl</a><br/>
</body></html>`))
	}))
	defer server.Close()

	service := NewPyPIMirrorService()

	ok, info, err := service.CheckPackage(server.URL, "requests", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected package to exist")
	}
	if info == nil || info.Version != "2.32.3" {
		t.Fatalf("expected latest 2.32.3, got %+v", info)
	}
	if info.VersionsCount != 2 {
		t.Fatalf("expected 2 versions, got %d", info.VersionsCount)
	}
}

func TestPypiCheckPackage_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewPyPIMirrorService()

	ok, info, err := service.CheckPackage(server.URL, "definitely-not-a-real-package", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for missing package")
	}
	if info != nil {
		t.Fatal("expected nil info on not found")
	}
}

func TestPypiCheckStatus_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/simple/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := NewPyPIMirrorService()

	alive, info, err := service.CheckStatus(server.URL, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alive {
		t.Fatal("expected mirror to be alive")
	}
	if info == nil || info.Status != "active" || info.TestedPath != "/simple/" {
		t.Fatalf("unexpected status info: %+v", info)
	}
}

func TestPypiCheckStatus_Fail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewPyPIMirrorService()

	alive, _, err := service.CheckStatus(server.URL, false)
	if alive {
		t.Fatal("expected alive=false for 404 mirror")
	}
	if err == nil {
		t.Fatal("expected error for unhealthy mirror")
	}
	if _, okType := err.(*HttpRequestError); !okType {
		t.Fatalf("expected HttpRequestError, got %T: %v", err, err)
	}
}
