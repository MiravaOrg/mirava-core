package backends_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MiravaOrg/mirava-core/internal/cargo"
	"github.com/MiravaOrg/mirava-core/internal/composer"
	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/gomod"
	"github.com/MiravaOrg/mirava-core/internal/npm"
	"github.com/MiravaOrg/mirava-core/internal/pypi"
)

// SPA server answers every path with HTML 200 — mirrors like kargadan.
func TestStatusRejectsHTMLCatchAll(t *testing.T) {
	spa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head></head><body>landing</body></html>"))
	}))
	defer spa.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := core.Config{HTTPClient: &http.Client{Timeout: 3 * time.Second}}

	// go: /@v/list 200 with HTML must NOT be OK.
	if res, err := gomod.New(cfg).Status(ctx, spa.URL); err == nil && res.OK {
		t.Errorf("gomod Status accepted HTML page: %+v", res)
	}
	if res, err := npm.New(cfg).Status(ctx, spa.URL); err == nil && res.OK {
		t.Errorf("npm Status accepted HTML page: %+v", res)
	}
	if res, err := pypi.New(cfg).Status(ctx, spa.URL); err == nil && res.OK {
		t.Errorf("pypi Status accepted HTML page: %+v", res)
	}
	if res, err := cargo.New(cfg).Status(ctx, spa.URL); err == nil && res.OK {
		t.Errorf("cargo Status accepted HTML page: %+v", res)
	}
	if res, err := composer.New(cfg).Status(ctx, spa.URL); err == nil && res.OK {
		t.Errorf("composer Status accepted HTML page: %+v", res)
	}
}
