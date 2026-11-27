//go:build integration

// Live-mirror integration tests. Run with:
//
//	go test -tags=integration ./...
package mirava_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	mirava "github.com/MiravaOrg/mirava-core"
)

func liveClient() *mirava.Mirava {
	return mirava.New(mirava.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}))
}

func TestIntegrationApt(t *testing.T) {
	m := liveClient()
	ctx := context.Background()

	st, err := m.Apt.Status(ctx, "https://archive.ubuntu.com/ubuntu/")
	if err != nil || !st.OK {
		t.Fatalf("status: ok=%v err=%v", st.OK, err)
	}

	pkg, err := m.Apt.Package(ctx, "https://archive.ubuntu.com/ubuntu/", mirava.PackageQuery{Name: "curl"})
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	if !pkg.Found {
		t.Fatalf("curl not found")
	}
	t.Logf("apt curl: version=%s suite=%s", pkg.Version, pkg.Detail)

	dl, err := m.Apt.Resolve(ctx, "https://archive.ubuntu.com/ubuntu/", mirava.PackageQuery{Name: "curl"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(dl.URL, ".deb") {
		t.Fatalf("url=%q", dl.URL)
	}
	t.Logf("apt resolve: %s (size=%d)", dl.URL, dl.Size)
}

func TestIntegrationAptDebian(t *testing.T) {
	m := liveClient()
	st, err := m.AptDebian.Status(context.Background(), "https://deb.debian.org/debian/")
	if err != nil || !st.OK {
		t.Fatalf("debian status: ok=%v err=%v", st.OK, err)
	}
}

func TestIntegrationNpm(t *testing.T) {
	m := liveClient()
	ctx := context.Background()
	st, err := m.Npm.Status(ctx, "https://registry.npmjs.org/")
	if err != nil || !st.OK {
		t.Fatalf("npm status: ok=%v err=%v", st.OK, err)
	}
	pkg, err := m.Npm.Package(ctx, "https://registry.npmjs.org", mirava.PackageQuery{Name: "react"})
	if err != nil || !pkg.Found {
		t.Fatalf("npm react: found=%v err=%v", pkg.Found, err)
	}
	dl, err := m.Npm.Resolve(ctx, "https://registry.npmjs.org", mirava.PackageQuery{Name: "react"})
	if err != nil || !strings.Contains(dl.URL, ".tgz") {
		t.Fatalf("npm resolve: %q err=%v", dl.URL, err)
	}
}

func TestIntegrationPypi(t *testing.T) {
	m := liveClient()
	ctx := context.Background()
	pkg, err := m.PyPi.Package(ctx, "https://pypi.org", mirava.PackageQuery{Name: "requests"})
	if err != nil || !pkg.Found {
		t.Fatalf("pypi requests: found=%v err=%v", pkg.Found, err)
	}
	dl, err := m.PyPi.Resolve(ctx, "https://pypi.org", mirava.PackageQuery{Name: "requests"})
	if err != nil || dl.URL == "" {
		t.Fatalf("pypi resolve: %q err=%v", dl.URL, err)
	}
}

func TestIntegrationCargo(t *testing.T) {
	m := liveClient()
	ctx := context.Background()
	pkg, err := m.Cargo.Package(ctx, "https://index.crates.io", mirava.PackageQuery{Name: "serde"})
	if err != nil || !pkg.Found {
		t.Fatalf("cargo serde: found=%v err=%v", pkg.Found, err)
	}
}

func TestIntegrationGo(t *testing.T) {
	m := liveClient()
	ctx := context.Background()
	pkg, err := m.Go.Package(ctx, "https://proxy.golang.org", mirava.PackageQuery{Name: "golang.org/x/mod"})
	if err != nil || !pkg.Found {
		t.Fatalf("go x/mod: found=%v err=%v", pkg.Found, err)
	}
}

func TestIntegrationDNSProbe(t *testing.T) {
	m := liveClient()
	res, err := m.DNSProbe(context.Background(), "https://archive.ubuntu.com/ubuntu/",
		mirava.DNSSystem, mirava.DNSCloudflareUDP)
	if err != nil {
		t.Fatalf("dns probe: %v", err)
	}
	if len(res.Providers) != 2 {
		t.Fatalf("providers=%d", len(res.Providers))
	}
	for _, p := range res.Providers {
		if p.Error != "" {
			t.Errorf("provider %s: %s", p.Provider, p.Error)
		}
	}
}

func TestIntegrationSpeed(t *testing.T) {
	m := liveClient()
	res, err := m.Apt.Speed(context.Background(), "https://archive.ubuntu.com/ubuntu/", mirava.SpeedOptions{
		Duration: 3 * time.Second,
		MinBytes: 1024,
	})
	if err != nil {
		t.Fatalf("speed: %v", err)
	}
	if res.SpeedMBps <= 0 {
		t.Fatalf("speed=%v", res.SpeedMBps)
	}
	t.Logf("apt speed: %.2f MB/s (%s)", res.SpeedMBps, res.Rating)
}
