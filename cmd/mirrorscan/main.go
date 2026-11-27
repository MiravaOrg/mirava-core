// Command mirrorscan runs mirava-core's Prober logic against a parsed
// mirror catalog (by ecosystem) and prints a per-mirror report. Used to
// validate the probe logic against real mirror lists.
//
// Usage: go run ./cmd/mirrorscan /path/to/mirrors_parsed.json
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mirava "github.com/MiravaOrg/mirava-core"
)

type mirror struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type result struct {
	Eco        string `json:"eco"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	StatusErr  string `json:"status_err,omitempty"`
	Package    string `json:"package"`
	PkgVersion string `json:"pkg_version,omitempty"`
	PkgErr     string `json:"pkg_err,omitempty"`
	Resolve    string `json:"resolve"`
	ResolveErr string `json:"resolve_err,omitempty"`
}

var testPackage = map[string]string{
	"apt":      "curl",
	"npm":      "react",
	"pypi":     "requests",
	"go":       "golang.org/x/mod",
	"cargo":    "serde",
	"composer": "laravel/framework",
	"docker":   "alpine",
	"pacman":   "bash",
}

func main() {
	path := "/tmp/mirrors_parsed.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	var parsed struct {
		Mirrors []map[string]any    `json:"mirrors"`
		ByEco   map[string][]mirror `json:"by_eco"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		fatal(err)
	}

	client := &http.Client{
		Timeout: 25 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     60 * time.Second,
		},
	}
	m := mirava.New(mirava.WithHTTPClient(client))

	supported := []mirava.MirrorType{"apt", "npm", "pypi", "go", "cargo", "composer", "docker", "pacman"}

	var (
		all   []result
		mu    sync.Mutex
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 10)
		nOK   atomic.Int64
		nFail atomic.Int64
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	for _, eco := range supported {
		mirrors := parsed.ByEco[string(eco)]
		prober, err := m.Prober(eco)
		if err != nil {
			continue
		}
		pkgName := testPackage[string(eco)]
		for _, mir := range mirrors {
			wg.Add(1)
			go func(eco mirava.MirrorType, mir mirror, pkgName string) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				res := result{Eco: string(eco), Name: mir.Name, URL: mir.URL}

				// Status.
				sctx, scancel := context.WithTimeout(ctx, 30*time.Second)
				st, err := prober.Status(sctx, mir.URL)
				scancel()
				switch {
				case err == nil && st.OK:
					res.Status = "ok"
				case err == nil:
					res.Status = "fail"
					res.StatusErr = st.Message
				default:
					res.Status = "error"
					res.StatusErr = short(err)
				}

				// Package.
				pctx, pcancel := context.WithTimeout(ctx, 90*time.Second)
				pkg, err := prober.Package(pctx, mir.URL, mirava.PackageQuery{Name: pkgName})
				pcancel()
				switch {
				case err == nil && pkg.Found:
					res.Package = "ok"
					res.PkgVersion = pkg.Version
					nOK.Add(1)
				case err == nil:
					res.Package = "notfound"
					nFail.Add(1)
				default:
					res.Package = "error"
					res.PkgErr = short(err)
					nFail.Add(1)
				}

				// Resolve (only when package found).
				if res.Package == "ok" {
					rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
					dl, rerr := prober.Resolve(rctx, mir.URL, mirava.PackageQuery{Name: pkgName})
					rcancel()
					switch {
					case rerr == nil && dl.URL != "":
						res.Resolve = "ok"
					case rerr == nil:
						res.Resolve = "empty"
						res.ResolveErr = "no URL returned"
					default:
						res.Resolve = "error"
						res.ResolveErr = short(rerr)
					}
				}

				mu.Lock()
				all = append(all, res)
				mu.Unlock()
			}(eco, mir, pkgName)
		}
	}
	wg.Wait()

	out, _ := json.MarshalIndent(all, "", "  ")
	_ = os.WriteFile("/tmp/mirrorscan_results.json", out, 0o644)

	total := len(all)
	fmt.Printf("total probes: %d\n\n", total)
	byEco := map[string]map[string]int{}
	for _, r := range all {
		m := byEco[r.Eco]
		if m == nil {
			m = map[string]int{}
			byEco[r.Eco] = m
		}
		m[r.Status]++
		m["pkg:"+r.Package]++
	}
	ecos := make([]string, 0, len(byEco))
	for e := range byEco {
		ecos = append(ecos, e)
	}
	for _, e := range ecos {
		m := byEco[e]
		fmt.Printf("%-9s status ok=%2d fail=%2d err=%2d | pkg ok=%2d notfound=%d err=%d\n",
			e, m["ok"], m["fail"], m["error"], m["pkg:ok"], m["pkg:notfound"], m["pkg:error"])
	}

	fmt.Println("\n--- non-ok detail ---")
	for _, r := range all {
		if r.Status != "ok" || r.Package != "ok" {
			fmt.Printf("[%s] %s %s\n  status: %s %s\n  pkg: %s %s\n  resolve: %s %s\n",
				r.Eco, r.Name, r.URL, r.Status, r.StatusErr, r.Package, r.PkgErr, r.Resolve, r.ResolveErr)
		}
	}
}

func short(err error) string {
	s := err.Error()
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 140 {
		s = s[:140]
	}
	return s
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fatal:", err)
	os.Exit(1)
}
